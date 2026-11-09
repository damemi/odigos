package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

var errWriteFailed = errors.New("insert observations: context canceled")

// An issue is only actionable with its evidence trace: it is what a user opens
// to investigate and what Accept folds into the baseline. A write that fails
// must leave the issue eligible to capture evidence on a later firing.
func TestKeepSampleReleasesSlotWhenWriteFails(t *testing.T) {
	r := newFakeRepo()
	r.insertObservationErr = errWriteFailed
	eng := New(r, nil, nil, nil)
	ctx := context.Background()

	tx := repo.Transaction{ID: 7}
	reason := repo.AnomalySampleReason("sig-write-fails")
	trace := &Trace{TraceID: "trace-1", rawOTLP: []byte("otlp")}

	if !eng.keepSample(ctx, trace, tx, reason, repo.AnomalyEvidenceSamplesPerIssue, nil) {
		t.Fatal("first sample should be queued")
	}
	eng.samples.drain(ctx)
	if len(r.observations) != 0 {
		t.Fatalf("observations stored = %d, want 0 while writes fail", len(r.observations))
	}
	// The slot was handed back, so the next firing retries.
	if !eng.keepSample(ctx, trace, tx, reason, repo.AnomalyEvidenceSamplesPerIssue, nil) {
		t.Fatal("slot still held after a failed write: the issue would never get evidence")
	}

	r.insertObservationErr = nil
	eng.samples.drain(ctx)
	if len(r.observations) != 1 {
		t.Fatalf("observations stored = %d, want 1 after the retry", len(r.observations))
	}
	// Once a trace landed, the slot stays taken.
	if eng.keepSample(ctx, trace, tx, reason, repo.AnomalyEvidenceSamplesPerIssue, nil) {
		t.Fatal("cap should be reached after a successful write")
	}
}

// Example traces are what a user compares against the baseline. A transient
// write failure must not permanently spend the transaction's quota for them.
func TestRecordExampleSampleKeepsBudgetWhenWriteFails(t *testing.T) {
	r := newFakeRepo()
	r.insertObservationErr = errWriteFailed
	eng := New(r, nil, nil, nil)
	eng.SetSystemConfig(newSystemConfig(repo.SystemSettings{ExamplesPerTransaction: 1, ExampleSampleIntervalSeconds: 60}))
	ctx := context.Background()

	tx := repo.Transaction{ID: 11}
	trace := &Trace{TraceID: "trace-1", rawOTLP: []byte("otlp")}
	entry := &transactionEntry{transactionID: tx.ID}

	eng.recordExampleSample(ctx, trace, tx, entry)
	eng.samples.drain(ctx)
	if len(r.observations) != 0 {
		t.Fatalf("observations stored = %d, want 0 while writes fail", len(r.observations))
	}
	if entry.lastExampleAt.IsZero() {
		t.Fatal("lastExampleAt should advance even on failure so retries stay throttled")
	}

	// Once writes recover the quota is spent normally. Clear the throttle so the
	// interval doesn't skip this attempt.
	r.insertObservationErr = nil
	entry.lastExampleAt = time.Time{}
	eng.recordExampleSample(ctx, trace, tx, entry)
	eng.samples.drain(ctx)
	if len(r.observations) != 1 {
		t.Fatalf("observations stored = %d, want 1 after a successful write", len(r.observations))
	}

	// The quota (1) is now spent: a further attempt keeps nothing.
	entry.lastExampleAt = time.Time{}
	eng.recordExampleSample(ctx, trace, tx, entry)
	eng.samples.drain(ctx)
	if len(r.observations) != 1 {
		t.Fatalf("observations stored = %d, want 1: the example cap must hold", len(r.observations))
	}
}

