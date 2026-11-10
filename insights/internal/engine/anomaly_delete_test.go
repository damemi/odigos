package engine

import (
	"context"
	"strings"
	"testing"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// Anomaly firings are buffered in memory and written on the flush cadence, so a
// delete that lands between the firing and the flush has to invalidate what is
// still in the buffer. These tests pin that: an operator deleting a transaction
// must not have the write-behind flush hand the finding straight back.

func deleteFixtureNormalTrace() *Trace {
	return mkTrace(
		withRoot("POST /checkout", "user-svc", tracepb.Span_SPAN_KIND_SERVER,
			map[string]string{"http.method": "POST"}),
		withChild("root", "inventory.reserve", tracepb.Span_SPAN_KIND_CLIENT,
			map[string]string{"server.address": "inventory", "server.port": "8080"}, "okhttp3"),
	)
}

func deleteFixtureAttackTrace() *Trace {
	return mkTrace(
		withRoot("POST /checkout", "user-svc", tracepb.Span_SPAN_KIND_SERVER,
			map[string]string{"http.method": "POST"}),
		withChild("root", "ldap.lookup", tracepb.Span_SPAN_KIND_CLIENT,
			map[string]string{"server.address": "attacker.com", "server.port": "1389"}, "ldap"),
	)
}

// newDeleteFixtureEngine wires an engine that fires on any signal. The policy is
// seeded in the repo too, because the delete/reset admin paths reload the alert
// snapshot from storage.
func newDeleteFixtureEngine(r *fakeRepo) *Engine {
	policy := repo.Policy{ID: 1, Name: "fire-at-1", Enabled: true, FireAtScore: 1}
	r.policies = append(r.policies, policy)
	return New(r, nil, countOnlyConfig(DefaultMinObservations), alertConfigFrom(policy))
}

// learnUntilPromoted sends enough normal traffic to promote every class and
// returns the transaction id the engine settled on.
func learnUntilPromoted(t *testing.T, eng *Engine, r *fakeRepo) int64 {
	t.Helper()
	ctx := context.Background()
	for i := int64(0); i < DefaultMinObservations; i++ {
		eng.Process(ctx, deleteFixtureNormalTrace())
	}
	if err := eng.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if len(r.transactions) != 1 {
		t.Fatalf("expected 1 transaction after learning, got %d", len(r.transactions))
	}
	return r.transactions[0].ID
}

func anomalyEvidenceObservations(obs []repo.Observation) int {
	n := 0
	for _, o := range obs {
		if strings.HasPrefix(o.SampleReason, "anomaly:") {
			n++
		}
	}
	return n
}

// TestEngine_DeleteTransactionDropsBufferedFiring is the regression for the
// zombie-finding bug: a firing recorded since the last flush lives only in the
// in-memory anomaly buffer. Deleting the transaction removed its anomaly_issues
// rows but left that buffer untouched, so the very next flush inserted the
// finding again - against a transaction that no longer exists. The findings list
// LEFT JOINs transactions, so the row still renders, with a blank
// service/namespace/operation and no transaction left to delete it through.
func TestEngine_DeleteTransactionDropsBufferedFiring(t *testing.T) {
	r := newFakeRepo()
	eng := newDeleteFixtureEngine(r)
	ctx := context.Background()

	txID := learnUntilPromoted(t, eng, r)

	// Fire, but do not flush: the occurrence is only in the buffer.
	eng.Process(ctx, deleteFixtureAttackTrace())
	if eng.anomalies.buf.empty() {
		t.Fatal("attack trace should have buffered a firing")
	}
	if len(r.anomalies) != 0 {
		t.Fatalf("firing must not reach storage before flush, got %d", len(r.anomalies))
	}

	if err := eng.DeleteTransaction(ctx, txID); err != nil {
		t.Fatalf("delete transaction: %v", err)
	}
	if err := eng.Flush(ctx); err != nil {
		t.Fatalf("flush after delete: %v", err)
	}

	if len(r.anomalies) != 0 {
		t.Fatalf("deleted transaction's finding came back on flush: %+v", r.anomalies)
	}
}

// TestEngine_ResetTransactionBaselinesDropsBufferedFiring covers the same hole
// on the re-learn path, which also clears the transaction's findings so they do
// not contradict the fresh baseline. A buffered firing scored against the wiped
// baseline must not be flushed back in behind the reset - if the behaviour is
// still anomalous it re-fires as a new issue once the baseline promotes again.
func TestEngine_ResetTransactionBaselinesDropsBufferedFiring(t *testing.T) {
	r := newFakeRepo()
	eng := newDeleteFixtureEngine(r)
	ctx := context.Background()

	txID := learnUntilPromoted(t, eng, r)

	eng.Process(ctx, deleteFixtureAttackTrace())
	if eng.anomalies.buf.empty() {
		t.Fatal("attack trace should have buffered a firing")
	}

	if err := eng.ResetTransactionBaselines(ctx, txID); err != nil {
		t.Fatalf("reset baselines: %v", err)
	}
	if err := eng.Flush(ctx); err != nil {
		t.Fatalf("flush after reset: %v", err)
	}

	if len(r.anomalies) != 0 {
		t.Fatalf("reset transaction's finding came back on flush: %+v", r.anomalies)
	}
}

// TestEngine_DeleteTransactionReleasesEvidenceGate covers the second half of the
// same oversight. Evidence is capped per (transaction, signature) by in-memory
// sample counts, and a deleted-then-relearned transaction hashes back to the
// same id and so the same signature. Without dropping its counts on delete, the
// issue re-fires with no evidence trace ever stored: nothing to investigate,
// and Accept fails with "no stored sample trace". DeleteAllTransactions already
// clears every count (sampleCounts.clear); a single delete has to drop its own
// transaction's.
func TestEngine_DeleteTransactionReleasesEvidenceGate(t *testing.T) {
	r := newFakeRepo()
	eng := newDeleteFixtureEngine(r)
	ctx := context.Background()

	txID := learnUntilPromoted(t, eng, r)

	eng.Process(ctx, deleteFixtureAttackTrace())
	if err := eng.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := anomalyEvidenceObservations(r.observations); got != 1 {
		t.Fatalf("evidence observations before delete = %d, want 1", got)
	}

	if err := eng.DeleteTransaction(ctx, txID); err != nil {
		t.Fatalf("delete transaction: %v", err)
	}
	if got := anomalyEvidenceObservations(r.observations); got != 0 {
		t.Fatalf("delete should have removed the stored evidence, got %d", got)
	}

	// Traffic returns, the transaction re-learns to the same id, and the same
	// behaviour fires again. It must capture a fresh evidence trace.
	if got := learnUntilPromoted(t, eng, r); got != txID {
		t.Fatalf("relearned transaction id = %d, want the stable %d", got, txID)
	}
	eng.Process(ctx, deleteFixtureAttackTrace())
	if err := eng.Flush(ctx); err != nil {
		t.Fatalf("flush after relearn: %v", err)
	}

	if len(r.anomalies) == 0 {
		t.Fatal("relearned transaction should fire the anomaly again")
	}
	if got := anomalyEvidenceObservations(r.observations); got != 1 {
		t.Fatalf("evidence observations after relearn = %d, want 1: the write-once gate still held the pre-delete signature", got)
	}
}
