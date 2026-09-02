package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

var errWriteFailed = errors.New("insert observation: context canceled")

// An issue is only actionable with its evidence trace: it is what a user opens
// to investigate and what Accept folds into the baseline. A write that fails
// must leave the issue eligible to capture evidence on a later firing.
func TestCaptureSampleReleasesGateWhenWriteFails(t *testing.T) {
	const signature = "sig-write-fails"
	tx := repo.Transaction{ID: 7}
	trace := &Trace{TraceID: "trace-1", rawOTLP: []byte("otlp")}

	attempts := 0
	storeErr := errWriteFailed
	store := func(context.Context, *Trace, repo.Transaction, string) error {
		attempts++
		return storeErr
	}
	a := newAnomalyRuntime(newFakeRepo(), time.Now, func(string, error) {}, store)

	a.captureSample(context.Background(), trace, tx, signature)
	if attempts != 1 {
		t.Fatalf("store attempts = %d, want 1", attempts)
	}
	if _, gated := a.samples.Load(signature); gated {
		t.Fatal("gate still held after a failed write: the signature would never be retried")
	}

	// The next firing retries; once a trace lands, the slot stays taken.
	storeErr = nil
	a.captureSample(context.Background(), trace, tx, signature)
	if attempts != 2 {
		t.Fatalf("store attempts = %d, want 2 (retry after failure)", attempts)
	}
	if _, gated := a.samples.Load(signature); !gated {
		t.Fatal("gate should be held after a successful write")
	}
}

// Example traces are what a user compares against the baseline. A transient
// write failure must not permanently spend the transaction's quota for them.
func TestRecordExampleSampleKeepsBudgetWhenWriteFails(t *testing.T) {
	r := newFakeRepo()
	r.insertObservationErr = errWriteFailed
	eng := New(r, nil, nil, nil)

	tx := repo.Transaction{ID: 11}
	trace := &Trace{TraceID: "trace-1", rawOTLP: []byte("otlp")}
	entry := &transactionEntry{transactionID: tx.ID}

	eng.recordExampleSample(context.Background(), trace, tx, entry)
	if entry.examplesStored != 0 {
		t.Fatalf("examplesStored = %d, want 0: a failed write must not spend the budget", entry.examplesStored)
	}
	if entry.lastExampleAt.IsZero() {
		t.Fatal("lastExampleAt should advance even on failure so retries stay throttled")
	}

	// Once writes recover the quota is spent normally. Clear the throttle so the
	// interval doesn't skip this attempt.
	r.insertObservationErr = nil
	entry.lastExampleAt = time.Time{}
	eng.recordExampleSample(context.Background(), trace, tx, entry)
	if entry.examplesStored != 1 {
		t.Fatalf("examplesStored = %d, want 1 after a successful write", entry.examplesStored)
	}
}

// An instrumented application going away mid-export must not cost the user the
// evidence for an issue the engine already decided to raise.
func TestCaptureSampleSurvivesCanceledRequestContext(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, nil, nil)

	tx := repo.Transaction{ID: 13}
	trace := &Trace{TraceID: "trace-1", rawOTLP: []byte("otlp")}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	eng.anomalies.captureSample(ctx, trace, tx, "sig-canceled")

	if len(r.observations) != 1 {
		t.Fatalf("observations stored = %d, want 1 despite the canceled request", len(r.observations))
	}
}
