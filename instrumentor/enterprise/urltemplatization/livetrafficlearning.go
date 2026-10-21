package urltemplatization

import (
	"context"
	"time"

	"github.com/odigos-io/odigos/api/k8sconsts"
	commonlogger "github.com/odigos-io/odigos/common/logger"
	"github.com/odigos-io/odigos/common/urltemplate"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
	"github.com/redis/go-redis/v9"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	commonurltemplatization "github.com/odigos-io/odigos-enterprise/common-enterprise/urltemplatization"
	learning "github.com/odigos-io/odigos-enterprise/common-enterprise/urltemplatization/learning"
)

const scanCount = 100

// LiveTrafficLearningJob periodically garbage-collects unmatched path examples
// that already match an accepted rule, then learns URL templatization rule
// suggestions from the remaining path counts in odigos-cache (Redis).
// Registered as a controller-runtime Runnable so it participates in manager
// lifecycle and leader election.
//
// When automaticRules is enabled, this job computes and logs suggested
// templates. Creating Actions from those suggestions is intentionally out of
// scope here.
type LiveTrafficLearningJob struct {
	// CacheEndpoint is the Redis address (host:port). Empty uses
	// k8sconsts.OdigosCacheEndpoint for the current Odigos namespace.
	CacheEndpoint string

	// Client is used to load existing rules and read learning settings from
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

	logger.Info("starting URL templatization live traffic learning job", "cacheEndpoint", endpoint)

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
			logger.Info("URL templatization live traffic learning job stopped")
			return nil
		}

		params := j.loadLearningParams(ctx)
		j.processUnmatchedPathKeys(ctx, params)

		timer := time.NewTimer(params.interval)
		select {
		case <-ctx.Done():
			// Stop may return false if the timer already fired; drain C so the
			// value is not left unread (this select took Done, not timer.C).
			if !timer.Stop() {
				<-timer.C
			}
			logger.Info("URL templatization live traffic learning job stopped")
			return nil
		case <-timer.C:
		}
	}
}

// processUnmatchedPathKeys iterates all unmatched-path Redis keys, garbage-collects
// paths covered by existing rules, then learns suggestions from what remains.
func (j *LiveTrafficLearningJob) processUnmatchedPathKeys(ctx context.Context, params liveTrafficLearningParams) {
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

			rawPathCount := len(fields)
			// Always GC matched paths, even when automatic rules / learning are off.
			j.gcMatchedPaths(ctx, logger, key, fields, existingRules)

			if !params.automaticRulesEnabled || len(fields) == 0 {
				continue
			}

			trie := learning.BuildPathTrie(fields)
			rules := learning.FindLiveTrafficLearningRules(trie, params.LiveTrafficLearningParams)
			if len(rules) == 0 {
				continue
			}

			templates := make([]string, len(rules))
			for i, rule := range rules {
				templates[i] = rule.Template
			}
			logger.Info("learned URL templatization rule suggestions",
				"key", key,
				"workload", wk,
				"pathCount", len(fields),
				"filteredOut", rawPathCount-len(fields),
				"totalObservations", trie.Root.TotalObservations,
				"suggestedRules", len(rules),
				"templates", templates,
			)
		}

		cursor = next
		if cursor == 0 {
			break
		}
	}
}
