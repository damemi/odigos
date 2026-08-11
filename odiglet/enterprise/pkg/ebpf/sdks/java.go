package sdks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/odigos-io/ebpf-core/pkg/collection"
	"github.com/odigos-io/ebpf-core/pkg/wasp"
	javaconfig "github.com/odigos-io/ebpf-java-instrumentation/v2/pkg/config"
	"github.com/odigos-io/ebpf-java-instrumentation/v2/jvmmetrics"
	"github.com/odigos-io/ebpf-java-instrumentation/v2/pkg/dynamic"
	"github.com/odigos-io/odigos-enterprise/odiglet/pkg/ebpf/logcapture"
	"github.com/odigos-io/odigos/api/k8sconsts"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common"
	commonapisampling "github.com/odigos-io/odigos/common/api/sampling"
	"github.com/odigos-io/odigos/distros/distro"
	"github.com/odigos-io/odigos/procdiscovery/pkg/process"
	"google.golang.org/grpc"

	auto "github.com/odigos-io/ebpf-java-instrumentation/v2"
	commonlogger "github.com/odigos-io/odigos/common/logger"
	"github.com/odigos-io/odigos/instrumentation"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
)

const (
	agentJarDistroOptionName = "agentJarName"
)

type JavaOtelEbpfSdkEnterprise struct {
	inst             *auto.Instrumentation
	config           instrumentation.Config
	logger           *slog.Logger
	waspMaps         *wasp.Maps
	waspMapsProvider wasp.MapsProvider
	podName          string
	pid              int
}

type JavaInstrumentationFactoryEnterprise struct {
	traceIdSuffix uint8
	agentJarName  string
	// location where the agent relevant files are located on the host
	agentFilesSourceDir string
	otlpConn            *grpc.ClientConn
	// logger is shared across every *auto.Instrumentation
	logger           *slog.Logger
	waspMapsProvider wasp.MapsProvider
}

func NewJavaInstrumentationFactoryEnterprise(traceIdSuffix uint8, otelDistro *distro.OtelDistro, ws wasp.MapsProvider, otlpConn *grpc.ClientConn) (instrumentation.Factory, error) {
	if otlpConn == nil {
		return nil, errNoOTLPConn
	}
	if otelDistro == nil {
		return nil, errors.New("otel distro is nil")
	}
	agentJar, exists := distro.RuntimeAgentOptionValue(otelDistro, agentJarDistroOptionName)
	if !exists || agentJar == "" {
		return nil, errors.New("agent jar name not found in distro options")
	}
	if len(otelDistro.RuntimeAgent.DirectoryNames) != 1 {
		return nil, fmt.Errorf("unexpected number of runtime agent directory names, expected 1, got %d", len(otelDistro.RuntimeAgent.DirectoryNames))
	}
	// in order to support instrumentation of java processes without a restart,
	// we are going to copy the required java agent files from the odiglet container filesystem into the target process filesystem.
	agentDirOnHost := otelDistro.RuntimeAgent.DirectoryNames[0]
	sourceDir := strings.ReplaceAll(agentDirOnHost, distro.AgentPlaceholderDirectory, k8sconsts.OdigletContainerAgentDirectory)
	return &JavaInstrumentationFactoryEnterprise{
		waspMapsProvider:    ws,
		traceIdSuffix:       traceIdSuffix,
		agentJarName:        agentJar,
		agentFilesSourceDir: sourceDir,
		otlpConn:            otlpConn,
		logger:              slog.New(commonlogger.NewSlogHandler("ebpfjava")),
	}, nil
}

