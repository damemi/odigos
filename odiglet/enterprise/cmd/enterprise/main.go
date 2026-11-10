package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	_ "net/http/pprof"
	"os"
	"strconv"

	"github.com/odigos-io/odigos/common/capabilities"
	"github.com/odigos-io/odigos/common/consts"
	"github.com/odigos-io/odigos/distros"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	"github.com/odigos-io/ebpf-core/pkg/instrumentors/kernel"
	coreotel "github.com/odigos-io/ebpf-core/pkg/otel"
	"github.com/odigos-io/ebpf-core/pkg/wasp"
	"github.com/odigos-io/odigos/odiglet/enterprise/pkg/ebpf/kernelobservability"
	"github.com/odigos-io/odigos/odiglet/enterprise/pkg/ebpf/logcapture"
	"github.com/odigos-io/odigos/odiglet/enterprise/pkg/ebpf/sdks"
	"github.com/odigos-io/odigos/api/k8sconsts"
	commonlogger "github.com/odigos-io/odigos/common/logger"
	enterprisedistros "github.com/odigos-io/odigos/distros/enterprise"
	commonInstrumentation "github.com/odigos-io/odigos/instrumentation"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
	"github.com/odigos-io/odigos/odiglet"
	"github.com/odigos-io/odigos/odiglet/pkg/ebpf"
	"github.com/odigos-io/odigos/odigosauth"
	"github.com/odigos-io/odigos/procdiscovery/pkg/process"
	"go.opentelemetry.io/collector/consumer"
)

func getLogLevel() string {
	if lvl := os.Getenv("ODIGOS_LOG_LEVEL"); lvl != "" {
		return lvl
	}
	return "info"
}