// An instrumented application going away mid-export must not cost the user the
// evidence for an issue the engine already decided to raise.
func TestKeepSampleSurvivesCanceledRequestContext(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, nil, nil)

	tx := repo.Transaction{ID: 13}
	trace := &Trace{TraceID: "trace-1", rawOTLP: []byte("otlp")}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if !eng.keepSample(ctx, trace, tx, repo.AnomalySampleReason("sig-canceled"), repo.AnomalyEvidenceSamplesPerIssue, nil) {
		t.Fatal("sample should be queued despite the canceled request")
	}
	eng.samples.drain(context.Background())
	if len(r.observations) != 1 {
		t.Fatalf("observations stored = %d, want 1 despite the canceled request", len(r.observations))
	}
}

// The sample caps are enforced from memory, but the first time a transaction
// wants to keep a sample its counts come from the DB: what a previous run (or
// another replica) already stored must still count against the cap.
func TestSampleCountsLoadStoredSamplesOnce(t *testing.T) {
	r := newFakeRepo()
	tx := repo.Transaction{ID: 17}
	reason := repo.AnomalySampleReason("sig-before-restart")
	r.observations = append(r.observations, repo.Observation{TransactionID: tx.ID, TraceID: "old", SampleReason: reason})

	eng := New(r, nil, nil, nil)
	ctx := context.Background()
	trace := &Trace{TraceID: "trace-1", rawOTLP: []byte("otlp")}
	if eng.keepSample(ctx, trace, tx, reason, repo.AnomalyEvidenceSamplesPerIssue, nil) {
		t.Fatal("evidence stored before the restart must count against the cap")
	}
	// A different reason on the same transaction shares the one load.
	if !eng.keepSample(ctx, trace, tx, repo.ExampleSampleReason, 1, nil) {
		t.Fatal("an unrelated reason should still have its slot")
	}
	if r.countObservationCalls != 1 {
		t.Fatalf("CountObservationsByReason calls = %d, want 1 (loaded once per transaction)", r.countObservationCalls)
	}
}

// Storing a sample must never block trace ingestion: when the writer cannot
// keep up, the sample is dropped and its slot released rather than queued
// forever.
func TestSampleWriterDropsWhenQueueFull(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, nil, nil)
	ctx := context.Background()
	tx := repo.Transaction{ID: 19}
	trace := &Trace{TraceID: "trace-1", rawOTLP: []byte("otlp")}

	for i := 0; i < sampleQueueSize; i++ {
		eng.samples.enqueue(repo.Observation{TransactionID: tx.ID, TraceID: "filler", SampleReason: "example"}, func() {})
	}
	released := false
	if !eng.keepSample(ctx, trace, tx, repo.ExampleSampleReason, 1, func() { released = true }) {
		t.Fatal("keepSample should still reserve the slot")
	}
	if !released {
		t.Fatal("a dropped sample must release its slot")
	}
	if !eng.keepSample(ctx, trace, tx, repo.ExampleSampleReason, 1, nil) {
		t.Fatal("the slot should be free again after the drop")
	}
}

// A guardrail violation group stores one evidence trace. If that write fails,
// the group must be able to capture evidence on a later violation.
func TestGuardrailEvidenceReopensGateWhenWriteFails(t *testing.T) {
	r := newFakeRepo()
	r.insertObservationErr = errWriteFailed
	eng := New(r, nil, nil, nil)
	ctx := context.Background()

	tx := repo.Transaction{ID: 23}
	full := &Trace{TraceID: "trace-1", rawOTLP: []byte("otlp")}
	capture := func() {
		eng.guardrails.captureEvidence(ctx, full, tx, "ns/svc", "allowed_egress", "evil.example.com")
	}

	capture()
	eng.samples.drain(ctx)
	if len(r.observations) != 0 {
		t.Fatalf("observations stored = %d, want 0 while writes fail", len(r.observations))
	}

	r.insertObservationErr = nil
	capture()
	eng.samples.drain(ctx)
	if len(r.observations) != 1 {
		t.Fatalf("observations stored = %d, want 1: the group gate must reopen after a failed write", len(r.observations))
	}

	capture()
	eng.samples.drain(ctx)
	if len(r.observations) != 1 {
		t.Fatalf("observations stored = %d, want 1: one evidence trace per group", len(r.observations))
	}
}
