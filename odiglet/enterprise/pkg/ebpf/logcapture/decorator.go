package logcapture

import (
	"context"
	"strings"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	commonlogger "github.com/odigos-io/odigos/common/logger"
	"github.com/odigos-io/odigos/instrumentation"
	"go.opentelemetry.io/otel/attribute"
)

// Preparer is implemented by SDK wrappers that attach language-specific eBPF
// maps (goroutine-id map for Go, span map for Java) before their process is
// registered with the log capture Manager. Implementing this interface is how
// an SDK opts into log-capture support.
type Preparer interface {
	PrepareLogCapture(ctx context.Context, pid int)
}

// WrapFactory returns a Factory that attaches the eBPF log capture lifecycle
// to instrumentations it produces. Apply only to factories whose SDK
// implements Preparer — for other SDKs the wrapper would be inert overhead.
func WrapFactory(inner instrumentation.Factory) instrumentation.Factory {
	return &factoryWrapper{Factory: inner}
}

type factoryWrapper struct {
	instrumentation.Factory
}

func (f *factoryWrapper) CreateInstrumentation(ctx context.Context, pid int, s instrumentation.Settings) (instrumentation.Instrumentation, error) {
	inst, err := f.Factory.CreateInstrumentation(ctx, pid, s)
	if err != nil {
		return nil, err
	}
	return &decorator{
		Instrumentation: inst,
		pid:             pid,
		packedAttrs:     packResourceAttributes(s.ServiceName, s.ResourceAttributes),
		config:          s.InitialConfig,
	}, nil
}

// decorator owns the eBPF log capture lifecycle for a single instrumentation.
// It embeds instrumentation.Instrumentation so any method we don't override
// (Run, plus any future additions to the interface) is passed through to the
// inner SDK unchanged.
type decorator struct {
	instrumentation.Instrumentation
	pid         int
	packedAttrs string
	config      instrumentation.Config
	registered  bool
}

func (d *decorator) Load(ctx context.Context) (instrumentation.Status, error) {
	status, err := d.Instrumentation.Load(ctx)
	if err != nil {
		return status, err
	}
	if isEnabled(d.config) {
		d.register(ctx)
	}
	return status, nil
}

func (d *decorator) ApplyConfig(ctx context.Context, cfg instrumentation.Config) error {
	if err := d.Instrumentation.ApplyConfig(ctx, cfg); err != nil {
		return err
	}
	d.config = cfg
	enabled := isEnabled(cfg)
	switch {
	case enabled && !d.registered:
		d.register(ctx)
	case !enabled && d.registered:
		GetInstance().UnregisterProcess(d.pid)
		d.registered = false
	}
	return nil
}

func (d *decorator) Close(ctx context.Context) error {
	if d.registered {
		GetInstance().UnregisterProcess(d.pid)
		d.registered = false
	}
	return d.Instrumentation.Close(ctx)
}

func (d *decorator) register(ctx context.Context) {
	if prep, ok := d.Instrumentation.(Preparer); ok {
		prep.PrepareLogCapture(ctx, d.pid)
	}
	if err := GetInstance().RegisterProcess(ctx, d.pid, d.packedAttrs); err != nil {
		commonlogger.LoggerCompat().With("subsystem", "ebpflogcapture").Info("log capture registration failed", "pid", d.pid, "error", err)
		return
	}
	d.registered = true
}

func isEnabled(conf instrumentation.Config) bool {
	if conf == nil {
		return false
	}
	sdkConfig, ok := conf.(*odigosv1.SdkConfig)
	if !ok {
		return false
	}
	return sdkConfig.EbpfLogCapture != nil &&
		sdkConfig.EbpfLogCapture.Enabled != nil &&
		*sdkConfig.EbpfLogCapture.Enabled
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
