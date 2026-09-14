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
	maxN := cfg.ExamplesPerTransaction
	if maxN <= 0 {
		return
	}
	interval := time.Duration(cfg.ExampleSampleIntervalSeconds) * time.Second
	now := e.clock()
	if !entry.lastExampleAt.IsZero() && now.Sub(entry.lastExampleAt) < interval {
		return
	}
	// The interval advances whether or not a sample is kept, so a transaction
	// at its cap does not re-check on every trace.
	entry.lastExampleAt = now
	e.keepSample(ctx, t, tx, repo.ExampleSampleReason, maxN, nil)
}
