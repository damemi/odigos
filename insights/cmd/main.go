// Command insights is the entrypoint for the odigos-enterprise-insights
// service. It boots the HTTP API (consumed by the UI) and the OTLP gRPC
// receiver (fed by the Odigos gateway), both backed by ClickHouse.
package main

import (
	"context"
	"errors"
	"log/slog"
	stdhttp "net/http"

	// Registers the /debug/pprof/* handlers.
	_ "net/http/pprof"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/odigos-io/odigos/common"
	commonlogger "github.com/odigos-io/odigos/common/logger"

	apihttp "github.com/odigos-io/odigos/insights/internal/api/http"
	"github.com/odigos-io/odigos/insights/internal/cache"
	"github.com/odigos-io/odigos/insights/internal/db/clickhouse"
	"github.com/odigos-io/odigos/insights/internal/db/clickhouse/migrate"
	"github.com/odigos-io/odigos/insights/internal/engine"
	"github.com/odigos-io/odigos/insights/internal/leader"
	"github.com/odigos-io/odigos/insights/internal/metrics"
	"github.com/odigos-io/odigos/insights/internal/notify"
	"github.com/odigos-io/odigos/insights/internal/notify/sinks"
	"github.com/odigos-io/odigos/insights/internal/otlp"
	"github.com/odigos-io/odigos/insights/internal/repo"
	"github.com/odigos-io/odigos/odigosauth"
)

const (
	componentName = "insights"

	httpAddr = ":8080"
	otlpAddr = ":4317"

	// pprofPort exposes the net/http/pprof endpoints on a dedicated port so
	// profiles can be pulled via a port-forward. 6060 matches the port used by
	// the other Odigos services.
	pprofPort = 6060

	// envClickHouseDSN names the env var holding the ClickHouse connection URI
	// (clickhouse://user:pass@host:9000/db). The Helm chart builds it from the
	// bundled ClickHouse Secret.
	envClickHouseDSN = "CLICKHOUSE_DSN"

	// leaderLease elects the one replica that runs the singleton jobs (findings
	// notifier, recommendation sweeps). The chart grants leases get/create/update.
	leaderLease = "odigos-insights-leader"
)

// getLogLevel reads ODIGOS_LOG_LEVEL, defaulting to "info" when unset.
func getLogLevel() string {
	if lvl := os.Getenv("ODIGOS_LOG_LEVEL"); lvl != "" {
		return lvl
	}
	return "info"
}

