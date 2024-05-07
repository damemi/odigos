package ebpf

import (
	"context"
	"fmt"

	auto "github.com/odigos-io/ebpf-nodejs-instrumentation"
	"github.com/odigos-io/ebpf-nodejs-instrumentation/pkg/opentelemetry"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/common/consts"
	"github.com/odigos-io/odigos/odiglet/pkg/env"
	"github.com/odigos-io/odigos/odiglet/pkg/kube/utils"
	"github.com/odigos-io/odigos/odiglet/pkg/log"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	apitrace "go.opentelemetry.io/otel/trace"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type NodeJSOtelEbpfSdkEnterprise struct {
	kubeclient client.Client
	inst       *auto.Instrumentation
}

type NodeJSInstrumentationFactoryEnterprise struct {
	kubeclient client.Client
}

func NewNodeJSInstrumentationFactoryEnterprise(kubeclient client.Client) InstrumentationFactoryEnterprise[*NodeJSOtelEbpfSdkEnterprise] {
	return &NodeJSInstrumentationFactoryEnterprise{
		kubeclient: kubeclient,
	}
}

func (n *NodeJSInstrumentationFactoryEnterprise) CreateEbpfInstrumentation(ctx context.Context, pid int, serviceName string, podWorkload *common.PodWorkload, containerName string, podName string) (*NodeJSOtelEbpfSdkEnterprise, error) {
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

	nodeInstWrapper := &NodeJSOtelEbpfSdkEnterprise{
		inst:       inst,
		kubeclient: n.kubeclient,
	}

	return nodeInstWrapper, nil
}

func (n *NodeJSOtelEbpfSdkEnterprise) Run(ctx context.Context) error {
	return n.inst.Run(ctx)
}

func (n *NodeJSOtelEbpfSdkEnterprise) Close(ctx context.Context) error {
	return n.inst.Close()
}

func (n *NodeJSOtelEbpfSdkEnterprise) ConfigBool(instrumentedModuleName string, optionKey string, spanKind apitrace.SpanKind, optionValue bool) error {
	return nil
}

func (n *NodeJSOtelEbpfSdkEnterprise) GetConfigOptions() map[string][]ConfigOption {
	return map[string][]ConfigOption{}
}
