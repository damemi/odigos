package autopilot

import (
	"context"
	"fmt"
	"time"

	commonlogger "github.com/odigos-io/odigos/common/logger"
)

// Client correlates java-ebpf traces with Redis profile stacks and merges a
// structure-only call tree into Redis (DB 1). Transaction inventory is the
// insights transactions table, written on the engine flush. The leader's call tree
// flusher copies the call trees into the autopilot store.
type Client struct {
	batcher *invocationBatcher
	logger  *commonlogger.OdigosLogger
}

// startupPingTimeout bounds the best-effort reachability log in Open.
const startupPingTimeout = 2 * time.Second

// Open builds the cacheDb Redis client (logical DB 1) and starts the flush
// loop. It fails only on invalid configuration: go-redis redials on every
// command, so a Redis that is unreachable at startup (e.g. cacheDb still
// rolling out) is picked up on a later flush instead of disabling
// correlation for the life of the pod.
func Open(redisEndpoint string, logger *commonlogger.OdigosLogger) (*Client, error) {
	if redisEndpoint == "" {
		return nil, fmt.Errorf("redis endpoint is required")
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
	batcher.start(&callTreeCorrelator{redis: rdb, logger: logger})
	logger.Info("autopilot call-tree writer ready",
		"redis_endpoint", redisEndpoint,
		"redis_db", redisDB,
		"redis_call_tree_prefix", redisCallTreeKeyPrefix,
		"redis_call_tree_ttl", redisCallTreeKeyTTL.String(),
		"flush_interval", defaultFlushInterval.String(),
		"flush_threshold", defaultFlushThreshold,
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
