package ebpf

import (
	"context"
	"fmt"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/common/consts"
	"github.com/odigos-io/odigos/k8sutils/pkg/workload"
	odigosebpf "github.com/odigos-io/odigos/odiglet/pkg/ebpf"
	"github.com/odigos-io/odigos/odiglet/pkg/env"
	"github.com/odigos-io/odigos/odiglet/pkg/kube/utils"
	"github.com/odigos-io/odigos/odiglet/pkg/log"
	"go.opentelemetry.io/auto"
	goAutoConfig "go.opentelemetry.io/auto/config"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	apitrace "go.opentelemetry.io/otel/trace"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type GoOtelEbpfSdkEnterprise struct {
	inst *auto.Instrumentation
	cp   *odigosebpf.ConfigProvider[goAutoConfig.InstrumentationConfig]
}

type GoInstrumentationFactoryEnterprise struct {
	kubeclient client.Client
}

// compile time check
var _ OtelEbpfSdkEnterprise = &GoOtelEbpfSdkEnterprise{}

// compile time check that Go enterprise Sdk is implementing the configurable SDK interface
var _ odigosebpf.ConfigurableOtelEbpfSdk = &GoOtelEbpfSdkEnterprise{}

func NewGoInstrumentationFactoryEnterprise(kubeclient client.Client) InstrumentationFactoryEnterprise[*GoOtelEbpfSdkEnterprise] {
	return &GoInstrumentationFactoryEnterprise{
		kubeclient: kubeclient,
	}
}

func (g *GoInstrumentationFactoryEnterprise) CreateEbpfInstrumentation(ctx context.Context, pid int, serviceName string, podWorkload *workload.PodWorkload, containerName string, podName string, loadedIndicator chan struct{}) (*GoOtelEbpfSdkEnterprise, error) {
	defaultExporter, err := otlptracegrpc.New(
		ctx,
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithEndpoint(fmt.Sprintf("%s:%d", env.Current.NodeIP, consts.OTLPPort)),
	)
	if err != nil {
		log.Logger.Error(err, "failed to create exporter")
		return nil, err
	}

	// Fetch initial config based on the InstrumentationConfig CR
	instrumentationConfig := &odigosv1.InstrumentationConfig{}
	initialConfig := goAutoConfig.InstrumentationConfig{}
	foundInstrumentationConfig := false
	instrumentationConfigKey := client.ObjectKey{
		Namespace: podWorkload.Namespace,
		Name:      workload.CalculateWorkloadRuntimeObjectName(podWorkload.Name, podWorkload.Kind),
	}
	if err := g.kubeclient.Get(ctx, instrumentationConfigKey, instrumentationConfig); err == nil {
		initialConfig = convertToGoInstrumentationConfig(instrumentationConfig)
		foundInstrumentationConfig = true
	}

	cp := odigosebpf.NewConfigProvider(initialConfig)

	// TODO: once we have all the desired functionality in the generic config provider this code should
	// be refactored to call the OSS Odiglet factory
	inst, err := auto.NewInstrumentation(
		ctx,
		auto.WithEnv(), // for OTEL_LOG_LEVEL
		auto.WithPID(pid),
		auto.WithResourceAttributes(utils.GetResourceAttributes(podWorkload, podName)...),
		auto.WithServiceName(serviceName),
		auto.WithTraceExporter(defaultExporter),
		auto.WithGlobal(),
		auto.WithConfigProvider(cp),
	)
	if err != nil {
		log.Logger.Error(err, "instrumentation setup failed")
		return nil, err
	}

	goInstWrapper := &GoOtelEbpfSdkEnterprise{
		inst: inst,
		cp:   cp,
	}

	configOptions := goInstWrapper.GetConfigOptions()

	if err := updateInstrumentedApplication(ctx, g.kubeclient, podWorkload, containerName, configOptions); err != nil {
		return nil, err
	}

	if foundInstrumentationConfig {
		err = applyConfigToInst(&instrumentationConfig.Spec, goInstWrapper, common.GoProgrammingLanguage)
		if err != nil {
			return nil, err
		}
	}

	return goInstWrapper, nil
}

func (g *GoOtelEbpfSdkEnterprise) Load(ctx context.Context) error {
	return g.inst.Load(ctx)
}

func (g *GoOtelEbpfSdkEnterprise) Run(ctx context.Context) error {
	return g.inst.Run(ctx)
}

func (g *GoOtelEbpfSdkEnterprise) Close(ctx context.Context) error {
	return g.inst.Close()
}

// Deprecated: this function will be removed in the future once we'll move to the generic config provider
func (g *GoOtelEbpfSdkEnterprise) ConfigBool(instrumentedModuleName string, optionKey string, spanKind apitrace.SpanKind, optionValue bool) error {

	// temporary workaround to avoid setting a config option that is not supported with this instance
	supportedConfigOptions := g.inst.GetConfigOptions()
	if _, ok := supportedConfigOptions[instrumentedModuleName]; !ok {
		// noop, in case the instrumentation library has not been loaded
		return nil
	}
	for _, option := range supportedConfigOptions[instrumentedModuleName] {
		if option.OptionKey == optionKey && option.SpanKind == spanKind {
			return g.inst.ConfigBool(instrumentedModuleName, optionKey, spanKind, optionValue)
		}
	}
	// if we get here, it means none of the options matched thus this config is not supported
	// make it a silent noop
	return nil
}

// Deprecated: this function might be removed/changed in the future once we'll move to the generic config provider
func (g *GoOtelEbpfSdkEnterprise) GetConfigOptions() map[string][]ConfigOption {

	// we need to transform the go internal structures to the ones we expose.
	// this allows the generic director code to work with any eBPF SDK
	sdkOptions := g.inst.GetConfigOptions()

	normalizedOptions := make(map[string][]ConfigOption)
	for instrumentationLibraryName, options := range sdkOptions {
		for _, option := range options {
			normalizedOptions[instrumentationLibraryName] = append(normalizedOptions[instrumentationLibraryName], ConfigOption{
				OptionKey: option.OptionKey,
				SpanKind:  option.SpanKind,
			})
		}
	}

	return normalizedOptions
}

// Deprecated: this is a duplication of the OSS odiglet code, used to avoid breaking the enterprise odiglet
// Once the enterprise odiglet is updated to use the generic config provider, this function will be removed
func (g *GoOtelEbpfSdkEnterprise) ApplyConfig(ctx context.Context, instConfig *odigosv1.InstrumentationConfig) error {
	return g.cp.SendConfig(ctx, convertToGoInstrumentationConfig(instConfig))
}

// Deprecated: this is a duplication of the OSS odiglet code, used to avoid breaking the enterprise odiglet
// Once the enterprise odiglet is updated to use the generic config provider, this function will be removed
func convertToGoInstrumentationConfig(instConfig *odigosv1.InstrumentationConfig) goAutoConfig.InstrumentationConfig {
	ic := goAutoConfig.InstrumentationConfig{}
	ic.InstrumentationLibraryConfigs = make(map[goAutoConfig.InstrumentationLibraryID]goAutoConfig.InstrumentationLibrary)
	for _, sdkConfig := range instConfig.Spec.SdkConfigs {
		if sdkConfig.Language != common.GoProgrammingLanguage {
			continue
		}
		for _, ilc := range sdkConfig.InstrumentationLibraryConfigs {
			libID := goAutoConfig.InstrumentationLibraryID{
				InstrumentedPkg: ilc.InstrumentationLibraryId.InstrumentationLibraryName,
				SpanKind:        common.SpanKindOdigosToOtel(ilc.InstrumentationLibraryId.SpanKind),
			}
			var tracesEnabled *bool
			if ilc.TraceConfig != nil {
				tracesEnabled = ilc.TraceConfig.Enabled
			}
			ic.InstrumentationLibraryConfigs[libID] = goAutoConfig.InstrumentationLibrary{
				TracesEnabled: tracesEnabled,
			}
		}

		// TODO: sampling config
	}
	return ic
}
