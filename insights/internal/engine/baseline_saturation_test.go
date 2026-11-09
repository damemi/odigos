package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// saturationTrace is one trace of a transaction whose call edges carry a
// per-request id, so every trace adds a new D1 edge and the class can never
// converge.
func saturationTrace(i int) *Trace {
	return mkTrace(
		withRoot("GET /orders", "orders", tracepb.Span_SPAN_KIND_SERVER, nil),
		withChild("root", fmt.Sprintf("lookup-%d", i), tracepb.Span_SPAN_KIND_INTERNAL, nil, "orders-lib"),
	)
}

func d1EdgeCount(t *testing.T, data json.RawMessage) int {
	t.Helper()
	d, err := decodeD1(data)
	if err != nil {
		t.Fatalf("decode d1: %v", err)
	}
	return len(d.Edges)
}

func baselineOf(t *testing.T, r *fakeRepo, class repo.DeviationClass) repo.BaselineClass {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range r.baselines {
		if b.Class == class {
			return b
		}
	}
	t.Fatalf("no %s baseline persisted", class)
	return repo.BaselineClass{}
}

// TestSaturation_FreezesClassAndBoundsPersistedMembers is the core guarantee:
// a set class that outgrows max_baseline_set_members is frozen as saturated,
// the store never receives more than the cap, the class never promotes, and
// the transaction still finishes learning on its other classes.
func TestSaturation_FreezesClassAndBoundsPersistedMembers(t *testing.T) {
	const cap = 3
	r := newFakeRepo()
	eng := New(r, nil, countOnlyConfig(10), nil)
	eng.clock = func() time.Time { return time.Unix(0, 0).UTC() }
	eng.store.setCaps(cap, 0)
	ctx := context.Background()

	for i := 0; i < 50; i++ {
		eng.Process(ctx, saturationTrace(i))
	}
	if err := eng.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	d1 := baselineOf(t, r, repo.D1CallEdges)
	if !d1.Saturated {
		t.Fatalf("D1 not saturated after %d unique edges with cap %d", 50, cap)
	}
	if d1.Promoted {
		t.Fatalf("saturated D1 must never promote")
	}
	if n := d1EdgeCount(t, d1.Data); n > cap {
		t.Fatalf("persisted D1 edges = %d, want <= cap %d", n, cap)
	}

	// Classes that did converge still graduate, and the transaction as a
	// whole counts as finished learning with D1 reported as saturated.
	if d5 := baselineOf(t, r, repo.D5Libraries); !d5.Promoted {
		t.Fatalf("D5 should promote on its own: %+v", d5)
	}
	stats, err := r.ListTransactionStats(ctx, repo.TransactionListParams{Limit: 10})
	if err != nil || len(stats) != 1 {
		t.Fatalf("stats: %v (%d rows)", err, len(stats))
	}
	if !stats[0].Promoted {
		t.Fatalf("transaction should be settled when the only unpromoted class is saturated: %+v", stats[0])
	}
	if len(stats[0].SaturatedClasses) != 1 || stats[0].SaturatedClasses[0] != repo.D1CallEdges {
		t.Fatalf("saturated_classes = %v, want [D1_call_edges]", stats[0].SaturatedClasses)
	}

	// Frozen: more identity-leaking traffic changes nothing in memory or the store.
	for i := 50; i < 80; i++ {
		eng.Process(ctx, saturationTrace(i))
	}
	if err := eng.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	after := baselineOf(t, r, repo.D1CallEdges)
	if n := d1EdgeCount(t, after.Data); n > cap {
		t.Fatalf("D1 kept growing after saturation: %d edges", n)
	}
	if after.ObservationCount != d1.ObservationCount {
		t.Fatalf("saturated class must stop counting observations: %d -> %d", d1.ObservationCount, after.ObservationCount)
	}
}

// TestSaturation_NeverScored guards the false-positive path: an operator can
// force-promote a saturated class, but scoring it would flag every member the
// truncated set never saw.
func TestSaturation_NeverScored(t *testing.T) {
	r := newFakeRepo()
	alerts := alertConfigFrom(repo.Policy{ID: 1, Name: "fire-at-0", Enabled: true, FireAtScore: 0})
	eng := New(r, nil, countOnlyConfig(5), alerts)
	eng.clock = func() time.Time { return time.Unix(0, 0).UTC() }
	eng.store.setCaps(2, 0)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		eng.Process(ctx, saturationTrace(i))
	}
	if err := eng.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	txID := r.transactions[0].ID
	if err := eng.PromoteBaselineClass(ctx, txID, repo.D1CallEdges); err != nil {
		t.Fatalf("force promote: %v", err)
	}
	before := len(r.anomalies)
	for i := 100; i < 110; i++ {
		eng.Process(ctx, saturationTrace(i))
	}
	if got := len(r.anomalies) - before; got != 0 {
		t.Fatalf("saturated D1 produced %d anomalies; it must not be scored", got)
	}
}

