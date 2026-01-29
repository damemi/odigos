package sdks

import (
	"context"
	"fmt"

	"github.com/odigos-io/ebpf-java-instrumentation/jvmmetrics"
	"github.com/odigos-io/ebpf-java-instrumentation/pkg/dynamic"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/common/consts"
	"github.com/odigos-io/odigos/procdiscovery/pkg/process"

	auto "github.com/odigos-io/ebpf-java-instrumentation"
	"github.com/odigos-io/odigos/instrumentation"
	"github.com/odigos-io/odigos/odiglet/pkg/log"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
)

type JavaOtelEbpfSdkEnterprise struct {
	inst   *auto.Instrumentation
	config instrumentation.Config
}

type JavaInstrumentationFactoryEnterprise struct {
	traceIdSuffix uint8
}

func NewJavaInstrumentationFactoryEnterprise(traceIdSuffix uint8) instrumentation.Factory {
	return &JavaInstrumentationFactoryEnterprise{traceIdSuffix: traceIdSuffix}
}

func (g *JavaInstrumentationFactoryEnterprise) CreateInstrumentation(ctx context.Context, pid int, settings instrumentation.Settings) (instrumentation.Instrumentation, error) {
	defaultExporter, err := otlptracegrpc.New(
		ctx,
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithEndpoint(fmt.Sprintf("localhost:%d", consts.OTLPPort)),
	)
	if err != nil {
		log.Logger.Error(err, "failed to create exporter")
		return nil, err
	}

	opts := []auto.InstrumentationOption{
		auto.WithPID(pid),
		auto.WithResourceAttributes(settings.ResourceAttributes...),
		auto.WithServiceName(settings.ServiceName),
		auto.WithTraceExporter(defaultExporter),
		auto.WithProcFS(process.HostProcDir()),
	}
	if g.traceIdSuffix != 0 {
		opts = append(opts, auto.WithTraceIdSuffix(g.traceIdSuffix))
	}

	if settings.MetricsMap.HashMapOfMaps != nil {
		opts = append(opts, auto.WithJVMMetrics(settings.MetricsMap.HashMapOfMaps, settings.MetricsMap.AttributesMap))
	}

	inst, err := auto.NewInstrumentation(
		ctx,
		opts...,
	)
	if err != nil {
		log.Logger.Error(err, "instrumentation setup failed")
		return nil, err
	}

	javaInstWrapper := &JavaOtelEbpfSdkEnterprise{
		inst:   inst,
		config: settings.InitialConfig,
	}

	return javaInstWrapper, nil
}

func (g *JavaOtelEbpfSdkEnterprise) Load(ctx context.Context) (instrumentation.Status, error) {
	javaStatus, err := g.inst.Load(ctx)
	if err != nil {
		return instrumentation.Status{}, err
	}

	status := instrumentation.Status{
		Components: make(map[string]error),
	}

	// Convert Java instrumentation status to odiglet status format
	if javaStatus != nil {
		// Add library components
		for _, lib := range javaStatus.Libraries {
			isLoaded := lib.ClassCount > 0
			isTransformed := len(lib.AdviceClasses) > 0

			if isTransformed {
				status.Components[lib.Name] = nil
			} else if isLoaded {
				status.Components[lib.Name] = fmt.Errorf("library loaded but not transformed")
			}
		}

		log.Logger.V(0).Info("Java instrumentation loaded",
			"nativeLibLoaded", javaStatus.NativeLibLoaded,
			"byteBuddyClassCount", javaStatus.ByteBuddyClassCount,
			"byteBuddyAuxiliaryCount", javaStatus.ByteBuddyAuxiliaryCount,
			"agentInitialized", javaStatus.AgentInitialized,
			"librariesCount", len(javaStatus.Libraries))
	}

	if err := g.ApplyConfig(ctx, g.config); err != nil {
		return instrumentation.Status{}, err
	}

	return status, nil
}

func (g *JavaOtelEbpfSdkEnterprise) Run(ctx context.Context) error {
	return g.inst.Run(ctx)
}

func (g *JavaOtelEbpfSdkEnterprise) Close(ctx context.Context) error {
	return g.inst.Close()
}

func (g *JavaOtelEbpfSdkEnterprise) ApplyConfig(ctx context.Context, conf instrumentation.Config) error {
	if conf == nil {
		log.Logger.V(0).Info("got nil config, skipping")
		return nil
	}

	sdkConfig, ok := conf.(*odigosv1.SdkConfig)
	if !ok {
		return fmt.Errorf("invalid config type, expected *odigosv1.SdkConfig, got %T", conf)
	}

	// TODO(edenfed): config per library is not implement in java-eBPF yet. merge all libraries
	var headers []string
	if sdkConfig.DefaultHeadersCollection != nil && sdkConfig.DefaultHeadersCollection.HeaderKeys != nil {
		headers = sdkConfig.DefaultHeadersCollection.HeaderKeys
	}

	for _, library := range sdkConfig.InstrumentationLibraryConfigs {
		if library.HeadersCollection != nil && library.HeadersCollection.HeaderKeys != nil {
			headers = append(headers, library.HeadersCollection.HeaderKeys...)
		}
	}

	err := g.inst.ApplyDynamicConfig(dynamic.HTTPHeaders(headers))
	if err != nil {
		return err
	}

	if sdkConfig.RuntimeMetrics != nil {
		jvmConfig := createJVMMetricsConfig(sdkConfig.RuntimeMetrics.Java)
		if jvmConfig != nil {
			if err := g.inst.ApplyDynamicConfig(dynamic.JVMMetricsProbe(*jvmConfig)); err != nil {
				return err
			}
		}
	}

	return nil
}

func createJVMMetricsConfig(config *common.MetricsSourceAgentJavaRuntimeMetricsConfiguration) *jvmmetrics.JVMMetricsConfig {
	if config == nil {
		return nil
	}

	// Global disabled flag
	jvmConfig := &jvmmetrics.JVMMetricsConfig{
		Disabled: config.Disabled,
	}

	// Convert individual metrics
	for _, metric := range config.Metrics {
		jvmConfig.Metrics = append(jvmConfig.Metrics, jvmmetrics.MetricConfig{
			Name:     metric.Name,
			Disabled: metric.Disabled,
		})
	}

	return jvmConfig
}
