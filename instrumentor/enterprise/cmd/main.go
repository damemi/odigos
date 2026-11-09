package main

import (
	"flag"
	"os"

	"github.com/odigos-io/odigos-enterprise/instrumentor/urltemplatization"
	distros "github.com/odigos-io/odigos/distros/enterprise"
	"github.com/odigos-io/odigos/odigosauth"

	"github.com/odigos-io/odigos/api/k8sconsts"
	"github.com/odigos-io/odigos/instrumentor"
	"github.com/odigos-io/odigos/instrumentor/controllers"

	commonlogger "github.com/odigos-io/odigos/common/logger"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	_ "k8s.io/client-go/plugin/pkg/client/auth"

	_ "net/http/pprof"
)

func getLogLevel() string {
	if lvl := os.Getenv("ODIGOS_LOG_LEVEL"); lvl != "" {
		return lvl
	}
	return "info"
}

func main() {
	commonlogger.Init(getLogLevel(), "instrumentor")
	logger := commonlogger.LoggerCompat().With("subsystem", "startup")

	managerOptions := controllers.KubeManagerOptions{}
	var telemetryDisabled bool

	flag.StringVar(&managerOptions.MetricsServerBindAddress, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&managerOptions.HealthProbeBindAddress, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&managerOptions.EnableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&telemetryDisabled, "telemetry-disabled", false, "Disable telemetry")
	flag.Parse()

	ctrl.SetLogger(commonlogger.ToLogr())
	managerOptions.Logger = commonlogger.ToLogr()

	odigosauth.AssertEnterpriseOdigosToken(commonlogger.LoggerCompat())

	logger.Info("Starting Odigos Enterprise Instrumentor")

	dp, err := distros.NewEnterpriseProvider()
	if err != nil {
		logger.Error("Failed to initialize distribution provider", "err", err)
		os.Exit(1)
	}

	var runnables []func(mgr manager.Manager) manager.Runnable
	if os.Getenv(k8sconsts.UrlTemplatizationLiveTrafficLearningEnvVar) != "" {
		runnables = append(runnables, func(mgr manager.Manager) manager.Runnable {
			return urltemplatization.NewLiveTrafficLearningJob(mgr.GetClient(), commonlogger.LoggerCompat())
		})
	}

	i, err := instrumentor.New(instrumentor.Options{
		ManagerOptions:  managerOptions,
		DistrosProvider: dp,
		Runnables:       runnables,
	})
	if err != nil {
		logger.Error("Failed to initialize instrumentor", "err", err)
		os.Exit(1)
	}

	ctx := signals.SetupSignalHandler()
	expirationChan, err := odigosauth.TokenExpirationChannel()
	if err != nil {
		logger.Error("Failed to get token expiration channel", "err", err)
		os.Exit(1)
	}
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-expirationChan:
			logger.Info("Odigos token has expired, shutting down instrumentor. Please contact Odigos support to renew your token.")
			os.Exit(1)
		}
	}()

	i.Run(ctx, telemetryDisabled)
	logger.Info("instrumentor exiting")
}
