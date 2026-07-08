// Package base is the provider-agnostic connector framework. A provider builds its store + Provider
// implementation and calls Run, which verifies the schema, registers, starts the coordinator loops,
// and serves the agents-api over both the Central reverse-WebSocket and the local odictl CLI endpoint.
package base

import (
	"context"
	"fmt"
	"net/http"
	"time"

	agentsapiversion "github.com/odigos-io/agents-api/go/server/graph/version"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/central"
	"github.com/odigos-io/odigos-enterprise/connectors/base/config"
	"github.com/odigos-io/odigos-enterprise/connectors/base/coordinator"
	"github.com/odigos-io/odigos-enterprise/connectors/base/gql"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
	"github.com/odigos-io/odigos-enterprise/connectors/base/store"
	cpg "github.com/odigos-io/odigos-enterprise/connectors/postgres"
)

var runLog = connlog.Subsystem("runtime")

// Run boots a connector runtime around the given provider and blocks until ctx is cancelled (or the
// GraphQL server errors). The caller owns the store's lifecycle (opens it, defers Close). It fails fast
// if the live Postgres schema does not match what this image was built against.
func Run(ctx context.Context, cfg config.Config, cfgMgr *config.Manager, st *store.Store, p api.Provider) error {
	if err := st.VerifySchemaVersion(ctx, cpg.ExpectedSchemaVersions); err != nil {
		return fmt.Errorf("schema version check failed: %w", err)
	}
	runLog.Info("schema version verified")

	// Load the operational config (DB overrides over code defaults) before starting the loops so they use
	// the configured intervals from the first cycle rather than pure defaults.
	if err := cfgMgr.Refresh(ctx); err != nil {
		runLog.Warn("initial config refresh failed; starting with defaults", "error", err)
	}

	// Fail fast on any provider startup precondition before registering or starting the coordinator loops,
	// so a misconfigured connector never touches provider resources.
	if v, ok := p.(api.StartupVerifier); ok {
		if err := v.VerifyStartup(ctx); err != nil {
			return fmt.Errorf("provider startup verification failed: %w", err)
		}
		runLog.Info("provider startup verification passed")
	}

	coord := coordinator.New(cfg, cfgMgr, st, p)

	if err := st.Register(ctx, cfg.RuntimeID, cfg.PodName, cfg.Version, cpg.HeartbeatStatusStarting, p.SupportedKinds()); err != nil {
		return fmt.Errorf("register: %w", err)
	}
	runLog.Info("runtime registered",
		"runtime_id", cfg.RuntimeID,
		"pod", cfg.PodName,
		"version", cfg.Version,
		"kinds", p.SupportedKinds(),
	)

	go coord.Start(ctx)

	// One agents-api executor backs both transports: the reverse WebSocket to Central and the local
	// odictl CLI endpoint.
	exec := gql.NewExecutor(coord)

	// Connect to Central over the reverse WebSocket and serve the agents-api there (same as the VM
	// agent). Central reaches the connector through this socket, not via the in-cluster Service.
	if cfg.CentralBackendURL != "" {
		schemaVersion, _ := agentsapiversion.Version()
		centralClient := central.New(central.Config{
			BackendURL:    cfg.CentralBackendURL,
			ProxyID:       cfg.PlatformID,
			PlatformName:  cfg.ConnectorName,
			Version:       cfg.Version,
			SchemaVersion: schemaVersion,
		}, exec)
		go centralClient.Run(ctx)
		runLog.Info("central reverse websocket starting", "url", cfg.CentralBackendURL)
	} else {
		runLog.Warn("central backend URL not set, running local-only")
	}

	// odictl CLI WebSocket endpoint (kubectl port-forward 23678:23678, then `odictl`).
	cliMux := http.NewServeMux()
	cliMux.Handle("/ws", gql.CLIHandler(exec))
	// ReadHeaderTimeout bounds the header read to defend against slow-header (Slowloris) clients without
	// severing an established WebSocket, which is upgraded (hijacked) after headers are read; IdleTimeout
	// reaps idle keep-alive connections. ReadTimeout/WriteTimeout are intentionally left unset because they
	// would kill long-lived WS connections.
	cliSrv := &http.Server{
		Addr:              cfg.CLIAddr,
		Handler:           cliMux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		runLog.Info("odictl CLI websocket listening", "addr", cfg.CLIAddr)
		if err := cliSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			runLog.Error("cli server failed", "error", err)
		}
	}()

	// Same timeout rationale as cliSrv: guard header reads and idle connections while leaving the gqlgen
	// WebSocket transport (subscriptions) free of read/write deadlines.
	srv := &http.Server{
		Addr:              cfg.GraphQLAddr,
		Handler:           gql.NewHandler(coord),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		_ = cliSrv.Shutdown(shutdownCtx)
	}()

	runLog.Info("graphql HTTP server listening", "addr", cfg.GraphQLAddr)
	srvErr := srv.ListenAndServe()
	if srvErr == http.ErrServerClosed {
		srvErr = nil
	}
	if srvErr != nil {
		// Don't return yet: a drain may have been requested, and skipping it on a server error would leave
		// customer instrumentation live. runShutdownDrain is a no-op unless a drain is pending.
		runLog.Error("graphql server exited with error; still running shutdown drain", "error", srvErr)
	}

	if err := runShutdownDrain(cfgMgr, st, coord); err != nil {
		runLog.Error("shutdown drain failed", "error", err)
		if srvErr != nil {
			return fmt.Errorf("graphql server: %w (shutdown drain also failed: %v)", srvErr, err)
		}
		return err
	}
	if srvErr != nil {
		return fmt.Errorf("graphql server: %w", srvErr)
	}
	runLog.Info("connector stopped")
	return nil
}

// runShutdownDrain rolls back instrumentation when the controller (or RequestCleanup) set the shared
// drain flag. Upgrades and reschedules leave the flag unset, so the process exits without touching
// provider workloads.
func runShutdownDrain(cfgMgr *config.Manager, st *store.Store, coord *coordinator.Coordinator) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfgMgr.Settings().ShutdownTimeout)
	defer cancel()

	drain, err := st.DrainRequested(shutdownCtx)
	if err != nil {
		return fmt.Errorf("read drain state: %w", err)
	}
	if !drain.Requested {
		runLog.Debug("shutdown without drain request; skipping instrumentation rollback")
		return nil
	}
	runLog.Info("drain requested; rolling back instrumentation", "reason", drain.Reason)
	return coord.Shutdown(shutdownCtx)
}
