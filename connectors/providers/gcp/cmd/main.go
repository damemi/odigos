// Command connector-gcp is the GCP provider connector runtime. It loads config + credentials, opens the
// shared Postgres base store, builds the two domain stores (workload + compute-platform) over it, and
// hands a base.Provider to the shared framework, which verifies the schema, registers, runs the
// discovery/reconcile loops, and serves the agents-api over Central + the odictl CLI endpoint.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/odigos-io/odigos-enterprise/connectors/base"
	baseconfig "github.com/odigos-io/odigos-enterprise/connectors/base/config"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
	computeplatformstore "github.com/odigos-io/odigos-enterprise/connectors/providers/gcp/pkg/computeplatform/store"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/gcp/pkg/config"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/gcp/pkg/gcpprovider"
	workloadstore "github.com/odigos-io/odigos-enterprise/connectors/providers/gcp/pkg/workload/store"
)

func main() {
	connlog.Init("debug", "connector-gcp")
	log := connlog.Subsystem("startup")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.FromEnv()
	if err != nil {
		log.Error("config load failed", "error", err)
		os.Exit(1)
	}
	log.Info("gcp connector starting",
		"connector", cfg.ConnectorName,
		"schema", cfg.Schema,
		"project", cfg.ProjectID,
		"workloadCapabilities", len(cfg.Capabilities.Workloads),
		"computePlatformCapabilities", len(cfg.Capabilities.ComputePlatforms),
	)

	// One base store (pool + connector_platform + sources/destinations) shared by both domain stores.
	bstore, err := basestore.Open(ctx, cfg.DatabaseURL, cfg.ConnectorName, cfg.Schema)
	if err != nil {
		log.Error("store open failed", "error", err)
		os.Exit(1)
	}
	defer bstore.Close()

	// Operational config lives in the DB (defaults + overrides), served live by the Manager. Bootstrap
	// config (credentials, project, provider, DSN, capabilities) stays on the command line above.
	cfgMgr := baseconfig.NewManager(bstore, baseconfig.MergeDefaults(baseconfig.Defaults(), config.Defaults()))

	provider := gcpprovider.New(cfg, cfgMgr, workloadstore.New(bstore), computeplatformstore.New(bstore))

	if err := base.Run(ctx, cfg.Config, cfgMgr, bstore, provider); err != nil {
		log.Error("connector stopped with error", "error", err)
		os.Exit(1)
	}
}
