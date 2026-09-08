package engine

import (
	"encoding/json"
	"testing"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
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

func TestClaims_SameItemSuppressedBelow(t *testing.T) {
	c := newAnomalyClaims()
	deviated := []repo.DeviationClass{repo.D2Egress}

	outer, outerEv := c.filter(deviated, d2Peers("attacker.com"))
	if len(outer) != 1 {
		t.Fatalf("the outermost entry must fire, got %v", outer)
	}
	c.take(outer, outerEv)

	inner, _ := c.filter(deviated, d2Peers("attacker.com"))
	if len(inner) != 0 {
		t.Errorf("an enclosed entry must not refire the same peer, got %v", inner)
	}
}

// A deviation the levels above did not fire is not a duplicate: it is the
// context-sensitive case the sub-transaction split exists to catch.
func TestClaims_UnclaimedItemStillFires(t *testing.T) {
	c := newAnomalyClaims()
	deviated := []repo.DeviationClass{repo.D2Egress}

	c.take(deviated, d2Peers("attacker.com"))

	inner, _ := c.filter(deviated, d2Peers("evil.com"))
	if len(inner) != 1 {
		t.Errorf("a peer no enclosing entry flagged must still fire, got %v", inner)
	}
}

// A class is suppressed only when every one of its items was already fired.
func TestClaims_PartialOverlapKeepsClass(t *testing.T) {
	c := newAnomalyClaims()
	deviated := []repo.DeviationClass{repo.D2Egress}

	c.take(deviated, d2Peers("attacker.com"))

	inner, innerEv := c.filter(deviated, d2Peers("attacker.com", "evil.com"))
	if len(inner) != 1 {
		t.Fatalf("a class holding an unclaimed item must survive, got %v", inner)
	}
	if innerEv[string(repo.D2Egress)] == nil {
		t.Error("evidence must be kept whole so the finding stays consistent")
	}
}

// D3 and D8 report no "new_*" items, so the class itself is the dedup unit.
func TestClaims_ItemlessClassDedupsByClass(t *testing.T) {
	c := newAnomalyClaims()
	deviated := []repo.DeviationClass{repo.D3Latency}
	ev := map[string]json.RawMessage{string(repo.D3Latency): json.RawMessage(`{"p99_ms":210}`)}

	c.take(deviated, ev)

	inner, _ := c.filter(deviated, ev)
	if len(inner) != 0 {
		t.Errorf("an itemless class must dedup by class, got %v", inner)
	}
}

// Claiming happens on fire, not on deviation, so an outer transaction whose
// policy did not fire cannot silence a stricter one deeper in the trace.
func TestClaims_FilterAloneDoesNotClaim(t *testing.T) {
	c := newAnomalyClaims()
	deviated := []repo.DeviationClass{repo.D2Egress}

	if got, _ := c.filter(deviated, d2Peers("attacker.com")); len(got) != 1 {
		t.Fatalf("first filter must pass the class through, got %v", got)
	}
	inner, _ := c.filter(deviated, d2Peers("attacker.com"))
	if len(inner) != 1 {
		t.Errorf("a deviation that never fired must stay available below, got %v", inner)
	}
}
