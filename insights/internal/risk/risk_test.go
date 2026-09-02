package risk

import (
	"testing"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

func TestSeverityForScore(t *testing.T) {
	cases := []struct {
		score int
		want  Severity
	}{
		{0, SeverityInfo},
		{19, SeverityInfo},
		{20, SeverityLow},
		{34, SeverityLow},
		{35, SeverityMedium},
		{49, SeverityMedium},
		{50, SeverityHigh},
		{74, SeverityHigh},
		{75, SeverityCritical},
		{100, SeverityCritical},
	}
	for _, c := range cases {
		if got := SeverityForScore(c.score); got != c.want {
			t.Errorf("SeverityForScore(%d) = %q, want %q", c.score, got, c.want)
		}
	}
}

func TestNewSignalScalesByConfidence(t *testing.T) {
	// D3 latency has weight 10. Half confidence -> contribution 5.
	s, ok := NewSignal(repo.D3Latency, 0.5, "latency")
	if !ok {
		t.Fatal("expected D3 to be in the catalog")
	}
	if s.Weight != 10 || s.Contribution != 5 {
		t.Fatalf("got weight=%d contribution=%d, want 10/5", s.Weight, s.Contribution)
	}
	if s.Category != CategoryResourceAbuse {
		t.Fatalf("got category %q, want resource_abuse", s.Category)
	}
}

func TestNewSignalClampsConfidence(t *testing.T) {
	s, _ := NewSignal(repo.D2Egress, 5, "egress") // >1 clamps to 1
	if s.Confidence != 1 || s.Contribution != 35 {
		t.Fatalf("got confidence=%v contribution=%d, want 1/35", s.Confidence, s.Contribution)
	}
}

func TestNewSignalUnknownClass(t *testing.T) {
	if _, ok := NewSignal(repo.DeviationClass("D99_made_up"), 1, "x"); ok {
		t.Fatal("expected ok=false for an unknown class")
	}
}

func TestAssessEmptyIsInfo(t *testing.T) {
	a := Assess(nil)
	if a.Score != 0 || a.Severity != SeverityInfo {
		t.Fatalf("empty assess = %d/%q, want 0/info", a.Score, a.Severity)
	}
}

func TestAssessLoneLatencyStaysInfo(t *testing.T) {
	// A single latency spike at full confidence contributes only 10 -> info,
	// so it does not fire on its own.
	s, _ := NewSignal(repo.D3Latency, 1, "latency")
	a := Assess([]Signal{s})
	if a.Score != 10 || a.Severity != SeverityInfo {
		t.Fatalf("lone latency = %d/%q, want 10/info", a.Score, a.Severity)
	}
	if a.Correlated {
		t.Fatal("single signal must not be correlated")
	}
}

func TestAssessCorrelationBonus(t *testing.T) {
	// New library (supply_chain 30) + new egress (exfiltration 35) = 65, plus
	// the +15 kill-chain bonus = 80 -> critical.
	lib, _ := NewSignal(repo.D5Libraries, 1, "new lib")
	egr, _ := NewSignal(repo.D2Egress, 1, "new peer")
	a := Assess([]Signal{lib, egr})
	if !a.Correlated {
		t.Fatal("two kill-chain categories should correlate")
	}
	if a.Score != 80 || a.Severity != SeverityCritical {
		t.Fatalf("lib+egress = %d/%q, want 80/critical", a.Score, a.Severity)
	}
}

func TestAssessNoBonusForSymptomOnlyCategories(t *testing.T) {
	// resource_abuse is not a kill-chain category, so two of them don't earn
	// the correlation bonus.
	lat, _ := NewSignal(repo.D3Latency, 1, "latency")
	pay, _ := NewSignal(repo.D8PayloadSize, 1, "payload")
	a := Assess([]Signal{lat, pay})
	if a.Correlated {
		t.Fatal("resource_abuse pair must not correlate")
	}
	if a.Score != 30 { // 10 + 20, no bonus
		t.Fatalf("latency+payload = %d, want 30", a.Score)
	}
}

func TestAssessCapsAt100(t *testing.T) {
	inj, _ := NewSignal(repo.D4ArgsReturns, 1, "inj")   // 40, injection
	egr, _ := NewSignal(repo.D2Egress, 1, "egr")        // 35, exfiltration
	lib, _ := NewSignal(repo.D5Libraries, 1, "lib")     // 30, supply_chain
	acc, _ := NewSignal(repo.D6AttrRelations, 1, "acc") // 35, access_anomaly
	a := Assess([]Signal{inj, egr, lib, acc})           // 140 + 15 bonus, capped
	if a.Score != 100 || a.Severity != SeverityCritical {
		t.Fatalf("over-cap assess = %d/%q, want 100/critical", a.Score, a.Severity)
	}
}
