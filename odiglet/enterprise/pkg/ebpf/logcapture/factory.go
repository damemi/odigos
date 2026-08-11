package logcapture

import (
	"context"
	"strings"
	"sync"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	commonlogger "github.com/odigos-io/odigos/common/logger"
	"github.com/odigos-io/odigos/instrumentation"
	"go.opentelemetry.io/otel/attribute"
)

// FactoryName is the key the eBPF log capture generic factory is registered under in the
// instrumentation manager. It is an internal key (not a distribution) and must not collide with
// any distribution name.
const FactoryName = "ebpf-log-capture"

// supportedDistros is the set of otel distribution names whose eBPF SDKs provision the
// span/goroutine correlation maps that log capture depends on. Only processes instrumented by one
// of these distributions are offered log capture; any other distribution opts out with
// instrumentation.ErrFactoryNotApplicable.
//
// These are hardcoded (rather than referencing the sdks package) to avoid an import cycle: the Go
// and Java SDKs import this package to provision their correlation maps.
var supportedDistros = map[string]struct{}{
	"golang-enterprise":          {}, // sdks.GoEnterpriseDistroName
	"java-ebpf-instrumentations": {},
}

// NewFactory returns the eBPF log capture generic factory. It is registered in the odiglet's
// GenericFactories so it is offered every process regardless of its distribution, and is run off
// the main instrumentation path: its lifecycle is never reported to the Reporter and a failure is
// never retried.
//
// Log capture relies on the span/goroutine correlation maps that only certain eBPF SDKs provision,
// so it is supported only for the distributions in supportedDistros (the Go and Java enterprise
// distros). For any other distribution the factory opts out by returning
// instrumentation.ErrFactoryNotApplicable. For a supported distribution it produces an
// instrumentation; whether that process actually captures logs is then gated per-workload by the
// ContainerAgentConfig.Logs.EbpfLogCapture config (see isEnabled), evaluated in Load and
// re-evaluated in ApplyConfig.
//
// The correlation maps are provisioned by those SDKs directly on the log capture Manager, decoupled
// from this factory's registration. The Manager supports attaching those maps either before or
// after a process is registered, so the ordering between this generic factory and the distro
// factory does not matter.
func NewFactory() instrumentation.Factory {
	return &factory{}
}

type factory struct{}

var _ instrumentation.Factory = (*factory)(nil)

func (f *factory) CreateInstrumentation(_ context.Context, pid int, s instrumentation.Settings) (instrumentation.Instrumentation, error) {
	cfg, ok := s.InitialConfig.(*odigosv1.ContainerAgentConfig)
	if !ok || cfg == nil {
		return nil, instrumentation.ErrFactoryNotApplicable
	}
	if _, supported := supportedDistros[cfg.OtelDistroName]; !supported {
		return nil, instrumentation.ErrFactoryNotApplicable
	}
	return &logCaptureInstrumentation{
		pid:         pid,
		packedAttrs: packResourceAttributes(s.ServiceName, s.ResourceAttributes),
		config:      s.InitialConfig,
		done:        make(chan struct{}),
	}, nil
}

// logCaptureInstrumentation owns the eBPF log capture lifecycle for a single process. It does no
// real work of its own beyond registering/unregistering the process with the shared log capture
// Manager based on config; the Manager owns the BPF programs and the SDKs own the correlation maps.
type logCaptureInstrumentation struct {
	pid         int
	packedAttrs string
	config      instrumentation.Config
	registered  bool
	done        chan struct{}
	closeOnce   sync.Once
}

var _ instrumentation.Instrumentation = (*logCaptureInstrumentation)(nil)

func (i *logCaptureInstrumentation) Load(ctx context.Context) (instrumentation.Status, error) {
	if isEnabled(i.config) {
		i.register(ctx)
	}
	return instrumentation.Status{}, nil
}

// Run is a keep-alive: the actual capture work happens in the shared Manager. It blocks until the
// process is closed or the context is canceled.
func (i *logCaptureInstrumentation) Run(ctx context.Context) error {
	select {
	case <-ctx.Done():
	case <-i.done:
	}
	return nil
}

func (i *logCaptureInstrumentation) ApplyConfig(ctx context.Context, cfg instrumentation.Config) error {
	i.config = cfg
	enabled := isEnabled(cfg)
	switch {
	case enabled && !i.registered:
		i.register(ctx)
	case !enabled && i.registered:
		GetInstance().UnregisterProcess(i.pid)
		i.registered = false
	}
	return nil
}

func (i *logCaptureInstrumentation) Close(_ context.Context) error {
	i.closeOnce.Do(func() { close(i.done) })
	if i.registered {
		GetInstance().UnregisterProcess(i.pid)
		i.registered = false
	}
	// The SDK-provisioned correlation maps are retained across enable/disable toggles for the life
	// of the process (see Manager.UnregisterProcess); now that the process is gone, drop them.
	GetInstance().ClearProcessOpts(i.pid)
	return nil
}

func (i *logCaptureInstrumentation) register(ctx context.Context) {
	if err := GetInstance().RegisterProcess(ctx, i.pid, i.packedAttrs); err != nil {
		commonlogger.LoggerCompat().With("subsystem", "ebpflogcapture").Info("log capture registration failed", "pid", i.pid, "error", err)
		return
	}
	i.registered = true
}

func isEnabled(conf instrumentation.Config) bool {
	if conf == nil {
		return false
	}
	containerConfig, ok := conf.(*odigosv1.ContainerAgentConfig)
	if !ok {
		return false
	}
	return containerConfig.Logs != nil &&
		containerConfig.Logs.EbpfLogCapture != nil &&
		containerConfig.Logs.EbpfLogCapture.Enabled != nil &&
		*containerConfig.Logs.EbpfLogCapture.Enabled
}

func packResourceAttributes(serviceName string, attrs []attribute.KeyValue) string {
	var parts []string
	if serviceName != "" {
		parts = append(parts, "service.name:"+serviceName)
	}
	for _, attr := range attrs {
		key := string(attr.Key)
		val := attr.Value.Emit()
		if key != "" && val != "" {
			parts = append(parts, key+":"+val)
		}
	}
	return strings.Join(parts, ",")
}
