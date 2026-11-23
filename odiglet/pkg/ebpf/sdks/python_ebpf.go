package sdks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strconv"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common/api/instrumentationrules"
	commonapisampling "github.com/odigos-io/odigos/common/api/sampling"
	"github.com/odigos-io/odigos/common/configprovider"
	commonlogger "github.com/odigos-io/odigos/common/logger"
	"github.com/odigos-io/odigos/instrumentation"
	"github.com/odigos-io/odigos/procdiscovery/pkg/process"
	"go.opentelemetry.io/collector/consumer"

	coreconfig "github.com/odigos-io/ebpf-core/pkg/config"
	pyebpf "github.com/odigos-io/python-ebpf"
)

// pythonEbpfDistroName is the distro this factory serves, as the webhook writes it to a pod's
// ODIGOS_DISTRO_NAME.
const pythonEbpfDistroName = "python-ebpf"

// odigosDistroNameEnv is the variable the instrumentor's webhook stamps on every container it
// instruments (k8sconsts.OdigosEnvVarDistroName).
const odigosDistroNameEnv = "ODIGOS_DISTRO_NAME"

// KNOWN LIMITATIONS (adversarial review 2026-10-18, consciously deferred -- NOT correctness bugs;
// the node-crash risk CRITICAL-1 and the amd64 gap HIGH-2 and the false headSampling claim HIGH-3
// were all fixed). These are enterprise control-plane feature divergences from sdks/java.go, left for
// a follow-up because the v1 goal is a correct, functional, no-restart attach:
//   - InitialConfig and ApplyConfig carry head sampling, span metrics, trace verbosity, request
//     headers, the messaging and DB query payload rules and Python custom probes (MEDIUM-4/5): the HTTP body rules from the
//     control plane are accepted but dropped until python-ebpf takes them, and the watch set is always DefaultWatchedQualnames. Full instrumentation REMOVAL still
//     works (that goes through Close, not ApplyConfig). Teardown/attach are unaffected.
//   - traceIdSuffix is not threaded (MEDIUM-6): python-ebpf has no WithTraceIdSuffix, so python-ebpf spans
//     use standard random trace IDs, not the enterprise timestamp-suffixed scheme Go/Java use.
//   - No log-to-trace correlation (NIT-13): python-ebpf is intentionally NOT logcapture-wrapped (it
//     emits spans out-of-process and populates no in-kernel ActiveSpanByLinuxTid map), so it lacks the
//     eBPF log correlation Go/Java get. Wrapping it would be wrong, not a fix.
//   - On a Run-error (e.g. eBPF load fails on an old kernel) the target /proc/<pid>/mem handle is held
//     until the pod exits (LOW-8), and Close does not block on the data plane's final flush (LOW-9, a
//     small span loss on odiglet shutdown). Both are bounded and non-fatal.

// PythonEbpfInstrumentationFactoryEnterprise is the odiglet Factory for the
// out-of-process, pure-eBPF CPython tracer (github.com/odigos-io/python-ebpf). It conforms to the
// SAME instrumentation.Factory / Instrumentation contract as the Go and Java eBPF instrumentors: the
// odiglet calls CreateInstrumentation(ctx, hostPID, settings) in-process, then Load -> go Run ->
// Close, exactly like sdks/java.go.
type PythonEbpfInstrumentationFactoryEnterprise struct {
	// tracesConsumer and metricsConsumer are the odiglet's OTLP/gRPC exporters to the node collector,
	// shared with the Go and Java instrumentations.
	tracesConsumer  consumer.Traces
	metricsConsumer consumer.Metrics
	logger          *slog.Logger
	// waspDisabled is the odiglet's wasp opt-out (-wasp-disabled), which the Go and Java
	// instrumentations honor through their wasp provider.
	waspDisabled bool
	// procDir is the host's /proc, as the odiglet mounts it.
	procDir string
}

func NewPythonEbpfInstrumentationFactoryEnterprise(tracesConsumer consumer.Traces, metricsConsumer consumer.Metrics, waspDisabled bool) (instrumentation.Factory, error) {
	if tracesConsumer == nil {
		return nil, errors.New("traces consumer is nil")
	}
	if metricsConsumer == nil {
		return nil, errors.New("metrics consumer is nil")
	}
	return &PythonEbpfInstrumentationFactoryEnterprise{
		tracesConsumer:  tracesConsumer,
		metricsConsumer: metricsConsumer,
		logger:          slog.New(commonlogger.NewSlogHandler("python-ebpf")),
		waspDisabled:    waspDisabled,
		procDir:         process.HostProcDir(),
	}, nil
}

