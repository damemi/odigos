package interrogation

import (
	"context"

	commonlogger "github.com/odigos-io/odigos/common/logger"
)

// callTrieCorrelator merges profile stacks into structure-only Redis trie
// hashes. Transaction inventory is the insights transactions table.
type callTrieCorrelator struct {
	redis  *tracesRedis
	logger *commonlogger.OdigosLogger
}

func (c *callTrieCorrelator) Close() error {
	if c == nil || c.redis == nil {
		return nil
	}
	err := c.redis.Close()
	c.redis = nil
	return err
}

func (c *callTrieCorrelator) correlate(ctx context.Context, traces []TraceBatch) error {
	if c == nil || c.redis == nil || len(traces) == 0 {
		return nil
	}

	var txN int
	var redisNodes int64
	for i := range traces {
		tb := &traces[i]
		traceID := []byte(tb.TraceID)
		txN += len(tb.Transactions)
		for j := range tb.Transactions {
			n, err := c.redis.mergeCallTrie(ctx, tb.Transactions[j], traceID)
			if err != nil {
				return err
			}
			redisNodes += n
		}
	}
	if redisNodes > 0 {
		c.logger.Debug("merged call trie nodes into redis",
			"traces", len(traces),
			"transactions", txN,
			"new_nodes", redisNodes,
		)
	}
	return nil
}
