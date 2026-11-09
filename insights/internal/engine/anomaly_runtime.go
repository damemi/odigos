package engine

import (
	"context"
	"encoding/json"
	"time"

	"github.com/odigos-io/odigos/insights/internal/repo"
	"github.com/odigos-io/odigos/insights/internal/risk"
)

// keepSampleFunc is Engine.keepSample as the runtimes see it. They are wired
// with it as a closure so they stay testable without a full Engine.
type keepSampleFunc func(ctx context.Context, t *Trace, tx repo.Transaction, reason string, max int, released func()) bool

type anomalyRuntime struct {
	repo  repo.Repository
	clock func() time.Time
	warn  func(msg string, err error)
	keep  keepSampleFunc

	buf *anomalyBuffer
}

func newAnomalyRuntime(
	r repo.Repository,
	clock func() time.Time,
	warn func(string, error),
	keep keepSampleFunc,
) *anomalyRuntime {
	return &anomalyRuntime{repo: r, clock: clock, warn: warn, keep: keep, buf: newAnomalyBuffer()}
}

// record buffers one anomaly firing. Firings are deduped by signature: the
// occurrence is summed into the in-memory buffer (flushed as one incrementing
// issue per interval, not one row per trace), and a single evidence trace is
// kept per issue for the investigation view. Extra samples are not useful;
// occurrences are counted separately.
//
// The evidence cap (one per issue) used to be a write-once in-memory gate backed
// by a DB count per firing; both are now the shared sample counts behind keep,
// which also queue the write instead of blocking on it.
func (a *anomalyRuntime) record(
	ctx context.Context,
	t *Trace,
	tx repo.Transaction,
	policy *repo.Policy,
	deviated []repo.DeviationClass,
	evidence map[string]json.RawMessage,
	assessment risk.Assessment,
	inventoryEpoch int,
) {
	now := a.clock()
	signature, offending := anomalySignature(tx.ID, deviated, evidence)
	evJSON, _ := json.Marshal(evidence)
	a.buf.add(repo.AnomalyOccurrence{
		TransactionID:    tx.ID,
		Signature:        signature,
		TriggeredClasses: deviated,
		Offending:        offending,
		Evidence:         evJSON,
		PolicyID:         policy.ID,
		MaxScore:         assessment.Score,
		LastSeen:         now,
		LastTraceID:      t.TraceID,
	}, inventoryEpoch)
	a.keep(ctx, t, tx, repo.AnomalySampleReason(signature), repo.AnomalyEvidenceSamplesPerIssue, nil)
}

// flush drains the buffered anomaly firings and records them as summed
// increments. Called on the flush cadence; a failure is logged, not fatal.
// Firings an inventory reset invalidated are dropped - see liveInventoryEpoch.
func (a *anomalyRuntime) flush(ctx context.Context) {
	if a.buf.empty() {
		return
	}
	currentInventoryEpoch, err := liveInventoryEpoch(ctx, a.repo)
	if err != nil {
		// Without it there is no way to tell live firings from ones an inventory
		// reset invalidated. Keep them buffered for the next flush rather than
		// risk reporting transactions that no longer exist.
		a.warn("read transaction inventory epoch for anomaly flush", err)
		return
	}
	occs := a.buf.drain(currentInventoryEpoch)
	if len(occs) == 0 {
		return
	}
	if err := a.repo.RecordAnomalyOccurrences(ctx, occs); err != nil {
		a.warn("flush anomaly occurrences", err)
	}
}

// forgetTransaction drops the firings one transaction buffered since the last
// flush. Called after an operator delete or baseline reset has removed the same
// state from storage, so the write-behind flush cannot put it back. The
// transaction's evidence slots are released by Engine (sampleCounts.forget).
func (a *anomalyRuntime) forgetTransaction(transactionID int64) {
	if a == nil {
		return
	}
	a.buf.dropTransaction(transactionID)
}
