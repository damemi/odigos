// connector-opamp-gateway is the public OpAMP endpoint for every remotely managed Odigos Collector.
//
// One endpoint serves the whole fleet across AWS, Azure and GCP. Each connection is authenticated by
// the credential the Collector presents and proxied to the connector that owns that Collector's cloud
// account. See collector/managed/OPAMP_ROUTING.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cloudconnectorsv1 "github.com/odigos-io/odigos/api/cloud-connectors/v1alpha1"

	"github.com/odigos-io/odigos-enterprise/connector-opamp-gateway/internal/config"
	"github.com/odigos-io/odigos-enterprise/connector-opamp-gateway/internal/gateway"
	"github.com/odigos-io/odigos-enterprise/connector-opamp-gateway/internal/registry"
	"github.com/odigos-io/odigos-enterprise/connector-opamp-gateway/internal/store"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorgateway"
	"github.com/odigos-io/odigos-enterprise/connectors/postgres"
)

// shutdownGrace bounds the drain after open sessions have been closed. Short on purpose: the sessions
// are already gone by then, so this only covers in-flight handshakes.
const shutdownGrace = 15 * time.Second

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	if err := run(log); err != nil {
		log.Error("connector-opamp-gateway exited", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := openPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	// Same fail-fast contract every connector runtime honors: the gateway never migrates, it verifies.
	// A gateway running against a schema without collector_credentials can only answer 503, and doing
	// that silently would look like a fleet-wide credential problem.
	if err := verifySchema(ctx, pool); err != nil {
		return err
	}

	credentials := store.NewCredentials(pool)
	connectors := registry.New()

	cache, err := newCache(cfg.Namespace)
	if err != nil {
		return err
	}
	if err := connectors.Start(ctx, cache); err != nil {
		return err
	}
	go func() {
		if err := cache.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("connector watch stopped", "error", err)
		}
	}()
	if !cache.WaitForCacheSync(ctx) {
		return fmt.Errorf("connector watch did not sync")
	}
	connectors.MarkSynced()
	log.Info("connector watch synced", "namespace", namespaceLabel(cfg.Namespace))

	handler := gateway.New(credentials, connectors, connectors.Upstream, log)

	revalidator := &gateway.Revalidator{
		Credentials: credentials,
		Connectors:  connectors,
		Sessions:    handler.SessionRegistry(),
		Interval:    cfg.RevalidateInterval,
		Log:         log,
	}
	go revalidator.Run(ctx)

	var ready atomic.Bool
	mux := http.NewServeMux()
	gateway.RegisterCollectorRoutes(mux, cfg.OpAMPPath, handler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		// Readiness is "can this replica authenticate", not "is the process up". A gateway that cannot
		// reach the credential store answers 503 to every Collector, so it should leave the edge's
		// rotation rather than absorb connections it will refuse.
		if !ready.Load() || !connectors.HasSynced() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		pingCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := credentials.Ping(pingCtx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	server := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: mux,
		// No ReadTimeout or WriteTimeout: an OpAMP WebSocket is long-lived and either would sever it
		// mid-session. ReadHeaderTimeout still bounds a client that opens a connection and stalls
		// before sending headers, which is the case those timeouts would otherwise be protecting.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Info("serving the managed-Collector OpAMP endpoint", "addr", cfg.ListenAddr, "path", cfg.OpAMPPath,
			"revalidate_interval", cfg.RevalidateInterval.String())
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()
	ready.Store(true)

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	ready.Store(false)
	log.Info("shutting down", "open_sessions", handler.SessionRegistry().Len())

	// Close the WebSockets first. Shutdown waits for active connections, and every open OpAMP session
	// is one — waiting for them would burn the whole grace period and end in a SIGKILL anyway, while
	// the Collectors that could have reconnected elsewhere sat idle.
	closed := handler.SessionRegistry().CloseAll()

	drainCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := server.Shutdown(drainCtx); err != nil {
		log.Warn("shutdown did not drain cleanly", "error", err, "sessions_closed", closed)
	}
	log.Info("stopped", "sessions_closed", closed)
	return nil
}

func openPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		// Never wrap the DSN into the message: it carries the database password.
		return nil, fmt.Errorf("DATABASE_URL is not a valid PostgreSQL DSN")
	}
	poolCfg.MaxConns = config.MaxDBConns

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("connecting to the credential store: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("credential store unreachable: %w", err)
	}
	return pool, nil
}

// verifySchema holds the gateway to the same schema contract the connector runtimes verify, against
// the same marker table.
func verifySchema(ctx context.Context, pool *pgxpool.Pool) error {
	want := postgres.ExpectedSchemaVersions[postgres.SchemaPlatform]

	var got string
	q := `SELECT version FROM ` + postgres.TableSchemaVersions + ` WHERE schema_name = $1`
	if err := pool.QueryRow(ctx, q, postgres.SchemaPlatform).Scan(&got); err != nil {
		return fmt.Errorf("reading the %s schema version (migrations may not have run): %w", postgres.SchemaPlatform, err)
	}
	if got != want {
		return fmt.Errorf("%s is at version %q, this build requires %q — run the connector-postgres migrations",
			postgres.SchemaPlatform, got, want)
	}
	return nil
}

func newCache(namespace string) (ctrlcache.Cache, error) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := cloudconnectorsv1.AddToScheme(scheme); err != nil {
		return nil, err
	}

	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("no Kubernetes config: %w", err)
	}

	opts := ctrlcache.Options{Scheme: scheme}
	if namespace != "" {
		// Namespace-scoped by default so the gateway's RBAC can be a Role rather than a ClusterRole.
		opts.DefaultNamespaces = map[string]ctrlcache.Config{namespace: {}}
	}
	// Cache only the Services that actually route to a connector. Without this the gateway holds every
	// Service in the namespace in memory and wakes on every unrelated edit.
	routable, err := labels.NewRequirement(collectorgateway.LabelConnectorID, selection.Exists, nil)
	if err != nil {
		return nil, err
	}
	opts.ByObject = map[client.Object]ctrlcache.ByObject{
		&corev1.Service{}: {Label: labels.NewSelector().Add(*routable)},
	}
	return ctrlcache.New(restCfg, opts)
}

func namespaceLabel(ns string) string {
	if ns == "" {
		return "<cluster-wide>"
	}
	return ns
}
