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

	var invocationsN int
	var redisNodes int64
	for i := range traces {
		tb := &traces[i]
		invocationsN += len(tb.ServiceInvocations)
		n, err := c.redis.mergeCallTrees(ctx, tb.TraceID, tb.ServiceInvocations)
		if err != nil {
			return err
		}
		redisNodes += n
	}
	if redisNodes > 0 {
		c.logger.Debug("merged call tree nodes into redis",
			"traces", len(traces),
			"service_invocations", invocationsN,
			"new_nodes", redisNodes,
		)
	}
	return nil
}