// ErrRunsOtherPythonAgent refuses a process whose pod the webhook prepared for another Python
// distro. That process loaded the distro's in-process agent at startup, and python-ebpf on top of it
// would trace every request twice. It happens when a workload switches to python-ebpf (an
// instrumentation rule, or instrumentationVersion v2): python-ebpf needs no restart, so pods that
// were already running keep the old agent. They switch when they restart.
var ErrRunsOtherPythonAgent = errors.New("the process runs another Python distro's agent; python-ebpf takes over when the pod restarts")

// otherPythonAgent reports the distro the webhook stamped on the process's pod when it is not
// python-ebpf. A process with no stamp started before its workload was instrumented, so it runs no
// agent. A process whose environment can't be read is let through: that is no evidence of a second
// agent, and python-ebpf's own attach fails cleanly if the process is gone.
func otherPythonAgent(procDir string, pid int) (string, bool) {
	environ, err := os.ReadFile(filepath.Join(procDir, strconv.Itoa(pid), "environ"))
	if err != nil {
		return "", false
	}
	prefix := []byte(odigosDistroNameEnv + "=")
	for _, kv := range bytes.Split(environ, []byte{0}) {
		if name, ok := bytes.CutPrefix(kv, prefix); ok {
			if d := string(name); d != "" && d != pythonEbpfDistroName {
				return d, true
			}
			return "", false
		}
	}
	return "", false
}

func (f *PythonEbpfInstrumentationFactoryEnterprise) CreateInstrumentation(ctx context.Context, pid int, settings instrumentation.Settings) (instrumentation.Instrumentation, error) {
	if d, ok := otherPythonAgent(f.procDir, pid); ok {
		return nil, fmt.Errorf("pid %d (%s=%s): %w", pid, odigosDistroNameEnv, d, ErrRunsOtherPythonAgent)
	}
	initialConfig, err := pythonEbpfConfig(settings.InitialConfig)
	if err != nil {
		return nil, err
	}
	cp := configprovider.NewConfigProvider(initialConfig)

	// Option set mirrors sdks/java.go's auto.NewInstrumentation(...) one-for-one: PID + resource attrs
	// + service name + traces and metrics consumers + proc fs + logger. The resident hook .so is embedded in the
	// python-ebpf module, so there are no agent files to ship. The watched qualnames' live code
	// objects are resolved DETERMINISTICALLY by reading the target's object graph out-of-process (Load is fast; libraries imported lazily inside request
	// handlers are caught by a periodic re-resolve in Run) -- the tracer never self-drives traffic and
	// there is no discovery window to configure. The target PID is a HOST pid; we read it via
	// process.HostProcDir() (/hostproc), identical to the Java/Go SDKs, so ptrace + /proc/<pid>/mem
	// attach works with no restart.
	opts := []pyebpf.Option{
		pyebpf.WithPID(pid),
		pyebpf.WithResourceAttributes(settings.ResourceAttributes...),
		pyebpf.WithServiceName(settings.ServiceName),
		pyebpf.WithTraceConsumer(f.tracesConsumer),
		// Span metrics stay off until the container's config asks for them.
		pyebpf.WithMetricsConsumer(f.metricsConsumer),
		pyebpf.WithProcDir(f.procDir),
		pyebpf.WithLogger(f.logger),
		// On a kernel that blocks bpf_probe_write_user (lockdown), python-ebpf writes through wasp,
		// which needs a signal handler in the app. With wasp opted out, such a process is refused.
		pyebpf.WithWaspDisabled(f.waspDisabled),
		// The built-in watch set plus the workload's head sampling and span metrics; ApplyConfig sends
		// updates through the same provider, and python-ebpf applies a sampling change from the next trace.
		pyebpf.WithConfigProvider(cp),
	}
	inst, err := pyebpf.NewInstrumentation(ctx, opts...)
	if err != nil {
		f.logger.Error("python-ebpf instrumentation setup failed", "error", err)
		return nil, err
	}
	return &pythonEbpfInstrumentation{inst: inst, cp: cp, logger: f.logger}, nil
}

// pythonEbpfInstrumentation adapts *pyebpf.Instrumentation (NewInstrumentation/Load/Run/Close) to the
// odigos instrumentation.Instrumentation contract (Load returns a Status; ApplyConfig exists). Same
// wrapper shape as sdks/java.go's JavaOtelEbpfSdkEnterprise.
type pythonEbpfInstrumentation struct {
	inst   *pyebpf.Instrumentation
	cp     *configprovider.ConfigProvider[pyebpf.Config]
	logger *slog.Logger
}

// Load reports each instrumented library python-ebpf found in the target (its PyPI distribution, or
// its module for the standard library) as a component, as java.go reports the Java agent's, and each
// custom probe by its module:qualname, nil while its function is watched, else why it is not.
func (w *pythonEbpfInstrumentation) Load(ctx context.Context) (instrumentation.Status, error) {
	err := w.inst.Load(ctx)
	components := w.inst.Libraries()
	if probes := w.inst.Probes(); len(probes) > 0 {
		if components == nil {
			components = make(map[string]error, len(probes))
		}
		maps.Copy(components, probes)
	}
	return instrumentation.Status{Components: components}, err
}

