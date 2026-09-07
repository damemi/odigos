package engine

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

type anomalyRuntime struct {
	repo  repo.Repository
	clock func() time.Time
	warn  func(msg string, err error)
	store func(ctx context.Context, t *Trace, tx repo.Transaction, reason string) error

	buf     *anomalyBuffer
	samples sync.Map // signature string -> struct{}, write-once evidence gate
}

func newAnomalyRuntime(
	r repo.Repository,
	clock func() time.Time,
	warn func(string, error),
	store func(context.Context, *Trace, repo.Transaction, string) error,
) *anomalyRuntime {
	return &anomalyRuntime{repo: r, clock: clock, warn: warn, store: store, buf: newAnomalyBuffer()}
}

// record buffers one anomaly firing. Firings are deduped by signature: the
// occurrence is summed into the in-memory buffer (flushed as one incrementing
// issue per interval, not one row per trace), and a single evidence trace is
// kept write-once for the investigation view.
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
	a.captureSample(ctx, t, tx, signature)
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

// clearSamples forgets the write-once evidence gate so issues relearned after an
// inventory reset or an identity-dimension change capture a fresh sample. The
// gate is keyed by signature only, so without this the first post-reset firing
// of a signature seen before the reset would store no evidence.
func (a *anomalyRuntime) clearSamples() {
	if a == nil {
		return
	}
	a.samples.Clear()
}

// captureSample keeps one evidence trace per issue signature. Occurrences are
// counted separately; extra samples are not useful. Best-effort: the in-memory
// gate races harmlessly across concurrent firings; CountObservations covers
// restarts where the gate is empty.
//
// The slot is claimed before the write, so a failed write has to hand it back.
// Otherwise the issue goes on counting occurrences with no trace to show for
// them: nothing to investigate, and nothing Accept can fold into the baseline.
func (a *anomalyRuntime) captureSample(ctx context.Context, t *Trace, tx repo.Transaction, signature string) {
	if _, loaded := a.samples.LoadOrStore(signature, struct{}{}); loaded {
		return
	}
	ctx, cancel := detachedEvidenceContext(ctx)
	defer cancel()

	reason := repo.AnomalySampleReason(signature)
	n, err := a.repo.CountObservations(ctx, tx.ID, reason)
	if err != nil {
		a.warn("count anomaly samples", err)
		a.samples.Delete(signature)
		return
	}
	if n >= repo.AnomalyEvidenceSamplesPerIssue {
		return
	}
	if err := a.store(ctx, t, tx, reason); err != nil {
		a.samples.Delete(signature)
	}
}
