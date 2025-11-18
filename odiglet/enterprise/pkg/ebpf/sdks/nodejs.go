package sdks

import (
	"context"

	auto "github.com/odigos-io/ebpf-nodejs-instrumentation"
	"github.com/odigos-io/ebpf-nodejs-instrumentation/pkg/opentelemetry"
	"github.com/odigos-io/odigos/instrumentation"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
	"github.com/odigos-io/odigos/k8sutils/pkg/service"
	"github.com/odigos-io/odigos/odiglet/pkg/log"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
)

type NodeJSOtelEbpfSdkEnterprise struct {
	inst *auto.Instrumentation
}

type NodeJSInstrumentationFactoryEnterprise struct{}

func NewNodeJSInstrumentationFactoryEnterprise() instrumentation.Factory {
	return &NodeJSInstrumentationFactoryEnterprise{}
}

func (n *NodeJSInstrumentationFactoryEnterprise) CreateInstrumentation(ctx context.Context, pid int, settings instrumentation.Settings) (instrumentation.Instrumentation, error) {
	c := otlptracegrpc.NewClient(otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithEndpoint(service.LocalTrafficOTLPGrpcDataCollectionEndpoint(env.Current.NodeIP)))
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
	)

	if err != nil {
		log.Logger.Error(err, "instrumentation setup failed")
		return nil, err
	}

	nodeInstWrapper := &NodeJSOtelEbpfSdkEnterprise{inst: inst}

	return nodeInstWrapper, nil
}

func (n *NodeJSOtelEbpfSdkEnterprise) Load(ctx context.Context) (instrumentation.Status, error) {
	return instrumentation.Status{}, n.inst.Load(ctx)
}

func (n *NodeJSOtelEbpfSdkEnterprise) Run(ctx context.Context) error {
	return n.inst.Run(ctx)
}

func (n *NodeJSOtelEbpfSdkEnterprise) Close(ctx context.Context) error {
	return n.inst.Close()
}

// Not implemented
func (n *NodeJSOtelEbpfSdkEnterprise) ApplyConfig(ctx context.Context, sdkConfig instrumentation.Config) error {
	return nil
}
