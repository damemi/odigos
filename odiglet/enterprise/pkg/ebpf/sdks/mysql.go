package sdks

import (
	"context"
	"fmt"

	auto "github.com/odigos-io/mysql-instrumentation"
	"github.com/odigos-io/odigos/common/consts"
	"github.com/odigos-io/odigos/instrumentation"
	"github.com/odigos-io/odigos/odiglet/pkg/log"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
)

type MySQLOtelEbpfSdkEnterprise struct {
	inst *auto.Instrumentation
}

type MySQLInstrumentationFactoryEnterprise struct{}

func NewMySQLInstrumentationFactoryEnterprise() instrumentation.Factory {
	return &MySQLInstrumentationFactoryEnterprise{}
}

func (g *MySQLInstrumentationFactoryEnterprise) CreateInstrumentation(ctx context.Context, pid int, settings instrumentation.Settings) (instrumentation.Instrumentation, error) {
	defaultExporter, err := otlptracegrpc.New(
		ctx,
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithEndpoint(fmt.Sprintf("localhost:%d", consts.OTLPPort)),
	)
	if err != nil {
		log.Logger.Error(err, "failed to create exporter")
		return nil, err
	}

	inst, err := auto.NewInstrumentation(
		ctx,
		auto.WithPID(pid),
		auto.WithResourceAttributes(settings.ResourceAttributes...),
		auto.WithServiceName(settings.ServiceName),
		auto.WithTraceExporter(defaultExporter),
	)
	if err != nil {
		log.Logger.Error(err, "instrumentation setup failed")
		return nil, err
	}

	mysqlInstWrapper := &MySQLOtelEbpfSdkEnterprise{inst: inst}

	return mysqlInstWrapper, nil
}

func (g *MySQLOtelEbpfSdkEnterprise) Load(ctx context.Context) error {
	return g.inst.Load(ctx)
}

func (g *MySQLOtelEbpfSdkEnterprise) Run(ctx context.Context) error {
	return g.inst.Run(ctx)
}

func (g *MySQLOtelEbpfSdkEnterprise) Close(ctx context.Context) error {
	return g.inst.Close()
}

// Not implemented
func (g *MySQLOtelEbpfSdkEnterprise) ApplyConfig(ctx context.Context, sdkConfig instrumentation.Config) error {
	return nil
}
