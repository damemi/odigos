package ebpf

import (
	"context"
	"fmt"

	auto "github.com/keyval-dev/ebpf-java-instrumentation/otel_agent_extension"
	"github.com/keyval-dev/ebpf-java-instrumentation/otel_agent_extension/pkg/opentelemetry"
	"github.com/keyval-dev/odigos/common"
	"github.com/keyval-dev/odigos/common/consts"
	"github.com/keyval-dev/odigos/odiglet/pkg/env"
	"github.com/keyval-dev/odigos/odiglet/pkg/kube/utils"
	"github.com/keyval-dev/odigos/odiglet/pkg/log"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	apitrace "go.opentelemetry.io/otel/trace"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type JavaExtensionOtelEbpfSdk struct {
	kubeclient client.Client
	inst       *auto.Instrumentation
}

type JavaExtensionInstrumentationFactoryEnterprise struct {
	kubeclient client.Client
}

func NewJavaExtensionInstrumentationFactoryEnterprise(kubeclient client.Client) InstrumentationFactoryEnterprise[*JavaExtensionOtelEbpfSdk] {
	return &JavaExtensionInstrumentationFactoryEnterprise{
		kubeclient: kubeclient,
	}
}

func (p *JavaExtensionInstrumentationFactoryEnterprise) CreateEbpfInstrumentation(ctx context.Context, pid int, serviceName string, podWorkload *common.PodWorkload, containerName string, podName string) (*JavaExtensionOtelEbpfSdk, error) {
	c := otlptracegrpc.NewClient(otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithEndpoint(fmt.Sprintf("%s:%d", env.Current.NodeIP, consts.OTLPPort)))
	exporter, err := opentelemetry.New(ctx, c)
	if err != nil {
		log.Logger.Error(err, "failed to create exporter")
		return nil, err
	}

	inst, err := auto.NewInstrumentation(
		ctx,
		auto.WithPID(pid),
		auto.WithResourceAttributes(utils.GetResourceAttributes(podWorkload, podName)...),
		auto.WithServiceName(serviceName),
		auto.WithTraceExporter(exporter),
	)

	if err != nil {
		log.Logger.Error(err, "instrumentation setup failed")
		return nil, err
	}

	javaInstWrapper := &JavaExtensionOtelEbpfSdk{
		inst:       inst,
		kubeclient: p.kubeclient,
	}

	return javaInstWrapper, nil
}

func (p *JavaExtensionOtelEbpfSdk) Run(ctx context.Context) error {
	return p.inst.Run(ctx)
}

func (p *JavaExtensionOtelEbpfSdk) Close(ctx context.Context) error {
	return p.inst.Close()
}

func (p *JavaExtensionOtelEbpfSdk) ConfigBool(instrumentedModuleName string, optionKey string, spanKind apitrace.SpanKind, optionValue bool) error {
	return nil
}

func (p *JavaExtensionOtelEbpfSdk) GetConfigOptions() map[string][]ConfigOption {
	return map[string][]ConfigOption{}
}