func (g *JavaInstrumentationFactoryEnterprise) CreateInstrumentation(ctx context.Context, pid int, settings instrumentation.Settings) (instrumentation.Instrumentation, error) {
	defaultExporter, err := otlptracegrpc.New(
		ctx,
		otlptracegrpc.WithGRPCConn(g.otlpConn),
	)
	if err != nil {
		g.logger.Error("failed to create exporter", "error", err)
		return nil, err
	}

	opts := []auto.InstrumentationOption{
		auto.WithPID(pid),
		auto.WithResourceAttributes(settings.ResourceAttributes...),
		auto.WithServiceName(settings.ServiceName),
		auto.WithTraceExporter(defaultExporter),
		auto.WithProcFS(process.HostProcDir()),
		auto.WithLogger(g.logger),
	}
	if g.traceIdSuffix != 0 {
		opts = append(opts, auto.WithTraceIdSuffix(g.traceIdSuffix))
	}

	if settings.MetricsMap.HashMapOfMaps != nil {
		opts = append(opts, auto.WithJVMMetrics(settings.MetricsMap.HashMapOfMaps, settings.MetricsMap.AttributesMap))
	}

	// check if the ODIGOS_POD_NAME is present in the target process environment variables.
	// if it does, it means this process is from a pod that went through our mutating webhook - indicating it should have all the required files
	// for the java agent and the JAVA_TOOL_OPTION env var set in one of the supported env injection methods.
	// if it does not, this is an instrumentation of a running process, and we need to make sure the java agent required files are accessible for the
	// target process, and do the attach dynamically with jattach.
	// note: we do not check the JAVA_TOOL_OPTIONS env var here since it can be added by LD_PRELOAD method, which will not reflect in proc fs.
	odigosEnvPresent, err := isEnvValPresent(pid, k8sconsts.OdigosEnvVarPodName)
	if err != nil {
		g.logger.Error("failed to check if native lib is loaded", "error", err)
		return nil, err
	}
	if !odigosEnvPresent {
		if err := process.InjectDirToProcessTempDir(pid, g.agentFilesSourceDir, false); err != nil {
			return nil, err
		}
		opts = append(opts, auto.WithDynamicAttachment(
			&auto.DynamicAttachConfig{
				AgentJarPath: filepath.Join(
					os.TempDir(),
					filepath.Base(g.agentFilesSourceDir),
					g.agentJarName,
				),
			},
		))
	}

	var (
		waspMaps *wasp.Maps
		podName  string
	)

	if g.waspMapsProvider != nil {
		if podName = podNameFromSettings(settings); podName != "" {
			g.logger.Info("using wasp handshake for Java process", "pod", podName)
			waspMaps, err = g.waspMapsProvider.Release(podName, pid)
			if err != nil {
				g.logger.Error("failed to acquire wasp maps", "error", err)
			} else {
				opts = append(opts, auto.WithWASPHandshakeMaps(waspMaps))
			}
		}
	}

	inst, err := auto.NewInstrumentation(
		ctx,
		opts...,
	)
	if err != nil {
		g.logger.Error("instrumentation setup failed", "error", err)
		if waspMaps != nil {
			// return ownership of the wasp maps to the provider in case we failed to instrument this process
			err = errors.Join(err, g.waspMapsProvider.Take(podName, waspMaps))
		}
		return nil, err
	}

	javaInstWrapper := &JavaOtelEbpfSdkEnterprise{
		inst:             inst,
		config:           settings.InitialConfig,
		logger:           g.logger,
		waspMaps:         waspMaps,
		waspMapsProvider: g.waspMapsProvider,
		podName:          podName,
		pid:              pid,
	}

	return javaInstWrapper, nil
}

func isEnvValPresent(pid int, envVarName string) (bool, error) {
	pidEnvironPath := filepath.Join(process.HostProcDir(), fmt.Sprintf("%d", pid), "environ")
	contents, err := os.ReadFile(pidEnvironPath)
	if err != nil {
		return false, fmt.Errorf("failed to read environ for pid:%d error:%w", pid, err)
	}

	return bytes.Index(contents, []byte(envVarName)) != -1, nil
}

func (g *JavaOtelEbpfSdkEnterprise) Load(ctx context.Context) (instrumentation.Status, error) {
	javaStatus, err := g.inst.Load(ctx)
	if err != nil {
		if g.waspMaps != nil {
			// if we failed to load, and the instrumentation took ownership over the wasp maps
			// we return ownership back to the provider
			err = errors.Join(err, g.waspMapsProvider.Take(g.podName, g.waspMaps))
		}
		return instrumentation.Status{}, err
	}

	status := instrumentation.Status{
		Components: make(map[string]error),
	}

	if javaStatus != nil {
		for _, lib := range javaStatus.Libraries {
			isLoaded := lib.ClassCount > 0
			isTransformed := len(lib.AdviceClasses) > 0

			if isTransformed {
				status.Components[lib.Name] = nil
			} else if isLoaded {
				status.Components[lib.Name] = fmt.Errorf("library loaded but not transformed")
			}
		}

		g.logger.Info("Java instrumentation loaded",
			"nativeLibLoaded", javaStatus.NativeLibLoaded,
			"byteBuddyClassCount", javaStatus.ByteBuddyClassCount,
			"byteBuddyAuxiliaryCount", javaStatus.ByteBuddyAuxiliaryCount,
			"agentInitialized", javaStatus.AgentInitialized,
			"librariesCount", len(javaStatus.Libraries))
	}

	if err := g.ApplyConfig(ctx, g.config); err != nil {
		return instrumentation.Status{}, err
	}

	g.prepareLogCapture()

	return status, nil
}

func (g *JavaOtelEbpfSdkEnterprise) Run(ctx context.Context) error {
	return g.inst.Run(ctx)
}

func (g *JavaOtelEbpfSdkEnterprise) Close(ctx context.Context) error {
	var err error
	if g.waspMaps != nil {
		// if the instrumentation had ownership of wasp maps - return ownership to the provider
		// this will allow us to re-use the wasp maps if a new instrumentation is required in the same pod after this.
		err = errors.Join(err, g.waspMapsProvider.Take(g.podName, g.waspMaps))
	}
	err = errors.Join(err, g.inst.Close())
	return err
}

