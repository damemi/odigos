// Package base is the provider-agnostic connector framework. A provider builds its store + Provider
// implementation and calls Run, which verifies the schema, registers, starts the coordinator loops,
// and serves the agents-api over both the Central reverse-WebSocket and the local odictl CLI endpoint.
package base

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/central"
	"github.com/odigos-io/odigos-enterprise/connectors/base/config"
	"github.com/odigos-io/odigos-enterprise/connectors/base/coordinator"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorgateway"
	"github.com/odigos-io/odigos-enterprise/connectors/base/gql"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
	"github.com/odigos-io/odigos-enterprise/connectors/base/opamp"
	"github.com/odigos-io/odigos-enterprise/connectors/base/store"
	cpg "github.com/odigos-io/odigos-enterprise/connectors/postgres"
)

var runLog = connlog.Subsystem("runtime")

// The connector is built against the agents-api schema frozen at v0.25.0.
// The Go dependency currently has a pseudo-version rooted at v0.23.1, and
// agents-api/version.Version() strips its commit suffix. Reporting that base
// version makes Central select the v0.23 dialect, which has no Managed
// Collector setup operations even though this runtime serves them. Advance
// this value with the agents-api schema snapshot used by this connector.
const connectorGraphQLSchemaVersion = "v0.28.0"

// Run boots a connector runtime around the given provider and blocks until ctx is cancelled (or the
// GraphQL server errors). The caller owns the store's lifecycle (opens it, defers Close). It fails fast
// if the live Postgres schema does not match what this image was built against.
func Run(ctx context.Context, cfg config.Config, cfgMgr *config.Manager, st *store.Store, p api.Provider) error {
	if err := st.VerifySchemaVersion(ctx, cpg.ExpectedSchemaVersions); err != nil {
		return fmt.Errorf("schema version check failed: %w", err)
	}
	runLog.Info("schema version verified")

	// Surface capability entries the provider spec cannot honor (e.g. automatic instrumentation requested for a
	// type this provider cannot instrument). Warnings only, deliberately: each case degrades to a correct
	// per-workload report, and aborting here would produce a connector that never registers or heartbeats —
	// less observable than the misconfiguration itself. See spec.UnsupportedCapabilities for why this is not a
	// permission check.
	for _, w := range p.Spec().UnsupportedCapabilities(cfg.Capabilities) {
		runLog.Warn("capability cannot be honored as declared", "detail", w)
	}

	// The public OpAMP endpoint is the one piece of managed-Collector configuration that comes from the
	// Central installation rather than from the connector's own capability set, so a connector can be
	// fully allowed to manage a Collector and still have nowhere to point one. Warned rather than fatal,
	// and only when a Collector was actually asked for: the connector still instruments workloads, it
	// simply deploys no Collector — which is the safe direction, since one that reaches nothing would
	// hold a credential and report as running.
	for _, problem := range collectorgateway.CheckEndpoint(cfg.OpAMPEndpoint, p.Spec().CollectorTypes(cfg.Capabilities)) {
		runLog.Warn("managed Collector cannot be deployed", "code", problem.Code, "detail", problem.Message)
	}

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

	// A recreated connector CR has a new UID, and a managed Collector's credential is bound to the UID
	// at issue — so unlike its infrastructure, which is adopted by tag, the credential is not inherited.
	// Revoke the predecessor's here, where the connector first knows its
	// own identity.
	//
	// This is about the authority rather than the access: the gateway already refuses a credential whose
	// connector UID is not registered. But with the interim local mint the hash lives only in this
	// database, so a credential left live while the connector can still see it is one nothing will ever
	// be able to revoke.
	//
	// Gated on PlatformIDFromCR: under the ConnectorName fallback no live credential would match, and
	// the sweep would read that as "every credential belongs to a predecessor".
	if cfg.PlatformIDFromCR {
		revoked, err := st.RevokeCollectorCredentialsForRecreatedConnector(ctx, cfg.PlatformID, collectorcredential.ActorConnector)
		if err != nil {
			// Warned, not fatal. A connector that cannot sweep must still instrument workloads, the
			// access is already closed at the gateway, and the next startup retries.
			runLog.Warn("could not revoke credentials issued under a previous connector UID",
				"connector_uid", cfg.PlatformID, "revoked", len(revoked), "error", err)
		} else if len(revoked) > 0 {
			runLog.Info("revoked managed-Collector credentials from a previous connector incarnation",
				"connector_uid", cfg.PlatformID, "count", len(revoked))
		}
	}

	go coord.Start(ctx)

	startCollectorOpAMPServer(ctx, cfg, st, p, coord)

	// One agents-api executor backs both transports: the reverse WebSocket to Central and the local
	// odictl CLI endpoint.
	exec := gql.NewExecutor(coord)

	// Connect to Central over the reverse WebSocket and serve the agents-api there (same as the VM
	// agent). Central reaches the connector through this socket, not via the in-cluster Service.
	if cfg.CentralBackendURL != "" {
		centralClient := central.New(central.Config{
			BackendURL:    cfg.CentralBackendURL,
			ProxyID:       cfg.PlatformID,
			PlatformName:  cfg.ConnectorName,
			Version:       cfg.Version,
			SchemaVersion: connectorGraphQLSchemaVersion,
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

// startCollectorOpAMPServer starts the connector's OpAMP server, but only for a connector that actually
// has a managed Collector to configure.
//
// Gated on CollectorTypes rather than on the deployment feature: a MANUAL-mode Collector is configured
// over OpAMP too — the customer deployed it and Odigos still owns its configuration — so the config
// plane runs in both modes and only the deployer is automatic-only.
//
// The desired configuration comes from the provider, as an optional capability. A provider that declares
// a collectors type but cannot render one is a mismatch worth saying out loud rather than a listener that
// accepts connections and has nothing to give them.
func startCollectorOpAMPServer(ctx context.Context, cfg config.Config, st *store.Store, p api.Provider, coord *coordinator.Coordinator) {
	types := p.Spec().CollectorTypes(cfg.Capabilities)
	if len(types) == 0 {
		return
	}

	desired, ok := p.(opamp.Desired)
	if !ok {
		runLog.Warn("a managed-Collector capability is enabled but this provider renders no Collector "+
			"configuration, so no OpAMP server is started and no Collector will be configured",
			"provider", cfg.Provider, "types", len(types))
		return
	}

	srv := opamp.New(opamp.Self{
		ConnectorUID: cfg.PlatformID,
		Provider:     cfg.Provider,
		AccountID:    cfg.AccountID,
	}, st, desired)
	srv.PublicEndpoint = cfg.OpAMPEndpoint
	srv.Metrics = coord.MetricsCache()

	go func() {
		// Warned, not fatal. The OpAMP server is the Collectors' config plane; losing it must not take
		// workload instrumentation down with it, and a Collector that is already running keeps exporting
		// under its last configuration while this is fixed.
		if err := srv.Start(ctx); err != nil {
			runLog.Error("the managed-Collector OpAMP server stopped", "error", err)
		}
	}()
}
