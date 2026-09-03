package engine

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

func TestSupersededByReset(t *testing.T) {
	if supersededByReset(3, 3) {
		t.Fatal("matching generations must not be superseded")
	}
	if !supersededByReset(2, 3) {
		t.Fatal("stale generation must be superseded")
	}
	if !supersededByReset(4, 3) {
		t.Fatal("future stamp vs live must still be treated as superseded")
	}
}

func TestGuardrailBuffer_DropsAllowedTransactionsAfterReset(t *testing.T) {
	buf := newGuardrailBuffer()
	at := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	buf.add(repo.ScopeService, "ns/shop", risk.RuleAllowedTransactions, "http\tGET /x", 60, "tid-old", at, 1)
	buf.add(repo.ScopeService, "ns/shop", risk.RuleAllowedCallers, "evil", 55, "tid-edge", at, 1)

	hits := buf.drain(2)
	if len(hits) != 1 {
		t.Fatalf("drain after reset = %d hits, want 1 (edge rule kept)", len(hits))
	}
	if hits[0].RuleKey != risk.RuleAllowedCallers {
		t.Fatalf("kept rule = %q, want allowed_callers", hits[0].RuleKey)
	}
	if !buf.empty() {
		t.Fatal("buffer should be empty after drain")
	}
}

func TestGuardrailBuffer_KeepsAllowedTransactionsOnMatchingEpoch(t *testing.T) {
	buf := newGuardrailBuffer()
	at := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	buf.add(repo.ScopeService, "ns/shop", risk.RuleAllowedTransactions, "http\tGET /x", 60, "tid-1", at, 7)

	hits := buf.drain(7)
	if len(hits) != 1 {
		t.Fatalf("drain = %d hits, want 1", len(hits))
	}
	if hits[0].Occurrences != 1 || hits[0].LastTraceID != "tid-1" {
		t.Fatalf("hit = %+v", hits[0])
	}
}

func TestGuardrailBuffer_EpochIsPartOfGroupKey(t *testing.T) {
	buf := newGuardrailBuffer()
	at := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	buf.add(repo.ScopeService, "ns/shop", risk.RuleAllowedTransactions, "http\tGET /x", 10, "tid-old", at, 1)
	buf.add(repo.ScopeService, "ns/shop", risk.RuleAllowedTransactions, "http\tGET /x", 40, "tid-new", at.Add(time.Minute), 2)

	hits := buf.drain(2)
	if len(hits) != 1 {
		t.Fatalf("drain = %d hits, want the live-epoch group only", len(hits))
	}
	if hits[0].Occurrences != 1 || hits[0].MaxScore != 40 || hits[0].LastTraceID != "tid-new" {
		t.Fatalf("live-epoch hit = %+v", hits[0])
	}
}

func TestGuardrailBuffer_AggregatesSameGroup(t *testing.T) {
	buf := newGuardrailBuffer()
	t1 := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	t0 := t1.Add(-time.Minute)
	t2 := t1.Add(time.Minute)
	buf.add(repo.ScopeService, "ns/shop", risk.RuleAllowedCallers, "frontend", 20, "tid-mid", t1, 1)
	buf.add(repo.ScopeService, "ns/shop", risk.RuleAllowedCallers, "frontend", 50, "tid-late", t2, 1)
	buf.add(repo.ScopeService, "ns/shop", risk.RuleAllowedCallers, "frontend", 10, "tid-early", t0, 1)

	if buf.empty() {
		t.Fatal("expected accumulated groups")
	}
	hits := buf.drain(1)
	if len(hits) != 1 {
		t.Fatalf("drain = %d hits, want 1 aggregated group", len(hits))
	}
	h := hits[0]
	if h.Occurrences != 3 {
		t.Fatalf("occurrences = %d, want 3", h.Occurrences)
	}
	if h.MaxScore != 50 {
		t.Fatalf("maxScore = %d, want 50", h.MaxScore)
	}
	if !h.FirstSeen.Equal(t0) {
		t.Fatalf("firstSeen = %v, want %v", h.FirstSeen, t0)
	}
	if !h.LastSeen.Equal(t2) || h.LastTraceID != "tid-late" {
		t.Fatalf("lastSeen/trace = %v %q", h.LastSeen, h.LastTraceID)
	}
}

func TestGuardrailBuffer_EmptyDrain(t *testing.T) {
	buf := newGuardrailBuffer()
	if !buf.empty() {
		t.Fatal("new buffer should be empty")
	}
	if hits := buf.drain(1); hits != nil {
		t.Fatalf("empty drain = %v, want nil", hits)
	}
}

func TestAnomalyBuffer_DropsStaleEpochOnDrain(t *testing.T) {
	buf := newAnomalyBuffer()
	t1 := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	buf.add(repo.AnomalyOccurrence{
		TransactionID:    9,
		Signature:        "sig-a",
		TriggeredClasses: []repo.DeviationClass{repo.D5Libraries},
		Offending:        "D5_libraries=commons-text",
		Evidence:         json.RawMessage(`{"new_libs":[{"ns":"commons-text"}]}`),
		MaxScore:         30,
		LastSeen:         t1,
		LastTraceID:      "tid-old",
	}, 1)

	if occs := buf.drain(2); len(occs) != 0 {
		t.Fatalf("stale-epoch drain = %+v, want dropped", occs)
	}
	if !buf.empty() {
		t.Fatal("buffer should be empty after drain")
	}
}

func TestAnomalyBuffer_KeepsMatchingEpochAndAggregates(t *testing.T) {
	buf := newAnomalyBuffer()
	t1 := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Minute)
	buf.add(repo.AnomalyOccurrence{
		TransactionID:    9,
		Signature:        "sig-a",
		TriggeredClasses: []repo.DeviationClass{repo.D2Egress},
		Offending:        "D2_egress=evil.com",
		Evidence:         json.RawMessage(`{"new_peers":[{"host":"evil.com"}]}`),
		MaxScore:         20,
		LastSeen:         t1,
		LastTraceID:      "tid-1",
	}, 4)
	buf.add(repo.AnomalyOccurrence{
		TransactionID:    9,
		Signature:        "sig-a",
		TriggeredClasses: []repo.DeviationClass{repo.D2Egress},
		Offending:        "D2_egress=evil.com",
		Evidence:         json.RawMessage(`{"new_peers":[{"host":"evil.com","port":443}]}`),
		MaxScore:         45,
		LastSeen:         t2,
		LastTraceID:      "tid-2",
	}, 4)

	occs := buf.drain(4)
	if len(occs) != 1 {
		t.Fatalf("drain = %d, want 1 aggregated issue", len(occs))
	}
	o := occs[0]
	if o.Occurrences != 2 {
		t.Fatalf("occurrences = %d, want 2", o.Occurrences)
	}
	if o.MaxScore != 45 {
		t.Fatalf("maxScore = %d, want 45", o.MaxScore)
	}
	if o.LastTraceID != "tid-2" || string(o.Evidence) != `{"new_peers":[{"host":"evil.com","port":443}]}` {
		t.Fatalf("newest firing should win display fields: %+v ev=%s", o, o.Evidence)
	}
	if !o.FirstSeen.Equal(t1) {
		t.Fatalf("firstSeen = %v, want %v", o.FirstSeen, t1)
	}
}

func TestAnomalyBuffer_EmptyDrain(t *testing.T) {
	buf := newAnomalyBuffer()
	if !buf.empty() {
		t.Fatal("new buffer should be empty")
	}
	if occs := buf.drain(1); occs != nil {
		t.Fatalf("empty drain = %v, want nil", occs)
	}
}
