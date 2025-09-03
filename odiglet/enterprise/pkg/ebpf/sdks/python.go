package sdks

import (
	"context"
	"fmt"

	auto "github.com/odigos-io/ebpf-generic-instrumentation"
	"github.com/odigos-io/ebpf-generic-instrumentation/pkg/opentelemetry"
	"github.com/odigos-io/odigos/common/consts"
	"github.com/odigos-io/odigos/instrumentation"
	"github.com/odigos-io/odigos/odiglet/pkg/log"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
)

type PythonOtelEbpfSdkEnterprise struct {
	inst *auto.Instrumentation
}

type PythonInstrumentationFactoryEnterprise struct{}

func NewPythonInstrumentationFactoryEnterprise() instrumentation.Factory {
	return &PythonInstrumentationFactoryEnterprise{}
}

func (p *PythonInstrumentationFactoryEnterprise) CreateInstrumentation(ctx context.Context, pid int, settings instrumentation.Settings) (instrumentation.Instrumentation, error) {
	c := otlptracegrpc.NewClient(otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithEndpoint(fmt.Sprintf("localhost:%d", consts.OTLPPort)))
	exporter, err := opentelemetry.New(ctx, c)
	if err != nil {
		log.Logger.Error(err, "failed to create exporter")
		return nil, err
	}

	// In Python, resource attributes are provided through OpAMP, which is why WithResourceAttributes is not sent as in other languages.
	inst, err := auto.NewInstrumentation(
		ctx,
		auto.WithPID(pid),
		auto.WithServiceName(settings.ServiceName),
		auto.WithTraceExporter(exporter),
	)

	if err != nil {
		log.Logger.Error(err, "instrumentation setup failed")
		return nil, err
	}

	pythonInstWrapper := &PythonOtelEbpfSdkEnterprise{
		inst: inst,
	}

	return pythonInstWrapper, nil
}

func (p *PythonOtelEbpfSdkEnterprise) Load(ctx context.Context) error {
	return p.inst.Load(ctx)
}

func (p *PythonOtelEbpfSdkEnterprise) Run(ctx context.Context) error {
	return p.inst.Run(ctx)
}

func (p *PythonOtelEbpfSdkEnterprise) Close(_ context.Context) error {
	return p.inst.Close()
}

// Not implemented
func (p *PythonOtelEbpfSdkEnterprise) ApplyConfig(ctx context.Context, sdkConfig instrumentation.Config) error {
	return nil
}
