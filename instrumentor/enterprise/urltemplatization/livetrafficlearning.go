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
// When automaticRules is enabled, this job learns suggested templates and
// persists them on a URL templatization Action per namespace. Each workload
// gets a rule scoped to that workload, with only the templates learned for it.
type LiveTrafficLearningJob struct {
	// CacheEndpoint is the Redis address (host:port). Empty uses
	// k8sconsts.OdigosCacheEndpoint for the current Odigos namespace.
	CacheEndpoint string

	// Client is used to load existing rules and read learning settings from
	// effective config. Expected to be the manager's cached client.
	Client client.Client

	// logger is set by NewLiveTrafficLearningJob and is non-nil for the job's lifetime.
	logger *commonlogger.OdigosLogger

	rdb *redis.Client
}

// NewLiveTrafficLearningJob returns a job that learns URL templates from live traffic.
// logger must be non-nil; a nil logger panics. The subsystem is attached here so
// callers do not repeat it.
func NewLiveTrafficLearningJob(c client.Client, logger *commonlogger.OdigosLogger) *LiveTrafficLearningJob {
	if logger == nil {
		panic("url templatization live traffic learning job requires a logger")
	}
	return &LiveTrafficLearningJob{
		Client: c,
		logger: logger.With("subsystem", "url-templatization-live-traffic-learning"),
	}
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

	j.logger.Info("starting URL templatization live traffic learning job", "cacheEndpoint", endpoint)

	j.rdb = redis.NewClient(&redis.Options{
		Addr:                  endpoint,
		DialTimeout:           2 * time.Second,
		ReadTimeout:           2 * time.Second,
		WriteTimeout:          2 * time.Second,
		ContextTimeoutEnabled: true,
	})
	defer func() {
		if err := j.rdb.Close(); err != nil {
			j.logger.Error("failed to close redis client", "err", err)
		}
		j.rdb = nil
	}()

	for {
		if err := ctx.Err(); err != nil {
			j.logger.Info("URL templatization live traffic learning job stopped")
			return nil
		}

		params := j.loadLearningParams(ctx)
		learnedByWorkload := j.processUnmatchedPathKeys(ctx, params)
		err := j.persistLearnedRules(ctx, learnedByWorkload)
		if err != nil {
			j.logger.Error("failed to persist learned URL templatization rules", "err", err)
		}

		timer := time.NewTimer(params.interval)
		select {
		case <-ctx.Done():
			// Stop may return false if the timer already fired; drain C so the
			// value is not left unread (this select took Done, not timer.C).
			if !timer.Stop() {
				<-timer.C
			}
			j.logger.Info("URL templatization live traffic learning job stopped")
			return nil
		case <-timer.C:
		}
	}
}

// processUnmatchedPathKeys iterates all unmatched-path Redis keys, garbage-collects
// paths covered by existing rules, then learns suggestions from what remains.
// Learned rules from every key of a workload are returned together, keyed by that workload.
func (j *LiveTrafficLearningJob) processUnmatchedPathKeys(ctx context.Context, params liveTrafficLearningParams) map[k8sconsts.PodWorkload][]learning.LearnedRule {
	existingRulesByWorkload := make(map[string]map[int][]urltemplate.PathRule)
	learnedByWorkload := make(map[k8sconsts.PodWorkload][]learning.LearnedRule)

	var cursor uint64
	for {
		keys, next, err := j.rdb.Scan(ctx, cursor, commonurltemplatization.UnmatchedPathsRedisKeyPattern, scanCount).Result()
		if err != nil {
			j.logger.Error("failed to scan unmatched path keys", "err", err)
			return learnedByWorkload
		}

		for _, key := range keys {
			meta, err := parseUnmatchedRedisKey(key)
			if err != nil {
				j.logger.Error("failed to parse unmatched redis key", "key", key, "err", err)
				continue
			}

			fields, err := j.rdb.HGetAll(ctx, key).Result()
			if err != nil {
				j.logger.Error("failed to fetch paths for key", "key", key, "err", err)
				continue
			}

			wk := workloadKey(meta.Workload)
			existingRules, ok := existingRulesByWorkload[wk]
			if !ok {
				existingRules, err = loadExistingPathRules(ctx, j.Client, meta.Workload)
				if err != nil {
					j.logger.Error("failed to load existing templatization rules", "workload", wk, "err", err)
					existingRules = nil
				}
				existingRulesByWorkload[wk] = existingRules
			}

			rawPathCount := len(fields)
			// Always GC matched paths, even when automatic rules / learning are off.
			j.gcMatchedPaths(ctx, key, fields, existingRules)

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
			j.logger.Info("learned URL templatization rule suggestions",
				"key", key,
				"workload", wk,
				"pathCount", len(fields),
				"filteredOut", rawPathCount-len(fields),
				"totalObservations", trie.Root.TotalObservations,
				"suggestedRules", len(rules),
				"templates", templates,
			)
			learnedByWorkload[meta.Workload] = append(learnedByWorkload[meta.Workload], rules...)
		}

		cursor = next
		if cursor == 0 {
			break
		}
	}
	return learnedByWorkload
}
