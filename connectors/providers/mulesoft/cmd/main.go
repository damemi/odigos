// Command connector-mulesoft is the MuleSoft provider connector runtime. It loads config and Connected App
// credentials, opens the shared Postgres base store, and hands a base.Provider to the shared framework, which
// verifies the schema, registers, runs the discovery/reconcile loops, and serves the agents-api over Central and
// the odictl CLI endpoint.
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
	"github.com/odigos-io/odigos-enterprise/connectors/providers/mulesoft/pkg/config"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/mulesoft/pkg/mulesoftprovider"
	workloadstore "github.com/odigos-io/odigos-enterprise/connectors/providers/mulesoft/pkg/workload/store"
)

func main() {
	connlog.Init("debug", "connector-mulesoft")
	log := connlog.Subsystem("startup")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.FromEnv()
	if err != nil {
		log.Error("config load failed", "error", err)
		os.Exit(1)
	}
	log.Info("mulesoft connector starting",
		"connector", cfg.ConnectorName,
		"schema", cfg.Schema,
		"organization", cfg.OrganizationID,
		"controlPlane", cfg.BootstrapRegion,
		"workloadCapabilities", len(cfg.Capabilities.Workloads),
	)

	bstore, err := basestore.Open(ctx, cfg.DatabaseURL, cfg.ConnectorName, cfg.Schema)
	if err != nil {
		log.Error("store open failed", "error", err)
		os.Exit(1)
	}
	defer bstore.Close()

	cfgMgr := baseconfig.NewManager(bstore, baseconfig.MergeDefaults(baseconfig.Defaults(), config.Defaults()))
	provider := mulesoftprovider.New(cfg, cfgMgr, workloadstore.New(bstore))

	if err := base.Run(ctx, cfg.Config, cfgMgr, bstore, provider); err != nil {
		log.Error("connector stopped with error", "error", err)
		os.Exit(1)
	}
}
