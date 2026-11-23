package autopilot

import (
	"context"

	commonlogger "github.com/odigos-io/odigos/common/logger"
)

// callTreeCorrelator merges profile stacks into structure-only Redis call tree
// hashes and hands the call trees that gained nodes to the syncer. Transaction
// inventory is the insights transactions table.
type callTreeCorrelator struct {
	redis  *tracesRedis
	syncer *callTreeSyncer
	logger *commonlogger.OdigosLogger
}

func (c *callTreeCorrelator) Close() error {
	if c == nil {
		return nil
	}
	if c.syncer != nil {
		c.syncer.stop()
		c.syncer = nil
	}
	if c.redis == nil {
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

	var invocationsN, changedN int
	for i := range traces {
		tb := &traces[i]
		invocationsN += len(tb.ServiceInvocations)
		changed, err := c.redis.mergeCallTrees(ctx, tb.TraceID, tb.ServiceInvocations)
		if err != nil {
			return err
		}
		changedN += len(changed)
		if c.syncer != nil {
			c.syncer.changed(changed)
		}
	}
	if changedN > 0 {
		c.logger.Debug("merged call tree nodes into redis",
			"traces", len(traces),
			"service_invocations", invocationsN,
			"changed_call_trees", changedN,
		)
	}
	return nil
}

func (c *callTreeCorrelator) mergeMeasures(ctx context.Context, set measureSet) error {
	if c == nil || c.redis == nil || len(set) == 0 {
		return nil
	}
	return c.redis.mergeMeasures(ctx, set)
}
