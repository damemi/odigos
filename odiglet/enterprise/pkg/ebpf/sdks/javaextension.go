package sdks

import (
	"context"
	"fmt"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common/consts"

	auto "github.com/odigos-io/ebpf-java-instrumentation/otel_agent_extension"
	"github.com/odigos-io/ebpf-java-instrumentation/otel_agent_extension/pkg/dynamic"
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
	opts := []auto.InstrumentationOption{
		auto.WithPID(pid),
		auto.WithResourceAttributes(settings.ResourceAttributes...),
		auto.WithServiceName(settings.ServiceName),
		auto.WithProcFS(process.HostProcDir()),
	}

	if settings.TracesMap.Map == nil {
		c := otlptracegrpc.NewClient(otlptracegrpc.WithInsecure(),
			otlptracegrpc.WithEndpoint(fmt.Sprintf("localhost:%d", consts.OTLPPort)))
		exporter, err := opentelemetry.New(ctx, c)
		if err != nil {
			log.Logger.Error(err, "failed to create exporter")
			return nil, err
		}
		opts = append(opts, auto.WithTraceExporter(exporter))
	} else {
		opts = append(opts, auto.WithEventsBuffer(settings.TracesMap.Map, settings.TracesMap.ExternalReader))
	}

	inst, err := auto.NewInstrumentation(ctx, opts...)
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

func (p *JavaExtensionOtelEbpfSdk) ApplyConfig(ctx context.Context, conf instrumentation.Config) error {
	if conf == nil {
		log.Logger.V(0).Info("got nil config, skipping custom instrumentation")
		return nil
	}

	sdkConfig, ok := conf.(*odigosv1.SdkConfig)
	if !ok {
		return fmt.Errorf("invalid config type, expected *odigosv1.SdkConfig, got %T", conf)
	}

	// Custom instrumentation is optional - if not configured, just skip
	if sdkConfig.CustomInstrumentations == nil || sdkConfig.CustomInstrumentations.Java == nil {
		log.Logger.V(0).Info("no custom instrumentation probes configured, skipping")
		return nil
	}

	var probes []dynamic.CustomProbe
	for _, probe := range sdkConfig.CustomInstrumentations.Java {
		probes = append(probes, dynamic.CustomProbe{
			ClassName:  probe.ClassName,
			MethodName: probe.MethodName,
		})
	}

	return p.inst.ApplyDynamicConfig(probes)
}