func main() {
	// `insights migrate` applies pending schema migrations and exits. It runs
	// once per rollout from the Helm migration Job (never from serving replicas)
	// so replicas can scale without racing on DDL. Handle it before any serving
	// setup and before the enterprise-token check.
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		os.Exit(runMigrate())
	}

	commonlogger.Init(getLogLevel(), componentName)
	log := commonlogger.LoggerCompat()
	log.Info("starting odigos insights service")

	// Hard-fail at startup if the enterprise token is missing or invalid.
	odigosauth.AssertEnterpriseOdigosToken(log)

	dsn := os.Getenv(envClickHouseDSN)
	if dsn == "" {
		log.Error("missing required env var", "var", envClickHouseDSN)
		os.Exit(1)
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// pprof server for CPU/heap/goroutine profiling.
	go func() {
		if err := common.StartPprofServer(rootCtx, commonlogger.ToLogr(), pprofPort); err != nil {
			log.Error("failed to start pprof server", "error", err)
		} else {
			log.Info("pprof server exited")
		}
	}()

	// Open the repository. The pool connects lazily, so we can build the router
	// and serve health checks immediately, then wait for the DB without crashing.
	r, schemaReady, closeRepo, err := openRepo(dsn, log)
	if err != nil {
		log.Error("failed to open repository", "error", err)
		os.Exit(1)
	}
	defer closeRepo()

	// Build the router up front. Its two health endpoints report correct status
	// regardless of DB state: /healthz (liveness) is always 200 while the process
	// is up, and /readyz (readiness) is 503 until the DB is reachable and the
	// schema is initialized.
	router, apiHandle := apihttp.NewRouter(r, log)

	// Findings integrations: the backend catalog serves the API on
	// every replica; the dispatcher that actually sends runs on the leader only
	// (see runNotifier below, after the schema is ready).
	integrationRegistry := sinks.NewRegistry(notify.DefaultDeps())
	apiHandle.SetIntegrationRegistry(integrationRegistry)

	httpSrv := &stdhttp.Server{
		Addr:              httpAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}
	httpErrCh := make(chan error, 1)
	go func() {
		log.Info("http server listening", "addr", httpAddr)
		if err := httpSrv.ListenAndServe(); err != nil && err != stdhttp.ErrServerClosed {
			httpErrCh <- err
		}
		close(httpErrCh)
	}()

	// Wait for the DB and the migration Job's schema (retrying). Liveness is
	// already served, so a slow DB or not-yet-run migration keeps /readyz at 503
	// without killing the pod. Returns an error only on a shutdown signal mid-wait.
	if err := waitForSchemaReady(rootCtx, log, r, schemaReady); err != nil {
		log.Info("shutting down before database became ready", "reason", err)
		shutdownHTTP(httpSrv, log)
		return
	}

	// Load the per-class learning policies into an in-memory snapshot so the
	// promotion gate never hits the DB on the per-trace path. A background
	// reloader refreshes it so edits apply at runtime without a restart.
	learningCfg, err := engine.LoadLearningConfig(rootCtx, r)
	if err != nil {
		log.Warn("failed to load learning policies; classes fall back to the count-only default", "error", err)
	}
	go runLearningPolicyReloader(rootCtx, log, r, learningCfg)

	// Same pattern for the scoped alert policies: an in-memory snapshot resolves
	// the policy off the per-trace path, kept fresh by a background reloader.
	alertCfg, err := engine.LoadAlertConfig(rootCtx, r)
	if err != nil {
		log.Warn("failed to load alert policies; anomaly alerting is off until the next reload", "error", err)
	}

	eng := engine.New(r, log, learningCfg, alertCfg)
	go runAlertPolicyReloader(rootCtx, log, eng)

	// Same pattern for service guardrails (operator-declared L7 segmentation):
	// an in-memory snapshot resolves a service's guardrail off the per-trace
	// path, kept fresh by a background reloader.
	guardrailCfg, err := engine.LoadGuardrailConfig(rootCtx, r)
	if err != nil {
		log.Warn("failed to load service guardrails; guardrail enforcement is off until the next reload", "error", err)
	}
	eng.SetGuardrailConfig(guardrailCfg)
	go runGuardrailPolicyReloader(rootCtx, log, eng)

	systemCfg, err := engine.LoadSystemConfig(rootCtx, r)
	if err != nil {
		log.Warn("failed to load system settings; using compiled defaults until the next reload", "error", err)
	}
	eng.SetSystemConfig(systemCfg)
	if err := eng.SyncTransactionAdmission(rootCtx); err != nil {
		log.Warn("failed to seed the per-service transaction index; the cap counts only this replica's admissions until the next reload", "error", err)
	}
	go runSystemSettingsReloader(rootCtx, log, eng)

	// Engine-backed admin for REST writes.
	apiHandle.SetAdmin(eng)

	// The engine learns baselines in memory to keep the DB off the per-trace
	// path. This background flusher persists that learning in batches on a timer
	// (and once more on shutdown), so DB writes track the flush interval, not the
	// trace rate.
	flusherCtx, stopFlusher := context.WithCancel(rootCtx)
	flusherDone := make(chan struct{})
	go func() {
		eng.RunBaselineFlusher(flusherCtx)
		close(flusherDone)
	}()
	// Same idea for sample traces (examples and finding evidence): the ingest
	// path only queues them, and this goroutine writes them in batches.
	sampleWriterDone := make(chan struct{})
	go func() {
		eng.RunSampleWriter(flusherCtx)
		close(sampleWriterDone)
	}()
	// Singleton jobs run only on the replica holding the Lease: the findings
	// notifier (delivering a finding twice is wrong) and the recommendation
	// miner (one miner serves the queued requests and the periodic sweeps;
	// requests handled on a follower are picked up by the leader's next sweep).
	go runLeaderJobs(rootCtx, log, r, integrationRegistry, eng)

	otlpSrv := otlp.NewServer(otlpAddr, eng, log)
	otlpErrCh, err := otlpSrv.Start()
	if err != nil {
		log.Error("failed to start OTLP server", "error", err)
		shutdownHTTP(httpSrv, log)
		os.Exit(1)
	}

	// Token expiration is a fatal shutdown trigger, like other enterprise services.
	expirationCh, tokenErr := odigosauth.TokenExpirationChannel()
	if tokenErr != nil {
		log.Warn("token expiration channel unavailable", "error", tokenErr)
		expirationCh = nil
	}

	select {
	case <-rootCtx.Done():
		log.Info("shutdown signal received")
	case err := <-httpErrCh:
		log.Error("http server stopped", "error", err)
	case err := <-otlpErrCh:
		log.Error("otlp server stopped", "error", err)
	case <-expirationCh:
		log.Info("enterprise token expired; shutting down. contact Odigos support to renew.")
	}

	// Stop accepting traces first, then drain the last baseline deltas and
	// queued sample traces before exiting (stopFlusher triggers the final flush
	// of both RunBaselineFlusher and RunSampleWriter).
	otlpSrv.Stop()
	stopFlusher()
	<-flusherDone
	<-sampleWriterDone
	shutdownHTTP(httpSrv, log)
	log.Info("insights service exited")
}

