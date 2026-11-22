package main

import (
	"flag"
	_ "net/http/pprof"
	"os"
	"strconv"

	"github.com/odigos-io/odigos/common/capabilities"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	"github.com/odigos-io/odigos/api/k8sconsts"
	commonlogger "github.com/odigos-io/odigos/common/logger"
	enterprisedistros "github.com/odigos-io/odigos/distros/enterprise"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
	"github.com/odigos-io/odigos/odiglet"
	"github.com/odigos-io/odigos/odigosauth"
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
		initLogger := commonlogger.LoggerCompat().With("subsystem", "init")
		if err := odiglet.InitPhase(clientset, initLogger); err != nil {
			initLogger.Error("Odiglet init phase failed", "err", err)
			os.Exit(-1)
		}
		os.Exit(0)
	}

	logger.Info("Starting odiglet")

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

	o, err := odiglet.New(ctx, clientset, odiglet.Config{
		HealthProbeBindPort: healthProbeBindPort,
		DistributionGetter:  enterpriseDistrosProvider.Getter,
		TraceIDSuffix:       uint8(suffix),
		WaspDisabled:        waspDisabled,
	})
	if err != nil {
		logger.Error("Failed to initialize odiglet", "error", err)
		os.Exit(1)
	}

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
