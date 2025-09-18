package main

import (
	"flag"
	"os"

	"github.com/odigos-io/odigos-enterprise/distros"

	"github.com/odigos-io/odigos/instrumentor/controllers"
	"github.com/odigos-io/odigos/instrumentor/sdks"

	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	"github.com/go-logr/zapr"
	bridge "github.com/odigos-io/opentelemetry-zap-bridge"

	"github.com/odigos-io/odigos/instrumentor"

	"github.com/odigos-io/odigos-enterprise/odigosauth"

	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrlzap "sigs.k8s.io/controller-runtime/pkg/log/zap"

	_ "net/http/pprof"
)

func main() {
	managerOptions := controllers.KubeManagerOptions{}
	var telemetryDisabled bool

	flag.StringVar(&managerOptions.MetricsServerBindAddress, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&managerOptions.HealthProbeBindAddress, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&managerOptions.EnableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&telemetryDisabled, "telemetry-disabled", false, "Disable telemetry")

	opts := ctrlzap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	zapLogger := ctrlzap.NewRaw(ctrlzap.UseFlagOptions(&opts))
	zapLogger = bridge.AttachToZapLogger(zapLogger)
	logger := zapr.NewLogger(zapLogger)

	odigosauth.AssertEnterpriseOdigosToken(logger)

	managerOptions.Logger = logger

	// TODO: remove once the webhook stops using the default SDKs from the sdks package
	sdks.SetDefaultSDKs()

	logger.Info("Starting Odigos Enterprise Instrumentor")

	dp, err := distros.NewEnterpriseProvider()
	if err != nil {
		logger.Error(err, "Failed to initialize distribution provider")
		os.Exit(1)
	}

	i, err := instrumentor.New(managerOptions, dp, nil)
	if err != nil {
		logger.Error(err, "Failed to initialize instrumentor")
		os.Exit(1)
	}

	ctx := signals.SetupSignalHandler()

	i.Run(ctx, telemetryDisabled)
	logger.V(0).Info("instrumentor exiting")
}