// DB connection retry policy. The backoff grows by dbRetryStep each failed
// attempt and caps at dbRetryMax, so a database that comes up late or briefly
// disappears never crashes the service.
const (
	dbRetryInitial = 2 * time.Second
	dbRetryStep    = 2 * time.Second
	dbRetryMax     = 15 * time.Second

	// dbRetryLogEvery bounds retry logging while the DB is down: the first
	// failure logs immediately, then at most one line per interval, so the log
	// isn't flooded on every attempt.
	dbRetryLogEvery = 60 * time.Second
)

// openRepo constructs the ClickHouse-backed repository without requiring the DB
// to be up (the connection is lazy). It returns the repo, a schemaReady func
// that gates serving on the migration Job having run, and a close func.
func openRepo(dsn string, log *commonlogger.OdigosLogger) (repo.Repository, func(context.Context) error, func(), error) {
	r, err := clickhouse.Open(dsn, engine.NewBaselineCodec())
	if err != nil {
		return nil, nil, func() {}, err
	}
	// schemaReady and closeRepo capture the concrete *clickhouse.Repo, while
	// callers use the instrumented+cached wrapper for queries. The service does
	// not create the schema (that's the migration Job's job, see migrate.Apply);
	// schemaReady keeps the app not-ready until that Job has run.
	schemaReady := func(ctx context.Context) error {
		return r.SchemaReady(ctx)
	}
	closeRepo := func() {
		if err := r.Close(); err != nil {
			log.Warn("failed to close clickhouse", "error", err)
		}
	}
	// Order matters: the policy cache wraps the metrics layer, which wraps the
	// real DB repo, so a cache hit returns before the metrics layer and the
	// DB-call metric counts only true misses. Transaction identity is resolved
	// in-memory by the engine and write-behind flushed - no per-trace txn cache.
	return cache.WrapPolicies(metrics.WrapRepo(r)), schemaReady, closeRepo, nil
}

// waitForSchemaReady blocks until the DB is reachable AND the migration Job has
// applied the schema (both retried with backoff). Ping alone isn't enough: the
// bundled ClickHouse can be up while the migration Job is still running. Returns
// the context error only when ctx is cancelled mid-wait.
func waitForSchemaReady(
	ctx context.Context,
	log *commonlogger.OdigosLogger,
	r repo.Repository,
	schemaReady func(context.Context) error,
) error {
	delay := dbRetryInitial
	var lastLog time.Time
	for attempt := 1; ; attempt++ {
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := r.Ping(checkCtx)
		if err == nil {
			err = schemaReady(checkCtx)
		}
		cancel()
		if err == nil {
			log.Info("database repository ready", "attempts", attempt)
			return nil
		}
		now := time.Now()
		if lastLog.IsZero() || now.Sub(lastLog) >= dbRetryLogEvery {
			log.Warn("waiting for database and schema migrations", "attempt", attempt, "retry_in", delay.String(), "error", err)
			lastLog = now
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay < dbRetryMax {
			delay += dbRetryStep
			if delay > dbRetryMax {
				delay = dbRetryMax
			}
		}
	}
}

// runMigrate applies pending schema migrations and returns a process exit code.
// It backs the `insights migrate` entrypoint, run once per rollout by the Helm
// migration Job. ClickHouse may still be starting, so it retries with the
// service's backoff until migrations succeed or the process is signalled; the
// Job's activeDeadlineSeconds bounds a genuinely stuck run.
func runMigrate() int {
	commonlogger.Init(getLogLevel(), componentName)
	log := commonlogger.LoggerCompat()

	dsn := os.Getenv(envClickHouseDSN)
	if dsn == "" {
		log.Error("missing required env var", "var", envClickHouseDSN)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	delay := dbRetryInitial
	var lastLog time.Time
	for attempt := 1; ; attempt++ {
		if err := migrate.Apply(dsn); err == nil {
			log.Info("schema migrations applied", "attempts", attempt)
			return 0
		} else {
			now := time.Now()
			if lastLog.IsZero() || now.Sub(lastLog) >= dbRetryLogEvery {
				log.Warn("migration failed, retrying (clickhouse may be starting)", "attempt", attempt, "retry_in", delay.String(), "error", err)
				lastLog = now
			}
		}

		select {
		case <-ctx.Done():
			log.Error("aborting migrations before completion", "reason", ctx.Err())
			return 1
		case <-time.After(delay):
		}
		if delay < dbRetryMax {
			delay += dbRetryStep
			if delay > dbRetryMax {
				delay = dbRetryMax
			}
		}
	}
}

// runLearningPolicyReloader periodically refreshes the in-memory learning-policy
// snapshot so operator edits take effect without a restart. A failed reload
// leaves the previous snapshot in place.
func runLearningPolicyReloader(
	ctx context.Context,
	log *commonlogger.OdigosLogger,
	r repo.Repository,
	cfg *engine.LearningConfig,
) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := cfg.Reload(ctx, r); err != nil {
				log.Warn("failed to reload learning policies; keeping previous snapshot", "error", err)
			}
		}
	}
}

