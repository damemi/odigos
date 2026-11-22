package odiglet

import (
	"context"
	"errors"
	"fmt"

	"github.com/odigos-io/ebpf-core/pkg/instrumentors/kernel"
	coreotel "github.com/odigos-io/ebpf-core/pkg/otel"
	"github.com/odigos-io/ebpf-core/pkg/wasp"
	"github.com/odigos-io/odigos/common/consts"
	commonlogger "github.com/odigos-io/odigos/common/logger"
	"github.com/odigos-io/odigos/distros"
	commonInstrumentation "github.com/odigos-io/odigos/instrumentation"
	"github.com/odigos-io/odigos/odiglet/pkg/ebpf/kernelobservability"
	"github.com/odigos-io/odigos/odiglet/pkg/ebpf/logcapture"
	"github.com/odigos-io/odigos/odiglet/pkg/ebpf/sdks"
	obisdk "github.com/odigos-io/odigos/odiglet/pkg/ebpf/sdks/obi"
	"github.com/odigos-io/odigos/procdiscovery/pkg/process"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/exporter"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// exporters send to the node collector and are shared by every instrumented process.
type exporters struct {
	otlpConn *grpc.ClientConn
	traces   exporter.Traces
	metrics  exporter.Metrics
}

func newExporters(ctx context.Context, logger *commonlogger.OdigosLogger) (*exporters, error) {
	endpoint := fmt.Sprintf("localhost:%d", consts.OTLPPort)
	e := &exporters{}
	var err error

	// create a single gRPC connection that will be shared across instrumentations
	// of different processes and different languages.
	// this allows us to keep a single connection to the local collector.
	// we share the underlying connection transport, but each instrumentation can have
	// its own exporter with its own lifecycle.
	// using WithGRPCConn when creating the exporter allows us to manage the lifecycle of this connection
	// which is open the entire lifecycle of the odiglet.
	// TODO: we should eventually delete this and move into the collector components such as
	// NewOTLPGRPCTracesExporter. New instrumentations should avoid using this and use the collector sink instead.
	e.otlpConn, err = grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to create a gRPC connection to local collector: %w", err)
	}

	// Build the shared OTLP/gRPC traces exporter once for the whole odiglet. Every
	// eBPF instrumentation stacks a per-process pdata pipeline on top of it, so a
	// single connection and send queue serve all instrumented processes while each
	// keeps its own process Resource. It is drained on odiglet shutdown.
	e.traces, err = coreotel.NewOTLPGRPCTracesExporter(ctx, commonlogger.Logger().Named("ebpf-traces"), endpoint)
	if err != nil {
		e.close(logger)
		return nil, fmt.Errorf("failed to create shared traces exporter for ebpf: %w", err)
	}

	// The same story for agent emitted metrics: one OTLP/gRPC metrics exporter for the
	// whole odiglet, with each instrumented process stacking its own pdata pipeline on
	// top of it so it keeps its own process Resource. It is drained on odiglet shutdown.
	e.metrics, err = coreotel.NewOTLPGRPCMetricsExporter(ctx, commonlogger.Logger().Named("ebpf-metrics"), endpoint)
	if err != nil {
		e.close(logger)
		return nil, fmt.Errorf("failed to create shared metrics exporter for ebpf: %w", err)
	}

	return e, nil
}

// close drains and releases the exporters in reverse creation order. Safe to call on a partially
// populated value.
func (e *exporters) close(logger *commonlogger.OdigosLogger) {
	if e.metrics != nil {
		if err := e.metrics.Shutdown(context.Background()); err != nil {
			logger.Error("failed to shut down shared ebpf metrics exporter", "error", err)
		}
	}
	if e.traces != nil {
		if err := e.traces.Shutdown(context.Background()); err != nil {
			logger.Error("failed to shut down shared ebpf traces exporter", "error", err)
		}
	}
	if e.otlpConn != nil {
		if err := e.otlpConn.Close(); err != nil {
			logger.Error("failed to close common OTLP connection", "error", err)
		}
	}
}

