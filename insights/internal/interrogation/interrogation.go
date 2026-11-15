// Package interrogation is the insights interrogation feature: a control loop
// that analyzes per-transaction call tries in ClickHouse with an LLM (via the
// in-cluster odigos-litellm), stores the recommended probes, and optionally
// reconciles InstrumentationRules to them.
//
// The caller runs this only on the insights leader replica, so analysis and
// rule writes stay single-writer. This package does not elect a leader.
package interrogation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sync/errgroup"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonlogger "github.com/odigos-io/odigos/common/logger"

	"github.com/odigos-io/odigos/insights/internal/interrogation/controllers"
	"github.com/odigos-io/odigos/insights/internal/interrogation/db/clickhouse"
	"github.com/odigos-io/odigos/insights/internal/interrogation/loop"
	"github.com/odigos-io/odigos/insights/internal/interrogation/model"
)

// Options configures the interrogation feature.
type Options struct {
	Log *commonlogger.OdigosLogger
	// ClickHouseDSN points at the interrogation database
	// (clickhouse://user:pass@host:9000/interrogation).
	ClickHouseDSN string
	// InsightsDatabase is the insights inventory database that holds
	// transactions.source_identity (usually odigos_insights).
	InsightsDatabase string
	// RedisEndpoint is the cacheDb Redis address (host:port) the loop flushes
	// call tries from (logical DB 1). When empty the flush is skipped.
	RedisEndpoint string
	// LiteLLMMasterKey authenticates to odigos-litellm. When empty the loop
	// still runs but skips model calls.
	LiteLLMMasterKey string
	// AutoApply writes parsed probes as InstrumentationRules instead of dry-run.
	AutoApply bool
	// MaxProbesPerWorkload caps distinct custom probes per workload. Zero uses
	// the loop default (20).
	MaxProbesPerWorkload int
}

// Run starts the InstrumentationRule cache and blocks in the analysis loop
// until ctx is cancelled or the controller manager fails. Call it only on the
// leader: losing leadership must cancel ctx so the loop and the cache stop.
func Run(ctx context.Context, opts Options) error {
	log := opts.Log
	if opts.ClickHouseDSN == "" {
		return errors.New("interrogation: missing ClickHouse DSN")
	}

	chClient, err := clickhouse.Open(opts.ClickHouseDSN, opts.InsightsDatabase)
	if err != nil {
		return fmt.Errorf("interrogation: open clickhouse: %w", err)
	}
	defer func() {
		if err := chClient.Close(); err != nil {
			log.Warn("failed to close interrogation clickhouse", "error", err)
		}
	}()

	// Buffered by one so a burst of rule events coalesces into one reconcile.
	ruleEvents := make(chan struct{}, 1)
	mgr, err := controllers.CreateManager(controllers.Options{
		Logger: commonlogger.ToLogr(),
		OnRuleChange: func() {
			select {
			case ruleEvents <- struct{}{}:
			default:
			}
		},
	})
	if err != nil {
		return fmt.Errorf("interrogation: create manager: %w", err)
	}

	modelCfg := model.Config{APIKey: opts.LiteLLMMasterKey}
	loopMgr := &loop.Manager{
		Client:               mgr.GetClient(),
		ClickHouse:           chClient,
		Log:                  log,
		Model:                model.New(modelCfg),
		ModelName:            model.LiteLLMModelName,
		AutoApply:            opts.AutoApply,
		MaxProbesPerWorkload: opts.MaxProbesPerWorkload,
		RuleEvents:           ruleEvents,
	}
	if modelCfg.Enabled() {
		log.Info("interrogation litellm configured",
			"url", model.LiteLLMServiceURL,
			"model_alias", model.LiteLLMModelName,
			"auto_apply", opts.AutoApply,
			"max_probes_per_workload", opts.MaxProbesPerWorkload,
		)
	} else {
		log.Warn("interrogation litellm master key missing; analysis will not call the model")
	}

	g, groupCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		log.Info("starting interrogation kube manager")
		if err := mgr.Start(groupCtx); err != nil {
			return fmt.Errorf("interrogation manager: %w", err)
		}
		return nil
	})

	g.Go(func() error {
		// Wait for the cache so List/Get in the loop hit memory.
		if ok := mgr.GetCache().WaitForCacheSync(groupCtx); !ok {
			return nil
		}
		log.Info("starting interrogation loop")
		waitForClickHouse(groupCtx, log, chClient)
		if opts.RedisEndpoint != "" {
			if flusher := openTrieFlusher(groupCtx, opts.RedisEndpoint, opts.ClickHouseDSN, log, mgr.GetClient()); flusher != nil {
				defer func() {
					if err := flusher.Close(); err != nil {
						log.Warn("interrogation trie flusher close failed", "error", err)
					}
				}()
				loopMgr.TrieFlusher = flusher
			}
		} else {
			log.Warn("interrogation redis endpoint unset; trie flusher disabled")
		}
		loopMgr.Run(groupCtx)
		return nil
	})

	return g.Wait()
}

// openTrieFlusher opens Redis + ClickHouse for the structure-only dump. It
// returns nil when either is unavailable, so the loop runs without flushing.
func openTrieFlusher(ctx context.Context, redisEndpoint, clickHouseDSN string, log *commonlogger.OdigosLogger, kube client.Client) *trieFlusher {
	rdb, err := openTracesRedis(redisEndpoint, log)
	if err != nil {
		log.Warn("interrogation trie flusher unavailable", "error", err)
		return nil
	}
	ch, err := openTracesClickHouse(ctx, clickHouseDSN, log)
	if err != nil {
		_ = rdb.Close()
		log.Warn("interrogation trie flusher clickhouse unavailable", "error", err)
		return nil
	}
	return &trieFlusher{redis: rdb, ch: ch, logger: log, kube: kube}
}

const (
	dbRetryInitial  = 2 * time.Second
	dbRetryStep     = 2 * time.Second
	dbRetryMax      = 15 * time.Second
	dbRetryLogEvery = 60 * time.Second
)

// waitForClickHouse retries Ping and SchemaReady until the migrate Job has
// created the interrogation tables, or ctx is cancelled.
func waitForClickHouse(ctx context.Context, log *commonlogger.OdigosLogger, ch *clickhouse.Client) {
	delay := dbRetryInitial
	var lastLog time.Time
	for attempt := 1; ; attempt++ {
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := ch.Ping(checkCtx)
		if err == nil {
			err = ch.SchemaReady(checkCtx)
		}
		cancel()
		if err == nil {
			log.Info("interrogation clickhouse ready", "attempts", attempt, "insights_database", ch.InsightsDatabase())
			return
		}
		now := time.Now()
		if lastLog.IsZero() || now.Sub(lastLog) >= dbRetryLogEvery {
			log.Warn("waiting for interrogation clickhouse", "attempt", attempt, "retry_in", delay.String(), "insights_database", ch.InsightsDatabase(), "error", err)
			lastLog = now
		}
		select {
		case <-ctx.Done():
			return
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