// runAlertPolicyReloader periodically refreshes the in-memory alert-policy scope
// snapshot so operator edits and new overrides take effect without a restart. A
// failed reload leaves the previous snapshot in place.
func runAlertPolicyReloader(
	ctx context.Context,
	log *commonlogger.OdigosLogger,
	eng *engine.Engine,
) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := eng.ReloadAlertConfig(ctx); err != nil {
				log.Warn("failed to reload alert policies; keeping previous snapshot", "error", err)
			}
		}
	}
}

// runGuardrailPolicyReloader periodically refreshes the in-memory
// service-guardrail scope snapshot so operator edits take effect without a
// restart. A failed reload leaves the previous snapshot in place.
func runGuardrailPolicyReloader(
	ctx context.Context,
	log *commonlogger.OdigosLogger,
	eng *engine.Engine,
) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := eng.ReloadGuardrailConfig(ctx); err != nil {
				log.Warn("failed to reload service guardrails; keeping previous snapshot", "error", err)
			}
		}
	}
}

func runSystemSettingsReloader(
	ctx context.Context,
	log *commonlogger.OdigosLogger,
	eng *engine.Engine,
) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := eng.ReloadSystemConfig(ctx); err != nil {
				log.Warn("failed to reload system settings; keeping previous snapshot", "error", err)
			}
		}
	}
}

// runLeaderJobs runs the singleton jobs on the replica holding the leader
// Lease, each in its own goroutine so a long mining sweep never delays a
// notifier tick. Losing the lease cancels them all. Outside a cluster (no
// in-cluster credentials) they run unconditionally, which is the local
// single-process case.
func runLeaderJobs(ctx context.Context, log *commonlogger.OdigosLogger, r repo.Repository, reg *notify.Registry, eng *engine.Engine) {
	dispatcher := notify.NewDispatcher(r, reg, slog.New(commonlogger.NewSlogHandler("notifier")), leader.Identity(), notify.PollInterval, notify.DefaultOverlap)
	jobs := []func(context.Context){dispatcher.Run, eng.RunRecommendationMiner}
	run := func(ctx context.Context) {
		var wg sync.WaitGroup
		for _, job := range jobs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				job(ctx)
			}()
		}
		wg.Wait()
	}

	elector, err := leader.New(leader.Config{LeaseName: leaderLease}, slog.New(commonlogger.NewSlogHandler("leader")))
	switch {
	case err == nil:
		elector.Run(ctx, run, metrics.SetLeader)
	case errors.Is(err, leader.ErrNotInCluster):
		log.Info("leader jobs running without leader election (not in a cluster)")
		metrics.SetLeader(true)
		run(ctx)
	default:
		log.Error("leader jobs disabled: leader election unavailable", "error", err)
	}
}

// shutdownHTTP gracefully shuts the HTTP server down within a 10s deadline.
func shutdownHTTP(srv *stdhttp.Server, log *commonlogger.OdigosLogger) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("http server shutdown error", "error", err)
	}
}
