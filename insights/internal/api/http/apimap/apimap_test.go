package apimap

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/engine"
	"github.com/odigos-io/odigos-enterprise/insights/internal/engine/histogram"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// jsonKeys marshals v and returns its top-level object keys.
func jsonKeys(t *testing.T, v any) map[string]bool {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	keys := make(map[string]bool, len(m))
	for k := range m {
		keys[k] = true
	}
	return keys
}

func assertKeys(t *testing.T, got map[string]bool, want ...string) {
	t.Helper()
	for _, k := range want {
		if !got[k] {
			t.Errorf("missing json key %q (got %v)", k, keysOf(got))
		}
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestTransactionStatWire flattens the embedded Transaction and emits snake_case.
func TestTransactionStatWire(t *testing.T) {
	d := FromTransactionStat(repo.TransactionStat{
		Transaction: repo.Transaction{ID: 7, Service: "svc", Namespace: "ns", Operation: "GET /x", Kind: repo.KindHTTP},
		Volume:      3,
		LastSeen:    time.Unix(1000, 0),
		HasBaseline: true,
		Promoted:    true,
	})
	assertKeys(t, jsonKeys(t, d), "id", "service", "namespace", "operation", "operation_name", "identity_dimensions", "kind", "volume", "last_seen", "has_baseline", "promoted")
	if d.Kind != "HTTP" {
		t.Errorf("kind = %q, want HTTP", d.Kind)
	}
	if d.HasBaseline == nil || !*d.HasBaseline || d.Promoted == nil || !*d.Promoted {
		t.Errorf("promotion state = has_baseline:%v promoted:%v, want true/true", d.HasBaseline, d.Promoted)
	}
}

// TestServiceStatWire checks the service rollup wire shape.
func TestServiceStatWire(t *testing.T) {
	d := FromServiceStat(repo.ServiceStat{Namespace: "ns", Service: "svc", TransactionCount: 2, Volume: 9})
	assertKeys(t, jsonKeys(t, d), "namespace", "service", "transaction_count", "volume", "last_seen")
}

// TestServiceProfileWire checks callers/callees/egress/transactions always serialize as arrays.
func TestServiceProfileWire(t *testing.T) {
	d := FromServiceProfile(repo.ServiceProfile{Namespace: "ns", Service: "svc", Callers: nil, Callees: []string{"a"}, Egress: nil, Transactions: nil})
	assertKeys(t, jsonKeys(t, d), "namespace", "service", "callers", "callees", "egress", "transactions")
	if len(d.Callers) != 0 || len(d.Egress) != 0 || len(d.Transactions) != 0 || len(d.Callees) != 1 {
		t.Fatalf("lists = callers:%v callees:%v egress:%v transactions:%v", d.Callers, d.Callees, d.Egress, d.Transactions)
	}
}

// TestServiceNameListWire checks the service-names list envelope.
func TestServiceNameListWire(t *testing.T) {
	d := ServiceNameList(nil)
	if d.Count != 0 || len(d.Items) != 0 {
		t.Fatalf("empty list = count:%d items:%v", d.Count, d.Items)
	}
	d = ServiceNameList([]string{"a", "b"})
	if d.Count != 2 || len(d.Items) != 2 {
		t.Fatalf("list = count:%d items:%v", d.Count, d.Items)
	}
}

// TestBlastRadiusWire checks the subgraph maps to snake_case, always emits
// nodes/edges as arrays (never null), carries the virtual flag, and widens the
// summed uint64 counts to int64 on the wire.
func TestBlastRadiusWire(t *testing.T) {
	d := FromBlastRadiusSubgraph(repo.BlastRadiusSubgraph{
		Root: repo.BlastRadiusNode{Namespace: "ns", Service: "svc"},
		Nodes: []repo.BlastRadiusNode{
			{Namespace: "ns", Service: "svc"},
			{Namespace: "", Service: "postgres:5432", IsVirtual: true},
		},
		Edges: []repo.BlastRadiusEdge{
			{ClientNamespace: "ns", ClientService: "svc", ServerNamespace: "", ServerService: "postgres:5432", ConnectionType: "database", RequestCount: 9100, FailedCount: 37, LastSeen: time.Unix(1000, 0)},
		},
	})
	assertKeys(t, jsonKeys(t, d), "root", "nodes", "edges")
	if len(d.Nodes) != 2 || len(d.Edges) != 1 {
		t.Fatalf("counts = nodes:%d edges:%d", len(d.Nodes), len(d.Edges))
	}
	if !d.Nodes[1].IsVirtual {
		t.Errorf("expected node[1] virtual")
	}
	if d.Edges[0].RequestCount != 9100 || d.Edges[0].FailedCount != 37 {
		t.Errorf("counts = req:%d failed:%d", d.Edges[0].RequestCount, d.Edges[0].FailedCount)
	}
	assertKeys(t, jsonKeys(t, d.Edges[0]),
		"client_namespace", "client_service", "server_namespace", "server_service",
		"connection_type", "request_count", "failed_count", "last_seen")

	// An edgeless anchor still serializes nodes/edges as [] rather than null.
	empty := FromBlastRadiusSubgraph(repo.BlastRadiusSubgraph{Root: repo.BlastRadiusNode{Namespace: "ns", Service: "lonely"}})
	b, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if s := string(b); !strings.Contains(s, `"nodes":[]`) || !strings.Contains(s, `"edges":[]`) {
		t.Errorf("empty subgraph should emit [] arrays, got %s", s)
	}
}

// TestObservationWire checks list vs detail wire shapes: summaries never carry
// raw_otlp; detail always includes it.
func TestObservationWire(t *testing.T) {
	summary := FromObservationSummary(repo.Observation{TraceID: "abc", TransactionID: 1, SampleReason: "example", RawOTLP: []byte("x")})
	assertKeys(t, jsonKeys(t, summary), "trace_id", "transaction_id", "observed_at", "duration_ms", "sample_reason")
	full := FromObservation(repo.Observation{TraceID: "abc", TransactionID: 1, SampleReason: "example", RawOTLP: []byte("x")})
	assertKeys(t, jsonKeys(t, full), "trace_id", "transaction_id", "observed_at", "duration_ms", "sample_reason", "raw_otlp")
}

// TestAnomalyIssueDetailWire ensures the investigate base shape always carries
// class_findings (empty until enrichment) and never invents optional traces.
func TestAnomalyIssueDetailWire(t *testing.T) {
	d := FromAnomalyIssue(repo.AnomalyIssue{
		TransactionID: 1, Signature: "sig", Service: "svc", Namespace: "ns",
		TriggeredClasses: []repo.DeviationClass{repo.D2Egress},
		Evidence:         json.RawMessage(`{"D2_egress":{}}`),
		Occurrences:      1, MaxScore: 40, Status: repo.AnomalyIssueOpen,
	})
	keys := jsonKeys(t, d)
	assertKeys(t, keys,
		"transaction_id", "signature", "service", "namespace", "triggered_classes",
		"evidence", "occurrences", "max_score", "severity", "status", "class_findings",
	)
	if keys["anomaly_trace"] || keys["baseline_trace"] || keys["risk"] {
		t.Fatal("optional investigate fields must be omitted until enrichment")
	}
	if d.ClassFindings == nil {
		t.Fatal("class_findings must be a non-nil empty slice")
	}
}

// TestFromClassFinding maps engine findings onto snake_case wire fields.
func TestFromClassFinding(t *testing.T) {
	f := FromClassFinding(engine.Finding{
		Class:   repo.D2Egress,
		Kind:    engine.FindingStructural,
		Title:   "Unexpected egress",
		Summary: "new peer",
		SpanHighlights: []engine.SpanHighlight{
			{SpanID: "abc", Severity: "high", Reason: "new peer"},
		},
		RawEvidence: json.RawMessage(`{"new_peers":["x"]}`),
	})
	assertKeys(t, jsonKeys(t, f), "class", "kind", "title", "summary", "span_highlights", "raw_evidence")
	if f.SpanHighlights[0].SpanId != "abc" {
		t.Fatalf("span_id = %q", f.SpanHighlights[0].SpanId)
	}
}

// TestPolicyWire widens the typed maps to string keys and emits snake_case.
func TestPolicyWire(t *testing.T) {
	d := FromPolicy(repo.Policy{
		ID: 1, Name: "default", Enabled: true, FireAtScore: 20,
		SignalWeights: map[repo.SignalSource]int{"D6_db_access": 30},
		EnricherLists: map[repo.EnricherListKey][]string{"sensitive_tables": {"admins"}},
		Scope:         repo.ScopeGlobal, ScopeKey: "",
	})
	assertKeys(t, jsonKeys(t, d), "id", "name", "enabled", "fire_at_score", "signal_weights", "enricher_lists", "scope", "scope_key")
	if d.SignalWeights["D6_db_access"] != 30 {
		t.Error("signal weight not mapped")
	}
	if d.Scope != "global" {
		t.Errorf("scope = %q, want global", d.Scope)
	}
}

// TestPolicyWireOmitsEmptyMaps keeps sparse policies clean on the wire.
func TestPolicyWireOmitsEmptyMaps(t *testing.T) {
	keys := jsonKeys(t, FromPolicy(repo.Policy{ID: 1, Scope: repo.ScopeGlobal}))
	if keys["signal_weights"] || keys["enricher_lists"] {
		t.Error("empty maps should be omitted")
	}
}

// TestPolicyRoundTrip checks the wire -> repo converter narrows string map keys.
func TestPolicyRoundTrip(t *testing.T) {
	got := ToPolicy(apitypes.Policy{
		Id: 2, Name: "svc", Enabled: true, FireAtScore: 40,
		SignalWeights: map[string]int{"D2_egress": 50},
		EnricherLists: map[string][]string{"sensitive_tables": {"users"}},
		Scope:         "service", ScopeKey: "ns/svc",
	})
	if got.Scope != repo.ScopeService || got.ScopeKey != "ns/svc" {
		t.Errorf("scope not mapped: %+v", got)
	}
	if got.SignalWeights[repo.SignalSource("D2_egress")] != 50 {
		t.Error("signal weight key not narrowed")
	}
	if got.EnricherLists[repo.EnricherListKey("sensitive_tables")][0] != "users" {
		t.Error("enricher list key not narrowed")
	}
}

// TestServiceGuardrailRoundTrip checks the DTO -> repo guardrail converter
// rebuilds the per-rule lists/modes and defaults an empty scope to service.
func TestServiceGuardrailRoundTrip(t *testing.T) {
	got := ToServiceGuardrail(apitypes.ServiceGuardrail{
		ScopeKey: "ns/svc",
		Rules: []apitypes.GuardrailRuleConfig{
			{Key: "allowed_callers", Mode: "enforce", Allowlist: []string{"web"}},
			{Key: "allowed_egress", Mode: "", Allowlist: nil},
		},
	})
	if got.Scope != repo.ScopeService {
		t.Errorf("empty scope should default to service, got %q", got.Scope)
	}
	if got.RuleLists[repo.RuleKey("allowed_callers")][0] != "web" {
		t.Error("allowlist not rebuilt")
	}
	if got.RuleModes[repo.RuleKey("allowed_callers")] != repo.RuleModeEnforce {
		t.Error("mode not mapped")
	}
	if got.RuleModes[repo.RuleKey("allowed_egress")] != repo.RuleModeSuggest {
		t.Error("empty mode should normalize to suggest")
	}
}

func TestViolationSummaryOnDetail(t *testing.T) {
	d := FromGuardrailViolationDetail(repo.GuardrailViolationGroup{
		Service: "frontend", Namespace: "default", ScopeKey: "default/frontend",
		RuleKey: "allowed_callees", Offending: "currency", Occurrences: 12, MaxScore: 60,
	})
	if d.Summary != "Allowed callees broken by currency." {
		t.Errorf("summary = %q", d.Summary)
	}
	if strings.Contains(d.Summary, "frontend") {
		t.Errorf("summary should not repeat the service (shown separately): %q", d.Summary)
	}
}

// TestBuildCatalogPopulated sanity-checks the catalog exposes the risk model.
func TestBuildCatalogPopulated(t *testing.T) {
	cat := BuildCatalog()
	if len(cat.DeviationClasses) == 0 || len(cat.Enrichers) == 0 || len(cat.GuardrailRules) == 0 {
		t.Error("catalog is missing sections")
	}
	if len(cat.SeverityBands) == 0 || cat.CorrelationBonus == 0 {
		t.Error("catalog missing severity bands / correlation bonus")
	}
	if len(cat.Tags) == 0 {
		t.Error("catalog missing threat tags")
	}
}

func TestSystemSettingsRoundTripMap(t *testing.T) {
	in := repo.DefaultSystemSettings()
	in.ExamplesPerTransaction = 7
	wire := FromSystemSettings(in)
	keys := jsonKeys(t, wire)
	assertKeys(t, keys, "sampling", "retention", "capacity", "writeback", "detection", "identity")
	out := ToSystemSettings(wire)
	if out.ExamplesPerTransaction != in.ExamplesPerTransaction {
		t.Fatalf("map mismatch: %+v vs %+v", out, in)
	}
	if len(out.TransactionIdentityDimensions) != len(in.TransactionIdentityDimensions) {
		t.Fatalf("identity dims: got %d want %d", len(out.TransactionIdentityDimensions), len(in.TransactionIdentityDimensions))
	}
	if out.TransactionIdentityDimensions[0].Key != in.TransactionIdentityDimensions[0].Key {
		t.Fatalf("identity dim key mismatch: %+v", out.TransactionIdentityDimensions)
	}
	if keys["notifications"] || keys["integrations"] {
		t.Fatal("notifications/integrations must not be on the wire")
	}
}

func TestWireOperationIdentity(t *testing.T) {
	name, dims := wireOperationIdentity("POST /buy [http.response.status_code=200]")
	if name != "POST /buy" || len(dims) != 1 {
		t.Fatalf("got name=%q dims=%v", name, dims)
	}
	if dims[0].Key != "http.response.status_code" || dims[0].Value != "200" {
		t.Fatalf("dim = %+v", dims[0])
	}

	name, dims = wireOperationIdentity("GET /x")
	if name != "GET /x" || len(dims) != 0 {
		t.Fatalf("plain: name=%q dims=%v", name, dims)
	}
}

func TestFromTransactionIdentityFields(t *testing.T) {
	tx := FromTransaction(repo.Transaction{
		ID:        1,
		Service:   "svc",
		Namespace: "ns",
		Operation: "POST /buy [http.response.status_code=403]",
		Kind:      repo.KindHTTP,
	})
	if tx.OperationName != "POST /buy" || len(tx.IdentityDimensions) != 1 {
		t.Fatalf("tx = %+v", tx)
	}
	if tx.Operation != "POST /buy [http.response.status_code=403]" {
		t.Fatalf("canonical operation changed: %q", tx.Operation)
	}
}

func TestBaselineClassLearningWire(t *testing.T) {
	d := FromBaselineClass(repo.BaselineClass{
		TransactionID: 7, Class: repo.D2Egress, ObservationCount: 80,
		ObservationCountAtLastChange: 30, Promoted: false,
	}, engine.BaselineLearning{
		Phase: "learning", ObservationsSinceLastChange: 50, QuietMinutes: 12,
		Mode: repo.LearningModeAll, ReadyToPromote: false,
		Stability: engine.BaselineStability{
			Observations: engine.StabilityProgress{
				DrivesPromotion: true, Current: 50, Target: 50, Met: true,
			},
			Duration: engine.StabilityProgress{Current: 12},
		},
	})
	assertKeys(t, jsonKeys(t, d), "transaction_id", "class", "class_label", "class_description", "observation_count", "promoted", "learning")
	assertKeys(t, jsonKeys(t, d.Learning),
		"phase", "observations_since_last_change", "quiet_minutes", "mode", "ready_to_promote", "stability")
	if d.ClassLabel != "Outbound peers" {
		t.Fatalf("class_label = %q, want Outbound peers", d.ClassLabel)
	}
	if d.ClassDescription == "" {
		t.Fatal("class_description empty")
	}
	if d.Learning.Mode != "all" || d.Learning.ReadyToPromote || !d.Learning.Stability.Observations.Met {
		t.Fatalf("learning = %+v", d.Learning)
	}
	assertKeys(t, jsonKeys(t, d.Learning.Stability), "observations", "duration")
	if d.Histogram != nil {
		t.Fatalf("non-histogram class should omit histogram, got %+v", d.Histogram)
	}
}

func TestBaselineClassHistogramWire(t *testing.T) {
	h := histogram.New()
	layout := histogram.DefaultLayout()
	h.Observe(100, layout)
	data, err := json.Marshal(map[string]any{"latency": h})
	if err != nil {
		t.Fatal(err)
	}
	d := FromBaselineClass(repo.BaselineClass{
		TransactionID: 9, Class: repo.D3Latency, Data: data, ObservationCount: 1,
	}, engine.BaselineLearning{Phase: "learning"})
	if d.Histogram == nil {
		t.Fatal("expected histogram presentation")
	}
	if d.Histogram.Unit != apitypes.Us {
		t.Fatalf("unit = %q", d.Histogram.Unit)
	}
	if len(d.Histogram.Series) != 1 || d.Histogram.Series[0].Name != "latency" {
		t.Fatalf("series = %+v", d.Histogram.Series)
	}
	bars := d.Histogram.Series[0].Bars
	if len(bars) != histogram.BucketCount {
		t.Fatalf("bars=%d", len(bars))
	}
	if bars[7].Count != 1 || bars[7].Lo != 64 || bars[7].Hi != 128 || bars[7].Label == "" {
		t.Fatalf("bar7 = %+v", bars[7])
	}
}

// validCorrelationGuardrail is a transaction-scoped guardrail carrying one
// well-formed cross-span correlation spec, used as the baseline the validation
// tests mutate.
func validCorrelationGuardrail() apitypes.ServiceGuardrail {
	return apitypes.ServiceGuardrail{
		Scope:    apitypes.PolicyScopeTransaction,
		ScopeKey: "42",
		Rules: []apitypes.GuardrailRuleConfig{{
			Key:  "attribute_correlation",
			Mode: "enforce",
			Correlations: []apitypes.CorrelationSpec{{
				Name:     "principal-matches-account",
				Left:     apitypes.CorrelationSelector{Service: "edge-gateway", Span: "resolvePrincipal", Attr: "return.value", Extract: `(\d+)`},
				Right:    apitypes.CorrelationSelector{Service: "account-service", Span: "getAccount", Attr: "arg.0"},
				Relation: apitypes.Equals,
			}},
		}},
	}
}

func TestValidateServiceGuardrail(t *testing.T) {
	if err := ValidateServiceGuardrail(validCorrelationGuardrail()); err != nil {
		t.Fatalf("valid guardrail rejected: %v", err)
	}

	serviceScoped := validCorrelationGuardrail()
	serviceScoped.Scope = apitypes.PolicyScopeService
	if err := ValidateServiceGuardrail(serviceScoped); err == nil {
		t.Fatal("correlation on a service scope should be rejected")
	}

	badRegex := validCorrelationGuardrail()
	badRegex.Rules[0].Correlations[0].Left.Extract = "(unterminated"
	if err := ValidateServiceGuardrail(badRegex); err == nil {
		t.Fatal("uncompilable extract regex should be rejected")
	}

	missingAttr := validCorrelationGuardrail()
	missingAttr.Rules[0].Correlations[0].Right.Attr = ""
	if err := ValidateServiceGuardrail(missingAttr); err == nil {
		t.Fatal("selector without attr should be rejected")
	}

	badRelation := validCorrelationGuardrail()
	badRelation.Rules[0].Correlations[0].Relation = "greater_than"
	if err := ValidateServiceGuardrail(badRelation); err == nil {
		t.Fatal("unknown relation should be rejected")
	}
}

// TestCorrelationRoundTrip checks the correlation spec survives wire->repo->wire
// and that an empty severity defaults to critical.
func TestCorrelationRoundTrip(t *testing.T) {
	repoG := ToServiceGuardrail(validCorrelationGuardrail())
	if repoG.Scope != repo.ScopeTransaction {
		t.Fatalf("scope = %q, want transaction", repoG.Scope)
	}
	specs := repoG.RuleCorrelations[repo.RuleKey("attribute_correlation")]
	if len(specs) != 1 {
		t.Fatalf("correlations = %d, want 1", len(specs))
	}
	if specs[0].Severity != "critical" {
		t.Fatalf("empty severity should default to critical, got %q", specs[0].Severity)
	}
	if specs[0].Left.Extract != `(\d+)` || specs[0].Relation != "equals" {
		t.Fatalf("spec not rebuilt: %+v", specs[0])
	}

	wire := FromServiceGuardrail(repoG)
	var got *apitypes.GuardrailRuleConfig
	for i := range wire.Rules {
		if wire.Rules[i].Key == "attribute_correlation" {
			got = &wire.Rules[i]
		}
	}
	if got == nil || len(got.Correlations) != 1 {
		t.Fatalf("wire correlations missing: %+v", wire.Rules)
	}
	if got.Correlations[0].Severity != apitypes.SeverityCritical {
		t.Fatalf("severity not round-tripped: %q", got.Correlations[0].Severity)
	}
}