func main() {
	commonlogger.Init(getLogLevel(), "odiglet")
	logger := commonlogger.LoggerCompat().With("subsystem", "startup")

	var healthProbeBindPort int
	var traceIdSuffix string
	var waspDisabled bool
	flag.IntVar(&healthProbeBindPort, "health-probe-bind-port", k8sconsts.OdigletDefaultHealthProbeBindPort, "The port the probe endpoint binds to.")
	flag.StringVar(&traceIdSuffix, "trace-id-suffix", "", "Hex value for the unique identifier for timestamp-based trace IDs. Must be 1 byte. Example: 5E.")
	flag.BoolVar(&waspDisabled, "wasp-disabled", false, "Opt out of wasp, even on kernels that require it for writing into instrumented processes.")
	flag.Parse()

	if err := capabilities.AlignEffectiveCapToPermitted(); err != nil {
		logger.Warn("failed to align effective capabilities to permitted set", "err", err)
	}

	// Init Kubernetes clientset
	cfg, err := rest.InClusterConfig()
	if err != nil {
		panic(err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		panic(err)
	}

	// If started in init mode
	if len(os.Args) == 2 && os.Args[1] == "init" {
		odiglet.OdigletInitPhase(clientset)
	}

	logger.Info("Starting odiglet enterprise")

	odigosauth.AssertEnterpriseOdigosToken(commonlogger.LoggerCompat())

	if err := env.Load(); err != nil {
		logger.Error("Failed to load env", "error", err)
		os.Exit(1)
	}

	enterpriseDistrosProvider, err := enterprisedistros.NewEnterpriseProvider()
	if err != nil {
		logger.Error("Failed to create enterprise distros getter", "error", err)
		os.Exit(1)
	}

	suffix := uint64(0)
	if traceIdSuffix != "" {
		suffix, err = strconv.ParseUint(traceIdSuffix, 16, 8)
		if err != nil {
			logger.Error("failed to parse TRACE_ID_SUFFIX", "error", err)
			os.Exit(1)
		}
	}

	ctx := signals.SetupSignalHandler()

	// create a single gRPC connection that will be shared across instrumentations
	// of different processes and different languages.
	// this allows us to keep a single connection to the local collector.
	// we share the underlying connection transport, but each instrumentation can have
	// its own exporter with its own lifecycle.
	// using WithGRPCConn when creating the exporter allows us to manage the lifecycle of this connection
	// which is open the entire lifecycle of the odiglet.
	// TODO: we should eventually delete this and move into the collector components such as
	// NewOTLPGRPCTracesExporter. New instrumentations should avoid using this and use the collector sink instead.
	otlpCommonConn, err := grpc.NewClient(
		fmt.Sprintf("localhost:%d", consts.OTLPPort),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		logger.Error("failed to create a gRPC connection to local collector", "error", err)
		os.Exit(1)
	}
	defer func() {
		err := otlpCommonConn.Close()
		if err != nil {
			logger.Error("failed to close common OTLP connection", "error", err)
		}
	}()

	// Build the shared OTLP/gRPC traces exporter once for the whole odiglet. Every
	// eBPF instrumentation stacks a per-process pdata pipeline on top of it, so a
	// single connection and send queue serve all instrumented processes while each
	// keeps its own process Resource. It is drained on odiglet shutdown.
	tracesConsumer, err := coreotel.NewOTLPGRPCTracesExporter(
		ctx,
		commonlogger.Logger().Named("ebpf-traces"),
		fmt.Sprintf("localhost:%d", consts.OTLPPort),
	)
	if err != nil {
		logger.Error("failed to create shared traces exporter for ebpf", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := tracesConsumer.Shutdown(context.Background()); err != nil {
			logger.Error("failed to shut down shared ebpf traces exporter", "error", err)
		}
	}()

	// No eBPF object is loaded here: the kernel instrumentation loads its programs and maps when the
	// first process whose workload enables kernel traces is loaded by its generic factory.
	kernelObservability, err := kernel.NewInstrumentation(tracesConsumer, kernel.WithLogger(slog.New(commonlogger.NewSlogHandler("kernelobservability"))))
	if err != nil {
		logger.Error("failed to create the kernel instrumentation", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := kernelObservability.Close(); err != nil {
			logger.Error("failed to close the kernel instrumentation", "error", err)
		}
	}()

	// The same story for agent emitted metrics: one OTLP/gRPC metrics exporter for the
	// whole odiglet, with each instrumented process stacking its own pdata pipeline on
	// top of it so it keeps its own process Resource. It is drained on odiglet shutdown.
	metricsConsumer, err := coreotel.NewOTLPGRPCMetricsExporter(
		ctx,
		commonlogger.Logger().Named("ebpf-metrics"),
		fmt.Sprintf("localhost:%d", consts.OTLPPort),
	)
	if err != nil {
		logger.Error("failed to create shared metrics exporter for ebpf", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := metricsConsumer.Shutdown(context.Background()); err != nil {
			logger.Error("failed to shut down shared ebpf metrics exporter", "error", err)
		}
	}()

	factories, err := ebpfInstrumentationFactories(uint8(suffix), logger, enterpriseDistrosProvider.Getter, otlpCommonConn, tracesConsumer, metricsConsumer, waspDisabled)
	if err != nil {
		logger.Error("failed to set up ebpf factories", "error", err)
		os.Exit(1)
	}

	// The enterprise eBPF SDKs write into shared maps, served over the unix socket to the
	// enterprise collector's odigosebpfreceiver. Neither end exists in OSS.
	mapExchange, err := newEbpfMapExchange()
	if err != nil {
		logger.Error("failed to set up the ebpf map exchange", "error", err)
		os.Exit(1)
	}
	defer mapExchange.close()

	instrumentationManagerOptions := ebpf.InstrumentationManagerOptions{
		Factories: factories,
		// eBPF log capture is a generic factory: it is offered every process regardless of distro and
		// is gated per-workload by the ebpfLogCapture config. Only processes whose SDKs provision the
		// log correlation maps (Go and Java) get trace context on their logs. It is run off the main
		// path and never reports its status. (OBI's generic factories are wired centrally in
		// odiglet.New.)
		GenericFactories: map[string]commonInstrumentation.Factory{
			logcapture.FactoryName:          logcapture.NewFactory(),
			kernelobservability.FactoryName: kernelobservability.NewFactory(kernelObservability),
		},
		DistributionGetter:         enterpriseDistrosProvider.Getter,
		OdigletHealthProbeBindPort: healthProbeBindPort,
	}
	mapExchange.applyTo(&instrumentationManagerOptions)

	o, err := odiglet.New(clientset, instrumentationManagerOptions)
	if err != nil {
		logger.Error("Failed to initialize odiglet", "error", err)
		os.Exit(1)
	}

	o.AddRunnable(odiglet.Runnable{
		Name: "ebpf map fd server",
		Run:  mapExchange.serve,
	})

	o.AddRunnable(odiglet.Runnable{
		Name: "ebpf map fd server",
		Run:  mapExchange.serve,
	})

	o.AddRunnable(odiglet.Runnable{
		Name:         "go offsets watcher",
		PropagateErr: true,
		Run: func(runCtx context.Context) error {
			return sdks.WatchGoOffsets(runCtx, o.InstrumentationRequests(), logger)
		},
	})

	expirationChan, err := odigosauth.TokenExpirationChannel()
	if err != nil {
		logger.Error("Failed to get token expiration channel", "error", err)
		os.Exit(1)
	}
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-expirationChan:
			logger.Info("Odigos token has expired, shutting down odiglet. Please contact Odigos support to renew your token.")
			os.Exit(1)
		}
	}()

	o.Run(ctx)

	logger.Info("odiglet exiting")
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
		// OBI (the "opentelemetry-ebpf-instrumentation" traces distro factory and the
		// network-metrics generic factory) is wired centrally in odiglet.New, identically for
		// OSS and enterprise, so it is intentionally not registered here.
		// dotnet, ruby and php are using native instrumentation and does not have ebpf instrumentation.
	}, nil
}