func (w *pythonEbpfInstrumentation) Run(ctx context.Context) error   { return w.inst.Run(ctx) }
func (w *pythonEbpfInstrumentation) Close(ctx context.Context) error { return w.inst.Close(ctx) }

// ApplyConfig forwards the workload's head sampling and span metrics; python-ebpf drains the provider on its own
// goroutine, so SendConfig's 50 ms window is not held up by a resolve.
func (w *pythonEbpfInstrumentation) ApplyConfig(ctx context.Context, config instrumentation.Config) error {
	cfg, err := pythonEbpfConfig(config)
	if err != nil {
		return err
	}
	return w.cp.SendConfig(ctx, cfg)
}

// pythonEbpfConfig is python-ebpf's config for a container: the built-in watch set (without it the
// target is attached but nothing is watched), its head sampling, nil keeping every trace, its trace
// verbosity, nil tracing every library, the HTTP headers its spans record, nil for none, its messaging
// payload rule, nil recording no message body, its DB query payload rule, nil recording no
// db.query.text, its custom probes, and its span metrics, nil leaving them off. A nil config keeps the
// defaults, as java.go does.
func pythonEbpfConfig(conf instrumentation.Config) (pyebpf.Config, error) {
	cfg := pyebpf.Config{WatchedQualnames: pyebpf.DefaultWatchedQualnames}
	if conf == nil {
		return cfg, nil
	}
	containerConfig, ok := conf.(*odigosv1.ContainerAgentConfig)
	if !ok {
		return cfg, fmt.Errorf("invalid config type, expected *odigosv1.ContainerAgentConfig, got %T", conf)
	}
	if containerConfig == nil {
		return cfg, nil
	}
	if containerConfig.Traces != nil {
		cfg.HeadSampling = convertPythonEbpfHeadSampling(containerConfig.Traces.HeadSampling)
		cfg.TraceVerbosity = convertPythonEbpfTraceVerbosity(containerConfig.Traces.TraceVerbosity)
		cfg.HTTPHeaders = convertPythonEbpfHeaders(containerConfig.Traces.HeadersCollection)
		cfg.MessagingPayload = convertPythonEbpfMessaging(containerConfig.Traces.PayloadCollection)
		cfg.DBQueryPayload = convertPythonEbpfDBQuery(containerConfig.Traces.PayloadCollection)
		cfg.Probes = convertPythonEbpfProbes(containerConfig.Traces.CustomInstrumentations)
	}
	// The instrumentor sets span metrics when the user opted in or a trace surge rule covers the
	// container; python-ebpf counts every would-be span in the kernel, before head sampling drops it.
	if containerConfig.Metrics != nil && containerConfig.Metrics.SpanMetrics != nil {
		sm := containerConfig.Metrics.SpanMetrics
		cfg.SpanMetrics = &pyebpf.SpanMetricsConfig{
			Dimensions:         sm.Dimensions,
			IntervalMs:         sm.IntervalMs,
			HistogramBucketsMs: sm.HistogramBucketsMs,
		}
	}
	return cfg, nil
}

// convertPythonEbpfTraceVerbosity passes the libraries of the trace-verbosity rules, which the
// instrumentor has already kept to Python's, by name: python-ebpf takes its library names and its scope
// names, as PyPI compares them, and an enabled library wins over a disabled one.
func convertPythonEbpfTraceVerbosity(tv *instrumentationrules.TraceVerbosity) *pyebpf.TraceVerbosity {
	if tv == nil {
		return nil
	}
	names := func(libs []instrumentationrules.InstrumentationLibrary) []string {
		var out []string
		for _, l := range libs {
			out = append(out, l.LibraryName)
		}
		return out
	}
	return &pyebpf.TraceVerbosity{DisabledLibraries: names(tv.DisabledLibraries), EnabledLibraries: names(tv.EnabledLibraries)}
}

// convertPythonEbpfHeaders passes the headers-collection rules' header keys as the request headers a
// server span records. python-ebpf lowercases them and refuses, with a warning, names past its limits
// (pyebpf.MaxHeaders, pyebpf.MaxHeaderName). It records no response or client request headers yet,
// which the Go agent records under the same keys.
func convertPythonEbpfHeaders(hc *instrumentationrules.HttpHeadersCollection) *pyebpf.HTTPHeaders {
	if hc == nil || len(hc.HeaderKeys) == 0 {
		return nil
	}
	return &pyebpf.HTTPHeaders{RequestKeys: hc.HeaderKeys}
}