// instrumentationFactories builds the per-distro and the generic instrumentation factories. The
// returned OBI manager backs both OBI factories and must be run alongside the instrumentation manager.
func instrumentationFactories(cfg Config, exp *exporters, kernelObservability *kernel.Instrumentation, logger *commonlogger.OdigosLogger) (map[string]commonInstrumentation.Factory, map[string]commonInstrumentation.Factory, *obisdk.Manager, error) {
	factories, err := ebpfInstrumentationFactories(cfg.TraceIDSuffix, logger, cfg.DistributionGetter, exp.otlpConn, exp.traces, exp.metrics, cfg.WaspDisabled)
	if err != nil {
		return nil, nil, nil, err
	}

	obiManager := obisdk.NewManager()
	factories[obisdk.DistroName] = obiManager.TracesFactory()

	// Generic factories are offered every process regardless of distro, are run off the main path
	// and never report their status.
	//   - eBPF log capture is gated per-workload by the ebpfLogCapture config. Only processes whose
	//     SDKs provision the log correlation maps (Go and Java) get trace context on their logs.
	//   - OBI network metrics is enabled per-workload via the networkMetrics InstrumentationRule.
	genericFactories := map[string]commonInstrumentation.Factory{
		logcapture.FactoryName:          logcapture.NewFactory(),
		kernelobservability.FactoryName: kernelobservability.NewFactory(kernelObservability),
		obisdk.MetricsFactoryName:       obiManager.MetricsFactory(),
	}

	return factories, genericFactories, obiManager, nil
}

// waspProvider returns the provider to use for writing into instrumented processes, or nil
// when wasp is not the mechanism for this kernel. Nil means probes use bpf_probe_write_user,
// or fail to load if the kernel offers neither.
func waspProvider(logger *commonlogger.OdigosLogger, disabled bool) wasp.MapsProvider {
	if disabled {
		logger.Info("wasp is explicitly disabled, it will not be used for eBPF instrumentations")
		return nil
	}

	if !wasp.IsRequired() {
		logger.Info("wasp is not required, it will not be used for eBPF instrumentations")
		return nil
	}

	if !wasp.IsWaspSupportedByKernel() {
		logger.Error("wasp is required but not supported by the kernel, probes that write to user memory will not load")
		return nil
	}

	logger.Info("wasp is required and supported by the kernel, it will be used for eBPF instrumentations")
	return wasp.NewSimpleProvider(process.HostProcDir())
}

func ebpfInstrumentationFactories(suffix uint8, logger *commonlogger.OdigosLogger, getter *distros.Getter, otlpConn *grpc.ClientConn, tracesConsumer consumer.Traces, metricsConsumer consumer.Metrics, waspDisabled bool) (map[string]commonInstrumentation.Factory, error) {
	wp := waspProvider(logger, waspDisabled)

	goFactory, err := sdks.NewGoInstrumentationFactoryEnterprise(suffix, wp, tracesConsumer)
	if err != nil {
		return nil, err
	}

	d := getter.GetDistroByName("java-ebpf-instrumentations")
	if d == nil {
		return nil, errors.New("java-ebpf-instrumentations distro not found")
	}

	javaEbpfFactory, err := sdks.NewJavaInstrumentationFactoryEnterprise(suffix, d, wp, tracesConsumer, metricsConsumer)
	if err != nil {
		return nil, fmt.Errorf("failed to create java ebpf factory: %w", err)
	}

	javaExtensionFactory, err := sdks.NewJavaExtensionInstrumentationFactoryEnterprise(metricsConsumer)
	if err != nil {
		return nil, fmt.Errorf("failed to create java extension factory: %w", err)
	}
	pythonEbpfFactory := sdks.NewPythonInstrumentationFactoryEnterprise()
	nodejsEbpfFactory := sdks.NewNodeJSInstrumentationFactoryEnterprise()
	mysqlFactory, err := sdks.NewMySQLInstrumentationFactoryEnterprise(otlpConn)
	if err != nil {
		return nil, fmt.Errorf("failed to create MySql instrumentation factory: %w", err)
	}
	// python-ebpf: out-of-process pure-eBPF CPython tracer (github.com/odigos-io/python-ebpf), wired in
	// the SAME in-process Factory shape as the Go/Java eBPF instrumentors.
	pythonOOPEbpfFactory, err := sdks.NewPythonEbpfInstrumentationFactoryEnterprise(tracesConsumer, metricsConsumer, waspDisabled)
	if err != nil {
		return nil, fmt.Errorf("failed to create python-ebpf instrumentation factory: %w", err)
	}

	return map[string]commonInstrumentation.Factory{
		sdks.GoEnterpriseDistroName:  goFactory,
		"java-ebpf-instrumentations": javaEbpfFactory,
		"java-enterprise":            javaExtensionFactory,
		"python-enterprise":          pythonEbpfFactory,
		"python-enterprise3.8":       pythonEbpfFactory,
		"nodejs-enterprise":          nodejsEbpfFactory,
		"nodejs-enterprise-14":       nodejsEbpfFactory,
		"mysql-enterprise":           mysqlFactory,
		"python-ebpf":                pythonOOPEbpfFactory,
		// dotnet, ruby and php are using native instrumentation and does not have ebpf instrumentation.
	}, nil
}
