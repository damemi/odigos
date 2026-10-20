package kernelobservability

import (
	"context"
	"errors"
	"slices"

	"github.com/odigos-io/ebpf-core/pkg/instrumentors/kernel"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	commonlogger "github.com/odigos-io/odigos/common/logger"
	"github.com/odigos-io/odigos/instrumentation"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.30.0"
)

// FactoryName is the key of the kernel observability generic factory in the instrumentation manager.
const FactoryName = "kernel-observability"

// NewFactory returns a generic factory that adds kernel spans to the processes whose workload
// enables kernel observability, under the spans their language instrumentation keeps active.
func NewFactory(inst *kernel.Instrumentation) instrumentation.Factory {
	return &factory{inst: inst}
}

type factory struct {
	inst *kernel.Instrumentation
}

func (f *factory) CreateInstrumentation(_ context.Context, pid int, s instrumentation.Settings) (instrumentation.Instrumentation, error) {
	return &processInstrumentation{
		inst:    f.inst,
		pid:     pid,
		attrs:   append(slices.Clone(s.ResourceAttributes), semconv.ServiceName(s.ServiceName)),
		initial: kernelConfig(s.InitialConfig),
	}, nil
}

type processInstrumentation struct {
	inst    *kernel.Instrumentation
	pid     int
	attrs   []attribute.KeyValue
	initial kernel.Config
	process *kernel.Process
}

func (i *processInstrumentation) Load(ctx context.Context) (instrumentation.Status, error) {
	statuses, err := i.apply(ctx, i.initial)
	return instrumentation.Status{Components: statuses}, err
}

// The kernel instrumentation reads the events of all processes, so there is nothing to run per process.
func (i *processInstrumentation) Run(context.Context) error {
	return nil
}

func (i *processInstrumentation) ApplyConfig(ctx context.Context, cfg instrumentation.Config) error {
	_, err := i.apply(ctx, kernelConfig(cfg))
	return err
}

func (i *processInstrumentation) Close(ctx context.Context) error {
	return i.closeProcess(ctx)
}

func (i *processInstrumentation) apply(ctx context.Context, cfg kernel.Config) (map[string]error, error) {
	if cfg == (kernel.Config{}) {
		return nil, i.closeProcess(ctx)
	}
	if i.process != nil {
		statuses, err := i.process.ApplyConfig(ctx, cfg)
		i.warnNotTraced(statuses)
		return statuses, err
	}

	process, err := i.inst.NewProcess(i.pid, kernel.WithResourceAttributes(i.attrs...))
	if err != nil {
		return nil, err
	}
	statuses, err := process.Load(ctx, cfg)
	if err != nil {
		return nil, errors.Join(err, process.Close(ctx))
	}
	i.process = process
	i.warnNotTraced(statuses)
	return statuses, nil
}

// warnNotTraced logs the categories the kernel can't trace, since the manager doesn't report the
// status of generic instrumentations.
func (i *processInstrumentation) warnNotTraced(statuses map[string]error) {
	for category, status := range statuses {
		if status != nil {
			commonlogger.LoggerCompat().With("subsystem", "kernelobservability").Warn("kernel category not traced", "pid", i.pid, "category", category, "error", status)
		}
	}
}

func (i *processInstrumentation) closeProcess(ctx context.Context) error {
	if i.process == nil {
		return nil
	}
	err := i.process.Close(ctx)
	i.process = nil
	return err
}

// kernelConfig returns the zero Config when the workload traces nothing.
func kernelConfig(c instrumentation.Config) kernel.Config {
	cc, ok := c.(*odigosv1.ContainerAgentConfig)
	if !ok || cc == nil || cc.Traces == nil || cc.Traces.KernelTraces == nil {
		return kernel.Config{}
	}
	traces := cc.Traces.KernelTraces

	var cfg kernel.Config
	if traces.Filesystem != nil {
		cfg.FS = &kernel.FSConfig{}
	}
	if traces.Subprocesses != nil {
		recordArgs := traces.Subprocesses.RecordCommandArgs
		cfg.Processes = &kernel.ProcessesConfig{CommandArgs: recordArgs != nil && *recordArgs}
	}
	if traces.CPUDelay != nil {
		cfg.CPUDelay = &kernel.CPUDelayConfig{}
	}
	return cfg
}
