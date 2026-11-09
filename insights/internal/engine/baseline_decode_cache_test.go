package engine

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// decodeCacheTraces returns a trace whose call edge is learned into the
// baseline, plus one that reaches somewhere new and should therefore deviate.
func decodeCacheTraces() (learned, novel *Trace) {
	learned = mkTrace(
		withRoot("/buy", "frontend", tracepb.Span_SPAN_KIND_SERVER, nil),
		withChild("root", "call pricing", tracepb.Span_SPAN_KIND_CLIENT, map[string]string{
			"server.address": "pricing",
			"server.port":    "8080",
		}, "lib"),
	)
	novel = mkTrace(
		withRoot("/buy", "frontend", tracepb.Span_SPAN_KIND_SERVER, nil),
		withChild("root", "call fraud", tracepb.Span_SPAN_KIND_CLIENT, map[string]string{
			"server.address": "fraud",
			"server.port":    "9090",
		}, "otherlib"),
	)
	return learned, novel
}

// TestClassState_DecodedCacheInvalidatedOnDataChange is the core guard on the
// decoded baseline: once a class has learned a call, that call must stop being
// reported as new. Scoring caches the decoded baseline, so if a write to the
// class's data left the cache in place the replica would keep scoring against
// the baseline it just replaced and keep firing on traffic it has already
// accepted.
func TestClassState_DecodedCacheInvalidatedOnDataChange(t *testing.T) {
	sc := d1CallEdges{}
	learned, novel := decodeCacheTraces()

	base, err := sc.Merge(learned, nil)
	if err != nil {
		t.Fatalf("merge learned: %v", err)
	}
	cs := &classState{value: repo.BaselineClass{Class: repo.D1CallEdges, Data: base}}

	// The new call deviates, and decoding it now populates the cache.
	if dev, _, err := cs.decodedFor(sc).score(novel); err != nil || !dev {
		t.Fatalf("novel trace should deviate against the learned baseline, got dev=%v err=%v", dev, err)
	}

	// Fold it in, exactly as the merge and adopt paths do.
	updated, err := sc.Merge(novel, base)
	if err != nil {
		t.Fatalf("merge novel: %v", err)
	}
	cs.setData(updated)

	if dev, _, err := cs.decodedFor(sc).score(novel); err != nil || dev {
		t.Fatalf("edge is now part of the baseline and must not deviate, got dev=%v err=%v: the decoded cache outlived the data it came from", dev, err)
	}
}

// TestBaselineStore_AdoptInvalidatesDecodedCache covers the multi-replica path:
// adopt is how what other replicas learned reaches this one, so a cache that
// survived it would leave this replica scoring against a baseline the fleet has
// already moved past.
func TestBaselineStore_AdoptInvalidatesDecodedCache(t *testing.T) {
	sc := d1CallEdges{}
	learned, novel := decodeCacheTraces()

	base, err := sc.Merge(learned, nil)
	if err != nil {
		t.Fatalf("merge learned: %v", err)
	}
	converged, err := sc.Merge(novel, base)
	if err != nil {
		t.Fatalf("merge novel: %v", err)
	}

	s := newBaselineStore(nil, nil)
	entry := &transactionEntry{transactionID: 1, classes: map[repo.DeviationClass]*classState{}}
	entry.lruElem = s.lru.PushFront(entry)
	s.entries[1] = entry
	cs := &classState{value: repo.BaselineClass{TransactionID: 1, Class: repo.D1CallEdges, Data: base}}
	entry.classes[repo.D1CallEdges] = cs

	if dev, _, err := cs.decodedFor(sc).score(novel); err != nil || !dev {
		t.Fatalf("precondition: novel trace should deviate, got dev=%v err=%v", dev, err)
	}

	s.adopt([]repo.BaselineClass{{TransactionID: 1, Class: repo.D1CallEdges, Data: converged}})

	if dev, _, err := cs.decodedFor(sc).score(novel); err != nil || dev {
		t.Fatalf("after adopting the converged baseline the edge is known and must not deviate, got dev=%v err=%v", dev, err)
	}
}

// TestScoreDecoded_DoesNotMutateSharedBaseline pins the contract that makes
// sharing one decoded baseline safe. Every trace of a transaction scores
// against the same value, so a scorer that folded the trace into it would
// quietly teach the baseline the very thing it just flagged - the anomaly would
// fire once and then never again.
func TestScoreDecoded_DoesNotMutateSharedBaseline(t *testing.T) {
	learned, novel := decodeCacheTraces()

	for _, sc := range defaultScorers() {
		bd, ok := sc.(baselineDecoder)
		if !ok {
			continue
		}
		t.Run(string(sc.Class()), func(t *testing.T) {
			data, err := sc.Merge(learned, nil)
			if err != nil {
				t.Fatalf("merge: %v", err)
			}
			cs := &classState{value: repo.BaselineClass{Class: sc.Class(), Data: data}}

			before, err := json.Marshal(cs.decodedFor(bd))
			if err != nil {
				t.Fatalf("marshal decoded baseline: %v", err)
			}
			devFirst, evFirst, errFirst := cs.decodedFor(bd).score(novel)
			after, err := json.Marshal(cs.decodedFor(bd))
			if err != nil {
				t.Fatalf("marshal decoded baseline: %v", err)
			}
			if !bytes.Equal(before, after) {
				t.Fatalf("scoring changed the shared baseline:\n before %s\n after  %s", before, after)
			}

			// Same trace, same shared value: the verdict must not drift.
			devSecond, evSecond, errSecond := cs.decodedFor(bd).score(novel)
			if devFirst != devSecond || !bytes.Equal(evFirst, evSecond) || (errFirst == nil) != (errSecond == nil) {
				t.Fatalf("re-scoring the same trace changed the verdict: first (dev=%v ev=%s err=%v), second (dev=%v ev=%s err=%v)",
					devFirst, evFirst, errFirst, devSecond, evSecond, errSecond)
			}
		})
	}
}
