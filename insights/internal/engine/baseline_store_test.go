package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// normalTrace is a stable normal-traffic trace used across the store tests.
func normalTrace() *Trace {
	return mkTrace(
		withRoot("POST /checkout", "user-svc", tracepb.Span_SPAN_KIND_SERVER,
			map[string]string{"http.method": "POST"}),
		withChild("root", "inventory.reserve", tracepb.Span_SPAN_KIND_CLIENT,
			map[string]string{"server.address": "inventory", "server.port": "8080"}, "okhttp3"),
	)
}

// TestStore_LoadThroughReadsOnce proves the baseline read leaves the hot path:
// the DB is read once per transaction (the load-through miss), not per trace.
func TestStore_LoadThroughReadsOnce(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, countOnlyConfig(DefaultMinObservations), nil)

	for i := 0; i < 20; i++ {
		eng.Process(context.Background(), normalTrace())
	}

	if r.getBaselineCalls != 1 {
		t.Fatalf("expected exactly 1 GetBaselineForTransaction (load-through), got %d", r.getBaselineCalls)
	}
}

// TestStore_FreezeStopsWritesAfterPromotion is the headline scale property:
// once every class is promoted, normal traffic produces no new deltas, so the
// flusher writes nothing.
func TestStore_FreezeStopsWritesAfterPromotion(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, countOnlyConfig(DefaultMinObservations), nil)

	for i := int64(0); i < DefaultMinObservations; i++ {
		eng.Process(context.Background(), normalTrace())
	}
	if err := eng.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	deltasAtPromotion := r.appliedDeltas

	// Post-promotion normal traffic: frozen classes must not accumulate.
	for i := 0; i < 100; i++ {
		eng.Process(context.Background(), normalTrace())
	}
	if err := eng.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if r.appliedDeltas != deltasAtPromotion {
		t.Fatalf("expected no new deltas after promotion, got %d extra",
			r.appliedDeltas-deltasAtPromotion)
	}
}

// TestStore_CountCorrectAfterFlush checks the observation_count is exact (no
// loss, no double-count) across multiple flush cycles interleaved with merges.
func TestStore_CountCorrectAfterFlush(t *testing.T) {
	r := newFakeRepo()
	// High threshold so nothing promotes/freezes during the run.
	eng := New(r, nil, countOnlyConfig(1_000_000), nil)

	const total = 37
	for i := 0; i < total; i++ {
		eng.Process(context.Background(), normalTrace())
		if i%7 == 0 { // flush at irregular intervals while still learning
			if err := eng.Flush(context.Background()); err != nil {
				t.Fatalf("flush: %v", err)
			}
		}
	}
	if err := eng.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	for _, b := range r.baselines {
		if b.ObservationCount != int64(total) {
			t.Errorf("class %s observation_count = %d, want %d", b.Class, b.ObservationCount, total)
		}
	}
}

// TestStore_RollbackOnFlushError proves a failed apply loses no learning: the
// drained deltas are restored and the next successful flush persists the exact
// total.
func TestStore_RollbackOnFlushError(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, countOnlyConfig(1_000_000), nil)

	const total = 10
	for i := 0; i < total; i++ {
		eng.Process(context.Background(), normalTrace())
	}

	r.applyErr = errors.New("db down")
	if err := eng.Flush(context.Background()); err == nil {
		t.Fatalf("expected flush error")
	}
	if r.appliedDeltas != 0 {
		t.Fatalf("expected nothing applied on failed flush, got %d", r.appliedDeltas)
	}

	r.applyErr = nil
	if err := eng.Flush(context.Background()); err != nil {
		t.Fatalf("retry flush: %v", err)
	}
	for _, b := range r.baselines {
		if b.ObservationCount != int64(total) {
			t.Errorf("class %s observation_count = %d, want %d after rollback+retry",
				b.Class, b.ObservationCount, total)
		}
	}
}

// TestStore_ConfirmOnAnomalySuppressesKnownEdge simulates the multi-replica
// race: replica A's in-memory baseline is stale (missing an edge another
// replica already persisted). A's score flags it, but confirm-on-anomaly
// re-reads the fresh baseline, sees the edge is known, and suppresses the false
// alert — and adopts the edge so it won't re-flag.
func TestStore_ConfirmOnAnomalySuppressesKnownEdge(t *testing.T) {
	r := newFakeRepo()
	r.policies = append(r.policies, repo.Policy{ID: 1, Enabled: true, FireAtScore: 1})
	eng := New(r, nil, countOnlyConfig(DefaultMinObservations), nil)

	// Learn + promote on traffic that only ever calls inventory.
	for i := int64(0); i < DefaultMinObservations; i++ {
		eng.Process(context.Background(), normalTrace())
	}
	if err := eng.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// Another replica learns a NEW legitimate call edge and persists it to the
	// shared DB, bypassing engine A's in-memory store. The differing span is an
	// INTERNAL child with the same egress peer and library as learned traffic,
	// so ONLY D1 (call edges) deviates — isolating the confirm-on-anomaly path.
	withPricing := mkTrace(
		withRoot("POST /checkout", "user-svc", tracepb.Span_SPAN_KIND_SERVER,
			map[string]string{"http.method": "POST"}),
		withChild("root", "inventory.reserve", tracepb.Span_SPAN_KIND_CLIENT,
			map[string]string{"server.address": "inventory", "server.port": "8080"}, "okhttp3"),
		withChild("root", "pricing.compute", tracepb.Span_SPAN_KIND_INTERNAL, nil, "okhttp3"),
	)
	d1Edges, err := (d1CallEdges{}).Merge(withPricing, nil)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	transactionID := r.transactions[0].ID
	if _, err := r.ApplyBaselineDeltas(context.Background(), []repo.BaselineDelta{{
		TransactionID: transactionID, Class: repo.D1CallEdges, Contribution: d1Edges,
	}}, classMerger{}); err != nil {
		t.Fatalf("seed other-replica edge: %v", err)
	}

	anomaliesBefore := len(r.anomalies)
	// Engine A sees the pricing edge for the first time; its stale memory would
	// flag it, but confirm-on-anomaly must suppress (the DB already has it).
	eng.Process(context.Background(), withPricing)
	if err := eng.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if len(r.anomalies) != anomaliesBefore {
		t.Fatalf("confirm-on-anomaly should have suppressed the known edge, fired %d",
			len(r.anomalies)-anomaliesBefore)
	}
}

