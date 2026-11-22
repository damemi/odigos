// Package autopilot is the insights autopilot feature: a control loop
// that analyzes per-transaction call tries with an LLM (via the
// in-cluster odigos-litellm), stores the recommended probes, and optionally
// reconciles InstrumentationRules to them.
//
// The caller runs this only on the insights leader replica, so analysis and
// rule writes stay single-writer. This package does not elect a leader.
package autopilot

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sync/errgroup"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonlogger "github.com/odigos-io/odigos/common/logger"

	"github.com/odigos-io/odigos/insights/internal/autopilot/controllers"
	"github.com/odigos-io/odigos/insights/internal/autopilot/loop"
	"github.com/odigos-io/odigos/insights/internal/autopilot/model"
	"github.com/odigos-io/odigos/insights/internal/autopilot/store"
)

// Options configures the autopilot feature.
type Options struct {
	Log *commonlogger.OdigosLogger
	// PostgresDSN points at the insights Postgres, which holds the autopilot
	// tables next to the insights transaction inventory.
	PostgresDSN string
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
	if opts.PostgresDSN == "" {
		return errors.New("autopilot: missing Postgres DSN")
	}

	st, err := store.Open(opts.PostgresDSN)
	if err != nil {
		return fmt.Errorf("autopilot: open store: %w", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			log.Warn("failed to close autopilot store", "error", err)
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
		return fmt.Errorf("autopilot: create manager: %w", err)
	}

	modelCfg := model.Config{APIKey: opts.LiteLLMMasterKey}
	loopMgr := &loop.Manager{
		Client:               mgr.GetClient(),
		Store:                st,
		Log:                  log,
		Model:                model.New(modelCfg),
		ModelName:            model.LiteLLMModelName,
		AutoApply:            opts.AutoApply,
		MaxProbesPerWorkload: opts.MaxProbesPerWorkload,
		RuleEvents:           ruleEvents,
	}
	if modelCfg.Enabled() {
		log.Info("autopilot litellm configured",
			"url", model.LiteLLMServiceURL,
			"model_alias", model.LiteLLMModelName,
			"auto_apply", opts.AutoApply,
			"max_probes_per_workload", opts.MaxProbesPerWorkload,
		)
	} else {
		log.Warn("autopilot litellm master key missing; analysis will not call the model")
	}

	g, groupCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		log.Info("starting autopilot kube manager")
		if err := mgr.Start(groupCtx); err != nil {
			return fmt.Errorf("autopilot manager: %w", err)
		}
		return nil
	})

	g.Go(func() error {
		// Wait for the cache so List/Get in the loop hit memory.
		if ok := mgr.GetCache().WaitForCacheSync(groupCtx); !ok {
			return nil
		}
		log.Info("starting autopilot loop")
		waitForStore(groupCtx, log, st)
		if opts.RedisEndpoint != "" {
			flusher := newTrieFlusher(opts.RedisEndpoint, st, log, mgr.GetClient())
			defer func() {
				if err := flusher.Close(); err != nil {
					log.Warn("autopilot trie flusher close failed", "error", err)
				}
			}()
			loopMgr.TrieFlusher = flusher
		} else {
			log.Warn("autopilot redis endpoint unset; trie flusher disabled")
		}
		loopMgr.Run(groupCtx)
		return nil
	})

	return g.Wait()
}

// newTrieFlusher builds the Redis client for the structure-only dump without
// dialing: an unreachable Redis at startup only fails the current tick's Flush
// (logged), and the next tick retries.
func newTrieFlusher(redisEndpoint string, st *store.Client, log *commonlogger.OdigosLogger, kube client.Client) *trieFlusher {
	return &trieFlusher{redis: newTracesRedis(redisEndpoint, log), store: st, logger: log, kube: kube}
}

const (
	dbRetryInitial  = 2 * time.Second
	dbRetryStep     = 2 * time.Second
	dbRetryMax      = 15 * time.Second
	dbRetryLogEvery = 60 * time.Second
)

// waitForStore retries Ping and SchemaReady until the migrate Job has
// created the autopilot tables, or ctx is cancelled.
func waitForStore(ctx context.Context, log *commonlogger.OdigosLogger, st *store.Client) {
	delay := dbRetryInitial
	var lastLog time.Time
	for attempt := 1; ; attempt++ {
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := st.Ping(checkCtx)
		if err == nil {
			err = st.SchemaReady(checkCtx)
		}
		cancel()
		if err == nil {
			log.Info("autopilot store ready", "attempts", attempt)
			return
		}
		now := time.Now()
		if lastLog.IsZero() || now.Sub(lastLog) >= dbRetryLogEvery {
			log.Warn("waiting for autopilot store", "attempt", attempt, "retry_in", delay.String(), "error", err)
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
