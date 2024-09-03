package ebpf

import (
	"context"
	"fmt"

	auto "github.com/odigos-io/mysql-instrumentation"
	"github.com/odigos-io/odigos/common/consts"
	"github.com/odigos-io/odigos/k8sutils/pkg/workload"
	"github.com/odigos-io/odigos/odiglet/pkg/env"
	"github.com/odigos-io/odigos/odiglet/pkg/kube/utils"
	"github.com/odigos-io/odigos/odiglet/pkg/log"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	apitrace "go.opentelemetry.io/otel/trace"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type MySQLOtelEbpfSdkEnterprise struct {
	kubeclient client.Client
	inst       *auto.Instrumentation
}

type MySQLInstrumentationFactoryEnterprise struct {
	kubeclient client.Client
}

// compile time check
var _ OtelEbpfSdkEnterprise = &MySQLOtelEbpfSdkEnterprise{}

func NewMySQLInstrumentationFactoryEnterprise(kubeclient client.Client) InstrumentationFactoryEnterprise[*MySQLOtelEbpfSdkEnterprise] {
	return &MySQLInstrumentationFactoryEnterprise{
		kubeclient: kubeclient,
	}
}

func (g *MySQLInstrumentationFactoryEnterprise) CreateEbpfInstrumentation(ctx context.Context, pid int, serviceName string, podWorkload *workload.PodWorkload, containerName string, podName string, loadedIndicator chan struct{}) (*MySQLOtelEbpfSdkEnterprise, error) {
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
		auto.WithResourceAttributes(utils.GetResourceAttributes(podWorkload, podName)...),
		auto.WithServiceName(serviceName),
		auto.WithTraceExporter(defaultExporter),
	)
	if err != nil {
		log.Logger.Error(err, "instrumentation setup failed")
		return nil, err
	}

	mysqlInstWrapper := &MySQLOtelEbpfSdkEnterprise{
		inst:       inst,
		kubeclient: g.kubeclient,
	}

	return mysqlInstWrapper, nil
}

func (g *MySQLOtelEbpfSdkEnterprise) Run(ctx context.Context) error {
	return g.inst.Run(ctx)
}

func (g *MySQLOtelEbpfSdkEnterprise) Close(ctx context.Context) error {
	return g.inst.Close()
}

// Deprecated: this will be removed in the future once we will move to the generic config provider
func (g *MySQLOtelEbpfSdkEnterprise) ConfigBool(instrumentedModuleName string, optionKey string, spanKind apitrace.SpanKind, optionValue bool) error {
	return nil
}

// Deprecated: this might be removed/renamed in the future once we will move to the generic config provider
func (g *MySQLOtelEbpfSdkEnterprise) GetConfigOptions() map[string][]ConfigOption {
	return map[string][]ConfigOption{}
}

