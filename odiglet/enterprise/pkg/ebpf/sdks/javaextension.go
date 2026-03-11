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
	commonlogger "github.com/odigos-io/odigos/common/logger"
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
			commonlogger.LoggerCompat().With("subsystem", "ebpfjavaext").Error("failed to create exporter", "error", err)
			return nil, err
		}
		opts = append(opts, auto.WithTraceExporter(exporter))
	} else {
		opts = append(opts, auto.WithEventsBuffer(settings.TracesMap.Map, settings.TracesMap.ExternalReader))
	}

	if settings.MetricsMap.HashMapOfMaps != nil {
		opts = append(opts, auto.WithJVMMetrics(settings.MetricsMap.HashMapOfMaps, settings.MetricsMap.AttributesMap))
	}

	inst, err := auto.NewInstrumentation(ctx, opts...)
	if err != nil {
		commonlogger.LoggerCompat().With("subsystem", "ebpfjavaext").Error("instrumentation setup failed", "error", err)
		return nil, err
	}

	javaInstWrapper := &JavaExtensionOtelEbpfSdk{
		inst:   inst,
		config: settings.InitialConfig,
	}

	return javaInstWrapper, nil
}

func (p *JavaExtensionOtelEbpfSdk) Load(ctx context.Context) (instrumentation.Status, error) {
	if err := p.inst.Load(ctx); err != nil {
		return instrumentation.Status{}, err
	}

	if err := p.ApplyConfig(ctx, p.config); err != nil {
		return instrumentation.Status{}, err
	}

	return instrumentation.Status{}, nil
}

func (p *JavaExtensionOtelEbpfSdk) Run(ctx context.Context) error {
	return p.inst.Run(ctx)
}

func (p *JavaExtensionOtelEbpfSdk) Close(ctx context.Context) error {
	return p.inst.Close()
}

func (p *JavaExtensionOtelEbpfSdk) ApplyConfig(ctx context.Context, conf instrumentation.Config) error {
	if conf == nil {
		commonlogger.LoggerCompat().With("subsystem", "ebpfjavaext").Info("got nil config, skipping custom instrumentation")
		return nil
	}

	sdkConfig, ok := conf.(*odigosv1.SdkConfig)
	if !ok {
		return fmt.Errorf("invalid config type, expected *odigosv1.SdkConfig, got %T", conf)
	}

	// Custom instrumentation - apply only if configured
	if sdkConfig.CustomInstrumentations != nil && sdkConfig.CustomInstrumentations.Java != nil {
		var probes []dynamic.CustomProbe
		for _, probe := range sdkConfig.CustomInstrumentations.Java {
			probes = append(probes, dynamic.CustomProbe{
				ClassName:  probe.ClassName,
				MethodName: probe.MethodName,
			})
		}
		if err := p.inst.ApplyDynamicConfig(dynamic.CustomInstrumentation(probes)); err != nil {
			return err
		}
	}

	// JVM metrics - apply only if configured
	if sdkConfig.RuntimeMetrics != nil {
		jvmConfig := createJVMMetricsConfig(sdkConfig.RuntimeMetrics.Java)
		if jvmConfig != nil {
			if err := p.inst.ApplyDynamicConfig(dynamic.JVMMetricsProbe(*jvmConfig)); err != nil {
				return err
			}
		}
	}

	return nil
}
