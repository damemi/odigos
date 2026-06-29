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

	"github.com/odigos-io/odigos/common/consts"
	"github.com/odigos-io/odigos/distros"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	cilumebpf "github.com/cilium/ebpf"
	"github.com/odigos-io/ebpf-core/pkg/wasp"
	waspserver "github.com/odigos-io/ebpf-core/pkg/wasp/server"
	enterprisedistros "github.com/odigos-io/odigos-enterprise/distros"
	"github.com/odigos-io/odigos-enterprise/odiglet/pkg/ebpf/logcapture"
	"github.com/odigos-io/odigos-enterprise/odiglet/pkg/ebpf/sdks"
	"github.com/odigos-io/odigos-enterprise/odigosauth"
	"github.com/odigos-io/odigos/api/k8sconsts"
	commonlogger "github.com/odigos-io/odigos/common/logger"
	commonInstrumentation "github.com/odigos-io/odigos/instrumentation"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
	"github.com/odigos-io/odigos/odiglet"
	"github.com/odigos-io/odigos/odiglet/pkg/ebpf"
	"github.com/odigos-io/odigos/odiglet/pkg/ebpf/sdks/obi"
	"github.com/odigos-io/odigos/procdiscovery/pkg/process"
)

func getLogLevel() string {
	if lvl := os.Getenv("ODIGOS_LOG_LEVEL"); lvl != "" {
		return lvl
	}
	return "info"
}

func main() {
	var healthProbeBindPort int
	var traceIdSuffix string
	var waspEnabled bool
	flag.IntVar(&healthProbeBindPort, "health-probe-bind-port", k8sconsts.OdigletDefaultHealthProbeBindPort, "The port the probe endpoint binds to.")
	flag.StringVar(&traceIdSuffix, "trace-id-suffix", "", "Hex value for the unique identifier for timestamp-based trace IDs. Must be 1 byte. Example: 5E.")
	flag.BoolVar(&waspEnabled, "wasp-enabled", false, "Enable Wasp integration")
	flag.Parse()

	// Init Kubernetes clientset
	cfg, err := rest.InClusterConfig()
	if err != nil {
		panic(err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		panic(err)
	}

	// Must run before OdigletInitPhase: LoggerCompat() is a no-op until Init (init container only runs that path).
	commonlogger.Init(getLogLevel(), "odiglet")

	// If started in init mode
	if len(os.Args) == 2 && os.Args[1] == "init" {
		odiglet.OdigletInitPhase(clientset)
	}

	logger := commonlogger.LoggerCompat().With("subsystem", "startup")
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

	// create a single gRPC connection that will be shared across instrumentations
	// of different processes and different languages.
	// this allows us to keep a single connection to the local collector.
	// we share the underlying connection transport, but each instrumentation can have
	// its own exporter with its own lifecycle.
	// using WithGRPCConn when creating the exporter allows us to manage the lifecycle of this connection
	// which is open the entire lifecycle of the odiglet.
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

	factories, err := ebpfInstrumentationFactories(uint8(suffix), waspEnabled, enterpriseDistrosProvider.Getter, otlpCommonConn)
	if err != nil {
		logger.Error("failed to set up ebpf factories", "error", err)
		os.Exit(1)
	}

	instrumentationManagerOptions := ebpf.InstrumentationManagerOptions{
		Factories:                  factories,
		DistributionGetter:         enterpriseDistrosProvider.Getter,
		OdigletHealthProbeBindPort: healthProbeBindPort,
		OnLogsMapCreated: func(m *cilumebpf.Map) {
			logcapture.GetInstance().SetExternalLogsMap(m)
		},
		LogsAttrSubscribe: func() (<-chan string, []string) {
			return logcapture.GetInstance().SubscribeAttrs()
		},
	}

	o, err := odiglet.New(clientset, instrumentationManagerOptions)
	if err != nil {
		logger.Error("Failed to initialize odiglet", "error", err)
		os.Exit(1)
	}

	ctx := signals.SetupSignalHandler()

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

func ebpfInstrumentationFactories(suffix uint8, waspEnabled bool, getter *distros.Getter, otlpConn *grpc.ClientConn) (map[string]commonInstrumentation.Factory, error) {
	var wp wasp.MapsProvider
	if waspEnabled {
		wp = waspserver.NewServer(slog.Default(), k8sconsts.OdigletWaspServicePort, process.HostProcDir())
		if server, ok := wp.(*waspserver.Server); ok {
			go server.Start()
		}
	}

	goFactory, err := sdks.NewGoInstrumentationFactoryEnterprise(suffix, wp, otlpConn)
	if err != nil {
		return nil, err
	}

	d := getter.GetDistroByName("java-ebpf-instrumentations")
	if d == nil {
		return nil, errors.New("java-ebpf-instrumentations distro not found")
	}

	javaEbpfFactory, err := sdks.NewJavaInstrumentationFactoryEnterprise(suffix, d, wp, otlpConn)
	if err != nil {
		return nil, fmt.Errorf("failed to create java ebpf factory: %w", err)
	}
	pythonEbpfFactory := sdks.NewPythonInstrumentationFactoryEnterprise()
	nodejsEbpfFactory := sdks.NewNodeJSInstrumentationFactoryEnterprise()
	mysqlFactory, err := sdks.NewMySQLInstrumentationFactoryEnterprise(otlpConn)
	if err != nil {
		return nil, fmt.Errorf("failed to create MySql instrumentation factory: %w", err)
	}

	return map[string]commonInstrumentation.Factory{
		sdks.GoEnterpriseDistroName:          logcapture.WrapFactory(goFactory),
		"java-ebpf-instrumentations":         logcapture.WrapFactory(javaEbpfFactory),
		"java-enterprise":                    sdks.NewJavaExtensionInstrumentationFactoryEnterprise(),
		"python-enterprise":                  pythonEbpfFactory,
		"python-enterprise3.8":               pythonEbpfFactory,
		"nodejs-enterprise":                  nodejsEbpfFactory,
		"nodejs-enterprise-14":               nodejsEbpfFactory,
		"mysql-enterprise":                   mysqlFactory,
		"opentelemetry-ebpf-instrumentation": obi.NewOBIInstrumentationFactory(),
		// dotnet, ruby and php are using native instrumentation and does not have ebpf instrumentation.
	}, nil
}
