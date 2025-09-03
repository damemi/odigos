package sdks

import (
	"context"
	"fmt"

	"github.com/odigos-io/ebpf-java-instrumentation/otel_agent_extension/pkg/dynamic"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common/consts"

	auto "github.com/odigos-io/ebpf-java-instrumentation/otel_agent_extension"
	"github.com/odigos-io/ebpf-java-instrumentation/otel_agent_extension/pkg/opentelemetry"
	"github.com/odigos-io/odigos/instrumentation"
	"github.com/odigos-io/odigos/odiglet/pkg/log"
	"github.com/odigos-io/odigos/procdiscovery/pkg/process"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
)

type JavaExtensionOtelEbpfSdk struct {
	inst   *auto.Instrumentation
	config instrumentation.Config
}

type JavaExtensionInstrumentationFactoryEnterprise struct{}

func NewJavaExtensionInstrumentationFactoryEnterprise() instrumentation.Factory {
	return &JavaExtensionInstrumentationFactoryEnterprise{}
}

func (p *JavaExtensionInstrumentationFactoryEnterprise) CreateInstrumentation(ctx context.Context, pid int, settings instrumentation.Settings) (instrumentation.Instrumentation, error) {
	c := otlptracegrpc.NewClient(otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithEndpoint(fmt.Sprintf("localhost:%d", consts.OTLPPort)))
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
		auto.WithProcFS(process.HostProcDir()),
	)

	if err != nil {
		log.Logger.Error(err, "instrumentation setup failed")
		return nil, err
	}

	javaInstWrapper := &JavaExtensionOtelEbpfSdk{
		inst:   inst,
		config: settings.InitialConfig,
	}

	return javaInstWrapper, nil
}

func (p *JavaExtensionOtelEbpfSdk) Load(ctx context.Context) error {
	if err := p.inst.Load(ctx); err != nil {
		return err
	}

	if err := p.ApplyConfig(ctx, p.config); err != nil {
		return err
	}

	return nil
}

func (p *JavaExtensionOtelEbpfSdk) Run(ctx context.Context) error {
	return p.inst.Run(ctx)
}

func (p *JavaExtensionOtelEbpfSdk) Close(ctx context.Context) error {
	return p.inst.Close()
}

// Not implemented
func (p *JavaExtensionOtelEbpfSdk) ApplyConfig(ctx context.Context, conf instrumentation.Config) error {
	if conf == nil {
		log.Logger.V(0).Info("got nil config, skipping")
		return nil
	}

	sdkConfig, ok := conf.(*odigosv1.SdkConfig)
	if !ok {
		return fmt.Errorf("invalid config type, expected *odigosv1.SdkConfig, got %T", conf)
	}

	var event dynamic.CustomInstrumentationEvent
	if sdkConfig.DefaultCustomInstrumentations != nil && sdkConfig.DefaultCustomInstrumentations.Probes != nil {
		for _, probe := range sdkConfig.DefaultCustomInstrumentations.Probes {
			event = append(event, dynamic.CustomProbe{
				ClassName:  probe.ClassName,
				MethodName: probe.MethodName,
			})
		}
	}

	return p.inst.ApplyDynamicConfig(dynamic.CustomInstrumentation(event))
}
