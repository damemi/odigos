package autopilot

import (
	"context"

	commonlogger "github.com/odigos-io/odigos/common/logger"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

// callTreeFlusher copies every Redis call tree into Postgres at the start of
// each autopilot loop tick. Each replica's callTreeSyncer already copies call
// trees as they change; this full flush catches up syncs that were lost, e.g.
// to a replica restart, for call trees that no longer change. Nodes already
// stored are skipped. Runs only on the insights leader.
type callTreeFlusher struct {
	redis  *tracesRedis
	store  CallTreeStore
	logger *commonlogger.OdigosLogger
}

func (f *callTreeFlusher) Close() error {
	if f == nil || f.redis == nil {
		return nil
	}
	err := f.redis.Close()
	f.redis = nil
	return err
}

func (f *callTreeFlusher) Flush(tickCtx context.Context) {
	keys, err := f.redis.scanCallTreeKeys(tickCtx)
	if err != nil {
		f.logger.Warn("call tree flusher scan failed", "error", err)
		return
	}
	if len(keys) == 0 {
		return
	}

	var nodes []repo.CallTreeNode
	var keysLoaded, keysSkipped int
	for _, key := range keys {
		meta, ok := parseSyncableCallTreeKey(key)
		if !ok {
			keysSkipped++
			continue
		}
		part, err := f.redis.loadCallTreeStructureNodes(tickCtx, key, meta)
		if err != nil {
			f.logger.Warn("call tree flusher load key failed", "key", key, "error", err)
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
	if err := f.store.InsertCallTreeNodes(tickCtx, nodes); err != nil {
		f.logger.Warn("call tree flusher insert failed", "error", err, "nodes", len(nodes))
		return
	}
	f.logger.Info("flushed redis call tree structure to postgres",
		"keys", len(keys),
		"keys_loaded", keysLoaded,
		"keys_skipped", keysSkipped,
		"nodes", len(nodes),
	)
}
