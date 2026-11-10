package engine

import (
	"encoding/json"
	"testing"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

func d2Peers(hosts ...string) map[string]json.RawMessage {
	peers := make([]map[string]any, 0, len(hosts))
	for _, h := range hosts {
		peers = append(peers, map[string]any{"host": h, "port": 443})
	}
	ev, err := json.Marshal(map[string]any{"new_peers": peers})
	if err != nil {
		panic(err)
	}
	return map[string]json.RawMessage{string(repo.D2Egress): ev}
}

// claimsOverForkedTrace builds the entry spans of
//
//	frontend -> coupon -> pricing
//	         -> moshe
//
// and the claims tracker over that shape, so a test fires on one entry and
// filters on another with the real enclosure relationships between them:
// pricing is inside coupon, moshe is inside neither.
func claimsOverForkedTrace() (*anomalyClaims, map[string]*Span) {
	entry := func(id, parent string) *Span {
		return &Span{SpanID: id, ParentSpanID: parent, Service: id, Kind: tracepb.Span_SPAN_KIND_SERVER}
	}
	frontend, coupon := entry("frontend", ""), entry("coupon", "frontend")
	pricing, moshe := entry("pricing", "coupon"), entry("moshe", "frontend")
	coupon.Children = []*Span{pricing}
	frontend.Children = []*Span{coupon, moshe}

	spans := map[string]*Span{"frontend": frontend, "coupon": coupon, "pricing": pricing, "moshe": moshe}
	return newAnomalyClaims(&Trace{TraceID: "tid", Root: frontend}), spans
}

func TestClaims_SameItemSuppressedBelow(t *testing.T) {
	c, spans := claimsOverForkedTrace()
	deviated := []repo.DeviationClass{repo.D2Egress}

	outer, outerEv := c.filter(spans["coupon"], deviated, d2Peers("attacker.com"))
	if len(outer) != 1 {
		t.Fatalf("the outermost entry must fire, got %v", outer)
	}
	c.take(spans["coupon"], outer, outerEv)

	inner, _ := c.filter(spans["pricing"], deviated, d2Peers("attacker.com"))
	if len(inner) != 0 {
		t.Errorf("an enclosed entry must not refire the same peer, got %v", inner)
	}
}

// A deviation the levels above did not fire is not a duplicate: it is the
// context-sensitive case the sub-transaction split exists to catch.
func TestClaims_UnclaimedItemStillFires(t *testing.T) {
	c, spans := claimsOverForkedTrace()
	deviated := []repo.DeviationClass{repo.D2Egress}

	c.take(spans["frontend"], deviated, d2Peers("attacker.com"))

	inner, _ := c.filter(spans["coupon"], deviated, d2Peers("evil.com"))
	if len(inner) != 1 {
		t.Errorf("a peer no enclosing entry flagged must still fire, got %v", inner)
	}
}

// A class is suppressed only when every one of its items was already fired.
func TestClaims_PartialOverlapKeepsClass(t *testing.T) {
	c, spans := claimsOverForkedTrace()
	deviated := []repo.DeviationClass{repo.D2Egress}

	c.take(spans["frontend"], deviated, d2Peers("attacker.com"))

	inner, innerEv := c.filter(spans["coupon"], deviated, d2Peers("attacker.com", "evil.com"))
	if len(inner) != 1 {
		t.Fatalf("a class holding an unclaimed item must survive, got %v", inner)
	}
	if innerEv[string(repo.D2Egress)] == nil {
		t.Error("evidence must be kept whole so the finding stays consistent")
	}
}

// D3 and D7 report no "new_*" items, so the class itself is the dedup unit.
func TestClaims_ItemlessClassDedupsByClass(t *testing.T) {
	c, spans := claimsOverForkedTrace()
	deviated := []repo.DeviationClass{repo.D3Latency}
	ev := map[string]json.RawMessage{string(repo.D3Latency): json.RawMessage(`{"p99_ms":210}`)}

	c.take(spans["frontend"], deviated, ev)

	inner, _ := c.filter(spans["coupon"], deviated, ev)
	if len(inner) != 0 {
		t.Errorf("an itemless class must dedup by class, got %v", inner)
	}
}

// Claiming happens on fire, not on deviation, so an outer transaction whose
// policy did not fire cannot silence a stricter one deeper in the trace.
func TestClaims_FilterAloneDoesNotClaim(t *testing.T) {
	c, spans := claimsOverForkedTrace()
	deviated := []repo.DeviationClass{repo.D2Egress}

	if got, _ := c.filter(spans["frontend"], deviated, d2Peers("attacker.com")); len(got) != 1 {
		t.Fatalf("first filter must pass the class through, got %v", got)
	}
	inner, _ := c.filter(spans["coupon"], deviated, d2Peers("attacker.com"))
	if len(inner) != 1 {
		t.Errorf("a deviation that never fired must stay available below, got %v", inner)
	}
}

// A trace is a tree: a firing on one branch speaks for what is inside that
// branch, never for a sibling. Suppressing across branches would drop the
// sibling's finding with nothing left reporting it, since the only entry that
// encloses both is the one that stayed quiet.
func TestClaims_SiblingBranchStillFires(t *testing.T) {
	c, spans := claimsOverForkedTrace()
	deviated := []repo.DeviationClass{repo.D2Egress}

	c.take(spans["coupon"], deviated, d2Peers("attacker.com"))

	sibling, _ := c.filter(spans["moshe"], deviated, d2Peers("attacker.com"))
	if len(sibling) != 1 {
		t.Errorf("moshe is not enclosed by coupon and must report its own peer, got %v", sibling)
	}
	enclosed, _ := c.filter(spans["pricing"], deviated, d2Peers("attacker.com"))
	if len(enclosed) != 0 {
		t.Errorf("pricing is enclosed by coupon and must stay quiet, got %v", enclosed)
	}
}
