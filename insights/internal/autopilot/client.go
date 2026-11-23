// Package autopilot builds per-transaction call trees for the insights
// autopilot feature. For each java-ebpf transaction the engine admits, it asks
// Redis to merge the profile stacks of the transaction's spans, written by the
// gateway's odigos_autopilot_profiles exporter, into a structure-only call
// tree, and syncs the call trees that gained nodes into Postgres.
package autopilot

import (
	"context"
	"fmt"
	"time"

	"github.com/odigos-io/odigos/common/consts"
	commonlogger "github.com/odigos-io/odigos/common/logger"
)

// Client correlates java-ebpf traces with Redis profile stacks, merges a
// structure-only call tree into Redis (DB 1), and syncs the call trees that
// gained nodes into Postgres. Transaction inventory is the insights
// transactions table, written on the engine flush.
type Client struct {
	batcher *invocationBatcher
	logger  *commonlogger.OdigosLogger
}

// startupPingTimeout bounds the best-effort reachability log in Open.
const startupPingTimeout = 2 * time.Second

// Open builds the cacheDb Redis client (logical DB 1) and starts the flush
// loop. Call trees that gain nodes are synced into st, the insights Postgres
// repo. It fails only on invalid configuration: go-redis and pgx both connect
// lazily, so a Redis or Postgres that is unreachable at startup (e.g. still
// rolling out) is picked up on a later flush or sync instead of disabling
// correlation for the life of the pod.
func Open(redisEndpoint string, st CallTreeStore, logger *commonlogger.OdigosLogger) (*Client, error) {
	if redisEndpoint == "" {
		return nil, fmt.Errorf("redis endpoint is required")
	}
	if st == nil {
		return nil, fmt.Errorf("call tree store is required")
	}
	if logger == nil {
		return nil, fmt.Errorf("logger is required")
	}
	rdb := newTracesRedis(redisEndpoint, logger)
	pingCtx, cancel := context.WithTimeout(context.Background(), startupPingTimeout)
	if err := rdb.ping(pingCtx); err != nil {
		logger.Warn("autopilot redis not reachable yet; correlation will retry on each flush", "error", err)
	}
	cancel()
	batcher := newInvocationBatcher(defaultFlushInterval, defaultFlushThreshold, logger)
	syncer := newCallTreeSyncer(rdb, st, logger, callTreeSyncDelay, callTreeSyncRetryDelay)
	syncer.start()
	batcher.start(&callTreeCorrelator{redis: rdb, syncer: syncer, logger: logger})
	logger.Info("autopilot call-tree writer ready",
		"redis_endpoint", redisEndpoint,
		"redis_db", consts.RedisDBAutopilotProfilesTransactionsCorrelation,
		"redis_call_tree_prefix", redisCallTreeKeyPrefix,
		"redis_call_tree_ttl", redisCallTreeKeyTTL.String(),
		"flush_interval", defaultFlushInterval.String(),
		"flush_threshold", defaultFlushThreshold,
		"sync_delay", callTreeSyncDelay.String(),
	)
	return &Client{batcher: batcher, logger: logger}, nil
}

// Record queues one TraceId's java-ebpf transactions for Redis correlation.
// Non-blocking; drops when the channel is full.
func (c *Client) Record(batch TraceBatch) {
	if c == nil || c.batcher == nil {
		return
	}
	c.batcher.record(batch)
}

// Close stops the batcher and closes Redis.
func (c *Client) Close() error {
	if c == nil || c.batcher == nil {
		return nil
	}
	err := c.batcher.stop()
	c.batcher = nil
	return err
}

// FlushInterval exposes the batcher interval for tests.
func (c *Client) FlushInterval() time.Duration {
	if c == nil || c.batcher == nil {
		return 0
	}
	return c.batcher.flushInterval
}
