package engine

import (
	"encoding/json"
	"testing"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

// d7Evidence builds the evidence map the engine passes to enrichers, with a D7
// blob shaped like scorer_d7.go emits.
func d7Evidence(accesses []map[string]string, shapes []map[string]string) map[string]json.RawMessage {
	blob, _ := json.Marshal(map[string]any{
		"new_accesses":     accesses,
		"new_query_shapes": shapes,
	})
	return map[string]json.RawMessage{string(repo.D7DBAccess): blob}
}

func sourcesOf(sigs []risk.Signal) map[string]risk.Signal {
	out := make(map[string]risk.Signal, len(sigs))
	for _, s := range sigs {
		out[string(s.Source)] = s
	}
	return out
}

// dangerous runs the D7 enricher with the built-in default config (the common
// case for these tests); the sensitive-table override is exercised separately.
// EnrichRisk takes the class's own evidence blob, so pull D7's out of the map.
func dangerous(ev map[string]json.RawMessage) []risk.Signal {
	return d7DBAccess{}.EnrichRisk(ev[string(repo.D7DBAccess)], enrichConfig{})
}

func TestD7Enricher_BenignReadIsQuiet(t *testing.T) {
	// A new SELECT on a mundane table must add NOTHING - the base D7 signal
	// already covers the "new access" novelty; we don't want to escalate noise.
	got := dangerous(d7Evidence(
		[]map[string]string{{"table": "products", "op": "SELECT"}}, nil))
	if len(got) != 0 {
		t.Fatalf("benign read should produce no enricher signals, got %+v", got)
	}
}

func TestD7Enricher_DestructiveOp(t *testing.T) {
	got := sourcesOf(dangerous(d7Evidence(
		[]map[string]string{{"table": "orders", "op": "DELETE"}}, nil)))
	s, ok := got["d7_destructive_op"]
	if !ok {
		t.Fatalf("expected a destructive-op signal, got %v", got)
	}
	if s.Category != risk.CategoryImpact {
		t.Errorf("destructive op category = %q, want %q", s.Category, risk.CategoryImpact)
	}
	if _, extra := got["d7_sensitive_table"]; extra {
		t.Errorf("orders is not sensitive; should not add a sensitive-table signal")
	}
}

func TestD7Enricher_SensitiveTable(t *testing.T) {
	got := sourcesOf(dangerous(d7Evidence(
		[]map[string]string{{"table": "user_credentials", "op": "SELECT"}}, nil)))
	if _, ok := got["d7_sensitive_table"]; !ok {
		t.Fatalf("expected a sensitive-table signal for user_credentials, got %v", got)
	}
	if _, ok := got["d7_destructive_op"]; ok {
		t.Errorf("SELECT is not destructive; should not add a destructive-op signal")
	}
}

func TestD7Enricher_InjectionShape(t *testing.T) {
	got := sourcesOf(dangerous(d7Evidence(nil,
		[]map[string]string{{"op": "SELECT", "tables": "admins,orders,users"}})))
	s, ok := got["d7_injection_shape"]
	if !ok {
		t.Fatalf("expected an injection-shape signal for a 3-table join, got %v", got)
	}
	if s.Category != risk.CategoryInjection {
		t.Errorf("injection shape category = %q, want %q", s.Category, risk.CategoryInjection)
	}

	// A two-table join is below the threshold; an opaque (#hash) shape is not a
	// real table list - neither should trip the enricher.
	quiet := dangerous(d7Evidence(nil, []map[string]string{
		{"op": "SELECT", "tables": "orders,users"},
		{"op": "SELECT", "tables": "#deadbeef"},
	}))
	if len(quiet) != 0 {
		t.Fatalf("two-table / opaque shapes should be quiet, got %+v", quiet)
	}
}

func TestD7Enricher_SchemaRecon(t *testing.T) {
	// A new access to a system-catalog table is schema enumeration; it must add
	// an Injection-category signal, and - being Injection - a mundane new table
	// beside it must not.
	got := sourcesOf(dangerous(d7Evidence(
		[]map[string]string{
			{"table": "information_schema.tables", "op": "SELECT"},
			{"table": "products", "op": "SELECT"},
		}, nil)))
	s, ok := got["d7_schema_recon"]
	if !ok {
		t.Fatalf("access to information_schema should flag schema recon, got %v", got)
	}
	if s.Category != risk.CategoryInjection {
		t.Errorf("schema recon category = %q, want %q", s.Category, risk.CategoryInjection)
	}

	// A plain business table must stay quiet.
	if got := (d7DBAccess{}).EnrichRisk(d7Evidence(
		[]map[string]string{{"table": "orders", "op": "SELECT"}}, nil)[string(repo.D7DBAccess)], enrichConfig{}); len(got) != 0 {
		t.Fatalf("ordinary table should not flag schema recon, got %+v", got)
	}
}

func TestD7Enricher_CustomSchemaCatalog(t *testing.T) {
	cfg := enrichConfig{lists: map[repo.EnricherListKey][]string{"schema_catalog_tables": {"catalog_meta"}}}
	// Custom list replaces defaults: the bespoke catalog table is flagged...
	if got := sourcesOf(d7DBAccess{}.EnrichRisk(d7Evidence(
		[]map[string]string{{"table": "catalog_meta", "op": "SELECT"}}, nil)[string(repo.D7DBAccess)], cfg)); len(got) == 0 {
		t.Fatalf("custom catalog keyword should flag catalog_meta")
	}
	// ...while the built-in information_schema no longer is.
	if got := (d7DBAccess{}).EnrichRisk(d7Evidence(
		[]map[string]string{{"table": "information_schema.tables", "op": "SELECT"}}, nil)[string(repo.D7DBAccess)], cfg); len(got) != 0 {
		t.Errorf("custom list should replace defaults; information_schema should be quiet, got %v", got)
	}
}

func TestD7Enricher_NoD7EvidenceIsQuiet(t *testing.T) {
	if got := dangerous(map[string]json.RawMessage{}); got != nil {
		t.Fatalf("no D7 evidence should yield nil, got %+v", got)
	}
}

// TestD7Enricher_EscalatesScore covers the core enricher property: a benign new
// read stays "low", but a destructive op on a sensitive table becomes
// "critical" - same base D7 deviation, very different score.
func TestD7Enricher_EscalatesScore(t *testing.T) {
	base, _ := risk.NewSignal(repo.D7DBAccess, 1.0, "new db access")

	benign := risk.Assess(append([]risk.Signal{base},
		dangerous(d7Evidence([]map[string]string{{"table": "products", "op": "SELECT"}}, nil))...))
	if benign.Severity != risk.SeverityLow {
		t.Errorf("benign new read severity = %q, want %q (score %d)", benign.Severity, risk.SeverityLow, benign.Score)
	}

	danger := risk.Assess(append([]risk.Signal{base},
		dangerous(d7Evidence([]map[string]string{{"table": "accounts", "op": "DELETE"}}, nil))...))
	if danger.Severity != risk.SeverityCritical {
		t.Errorf("DELETE on accounts severity = %q, want %q (score %d)", danger.Severity, risk.SeverityCritical, danger.Score)
	}
	if danger.Score <= benign.Score {
		t.Errorf("dangerous score %d should exceed benign %d", danger.Score, benign.Score)
	}
}

// TestEnricherWeightOverrideMutes proves a policy can tune an enricher by source
// like any class signal: setting d7_destructive_op to 0 drops its contribution,
// so the same DELETE no longer escalates.
func TestEnricherWeightOverrideMutes(t *testing.T) {
	base, _ := risk.NewSignal(repo.D7DBAccess, 1.0, "new db access")
	sigs := append([]risk.Signal{base},
		dangerous(d7Evidence([]map[string]string{{"table": "orders", "op": "DELETE"}}, nil))...)

	full := risk.Assess(sigs).Score
	muted := risk.Assess(risk.ApplyWeightOverrides(sigs, map[repo.SignalSource]int{"d7_destructive_op": 0})).Score
	if muted >= full {
		t.Fatalf("muting d7_destructive_op should lower the score: full=%d muted=%d", full, muted)
	}
}

// TestD7Enricher_CustomSensitiveTables proves a scope's sensitive-table list
// replaces the built-in defaults: a table that matches only the custom keyword
// is flagged, while one that matched a default keyword no longer is.
func TestD7Enricher_CustomSensitiveTables(t *testing.T) {
	cfg := enrichConfig{lists: map[repo.EnricherListKey][]string{"sensitive_tables": {"ledger"}}}

	got := sourcesOf(d7DBAccess{}.EnrichRisk(d7Evidence(
		[]map[string]string{{"table": "billing_ledger", "op": "SELECT"}}, nil)[string(repo.D7DBAccess)], cfg))
	if _, ok := got["d7_sensitive_table"]; !ok {
		t.Fatalf("custom keyword 'ledger' should flag billing_ledger, got %v", got)
	}

	// "account" is a default keyword but NOT in the custom list, so with the
	// override in effect it must no longer be treated as sensitive.
	got = sourcesOf(d7DBAccess{}.EnrichRisk(d7Evidence(
		[]map[string]string{{"table": "accounts", "op": "SELECT"}}, nil)[string(repo.D7DBAccess)], cfg))
	if _, ok := got["d7_sensitive_table"]; ok {
		t.Errorf("custom list should replace defaults; 'accounts' should not be flagged, got %v", got)
	}
}

// d1Evidence builds a D1 blob shaped like scorer_d1.go emits, with one new edge
// per target service so distinct-callee counting is easy to reason about.
func d1Evidence(services ...string) json.RawMessage {
	edges := make([]d1Edge, 0, len(services))
	for _, svc := range services {
		edges = append(edges, d1Edge{From: d1Node{Svc: "frontend"}, To: d1Node{Svc: svc}})
	}
	blob, _ := json.Marshal(map[string]any{"new_edges": edges})
	return blob
}

func TestD1Enricher_FanoutOnManyNewServices(t *testing.T) {
	got := sourcesOf(d1CallEdges{}.EnrichRisk(
		d1Evidence("a", "b", "c", "d", "e"), enrichConfig{}))
	s, ok := got["d1_edge_fanout"]
	if !ok {
		t.Fatalf("reaching 5 new services should flag fan-out, got %v", got)
	}
	if s.Category != risk.CategoryLateralMovement {
		t.Errorf("fan-out category = %q, want %q", s.Category, risk.CategoryLateralMovement)
	}
}

func TestD1Enricher_QuietBelowThresholdAndDedup(t *testing.T) {
	// Four distinct services is below the threshold.
	if got := (d1CallEdges{}).EnrichRisk(d1Evidence("a", "b", "c", "d"), enrichConfig{}); len(got) != 0 {
		t.Fatalf("4 new services is below threshold, should be quiet, got %+v", got)
	}
	// Many edges but only three DISTINCT services (multiple ops each) must not
	// trip: breadth of services is the signal, not raw edge count.
	edges := []d1Edge{
		{From: d1Node{Svc: "frontend", Op: "GET /a"}, To: d1Node{Svc: "x", Op: "op1"}},
		{From: d1Node{Svc: "frontend", Op: "GET /a"}, To: d1Node{Svc: "x", Op: "op2"}},
		{From: d1Node{Svc: "frontend", Op: "GET /a"}, To: d1Node{Svc: "y", Op: "op1"}},
		{From: d1Node{Svc: "frontend", Op: "GET /a"}, To: d1Node{Svc: "y", Op: "op2"}},
		{From: d1Node{Svc: "frontend", Op: "GET /a"}, To: d1Node{Svc: "z", Op: "op1"}},
	}
	blob, _ := json.Marshal(map[string]any{"new_edges": edges})
	if got := (d1CallEdges{}).EnrichRisk(blob, enrichConfig{}); len(got) != 0 {
		t.Fatalf("3 distinct services should be quiet regardless of edge count, got %+v", got)
	}
}

// d2Evidence builds a D2 blob shaped like scorer_d2.go emits. Hosts default to
// port 443 (normal TLS); use d2EvidencePeers for explicit ports.
func d2Evidence(hosts ...string) json.RawMessage {
	peers := make([]peer, 0, len(hosts))
	for _, h := range hosts {
		peers = append(peers, peer{Host: h, Port: 443})
	}
	blob, _ := json.Marshal(map[string]any{"new_peers": peers})
	return blob
}

func d2EvidencePeers(peers ...peer) json.RawMessage {
	blob, _ := json.Marshal(map[string]any{"new_peers": peers})
	return blob
}

func TestD2Enricher_MetadataEndpoint(t *testing.T) {
	got := sourcesOf(d2Egress{}.EnrichRisk(
		d2Evidence("api.stripe.com", "169.254.169.254"), enrichConfig{}))
	s, ok := got["d2_metadata_endpoint"]
	if !ok {
		t.Fatalf("egress to 169.254.169.254 should flag metadata endpoint, got %v", got)
	}
	if s.Category != risk.CategoryExfiltration {
		t.Errorf("metadata endpoint category = %q, want %q", s.Category, risk.CategoryExfiltration)
	}
}

func TestD2Enricher_QuietForNormalEgressAndExactMatch(t *testing.T) {
	if got := (d2Egress{}).EnrichRisk(d2Evidence("api.stripe.com"), enrichConfig{}); len(got) != 0 {
		t.Fatalf("normal egress should be quiet, got %+v", got)
	}
	// Exact-match only: an address that merely CONTAINS a metadata IP must not trip.
	if got := (d2Egress{}).EnrichRisk(d2Evidence("my-169.254.169.254-proxy.example.com"), enrichConfig{}); len(got) != 0 {
		t.Fatalf("substring of a metadata IP should not match, got %+v", got)
	}
}

func TestD2Enricher_DangerousPort(t *testing.T) {
	// A new peer on a reverse-shell port (default list) must flag; a normal peer
	// on 443 alongside it must not add a second port signal.
	got := sourcesOf((d2Egress{}).EnrichRisk(d2EvidencePeers(
		peer{Host: "attacker.example.com", Port: 4444},
		peer{Host: "api.stripe.com", Port: 443},
	), enrichConfig{}))
	s, ok := got["d2_dangerous_port"]
	if !ok {
		t.Fatalf("egress on port 4444 should flag a dangerous port, got %v", got)
	}
	if s.Category != risk.CategoryExfiltration {
		t.Errorf("dangerous port category = %q, want %q", s.Category, risk.CategoryExfiltration)
	}
	if _, meta := got["d2_metadata_endpoint"]; meta {
		t.Errorf("no metadata peer present; should not add a metadata signal")
	}
}

func TestD2Enricher_KnownHostNewDangerousPort(t *testing.T) {
	// The whole point: a new dangerous port even on an ordinary host is caught,
	// because D2 keys on {host,port} so this is a new peer.
	if got := (d2Egress{}).EnrichRisk(d2EvidencePeers(
		peer{Host: "internal-svc", Port: 1337}), enrichConfig{}); len(got) == 0 {
		t.Fatalf("port 1337 on a known host should still flag")
	}
	// A normal port stays quiet.
	if got := (d2Egress{}).EnrichRisk(d2EvidencePeers(
		peer{Host: "internal-svc", Port: 8080}), enrichConfig{}); len(got) != 0 {
		t.Fatalf("port 8080 should be quiet, got %+v", got)
	}
}

func TestD2Enricher_CustomDangerousPorts(t *testing.T) {
	cfg := enrichConfig{lists: map[repo.EnricherListKey][]string{"dangerous_ports": {"9999"}}}
	// Custom list replaces defaults: 9999 flags...
	if got := (d2Egress{}).EnrichRisk(d2EvidencePeers(peer{Host: "h", Port: 9999}), cfg); len(got) == 0 {
		t.Fatalf("custom port 9999 should flag")
	}
	// ...and the built-in 4444 no longer does.
	if got := (d2Egress{}).EnrichRisk(d2EvidencePeers(peer{Host: "h", Port: 4444}), cfg); len(got) != 0 {
		t.Fatalf("custom list should replace defaults; 4444 should be quiet, got %+v", got)
	}
}

func TestD2Enricher_CustomMetadataList(t *testing.T) {
	cfg := enrichConfig{lists: map[repo.EnricherListKey][]string{"metadata_endpoints": {"100.64.0.1"}}}
	// The custom list replaces defaults: the private endpoint is now flagged...
	if got := sourcesOf(d2Egress{}.EnrichRisk(d2Evidence("100.64.0.1"), cfg)); len(got) == 0 {
		t.Fatalf("custom metadata address should be flagged")
	}
	// ...while the built-in default is no longer treated as metadata.
	if got := (d2Egress{}).EnrichRisk(d2Evidence("169.254.169.254"), cfg); len(got) != 0 {
		t.Fatalf("custom list should replace defaults; 169.254.169.254 should be quiet, got %+v", got)
	}
}

// d8Evidence builds a D8 blob shaped like scorer_d8.go emits for the response side.
func d8Evidence(respSize, respP99 int64) json.RawMessage {
	blob, _ := json.Marshal(map[string]any{
		"response_body_size": respSize,
		"response_p99":       respP99,
	})
	return blob
}

func TestD8Enricher_OversizedResponse(t *testing.T) {
	got := sourcesOf(d8PayloadSize{}.EnrichRisk(d8Evidence(2_000_000, 100_000), enrichConfig{}))
	s, ok := got["d8_response_exfil"]
	if !ok {
		t.Fatalf("20x response should flag bulk exfil, got %v", got)
	}
	if s.Category != risk.CategoryExfiltration {
		t.Errorf("response exfil category = %q, want %q", s.Category, risk.CategoryExfiltration)
	}
}

func TestD8Enricher_QuietBelowRatio(t *testing.T) {
	// 3x over p99 trips the base D8 signal but is below the exfil ratio.
	if got := (d8PayloadSize{}).EnrichRisk(d8Evidence(300_000, 100_000), enrichConfig{}); len(got) != 0 {
		t.Fatalf("3x response is below exfil ratio, should be quiet, got %+v", got)
	}
	// A request-only deviation carries no response fields; must be quiet.
	reqOnly, _ := json.Marshal(map[string]any{"request_body_size": 9_000_000, "request_p99": 1_000})
	if got := (d8PayloadSize{}).EnrichRisk(reqOnly, enrichConfig{}); len(got) != 0 {
		t.Fatalf("request-only deviation should not trip response exfil, got %+v", got)
	}
}
