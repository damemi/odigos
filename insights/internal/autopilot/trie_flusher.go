package autopilot

import (
	"context"
	"fmt"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	commonlogger "github.com/odigos-io/odigos/common/logger"
	"github.com/odigos-io/odigos/insights/internal/autopilot/store"
	"github.com/odigos-io/odigos/k8sutils/pkg/workload"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// trieFlusher copies Redis trie nodes into the autopilot store at the start
// of each autopilot loop tick. Nodes already stored are skipped. Runs only on
// the insights leader.
type trieFlusher struct {
	redis *tracesRedis
	// store is owned by the caller, which closes it.
	store  *store.Client
	logger *commonlogger.OdigosLogger
	// kube reads InstrumentationConfigs from the insights controller cache.
	kube client.Client
}

func (f *trieFlusher) Close() error {
	if f == nil || f.redis == nil {
		return nil
	}
	err := f.redis.Close()
	f.redis = nil
	return err
}

func (f *trieFlusher) Flush(tickCtx context.Context) {
	keys, err := f.redis.scanTrieKeys(tickCtx)
	if err != nil {
		f.logger.Warn("trie flusher scan failed", "error", err)
		return
	}
	if len(keys) == 0 {
		return
	}

	var nodes []store.FlushedTrieNode
	var keysLoaded, keysSkipped int
	for _, key := range keys {
		meta, ok := parseRedisTrieKey(key)
		if !ok || (meta.SampleType != "events" && meta.SampleType != "samples") {
			keysSkipped++
			continue
		}
		present, err := f.workloadInstrumented(tickCtx, meta)
		if err != nil {
			f.logger.Warn("trie flusher instrumentation config lookup failed",
				"namespace", meta.Namespace,
				"kind", meta.WorkloadKind,
				"name", meta.WorkloadName,
				"error", err,
			)
			continue
		}
		if !present {
			// An uninstrumented workload should not produce more spans. If a few
			// still arrive and recreate the key, the next flush deletes it again.
			if err := f.redis.deleteKey(tickCtx, key); err != nil {
				f.logger.Warn("trie flusher delete uninstrumented key failed", "key", key, "error", err)
			}
			keysSkipped++
			continue
		}
		part, err := f.redis.loadTrieStructureNodes(tickCtx, key, meta)
		if err != nil {
			f.logger.Warn("trie flusher load key failed", "key", key, "error", err)
			continue
		}
		if len(part) == 0 {
			keysSkipped++
			continue
		}
		keysLoaded++
		nodes = append(nodes, part...)
	}
	if len(nodes) == 0 {
		return
	}
	nodesSkipped, err := f.store.InsertCallTrieNodes(tickCtx, nodes)
	if err != nil {
		f.logger.Warn("trie flusher insert failed", "error", err, "nodes", len(nodes))
		return
	}
	f.logger.Info("flushed redis trie structure to the autopilot store",
		"keys", len(keys),
		"keys_loaded", keysLoaded,
		"keys_skipped", keysSkipped,
		"nodes", len(nodes),
		"nodes_skipped", nodesSkipped,
	)
}

func (f *trieFlusher) workloadInstrumented(ctx context.Context, meta redisTrieKeyMeta) (bool, error) {
	if f == nil || f.kube == nil {
		return false, fmt.Errorf("instrumentation config client is nil")
	}
	name := workload.CalculateWorkloadRuntimeObjectName(meta.WorkloadName, meta.WorkloadKind)
	var ic odigosv1.InstrumentationConfig
	err := f.kube.Get(ctx, client.ObjectKey{Namespace: meta.Namespace, Name: name}, &ic)
	if apierrors.IsNotFound(err) {
		if f.logger != nil {
			f.logger.Debug("skipping trie flush; instrumentation config not found",
				"namespace", meta.Namespace,
				"kind", meta.WorkloadKind,
				"name", meta.WorkloadName,
			)
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get instrumentation config %s/%s: %w", meta.Namespace, name, err)
	}
	return true, nil
}