// convertPythonEbpfMessaging passes the messaging payload rule: python-ebpf records a Kafka producer's
// and consumer's message value as messaging.message.body (kafka-python, aiokafka, confluent-kafka), as
// the Go agent does, reading at most pyebpf.MaxMessageBody bytes of it in the kernel. A nil
// MaxPayloadLength sets no other limit, and a nil DropPartialPayloads cuts a longer body.
func convertPythonEbpfMessaging(pc *instrumentationrules.PayloadCollection) *pyebpf.MessagingPayload {
	if pc == nil || pc.Messaging == nil {
		return nil
	}
	m := &pyebpf.MessagingPayload{}
	if pc.Messaging.MaxPayloadLength != nil {
		m.MaxPayloadLength = *pc.Messaging.MaxPayloadLength
	}
	if pc.Messaging.DropPartialPayloads != nil {
		m.DropPartialPayloads = *pc.Messaging.DropPartialPayloads
	}
	return m
}

// convertPythonEbpfDBQuery passes the DB query payload rule: while it is set, a DB client span's
// db.query.text is the whole query, which python-ebpf's kernel sends up to pyebpf.MaxDBQuery bytes of,
// and without it a span records none, as the Go agent does. A nil MaxPayloadLength sets no other limit,
// and a nil DropPartialPayloads cuts a longer query. SanitizationPolicy is not passed, as go.go and
// java.go do not: the collector's DbQueryTemplatization action redacts queries.
func convertPythonEbpfDBQuery(pc *instrumentationrules.PayloadCollection) *pyebpf.DBQueryPayload {
	if pc == nil || pc.DbQuery == nil {
		return nil
	}
	q := &pyebpf.DBQueryPayload{}
	if pc.DbQuery.MaxPayloadLength != nil {
		q.MaxPayloadLength = *pc.DbQuery.MaxPayloadLength
	}
	if pc.DbQuery.DropPartialPayloads != nil {
		q.DropPartialPayloads = *pc.DbQuery.DropPartialPayloads
	}
	return q
}

// convertPythonEbpfProbes passes the custom instrumentation rules' Python probes, which the
// instrumentor has already kept to Python containers: python-ebpf gives each function a span of its
// own while it is in the config.
func convertPythonEbpfProbes(ci *instrumentationrules.CustomInstrumentations) []pyebpf.Probe {
	if ci == nil || len(ci.Python) == 0 {
		return nil
	}
	probes := make([]pyebpf.Probe, 0, len(ci.Python))
	for _, p := range ci.Python {
		probe := pyebpf.Probe{Module: p.Module, Qualname: p.Qualname}
		for _, c := range p.Captures {
			probe.Captures = append(probe.Captures, pyebpf.Capture{Path: c.Path, Mode: pyebpf.CaptureMode(c.Mode)})
		}
		probes = append(probes, probe)
	}
	return probes
}

// convertPythonEbpfHeadSampling maps the Odigos head-sampling config to ebpf-core's, the type
// python-ebpf decides with in the kernel.
func convertPythonEbpfHeadSampling(hs *commonapisampling.HeadSamplingConfig) *coreconfig.HeadSamplingConfig {
	if hs == nil {
		return nil
	}
	cfg := &coreconfig.HeadSamplingConfig{
		DryRun:          hs.DryRun,
		SpanMetricsMode: coreconfig.SpanMetricsMode(hs.SpanMetricsMode),
	}
	for _, no := range hs.NoisyOperations {
		rule := coreconfig.NoisyOperationRule{
			ID:               no.Id,
			Name:             no.Name,
			Disabled:         no.Disabled,
			PercentageAtMost: no.PercentageAtMost,
		}
		if no.Operation != nil {
			matcher := &coreconfig.HeadSamplingOperationMatcher{}
			if no.Operation.HttpServer != nil {
				matcher.HTTPServer = &coreconfig.HeadSamplingHTTPServerMatcher{
					Route:       no.Operation.HttpServer.Route,
					RoutePrefix: no.Operation.HttpServer.RoutePrefix,
					Method:      no.Operation.HttpServer.Method,
				}
			}
			if no.Operation.HttpClient != nil {
				matcher.HTTPClient = &coreconfig.HeadSamplingHTTPClientMatcher{
					ServerAddress:       no.Operation.HttpClient.ServerAddress,
					TemplatedPath:       no.Operation.HttpClient.TemplatedPath,
					TemplatedPathPrefix: no.Operation.HttpClient.TemplatedPathPrefix,
					Method:              no.Operation.HttpClient.Method,
				}
			}
			rule.Operation = matcher
		}
		cfg.NoisyOperations = append(cfg.NoisyOperations, rule)
	}
	return cfg
}
