package urltemplatization

import (
	"context"
	"time"

	"github.com/odigos-io/odigos/api/k8sconsts"
	"github.com/odigos-io/odigos/common"
	commonlogger "github.com/odigos-io/odigos/common/logger"
	"github.com/odigos-io/odigos/common/urltemplate"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
	"github.com/odigos-io/odigos/k8sutils/pkg/utils"
	"github.com/redis/go-redis/v9"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	commonurltemplatization "github.com/odigos-io/odigos-enterprise/common-enterprise/urltemplatization"
)

const (
	defaultLiveTrafficLearningInterval = 30 * time.Second

	scanCount = 100
)

// liveTrafficLearningParams holds resolved settings from effective config.
type liveTrafficLearningParams struct {
	interval time.Duration
}

// LiveTrafficLearningJob periodically garbage-collects unmatched path examples
// in odigos-cache (Redis) that already match an accepted URL templatization rule.
// Registered as a controller-runtime Runnable so it participates in manager
// lifecycle and leader election.
//
// Rule learning / automatic Actions are intentionally out of scope for this job
// for now; this runnable only reclaims Redis quota for paths covered by existing rules.
type LiveTrafficLearningJob struct {
	// CacheEndpoint is the Redis address (host:port). Empty uses
	// k8sconsts.OdigosCacheEndpoint for the current Odigos namespace.
	CacheEndpoint string

	// Client is used to load existing rules and read learningInterval from
	// effective config. Expected to be the manager's cached client.
	Client client.Client

	rdb *redis.Client
}

var _ manager.Runnable = (*LiveTrafficLearningJob)(nil)
var _ manager.LeaderElectionRunnable = (*LiveTrafficLearningJob)(nil)

func (j *LiveTrafficLearningJob) NeedLeaderElection() bool {
	return true
}

func (j *LiveTrafficLearningJob) Start(ctx context.Context) error {
	endpoint := j.CacheEndpoint
	if endpoint == "" {
		endpoint = k8sconsts.OdigosCacheEndpoint(env.GetCurrentNamespace())
	}

	logger := commonlogger.LoggerCompat().With("subsystem", "url-templatization-live-traffic-learning")

	logger.Info("starting URL templatization live traffic path-example GC job", "cacheEndpoint", endpoint)

	j.rdb = redis.NewClient(&redis.Options{
		Addr:                  endpoint,
		DialTimeout:           2 * time.Second,
		ReadTimeout:           2 * time.Second,
		WriteTimeout:          2 * time.Second,
		ContextTimeoutEnabled: true,
	})
	defer func() {
		if err := j.rdb.Close(); err != nil {
			logger.Error("failed to close redis client", "err", err)
		}
		j.rdb = nil
	}()

	for {
		if err := ctx.Err(); err != nil {
			logger.Info("URL templatization live traffic path-example GC job stopped")
			return nil
		}

		params := j.loadLearningParams(ctx)
		j.processUnmatchedPathKeys(ctx)

		timer := time.NewTimer(params.interval)
		select {
		case <-ctx.Done():
			// Stop may return false if the timer already fired; drain C so the
			// value is not left unread (this select took Done, not timer.C).
			if !timer.Stop() {
				<-timer.C
			}
			logger.Info("URL templatization live traffic path-example GC job stopped")
			return nil
		case <-timer.C:
		}
	}
}

func (j *LiveTrafficLearningJob) loadLearningParams(ctx context.Context) liveTrafficLearningParams {
	params := liveTrafficLearningParams{
		interval: defaultLiveTrafficLearningInterval,
	}
	if j.Client == nil {
		return params
	}
	cfg, err := utils.GetCurrentOdigosConfiguration(ctx, j.Client)
	if err != nil {
		return params
	}
	if d := learningIntervalFromConfig(&cfg); d > 0 {
		params.interval = d
	}
	return params
}

func learningIntervalFromConfig(cfg *common.OdigosConfiguration) time.Duration {
	if cfg == nil ||
		cfg.CardinalityControl == nil ||
		cfg.CardinalityControl.UrlTemplatization == nil ||
		cfg.CardinalityControl.UrlTemplatization.LiveTrafficLearning == nil {
		return 0
	}
	raw := cfg.CardinalityControl.UrlTemplatization.LiveTrafficLearning.LearningInterval
	if raw == "" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// processUnmatchedPathKeys iterates all unmatched-path Redis keys and runs the
// per-key steps on each one.
func (j *LiveTrafficLearningJob) processUnmatchedPathKeys(ctx context.Context) {
	logger := commonlogger.LoggerCompat().With("subsystem", "url-templatization-live-traffic-learning")

	existingRulesByWorkload := make(map[string]map[int][]urltemplate.PathRule)

	var cursor uint64
	for {
		keys, next, err := j.rdb.Scan(ctx, cursor, commonurltemplatization.UnmatchedPathsRedisKeyPattern, scanCount).Result()
		if err != nil {
			logger.Error("failed to scan unmatched path keys", "err", err)
			return
		}

		for _, key := range keys {
			meta, err := parseUnmatchedRedisKey(key)
			if err != nil {
				logger.Error("failed to parse unmatched redis key", "key", key, "err", err)
				continue
			}

			fields, err := j.rdb.HGetAll(ctx, key).Result()
			if err != nil {
				logger.Error("failed to fetch paths for key", "key", key, "err", err)
				continue
			}

			wk := workloadKey(meta.Workload)
			existingRules, ok := existingRulesByWorkload[wk]
			if !ok {
				existingRules, err = loadExistingPathRules(ctx, j.Client, meta.Workload)
				if err != nil {
					logger.Error("failed to load existing templatization rules", "workload", wk, "err", err)
					existingRules = nil
				}
				existingRulesByWorkload[wk] = existingRules
			}

			j.gcMatchedPaths(ctx, logger, key, fields, existingRules)
		}

		cursor = next
		if cursor == 0 {
			break
		}
	}
}
