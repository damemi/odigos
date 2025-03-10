package sdks

import (
	"context"

	auto "github.com/odigos-io/ebpf-java-instrumentation"
	"github.com/odigos-io/odigos/instrumentation"
	"github.com/odigos-io/odigos/k8sutils/pkg/service"
	"github.com/odigos-io/odigos/odiglet/pkg/env"
	"github.com/odigos-io/odigos/odiglet/pkg/log"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
)

type JavaOtelEbpfSdkEnterprise struct {
	inst       *auto.Instrumentation
}

type JavaInstrumentationFactoryEnterprise struct {}

func NewJavaInstrumentationFactoryEnterprise() instrumentation.Factory {
	return &JavaInstrumentationFactoryEnterprise{}
}

func (g *JavaInstrumentationFactoryEnterprise) CreateInstrumentation(ctx context.Context, pid int, settings instrumentation.Settings) (instrumentation.Instrumentation, error) {
	defaultExporter, err := otlptracegrpc.New(
		ctx,
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithEndpoint(service.LocalTrafficOTLPGrpcDataCollectionEndpoint(env.Current.NodeIP)),
	)
	if err != nil {
		log.Logger.Error(err, "failed to create exporter")
		return nil, err
	}

	inst, err := auto.NewInstrumentation(
		ctx,
		auto.WithPID(pid),
		auto.WithResourceAttributes(settings.ResourceAttributes...),
		auto.WithServiceName(settings.ServiceName),
		auto.WithTraceExporter(defaultExporter),
	)
	if err != nil {
		log.Logger.Error(err, "instrumentation setup failed")
		return nil, err
	}

	javaInstWrapper := &JavaOtelEbpfSdkEnterprise{
		inst:       inst,
	}

	return javaInstWrapper, nil
}

func (g *JavaOtelEbpfSdkEnterprise) Load(ctx context.Context) error {
	return g.inst.Load(ctx)
}

func (g *JavaOtelEbpfSdkEnterprise) Run(ctx context.Context) error {
	return g.inst.Run(ctx)
}

func (g *JavaOtelEbpfSdkEnterprise) Close(ctx context.Context) error {
	return g.inst.Close()
}

// Not implemented
func (g *JavaOtelEbpfSdkEnterprise) ApplyConfig(ctx context.Context, sdkConfig instrumentation.Config) error {
	return nil
}
