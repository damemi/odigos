package sdks

import (
	"context"

	auto "github.com/odigos-io/ebpf-java-instrumentation/otel_agent_extension"
	"github.com/odigos-io/ebpf-java-instrumentation/otel_agent_extension/pkg/opentelemetry"
	"github.com/odigos-io/odigos/instrumentation"
	"github.com/odigos-io/odigos/k8sutils/pkg/service"
	"github.com/odigos-io/odigos/odiglet/pkg/env"
	"github.com/odigos-io/odigos/odiglet/pkg/log"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
)

type JavaExtensionOtelEbpfSdk struct {
	inst       *auto.Instrumentation
}

type JavaExtensionInstrumentationFactoryEnterprise struct {}

func NewJavaExtensionInstrumentationFactoryEnterprise() instrumentation.Factory {
	return &JavaExtensionInstrumentationFactoryEnterprise{}
}

func (p *JavaExtensionInstrumentationFactoryEnterprise) CreateInstrumentation(ctx context.Context, pid int, settings instrumentation.Settings) (instrumentation.Instrumentation, error) {
	c := otlptracegrpc.NewClient(otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithEndpoint(service.LocalTrafficOTLPGrpcDataCollectionEndpoint(env.Current.NodeIP)))
	exporter, err := opentelemetry.New(ctx, c)
	if err != nil {
		log.Logger.Error(err, "failed to create exporter")
		return nil, err
	}

	inst, err := auto.NewInstrumentation(
		ctx,
		auto.WithPID(pid),
		auto.WithResourceAttributes(settings.ResourceAttributes...),
		auto.WithServiceName(settings.ServiceName),
		auto.WithTraceExporter(exporter),
	)

	if err != nil {
		log.Logger.Error(err, "instrumentation setup failed")
		return nil, err
	}

	javaInstWrapper := &JavaExtensionOtelEbpfSdk{inst: inst}

	return javaInstWrapper, nil
}

func (p *JavaExtensionOtelEbpfSdk) Load(ctx context.Context) error {
	return p.inst.Load(ctx)
}

func (p *JavaExtensionOtelEbpfSdk) Run(ctx context.Context) error {
	return p.inst.Run(ctx)
}

func (p *JavaExtensionOtelEbpfSdk) Close(ctx context.Context) error {
	return p.inst.Close()
}

// Not implemented
func (p *JavaExtensionOtelEbpfSdk) ApplyConfig(ctx context.Context, sdkConfig instrumentation.Config) error {
	return nil
}
