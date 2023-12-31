package ebpf

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	odigosv1 "github.com/keyval-dev/odigos/api/odigos/v1alpha1"
	"github.com/keyval-dev/odigos/common"
	"github.com/keyval-dev/odigos/common/consts"
	commonutils "github.com/keyval-dev/odigos/common/utils"
	"github.com/keyval-dev/odigos/odiglet/pkg/env"
	"github.com/keyval-dev/odigos/odiglet/pkg/kube/utils"
	"github.com/keyval-dev/odigos/odiglet/pkg/log"
	"go.opentelemetry.io/auto"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	apitrace "go.opentelemetry.io/otel/trace"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type GoOtelEbpfSdkEnterprise struct {
	inst *auto.Instrumentation
}

type GoInstrumentationFactoryEnterprise struct {
	kubeclient client.Client
}

func NewGoInstrumentationFactoryEnterprise(kubeclient client.Client) InstrumentationFactoryEnterprise[*GoOtelEbpfSdkEnterprise] {
	return &GoInstrumentationFactoryEnterprise{
		kubeclient: kubeclient,
	}
}

func (g *GoInstrumentationFactoryEnterprise) CreateEbpfInstrumentation(ctx context.Context, pid int, serviceName string, podWorkload *common.PodWorkload, containerName string) (*GoOtelEbpfSdkEnterprise, error) {

	defaultExporter, err := otlptracegrpc.New(
		ctx,
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithEndpoint(fmt.Sprintf("%s:%d", env.Current.NodeIP, consts.OTLPPort)),
	)
	if err != nil {
		log.Logger.Error(err, "failed to create exporter")
		return nil, err
	}

	inst, err := auto.NewInstrumentation(
		ctx,
		auto.WithPID(pid),
		auto.WithResourceAttributes(utils.GetResourceAttributes(podWorkload)...),
		auto.WithServiceName(serviceName),
		auto.WithTraceExporter(defaultExporter),
		auto.WithGlobal(),
	)
	if err != nil {
		log.Logger.Error(err, "instrumentation setup failed")
		return nil, err
	}

	goInstWrapper := &GoOtelEbpfSdkEnterprise{
		inst: inst,
	}

	configOptions := goInstWrapper.GetConfigOptions()

	if err := updateInstrumentedApplication(ctx, g.kubeclient, podWorkload, containerName, configOptions); err != nil {
        return nil, err
    }

	// Fetch the InstrumentationConfig instrumentationConfig
	instrumentationConfig := &odigosv1.InstrumentationConfig{}
	instrumentationConfigKey := client.ObjectKey{
		Namespace: podWorkload.Namespace,
		Name:      commonutils.GetRuntimeObjectName(podWorkload.Name, podWorkload.Kind),
	}
	err = g.kubeclient.Get(ctx, instrumentationConfigKey, instrumentationConfig)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return goInstWrapper, nil
		}
		return nil, err
	}



	err = applyConfigToInst(&instrumentationConfig.Spec, goInstWrapper, common.GoProgrammingLanguage)
	if err != nil {
		return nil, err
	}

	return goInstWrapper, nil
}

func (g *GoOtelEbpfSdkEnterprise) Run(ctx context.Context) error {
	return g.inst.Run(ctx)
}

func (g *GoOtelEbpfSdkEnterprise) Close(ctx context.Context) error {
	return g.inst.Close()
}

func (g *GoOtelEbpfSdkEnterprise) ConfigBool(instrumentedModuleName string, optionKey string, spanKind apitrace.SpanKind, optionValue bool) error {
	return g.inst.ConfigBool(instrumentedModuleName, optionKey, spanKind, optionValue)
}

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