// TestStore_EvictionFlushBeforeEvict proves the LRU cap only drops CLEAN
// (already-flushed) entries, never unflushed deltas.
func TestStore_EvictionFlushBeforeEvict(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, countOnlyConfig(1_000_000), nil)
	eng.store.maxTxns = 2 // tiny cap to force eviction

	mk := func(op string) *Trace {
		return mkTrace(withRoot(op, "svc", tracepb.Span_SPAN_KIND_SERVER, nil))
	}

	// Three distinct transactions, all dirty (never flushed): the cap is 2 but
	// nothing may be evicted because everything is dirty.
	eng.Process(context.Background(), mk("/a"))
	eng.Process(context.Background(), mk("/b"))
	eng.Process(context.Background(), mk("/c"))

	eng.store.mu.Lock()
	resident := len(eng.store.entries)
	eng.store.mu.Unlock()
	if resident != 3 {
		t.Fatalf("dirty entries must not be evicted; resident=%d want 3", resident)
	}

	// Flush makes them clean; new transactions can now evict the LRU ones.
	if err := eng.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	eng.Process(context.Background(), mk("/d"))
	eng.Process(context.Background(), mk("/e"))
	eng.store.mu.Lock()
	resident = len(eng.store.entries)
	eng.store.mu.Unlock()
	if resident > 3 {
		t.Fatalf("expected eviction back toward the cap, resident=%d", resident)
	}

	// Everything that was learned is still persisted (eviction never dropped
	// unflushed data).
	if err := eng.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	seen := map[string]bool{}
	for _, tx := range r.transactions {
		seen[tx.Operation] = true
	}
	for _, op := range []string{"/a", "/b", "/c", "/d", "/e"} {
		if !seen[op] {
			t.Errorf("transaction %s lost", op)
		}
	}
}

// TestStore_ConcurrentProcessAndFlush hammers the store from many goroutines
// while a flusher drains concurrently, then asserts every class's count is
// EXACT (no lost or double-counted observations) after a final quiescent
// flush. Best run under -race.
func TestStore_ConcurrentProcessAndFlush(t *testing.T) {
	r := newFakeRepo()
	// No promotion (high threshold) so every trace keeps merging and counts
	// stay a clean function of the number of traces sent.
	eng := New(r, nil, countOnlyConfig(1_000_000), nil)

	const (
		numTx           = 4
		workersPerTx    = 3
		tracesPerWorker = 60
	)

	trace := func(op string) *Trace {
		return mkTrace(
			withRoot(op, "svc", tracepb.Span_SPAN_KIND_SERVER, map[string]string{"http.method": "GET"}),
			withChild("root", "child", tracepb.Span_SPAN_KIND_CLIENT,
				map[string]string{"server.address": "peer", "server.port": "80"}, "okhttp3"),
		)
	}

	stop := make(chan struct{})
	var flusher sync.WaitGroup
	flusher.Add(1)
	go func() {
		defer flusher.Done()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_ = eng.Flush(context.Background())
			}
		}
	}()

	var workers sync.WaitGroup
	for tx := 0; tx < numTx; tx++ {
		op := fmt.Sprintf("/op-%d", tx)
		for w := 0; w < workersPerTx; w++ {
			workers.Add(1)
			go func(op string) {
				defer workers.Done()
				for i := 0; i < tracesPerWorker; i++ {
					eng.Process(context.Background(), trace(op))
				}
			}(op)
		}
	}
	workers.Wait()
	close(stop)
	flusher.Wait()

	// Final quiescent flush converges any deltas left in flight.
	if err := eng.Flush(context.Background()); err != nil {
		t.Fatalf("final flush: %v", err)
	}

	want := int64(workersPerTx * tracesPerWorker)
	if len(r.baselines) == 0 {
		t.Fatalf("no baselines persisted")
	}
	for _, b := range r.baselines {
		if b.ObservationCount != want {
			t.Errorf("tx %d class %s: observation_count = %d, want %d",
				b.TransactionID, b.Class, b.ObservationCount, want)
		}
	}
}
