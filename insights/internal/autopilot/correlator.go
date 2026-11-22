package autopilot

import (
	"context"

	commonlogger "github.com/odigos-io/odigos/common/logger"
)

// callTreeCorrelator merges profile stacks into structure-only Redis call tree
// hashes. Transaction inventory is the insights transactions table.
type callTreeCorrelator struct {
	redis  *tracesRedis
	logger *commonlogger.OdigosLogger
}

func (c *callTreeCorrelator) Close() error {
	if c == nil || c.redis == nil {
		return nil
	}
	err := c.redis.Close()
	c.redis = nil
	return err
}

func (c *callTreeCorrelator) correlate(ctx context.Context, traces []TraceBatch) error {
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
			n, err := c.redis.mergeCallTree(ctx, tb.Transactions[j], traceID)
			if err != nil {
				return err
			}
			redisNodes += n
		}
	}
	if redisNodes > 0 {
		c.logger.Debug("merged call tree nodes into redis",
			"traces", len(traces),
			"transactions", txN,
			"new_nodes", redisNodes,
		)
	}
	return nil
}
