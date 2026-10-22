// Command connector-runtime is the Central-cluster controller that reconciles OdigosCloudConnector
// resources into provider connector Deployments + Services and mirrors runtime state into CRD status.
package main

import (
	"flag"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	cloudconnectorsv1 "github.com/odigos-io/odigos/api/cloud-connectors/v1alpha1"

	"github.com/odigos-io/odigos-enterprise/connector-runtime/internal/controller"
	"github.com/odigos-io/odigos-enterprise/connector-runtime/internal/heartbeat"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(cloudconnectorsv1.AddToScheme(scheme))
}

func main() {
	var metricsAddr, probeAddr string
	var enableLeaderElection bool
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election for controller manager.")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	setupLog := ctrl.Log.WithName("setup")

	options := ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "connector-runtime.odigos.io",
	}
	// Connectors are namespaced and run in the controller's own namespace. Scoping the cache to that
	// namespace keeps list/watch namespaced, so the controller needs only a namespaced Role (not a
	// ClusterRole). When POD_NAMESPACE is unset the cache stays cluster-wide (requires a ClusterRole).
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		options.Cache = cache.Options{DefaultNamespaces: map[string]cache.Config{ns: {}}}
	} else {
		// Cluster-wide fallback: the controller then manages same-named CRs across namespaces, which share
		// one connector_name (heartbeat/sources row key) in Postgres and would cross-talk. Deploy with
		// POD_NAMESPACE set (the chart does, via the downward API) so the controller stays namespace-scoped.
		setupLog.Info("WARNING: POD_NAMESPACE is unset; running cluster-wide. Same-named connectors in different namespaces will collide on shared Postgres state — set POD_NAMESPACE to scope the controller.")
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), options)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()

	// Best-effort heartbeat reads from Postgres for the CRD status cache.
	hbReader, err := heartbeat.NewReader(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		setupLog.Error(err, "unable to initialize heartbeat reader")
		os.Exit(1)
	}
	defer hbReader.Close()

	cfg := controller.Config{
		DBSecretName:      getenvDefault("CONNECTOR_DB_SECRET_NAME", "odigos-connector-postgres"),
		DBSecretKey:       getenvDefault("CONNECTOR_DB_SECRET_KEY", "dsn"),
		ProviderImages:    providerImagesFromEnv(),
		CentralBackendURL: getenvDefault("CONNECTOR_CENTRAL_BACKEND_URL", "http://central-backend:8081"),
		// Optional override for the Fargate agents bundle staged by the AWS connector's ECS instrumentor;
		// empty keeps the connector's compiled-in default (registry.odigos.io/odigos-fargate-agents).
		FargateAgentsImage:    os.Getenv("CONNECTOR_AWS_FARGATE_AGENTS_IMAGE"),
		ManagedCollectorImage: os.Getenv("CONNECTOR_COLLECTOR_IMAGE"),
		// Per environment, from cloudConnectors.managedCollectors.externalEndpoint in the odigos-central
		// chart. Deliberately not defaulted: there is no address that is right for an unknown Central
		// installation, and a wrong default would point every Collector in a customer's cloud account at
		// somebody else's endpoint. Empty disables managed-Collector deployment with a reported reason.
		OpAMPEndpoint: os.Getenv("CONNECTOR_OPAMP_ENDPOINT"),
		// Empty means the connector's own namespace. Set it only when the gateway runs elsewhere: an empty
		// namespace selector in a NetworkPolicy means EVERY namespace, so BuildNetworkPolicy resolves this
		// deliberately rather than leaving it to a default nobody would guess.
		OpAMPGatewayNamespace: os.Getenv("CONNECTOR_OPAMP_GATEWAY_NAMESPACE"),
		RequeueInterval:       30 * time.Second,
	}

	if err := (&controller.Reconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Config:    cfg,
		Heartbeat: hbReader,
		Recorder:  mgr.GetEventRecorderFor("connector-runtime"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting connector-runtime manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "manager exited with error")
		os.Exit(1)
	}
}

func providerImagesFromEnv() map[string]string {
	images := map[string]string{}
	if v := os.Getenv("CONNECTOR_AWS_IMAGE"); v != "" {
		images["aws"] = v
	}
	if v := os.Getenv("CONNECTOR_GCP_IMAGE"); v != "" {
		images["gcp"] = v
	}
	if v := os.Getenv("CONNECTOR_AZURE_IMAGE"); v != "" {
		images["azure"] = v
	}
	return images
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
