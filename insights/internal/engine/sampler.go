package engine

import (
	"context"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// Defaults remain as package consts for tests and migration seeds; runtime
// values come from SystemConfig.
const (
	ExamplesPerTransaction = repo.DefaultExamplesPerTransaction
	ExampleSampleInterval  = time.Duration(repo.DefaultExampleSampleIntervalSeconds) * time.Second
)

// recordExampleSample keeps up to ExamplesPerTransaction normal-traffic traces
// while learning, at most one per configured interval, so a user has real
// traces to compare against the baseline. Once the cap is reached collection
// stops (no rolling window / prune). The cap itself is enforced by keepSample
// from in-memory counts; the only DB work here is the background write.
func (e *Engine) recordExampleSample(ctx context.Context, t *Trace, tx repo.Transaction, entry *transactionEntry) {
	cfg := e.system.Get()
	e.recordThrottledSample(ctx, t, tx, &entry.lastExampleAt, repo.ExampleSampleReason,
		cfg.ExamplesPerTransaction, time.Duration(cfg.ExampleSampleIntervalSeconds)*time.Second)
}

// recordRelationSample keeps the whole traces the correlation recommendation
// miner reads once the transaction promotes. It needs more traces than the UI
// examples (a relation seen on 50 traces is a pattern, on 10 a coincidence)
// so it has its own, larger cap and its own shorter retention (see the
// trace_observations TTL). The full trace is kept, not the sub-trace, because
// a correlation spans services: the value written by one service is read by
// another further down the call.
func (e *Engine) recordRelationSample(ctx context.Context, full *Trace, tx repo.Transaction, entry *transactionEntry) {
	if full == nil {
		return
	}
	cfg := e.system.Get()
	e.recordThrottledSample(ctx, full, tx, &entry.lastRelationAt, repo.RelationSampleReason,
		cfg.RelationSamplesPerTransaction, time.Duration(cfg.RelationSampleIntervalSeconds)*time.Second)
}

// recordThrottledSample keeps t under reason if at least interval passed since
// the last attempt for this transaction. The interval advances whether or not
// a sample is kept, so a transaction at its cap does not re-check on every
// trace. Caller holds entry.mu (lastAt is an entry field).
func (e *Engine) recordThrottledSample(ctx context.Context, t *Trace, tx repo.Transaction, lastAt *time.Time, reason string, maxN int, interval time.Duration) {
	if maxN <= 0 {
		return
	}
	now := e.clock()
	if !lastAt.IsZero() && now.Sub(*lastAt) < interval {
		return
	}
	*lastAt = now
	e.keepSample(ctx, t, tx, reason, maxN, nil)
}
