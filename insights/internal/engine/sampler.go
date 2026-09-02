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
// while learning, at most one per configured interval. Once the cap is reached
// collection stops (no rolling window / prune).
func (e *Engine) recordExampleSample(ctx context.Context, t *Trace, tx repo.Transaction, entry *transactionEntry) {
	cfg := e.system.Get()
	maxN := cfg.ExamplesPerTransaction
	if maxN <= 0 {
		return
	}
	ctx, cancel := detachedEvidenceContext(ctx)
	defer cancel()

	if !entry.examplesCounted {
		n, err := e.repo.CountObservations(ctx, tx.ID, repo.ExampleSampleReason)
		if err != nil {
			e.warn("count example samples", err)
			return
		}
		entry.examplesStored = n
		entry.examplesCounted = true
	}
	if entry.examplesStored >= maxN {
		return
	}
	interval := time.Duration(cfg.ExampleSampleIntervalSeconds) * time.Second
	now := e.clock()
	if !entry.lastExampleAt.IsZero() && now.Sub(entry.lastExampleAt) < interval {
		return
	}
	// The quota counts traces a user can actually open next to the baseline, so
	// only a sample that landed spends one. The interval still advances on
	// failure to keep retries off a struggling database.
	entry.lastExampleAt = now
	if err := e.storeObservation(ctx, t, tx, repo.ExampleSampleReason); err != nil {
		return
	}
	entry.examplesStored++
}
