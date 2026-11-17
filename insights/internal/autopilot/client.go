package autopilot

import (
	"fmt"
	"time"

	commonlogger "github.com/odigos-io/odigos/common/logger"
)

// Client correlates java-ebpf traces with Redis profile stacks and merges a
// structure-only call trie into Redis (DB 1). Transaction inventory is the
// insights transactions table (source_identity), written on the engine
// flush. Structure trie dumps to ClickHouse are handled by the leader flusher.
type Client struct {
	batcher *invocationBatcher
	logger  *commonlogger.OdigosLogger
}

// Open connects to cacheDb Redis (logical DB 1) and starts the flush loop.
func Open(redisEndpoint string, logger *commonlogger.OdigosLogger) (*Client, error) {
	if redisEndpoint == "" {
		return nil, fmt.Errorf("redis endpoint is required")
	}
	if logger == nil {
		return nil, fmt.Errorf("logger is required")
	}
	rdb, err := openTracesRedis(redisEndpoint, logger)
	if err != nil {
		return nil, err
	}
	batcher := newInvocationBatcher(defaultFlushInterval, defaultFlushThreshold, logger)
	batcher.start(&callTrieCorrelator{redis: rdb, logger: logger})
	logger.Info("autopilot call-trie writer ready",
		"redis_endpoint", redisEndpoint,
		"redis_db", redisDB,
		"redis_trie_prefix", redisTrieKeyPrefix,
		"redis_trie_ttl", redisTrieKeyTTL.String(),
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
