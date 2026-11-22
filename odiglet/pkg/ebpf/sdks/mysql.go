package sdks

import (
	"context"

	auto "github.com/odigos-io/mysql-instrumentation"
	commonlogger "github.com/odigos-io/odigos/common/logger"
	"github.com/odigos-io/odigos/instrumentation"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"google.golang.org/grpc"
)

type MySQLOtelEbpfSdkEnterprise struct {
	inst *auto.Instrumentation
}

type MySQLInstrumentationFactoryEnterprise struct {
	otlpConn *grpc.ClientConn
}

func NewMySQLInstrumentationFactoryEnterprise(otlpConn *grpc.ClientConn) (instrumentation.Factory, error) {
	if otlpConn == nil {
		return nil, errNoOTLPConn
	}
	return &MySQLInstrumentationFactoryEnterprise{otlpConn: otlpConn}, nil
}

func (g *MySQLInstrumentationFactoryEnterprise) CreateInstrumentation(ctx context.Context, pid int, settings instrumentation.Settings) (instrumentation.Instrumentation, error) {
	defaultExporter, err := otlptracegrpc.New(
		ctx,
		otlptracegrpc.WithGRPCConn(g.otlpConn),
	)
	if err != nil {
		commonlogger.LoggerCompat().With("subsystem", "ebpfmysql").Error("failed to create exporter", "error", err)
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
		commonlogger.LoggerCompat().With("subsystem", "ebpfmysql").Error("instrumentation setup failed", "error", err)
		return nil, err
	}

	mysqlInstWrapper := &MySQLOtelEbpfSdkEnterprise{inst: inst}

	return mysqlInstWrapper, nil
}

func (g *MySQLOtelEbpfSdkEnterprise) Load(ctx context.Context) (instrumentation.Status, error) {
	return instrumentation.Status{}, g.inst.Load(ctx)
}

func (g *MySQLOtelEbpfSdkEnterprise) Run(ctx context.Context) error {
	return g.inst.Run(ctx)
}

func (g *MySQLOtelEbpfSdkEnterprise) Close(ctx context.Context) error {
	return g.inst.Close()
}

// Not implemented
func (g *MySQLOtelEbpfSdkEnterprise) ApplyConfig(ctx context.Context, containerConfig instrumentation.Config) error {
	return nil
}