// prepareLogCapture attaches the global Linux TID span map so logs emitted from this Java process
// can be correlated with their originating span. It is invoked at the end of Load, once the probes
// (and their shared maps) are loaded. This is done regardless of whether log capture is currently
// enabled for the workload: the log capture generic factory registers/unregisters the process
// based on config, and the Manager supports attaching this map before or after registration.
func (g *JavaOtelEbpfSdkEnterprise) prepareLogCapture() {
	spanMap, err := collection.LookupSharedMap(auto.ActiveSpanByLinuxTidMapName, collection.WithGlobalScope())
	if err != nil {
		// Not loaded yet (no Java probe attached) is expected; only surface real errors.
		if !errors.Is(err, collection.ErrSharedMapNotFound) {
			g.logger.Info("failed to look up span map for log correlation", "pid", g.pid, "error", err)
		}
		return
	}
	logcapture.GetInstance().AddSpanMap(uint32(g.pid), spanMap)
}

func (g *JavaOtelEbpfSdkEnterprise) ApplyConfig(ctx context.Context, conf instrumentation.Config) error {
	if conf == nil {
		g.logger.Info("got nil config, skipping")
		return nil
	}

	containerConfig, ok := conf.(*odigosv1.ContainerAgentConfig)
	if !ok {
		return fmt.Errorf("invalid config type, expected *odigosv1.ContainerAgentConfig, got %T", conf)
	}

	var headers []string
	if containerConfig.Traces != nil && containerConfig.Traces.HeadersCollection != nil {
		headers = containerConfig.Traces.HeadersCollection.HeaderKeys
	}

	err := g.inst.ApplyDynamicConfig(dynamic.HTTPHeaders(headers))
	if err != nil {
		return err
	}

	if containerConfig.Traces != nil && containerConfig.Traces.CustomInstrumentations != nil && containerConfig.Traces.CustomInstrumentations.Java != nil {
		var probes []dynamic.CustomProbe
		for _, probe := range containerConfig.Traces.CustomInstrumentations.Java {
			probes = append(probes, dynamic.CustomProbe{
				ClassName:  probe.ClassName,
				MethodName: probe.MethodName,
			})
		}
		if err := g.inst.ApplyDynamicConfig(dynamic.CustomInstrumentation(probes)); err != nil {
			return err
		}
	}

	if containerConfig.Metrics != nil && containerConfig.Metrics.RuntimeMetrics != nil {
		jvmConfig := createJVMMetricsConfig(containerConfig.Metrics.RuntimeMetrics.Java)
		if jvmConfig != nil {
			if err := g.inst.ApplyDynamicConfig(dynamic.JVMMetricsProbe(*jvmConfig)); err != nil {
				return err
			}
		}
	}

	kafkaBodyEnabled := false
	if containerConfig.Traces != nil && containerConfig.Traces.PayloadCollection != nil && containerConfig.Traces.PayloadCollection.Messaging != nil {
		kafkaBodyEnabled = true
	}
	if err := g.inst.ApplyDynamicConfig(dynamic.KafkaMessageBody(dynamic.KafkaMessageBodyEvent(kafkaBodyEnabled))); err != nil {
		return err
	}

	var headSampling *javaconfig.HeadSamplingConfig
	if containerConfig.Traces != nil && containerConfig.Traces.HeadSampling != nil {
		headSampling = convertJavaHeadSampling(containerConfig.Traces.HeadSampling)
	}
	// A nil config clears any previously applied head-sampling policy.
	if err := g.inst.ApplyDynamicConfig(dynamic.HeadSampling(headSampling)); err != nil {
		return err
	}

	return nil
}

// convertJavaHeadSampling maps the Odigos head-sampling config to the Java eBPF
// SDK's config type.
func convertJavaHeadSampling(hs *commonapisampling.HeadSamplingConfig) *javaconfig.HeadSamplingConfig {
	if hs == nil {
		return nil
	}
	cfg := &javaconfig.HeadSamplingConfig{
		DryRun:          hs.DryRun,
		SpanMetricsMode: javaconfig.SpanMetricsMode(hs.SpanMetricsMode),
	}
	for _, no := range hs.NoisyOperations {
		rule := javaconfig.NoisyOperationRule{
			ID:               no.Id,
			Name:             no.Name,
			Disabled:         no.Disabled,
			PercentageAtMost: no.PercentageAtMost,
		}
		if no.Operation != nil {
			matcher := &javaconfig.HeadSamplingOperationMatcher{}
			if no.Operation.HttpServer != nil {
				matcher.HTTPServer = &javaconfig.HeadSamplingHTTPServerMatcher{
					Route:       no.Operation.HttpServer.Route,
					RoutePrefix: no.Operation.HttpServer.RoutePrefix,
					Method:      no.Operation.HttpServer.Method,
				}
			}
			if no.Operation.HttpClient != nil {
				matcher.HTTPClient = &javaconfig.HeadSamplingHTTPClientMatcher{
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

func createJVMMetricsConfig(config *common.MetricsSourceAgentJavaRuntimeMetricsConfiguration) *jvmmetrics.JVMMetricsConfig {
	if config == nil {
		return nil
	}

	jvmConfig := &jvmmetrics.JVMMetricsConfig{
		Disabled: config.Disabled,
	}

	for _, metric := range config.Metrics {
		jvmConfig.Metrics = append(jvmConfig.Metrics, jvmmetrics.MetricConfig{
			Name:     metric.Name,
			Disabled: metric.Disabled,
		})
	}

	return jvmConfig
}
