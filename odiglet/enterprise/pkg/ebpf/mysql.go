package ebpf

import (
	"context"
	"fmt"

	auto "github.com/keyval-dev/mysql-instrumentation"
	"github.com/keyval-dev/odigos/common"
	"github.com/keyval-dev/odigos/common/consts"
	"github.com/keyval-dev/odigos/odiglet/pkg/env"
	"github.com/keyval-dev/odigos/odiglet/pkg/kube/utils"
	"github.com/keyval-dev/odigos/odiglet/pkg/log"
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

func NewMySQLInstrumentationFactoryEnterprise(kubeclient client.Client) InstrumentationFactoryEnterprise[*MySQLOtelEbpfSdkEnterprise] {
	return &MySQLInstrumentationFactoryEnterprise{
		kubeclient: kubeclient,
	}
}

func (g *MySQLInstrumentationFactoryEnterprise) CreateEbpfInstrumentation(ctx context.Context, pid int, serviceName string, podWorkload *common.PodWorkload, containerName string, podName string) (*MySQLOtelEbpfSdkEnterprise, error) {
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

func (g *MySQLOtelEbpfSdkEnterprise) ConfigBool(instrumentedModuleName string, optionKey string, spanKind apitrace.SpanKind, optionValue bool) error {
	return nil
}

func (g *MySQLOtelEbpfSdkEnterprise) GetConfigOptions() map[string][]ConfigOption {
	return map[string][]ConfigOption{}
}