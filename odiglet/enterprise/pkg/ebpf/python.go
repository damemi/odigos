package ebpf

import (
	"context"
	"fmt"

	auto "github.com/keyval-dev/ebpf-generic-instrumentation"
	"github.com/keyval-dev/ebpf-generic-instrumentation/pkg/opentelemetry"
	"github.com/keyval-dev/odigos/common"
	"github.com/keyval-dev/odigos/common/consts"
	"github.com/keyval-dev/odigos/odiglet/pkg/env"
	"github.com/keyval-dev/odigos/odiglet/pkg/kube/utils"
	"github.com/keyval-dev/odigos/odiglet/pkg/log"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	apitrace "go.opentelemetry.io/otel/trace"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type PythonOtelEbpfSdkEnterprise struct {
	kubeclient client.Client
	inst       *auto.Instrumentation
}

type PythonInstrumentationFactoryEnterprise struct {
	kubeclient client.Client
}

func NewPythonInstrumentationFactoryEnterprise(kubeclient client.Client) InstrumentationFactoryEnterprise[*PythonOtelEbpfSdkEnterprise] {
	return &PythonInstrumentationFactoryEnterprise{
		kubeclient: kubeclient,
	}
}

func (p *PythonInstrumentationFactoryEnterprise) CreateEbpfInstrumentation(ctx context.Context, pid int, serviceName string, podWorkload *common.PodWorkload, containerName string, podName string) (*PythonOtelEbpfSdkEnterprise, error) {
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

	pythonInstWrapper := &PythonOtelEbpfSdkEnterprise{
		inst:       inst,
		kubeclient: p.kubeclient,
	}

	return pythonInstWrapper, nil
}

func (p *PythonOtelEbpfSdkEnterprise) Run(ctx context.Context) error {
	return p.inst.Run(ctx)
}

func (p *PythonOtelEbpfSdkEnterprise) Close(ctx context.Context) error {
	return p.inst.Close()
}

func (p *PythonOtelEbpfSdkEnterprise) ConfigBool(instrumentedModuleName string, optionKey string, spanKind apitrace.SpanKind, optionValue bool) error {
	return nil
}

func (p *PythonOtelEbpfSdkEnterprise) GetConfigOptions() map[string][]ConfigOption {
	return map[string][]ConfigOption{}
}