// TestSaturation_AdoptedByOtherReplica models two pods sharing the store: the
// flag one replica raises is authoritative for the other on its next load, so
// the second replica neither learns the class nor ships its own overflow.
func TestSaturation_AdoptedByOtherReplica(t *testing.T) {
	const cap = 3
	r := newFakeRepo()
	ctx := context.Background()
	newReplica := func() *Engine {
		eng := New(r, nil, countOnlyConfig(100), nil)
		eng.clock = func() time.Time { return time.Unix(0, 0).UTC() }
		eng.store.setCaps(cap, 0)
		return eng
	}

	a := newReplica()
	for i := 0; i < 10; i++ {
		a.Process(ctx, saturationTrace(i))
	}
	if err := a.Flush(ctx); err != nil {
		t.Fatalf("flush a: %v", err)
	}
	if !baselineOf(t, r, repo.D1CallEdges).Saturated {
		t.Fatalf("replica a did not persist saturation")
	}

	b := newReplica()
	for i := 1000; i < 1010; i++ {
		b.Process(ctx, saturationTrace(i))
	}
	txID := r.transactions[0].ID
	entry := b.store.entries[txID]
	entry.mu.Lock()
	cs := entry.classes[repo.D1CallEdges]
	saturated, edges := cs.value.Saturated, d1EdgeCount(t, cs.value.Data)
	entry.mu.Unlock()
	if !saturated {
		t.Fatalf("replica b did not adopt the saturated flag on load")
	}
	if edges > cap {
		t.Fatalf("replica b holds %d edges in memory, want <= %d", edges, cap)
	}
	if err := b.Flush(ctx); err != nil {
		t.Fatalf("flush b: %v", err)
	}
	if n := d1EdgeCount(t, baselineOf(t, r, repo.D1CallEdges).Data); n > cap {
		t.Fatalf("replica b shipped overflow: %d persisted edges", n)
	}
}

// TestSaturation_OversizedRowIsCappedOnLoad covers rows written before the cap
// existed (or by a replica running with a larger one): they are capped and
// flagged the moment a replica loads them, and the flag is flushed back.
func TestSaturation_OversizedRowIsCappedOnLoad(t *testing.T) {
	const cap = 3
	r := newFakeRepo()
	ctx := context.Background()
	tx, err := r.UpsertTransaction(ctx, "orders", "unknown", "GET /orders", repo.KindHTTP)
	if err != nil {
		t.Fatalf("upsert tx: %v", err)
	}
	var big json.RawMessage
	for i := 0; i < 20; i++ {
		big, err = (d1CallEdges{}).Merge(saturationTrace(i), big)
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
	}
	if err := r.UpsertBaselineClass(ctx, repo.BaselineClass{
		TransactionID: tx.ID, Class: repo.D1CallEdges, Data: big, DataSchemaVersion: 1,
		ObservationCount: 20, LearningStartedAt: time.Unix(0, 0).UTC(),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	eng := New(r, nil, countOnlyConfig(100), nil)
	eng.clock = func() time.Time { return time.Unix(0, 0).UTC() }
	eng.store.setCaps(cap, 0)
	eng.Process(ctx, saturationTrace(999))
	if err := eng.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	got := baselineOf(t, r, repo.D1CallEdges)
	if !got.Saturated {
		t.Fatalf("oversized legacy row was not flagged saturated on load")
	}
	entry := eng.store.entries[tx.ID]
	entry.mu.Lock()
	edges := d1EdgeCount(t, entry.classes[repo.D1CallEdges].value.Data)
	entry.mu.Unlock()
	if edges > cap {
		t.Fatalf("in-memory D1 = %d edges, want <= %d", edges, cap)
	}
}

func TestLearningOf_SaturatedPhase(t *testing.T) {
	bc := repo.BaselineClass{Saturated: true, ObservationCount: 40, LastChangedAt: time.Unix(10, 0), ObservationCountAtLastChange: 39}
	got := LearningOf(bc, repo.LearningPolicy{}, time.Unix(1000, 0))
	if got.Phase != "saturated" {
		t.Fatalf("phase = %q, want saturated", got.Phase)
	}
	if got.ReadyToPromote {
		t.Fatalf("saturated class must not be ready to promote")
	}
	if got.Stability.Observations.Met || got.Stability.Duration.Met {
		t.Fatalf("saturated class must not report stability met: %+v", got.Stability)
	}
}
