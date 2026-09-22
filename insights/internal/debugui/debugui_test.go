package debugui

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/engine"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

// TestParseTemplates ensures every page template (including anomaly_compare and
// its cmpWaterfall sub-template) parses - parseTemplates uses template.Must, so
// a broken template would panic here.
func TestParseTemplates(t *testing.T) {
	tmpls := parseTemplates()
	for _, page := range []string{"anomaly_compare", "transaction_detail", "anomalies", "services"} {
		if tmpls[page] == nil {
			t.Fatalf("template %q not parsed", page)
		}
	}
}

// TestBuildServiceMap exercises the per-service map layout embedded on the
// Findings page: a subgraph with a caller, a callee, and a virtual dependency
// reached over a failing edge lays out into the expected counts with the root,
// virtual, and failing-edge flags set.
func TestBuildServiceMap(t *testing.T) {
	sub := repo.BlastRadiusSubgraph{
		Root: repo.BlastRadiusNode{Namespace: "billing", Service: "checkout"},
		Nodes: []repo.BlastRadiusNode{
			{Namespace: "billing", Service: "checkout"},
			{Namespace: "web", Service: "frontend"},
			{Namespace: "billing", Service: "cart"},
			{Namespace: "", Service: "postgres:5432", IsVirtual: true},
		},
		Edges: []repo.BlastRadiusEdge{
			{ClientNamespace: "web", ClientService: "frontend", ServerNamespace: "billing", ServerService: "checkout", RequestCount: 15000, LastSeen: time.Now()},
			{ClientNamespace: "billing", ClientService: "checkout", ServerNamespace: "billing", ServerService: "cart", RequestCount: 4200, LastSeen: time.Now()},
			{ClientNamespace: "billing", ClientService: "checkout", ServerNamespace: "", ServerService: "postgres:5432", ConnectionType: "database", RequestCount: 9100, FailedCount: 37, LastSeen: time.Now()},
		},
	}
	m := buildServiceMap(sub)
	if !m.HasGraph || m.NodeCount != 4 || m.EdgeCount != 3 {
		t.Fatalf("unexpected layout: hasGraph=%v nodes=%d edges=%d", m.HasGraph, m.NodeCount, m.EdgeCount)
	}
	var root, virtual, failing bool
	for _, n := range m.Nodes {
		if n.IsRoot && n.Service == "checkout" {
			root = true
		}
		if n.IsVirtual && n.Service == "postgres:5432" {
			virtual = true
		}
	}
	for _, e := range m.Edges {
		if e.HasFailures {
			failing = true
		}
	}
	if !root || !virtual || !failing {
		t.Errorf("expected root, virtual, and failing-edge flags: root=%v virtual=%v failing=%v", root, virtual, failing)
	}

	// An edgeless anchor yields an empty map (rendered as a short note).
	if buildServiceMap(repo.BlastRadiusSubgraph{Root: repo.BlastRadiusNode{Namespace: "billing", Service: "lonely"}}).HasGraph {
		t.Error("expected empty map for an edgeless anchor")
	}
}

// TestRenderAnomalyCompare executes the compare page end-to-end against a
// representative data map, catching field/method mismatches the parse step
// can't.
func TestRenderAnomalyCompare(t *testing.T) {
	tmpls := parseTemplates()
	svcMap := buildServiceMap(repo.BlastRadiusSubgraph{
		Root: repo.BlastRadiusNode{Namespace: "default", Service: "frontend"},
		Nodes: []repo.BlastRadiusNode{
			{Namespace: "default", Service: "frontend"},
			{Namespace: "default", Service: "user"},
			{Namespace: "", Service: "postgres:5432", IsVirtual: true},
		},
		Edges: []repo.BlastRadiusEdge{
			{ClientNamespace: "default", ClientService: "user", ServerNamespace: "default", ServerService: "frontend", RequestCount: 2600, LastSeen: time.Now()},
			{ClientNamespace: "default", ClientService: "frontend", ServerNamespace: "", ServerService: "postgres:5432", ConnectionType: "database", RequestCount: 900, FailedCount: 12, LastSeen: time.Now()},
		},
	})
	svcMap.Depth = serviceMapDepth
	data := map[string]any{
		"Active":     "anomalies",
		"Flash":      "",
		"Error":      "",
		"ServiceMap": svcMap,
		"Issue": repo.AnomalyIssue{
			TransactionID: 42, Signature: "abc123", Occurrences: 15000, MaxScore: 45,
			TriggeredClasses: []repo.DeviationClass{repo.D3Latency}, Offending: "d3_latency",
			FirstSeen: time.Now().Add(-time.Hour), LastSeen: time.Now(), LastTraceID: "abc",
			Status: repo.AnomalyIssueOpen, Namespace: "default", Service: "frontend",
		},
		"Transaction": &repo.Transaction{
			ID: 42, Service: "frontend", Operation: "POST /buy",
		},
		"AnomalyRows": []SpanRow{{
			SpanID: "root", Name: "POST /buy", Service: "frontend", Kind: "SERVER",
			DurationMs: 12.3, Highlight: "high", HighlightReason: "slow",
		}},
		"BaselineRows":    []SpanRow{{SpanID: "root", Name: "POST /buy", Service: "frontend", Kind: "SERVER"}},
		"BaselineTraceID": "def",
		"Samples": []sampleView{
			{TraceID: "abc", ObservedAt: time.Now()},
			{TraceID: "xyz", ObservedAt: time.Now().Add(-time.Minute)},
		},
		"ChosenTrace": "abc",
		"Severity":    "high",
		"Findings": []engine.Finding{{
			Class: repo.D3Latency, Kind: engine.FindingMetric, Title: "Latency above baseline",
			Summary: "slow", Metric: &engine.MetricComparison{Observed: 1800000, Baseline: 1100000, Unit: "us"},
			SpanHighlights: []engine.SpanHighlight{{SpanID: "root", Severity: "medium", Reason: "slow"}},
		}},
	}
	var buf bytes.Buffer
	if err := tmpls["anomaly_compare"].ExecuteTemplate(&buf, "base.html", data); err != nil {
		t.Fatalf("execute anomaly_compare: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("empty render")
	}
	// The blast-radius service map is embedded on the compare page.
	out := buf.String()
	for _, want := range []string{"<svg", "br-node-root", "br-node-virtual", "br-edge-fail", "postgres:5432"} {
		if !strings.Contains(out, want) {
			t.Errorf("compare service map missing %q", want)
		}
	}
}

// TestRenderAnomalyPolicies executes the scoped anomaly-policy editor against a
// representative data map (global default + one service and one transaction
// override), catching template/data-shape mismatches the parse step can't.
func TestRenderAnomalyPolicies(t *testing.T) {
	tmpls := parseTemplates()
	global := repo.Policy{Scope: repo.ScopeGlobal, Name: "global default", FireAtScore: 20, Enabled: true}
	data := map[string]any{
		"Active":      "settings",
		"SettingsTab": "detection",
		"Flash":       "",
		"Error":       "",
		"Global":      global,
		"Classes":     repo.AllDeviationClasses,
		"Overrides": []anomalyOverrideView{
			{Scope: "service", Label: "billing/checkout", Policy: repo.Policy{
				Scope: repo.ScopeService, ScopeKey: "billing/checkout", Name: "svc", FireAtScore: 35,
				SignalWeights: map[repo.SignalSource]int{repo.SignalSource(repo.D6DBAccess): 10, "d6_destructive_op": 0}, Enabled: true,
			}},
			{Scope: "transaction", Label: "123", Link: "/debug/transactions/123", Policy: repo.Policy{
				Scope: repo.ScopeTransaction, ScopeKey: "123", Name: "txn", FireAtScore: 50, Enabled: false,
			}},
		},
	}
	var buf bytes.Buffer
	if err := tmpls["anomaly_policies"].ExecuteTemplate(&buf, "base.html", data); err != nil {
		t.Fatalf("execute anomaly_policies: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("empty render")
	}
}

// TestRenderSettingsPages executes the settings sub-pages that share the
// settingsNav rail (learning, guardrails, risk model, system) against
// representative data, catching template/data-shape mismatches parse can't.
func TestRenderSettingsPages(t *testing.T) {
	tmpls := parseTemplates()
	cases := []struct {
		page string
		data map[string]any
	}{
		{"learning_policies", map[string]any{
			"Active": "settings", "SettingsTab": "learning", "Flash": "", "Error": "",
			"Classes": []learningClassView{{
				Class:  repo.D2Egress,
				Global: repo.LearningPolicy{Class: repo.D2Egress, Scope: repo.ScopeGlobal},
			}, {
				Class:  repo.D3Latency,
				Global: repo.LearningPolicy{Class: repo.D3Latency, Scope: repo.ScopeGlobal},
			}},
		}},
		{"settings_risk_model", map[string]any{
			"Active": "settings", "SettingsTab": "risk-model", "Flash": "", "Error": "",
			"Classes": repo.AllDeviationClasses,
		}},
		{"settings_system", map[string]any{
			"Active": "settings", "SettingsTab": "system", "Flash": "", "Error": "",
			"Settings": repo.DefaultSystemSettings(),
		}},
		{"settings_storage", map[string]any{
			"Active": "settings", "SettingsTab": "storage", "Flash": "", "Error": "",
			"Health": apitypes.StorageHealth{
				Status:    apitypes.StorageHealthStatusHealthy,
				CheckedAt: time.Now().UTC(),
				Connection: apitypes.StorageConnection{
					Reachable: true, PingLatencyMs: 12, Version: "24.8.14", Database: "odigos_insights",
				},
				Disk: apitypes.StorageDisk{
					InsightsBytes: 2e9, TotalBytes: 100e9, UsedBytes: 18e9, AvailableBytes: 82e9, UsedPercent: 18, Status: apitypes.StorageDiskStatusOk,
				},
				Memory: apitypes.StorageMemory{ProcessRssBytes: 1e9, CgroupUsedBytes: 1.5e9, CgroupTotalBytes: 2e9, UptimeSeconds: 3600},
				Queries: apitypes.StorageQueries{
					Active: 1, LongestSeconds: 0.05, PeakMemoryBytes: 12e6,
					Items: []apitypes.StorageQuery{{ElapsedSeconds: 0.05, User: "odigos_insights", MemoryBytes: 12e6, Query: "SELECT 1"}},
					RecentHeaviest: []apitypes.StorageQuery{{
						ElapsedSeconds: 0.5, User: "odigos_insights", MemoryBytes: 8e6, Query: "SELECT heavy",
						FinishedAt: time.Now().UTC(),
					}},
					RecentLatest: []apitypes.StorageQuery{{
						ElapsedSeconds: 0.02, User: "odigos_insights", MemoryBytes: 4e6, Query: "SELECT latest",
						FinishedAt: time.Now().UTC(),
					}},
				},
				Tables:        []apitypes.StorageTable{{Name: "trace_observations", Rows: 1000, BytesOnDisk: 2e9, ActiveParts: 4}},
				WritePressure: apitypes.StorageWritePressure{},
				Writeback:     apitypes.StorageWriteback{FlushIntervalSeconds: 15, EverFlushed: true, LastFlushOk: true, LastFlushAt: time.Now().UTC()},
			},
		}},
		{"guardrails", map[string]any{
			"Active": "settings", "SettingsTab": "guardrails", "Flash": "", "Error": "",
			"Guardrails": []guardrailView{}, "Namespaces": []string{}, "ServicesJSON": "{}",
		}},
	}
	for _, tc := range cases {
		var buf bytes.Buffer
		if err := tmpls[tc.page].ExecuteTemplate(&buf, "base.html", tc.data); err != nil {
			t.Fatalf("execute %s: %v", tc.page, err)
		}
		if buf.Len() == 0 {
			t.Fatalf("%s: empty render", tc.page)
		}
	}
}

// TestRenderFindings executes the unified Findings feed against representative
// rows of both kinds (a policy violation and an anomaly, as mapped from the
// product Finding wire type), in every status, catching field/method mismatches
// in the kind-specific investigate/resolve branches the parse step can't.
func TestRenderFindings(t *testing.T) {
	tmpls := parseTemplates()
	rangeView := findingsTimeRangeViewFor("/debug/anomalies", nil, repo.FindingsTimeRange{
		From: time.Now().Add(-7 * 24 * time.Hour), Until: time.Now(), WindowHours: 168,
	})
	for _, tab := range []string{"open", "dismissed", "accepted"} {
		rows := findingRowsFromWire([]apitypes.Finding{{
			Kind: "violation", Service: "db-permissions-leak", Namespace: "nodejs-db-permissions-leak",
			Title:   "allowed egress",
			Summary: "Allowed egress broken by postgres:5432.",
			Score:   25, Severity: "low", Occurrences: 760, LastSeen: time.Now(), Status: tab,
			ScopeKey: "nodejs-db-permissions-leak/db-permissions-leak",
			RuleKey:  "allowed_egress", Offending: "postgres:5432",
		}, {
			Kind: "anomaly", Service: "db-permissions-leak", Namespace: "nodejs-db-permissions-leak",
			Title:   "GET /api/users",
			Summary: "Transaction deviated from its baseline: Database access anomaly.",
			Score:   45, Severity: "high", Occurrences: 12, LastSeen: time.Now(), Status: tab,
			TransactionId: 1, Signature: "sig1",
			TriggeredClasses: []apitypes.DeviationClass{apitypes.DeviationClass(repo.D6DBAccess)},
		}})
		if got := len(filterFindingRowsForTab(rows, tab)); got != 2 {
			t.Fatalf("tab=%s: filtered %d rows, want both kinds", tab, got)
		}
		data := map[string]any{
			"Active": "anomalies", "Flash": "", "Error": "",
			"Findings": rows, "Tab": tab,
			"Counts":       findingCounts{Open: 3, Dismissed: 1, Accepted: 2},
			"TimeRange":    rangeView,
			"TabOpen":      "/debug/anomalies?tab=open",
			"TabDismissed": "/debug/anomalies?tab=dismissed",
			"TabAccepted":  "/debug/anomalies?tab=accepted",
		}
		var buf bytes.Buffer
		if err := tmpls["anomalies"].ExecuteTemplate(&buf, "base.html", data); err != nil {
			t.Fatalf("execute anomalies (tab=%s): %v", tab, err)
		}
		out := buf.String()
		for _, want := range []string{
			"Allowed egress broken by postgres:5432.",
			"Transaction deviated from its baseline: Database access anomaly.",
			"/debug/anomalies/issue?transaction_id=1&amp;signature=sig1",
			"/debug/guardrails/violations/evidence?scope_key=nodejs-db-permissions-leak%2fdb-permissions-leak&amp;rule_key=allowed_egress&amp;offending=postgres%3a5432",
		} {
			if !strings.Contains(out, want) {
				t.Fatalf("tab=%s: render missing %q", tab, want)
			}
		}
	}
}

// TestRenderRecommendations executes the recommendations page for every tab
// with more rows than the fold shows, so the row layout, the "show more" row,
// and the edit dialogs all get exercised.
func TestRenderRecommendations(t *testing.T) {
	tmpls := parseTemplates()
	var items []apitypes.Recommendation
	for i := 0; i < recRowsShown+2; i++ {
		items = append(items, apitypes.Recommendation{
			Id: "rec-" + string(rune('a'+i)), Kind: apitypes.RecommendationKindAttributeCorrelation, State: apitypes.RecommendationStateOpen,
			Rank: i, TransactionId: 7, Service: "edge-gateway", Namespace: "bank", Operation: "GET banking/statements/{id}",
			TransactionKind: apitypes.TransactionKind(repo.KindHTTP),
			Title:           "edge-gateway/resolvePrincipal return.value matches account-service/getStatement arg.0",
			Summary:         "return.value on resolvePrincipal (edge-gateway) matches arg.0 on getStatement (account-service).",
			Transform:       "core",
			Spec: &apitypes.CorrelationSpec{
				Name:     "principal-matches-statement",
				Left:     apitypes.CorrelationSelector{Service: "edge-gateway", Span: "resolvePrincipal", Attr: "return.value", Extract: `^client-(\d+)$`},
				Right:    apitypes.CorrelationSelector{Service: "account-service", Span: "getStatement", Attr: "arg.0"},
				Relation: apitypes.Equals, Severity: apitypes.SeverityCritical, Why: "why",
			},
			Confidence: apitypes.RecommendationConfidence{Level: apitypes.RecommendationConfidenceLevelHigh, Observed: 51, Held: 51, Distinct: 10, SampleCount: 51,
				HoldRatio: 1, Reasons: []string{"Held on 51 of 51"}, LiveHeld: 12, LiveSince: time.Now(), LiveLast: time.Now()},
			Examples: []apitypes.RecommendationExample{{TraceId: "t1", LeftValue: "client-1001", RightValue: "1001", ObservedAt: time.Now()}},
			MinedAt:  time.Now(),
		})
	}
	items[1].Examples = nil
	items[2].Stale, items[3].AlreadyCovered = true, true
	// Transport-mirrored relations: ordinary low-severity rows with the tag.
	for _, id := range []string{"rec-t1", "rec-t2"} {
		tr := items[0]
		spec := *tr.Spec
		tr.Spec = &spec
		tr.Id, tr.Rank, tr.Transport = id, 100, true
		tr.Spec.Severity = apitypes.SeverityLow
		tr.Spec.Left = apitypes.CorrelationSelector{Service: "edge-gateway", Span: "documents publish", Attr: "messaging.kafka.message.key"}
		tr.Spec.Right = apitypes.CorrelationSelector{Service: "render-worker", Span: "documents receive", Attr: "messaging.kafka.message.key"}
		items = append(items, tr)
	}
	// Service guardrail recommendations in the same list: one on the service
	// that also has correlation rows, one on a service with nothing else, so
	// both card shapes render.
	items = append(items, apitypes.Recommendation{
		Id: "sg-edge", Kind: apitypes.RecommendationKindServiceGuardrail, State: apitypes.RecommendationStateOpen, Scope: apitypes.PolicyScopeService,
		Namespace: "bank", Service: "edge-gateway", Title: "Lock down edge-gateway - 2 rules ready", Summary: "summary", WhyItMatters: "why it matters",
		Confidence: apitypes.RecommendationConfidence{Level: apitypes.RecommendationConfidenceLevelMedium, SampleCount: 3, LiveHeld: 40, Reasons: []string{"allowed_callees: 40 live traces held"}},
		Rules: []apitypes.RecommendationRule{
			{Rule: string(risk.RuleAllowedCallees), Label: "Allowed callees", Description: "desc", Items: []string{"account-service", "render-worker"}, Confidence: apitypes.RecommendationConfidenceLevelHigh, LiveChecked: 40, LiveSince: time.Now(), LiveLast: time.Now()},
			{Rule: string(risk.RuleAllowedTransactions), Label: "Allowed transactions", Description: "desc", Items: []string{"HTTP\tGET banking/statements/{id}"}, Confidence: apitypes.RecommendationConfidenceLevelMedium},
		},
		AppliedRules: []string{string(risk.RuleAllowedCallees)},
		CreatedAt:    time.Now(), UpdatedAt: time.Now(),
	}, apitypes.Recommendation{
		Id: "sg-render", Kind: apitypes.RecommendationKindServiceGuardrail, State: apitypes.RecommendationStateOpen, Scope: apitypes.PolicyScopeService,
		Namespace: "bank", Service: "render-worker", Title: "Lock down render-worker - 1 rule ready", Summary: "summary", WhyItMatters: "why",
		Confidence: apitypes.RecommendationConfidence{Level: apitypes.RecommendationConfidenceLevelHigh, SampleCount: 1, LiveHeld: 90},
		Rules:      []apitypes.RecommendationRule{{Rule: string(risk.RuleAllowedEgress), Label: "Allowed egress", Description: "desc", Confidence: apitypes.RecommendationConfidenceLevelHigh, LiveChecked: 90}},
		CreatedAt:  time.Now(), UpdatedAt: time.Now(),
	})
	cards := buildRecommendationCards(items)
	if len(cards) != 2 || cards[0].Service != "edge-gateway" || cards[0].Guardrail == nil || len(cards[0].Transactions) != 1 || cards[0].Transactions[0].Shown != recRowsShown {
		t.Fatalf("cards = %+v", cards)
	}
	if txn := cards[0].Transactions[0]; len(txn.Items) != recRowsShown+4 || cards[0].Count != recRowsShown+5 || txn.Items[len(txn.Items)-1].Id != "rec-t2" {
		t.Fatalf("transport rows should list with the rest in rank order: %+v", txn)
	}
	if c := cards[1]; c.Service != "render-worker" || c.Guardrail == nil || len(c.Transactions) != 0 || c.Count != 1 || c.HighCount != 1 {
		t.Fatalf("guardrail-only card = %+v", c)
	}
	for _, tab := range []string{"open", "applied", "dismissed"} {
		data := map[string]any{
			"Active": "recommendations", "Flash": "", "Error": "",
			"Tab": tab, "Kind": "", "Kinds": recommendationKinds, "Namespace": "", "Service": "", "TransactionID": int64(0), "IncludeStale": false,
			"Cards": cards, "Counts": recCounts{Open: 7},
			"TabOpen": "/debug/recommendations?tab=open", "TabApplied": "/debug/recommendations?tab=applied", "TabDismissed": "/debug/recommendations?tab=dismissed",
			"SelfURL":    "/debug/recommendations?tab=" + tab,
			"Namespaces": []string{"bank"}, "Services": map[string][]string{"bank": {"edge-gateway"}},
		}
		var buf bytes.Buffer
		if err := tmpls["recommendations"].ExecuteTemplate(&buf, "base.html", data); err != nil {
			t.Fatalf("execute recommendations (tab=%s): %v", tab, err)
		}
		html := buf.String()
		if !strings.Contains(html, "show 4 more") || strings.Count(html, `class="rec-more" hidden`) != 4 {
			t.Fatalf("tab=%s: fold row missing", tab)
		}
		// Sample values live in the per-row samples dialog, not in the row; the
		// row without stored samples gets a plain count instead of a link.
		if !strings.Contains(html, `id="rec-samples-rec-a"`) || !strings.Contains(html, "<code>client-1001</code>") || strings.Contains(html, "rec-summary") || strings.Contains(html, "rec-ex-line") {
			t.Fatalf("tab=%s: samples should render in the dialog only", tab)
		}
		if strings.Contains(html, `id="rec-samples-rec-b"`) || strings.Count(html, `class="conf-detail conf-link`) != recRowsShown+3 {
			t.Fatalf("tab=%s: sample link should exist only for rows with stored samples", tab)
		}
		// Low severity, transport tag, explanation on hover.
		if strings.Count(html, `class="rec-flag rec-flag-transport`) != 2 || !strings.Contains(html, `class="sev sev-low`) ||
			!strings.Contains(html, "in-flight tampering or misattribution") || !strings.Contains(html, `id="rec-samples-rec-t1"`) {
			t.Fatalf("tab=%s: transport tag missing", tab)
		}
		// The card is edge-gateway's, so only the account-service side names its service.
		if strings.Contains(html, `<span class="rc-svc">edge-gateway /</span>`) || !strings.Contains(html, `<span class="rc-svc">account-service /</span>`) {
			t.Fatalf("tab=%s: service prefix should only appear on the foreign side", tab)
		}
		// Service guardrail cards: one block per service, rule rows with the
		// items apply writes, identities shown without the tab separator, an
		// empty list spelled out rather than blank.
		if strings.Count(html, `class="rec-guardrail" data-rec=`) != 2 || !strings.Contains(html, `Lock down render-worker`) ||
			!strings.Contains(html, `<code>HTTP GET banking/statements/{id}</code>`) || !strings.Contains(html, "none observed - enforcing forbids any") {
			t.Fatalf("tab=%s: service guardrail card missing", tab)
		}
		switch tab {
		case "open":
			// Same actions as a correlation row: apply (all rules) posts only the
			// id; edit & apply opens a dialog with one ticked box per rule.
			if !strings.Contains(html, `id="rec-edit-sg-edge"`) || !strings.Contains(html, `id="rec-edit-sg-render"`) ||
				strings.Count(html, `class="rec-rule-chk" name="rule"`) != 3 || strings.Count(html, `name="rule" value="allowed_callees" id="rec-edit-sg-edge-allowed_callees" checked`) != 1 ||
				!strings.Contains(html, `data-needs-rule="1"`) || strings.Contains(html, `name="guardrail"`) {
				t.Fatalf("tab=%s: service guardrail actions wrong", tab)
			}
			if got := strings.Count(html, `>edit &amp; apply</button>`); got != recRowsShown+4+2 {
				t.Fatalf("tab=%s: every open recommendation gets edit & apply, got %d", tab, got)
			}
		case "applied":
			// The rule left out at apply time is marked, the enforced one too.
			if !strings.Contains(html, `>enforced</span>`) || !strings.Contains(html, `>left out</span>`) || strings.Contains(html, `class="rec-rule-chk"`) ||
				!strings.Contains(html, `href="/debug/settings/guardrails?namespace=bank&amp;service=edge-gateway"`) {
				t.Fatalf("tab=%s: applied guardrail card wrong", tab)
			}
		}
	}
}

// TestDumpComparePreview writes a fully-populated compare page to
// INSIGHTS_PREVIEW_OUT (a file path) so the layout can be eyeballed in a
// browser without a cluster. Skipped unless the env var is set.
func TestDumpComparePreview(t *testing.T) {
	out := os.Getenv("INSIGHTS_PREVIEW_OUT")
	if out == "" {
		t.Skip("set INSIGHTS_PREVIEW_OUT to render a preview")
	}
	tmpls := parseTemplates()

	dbRow := SpanRow{
		SpanID: "c-DELETE users", Name: "DELETE users", Service: "checkout-svc", Kind: "CLIENT",
		DurationMs: 8.4, OffsetMs: 3.2, Highlight: "high", HighlightReason: "db access not seen during learning",
		Scope: "pgx@5.5.0",
		Attrs: []KV{{Key: "db.system", Value: "postgresql"}, {Key: "db.query.text", Value: "DELETE FROM users WHERE id = $1"}},
	}
	rootRow := SpanRow{SpanID: "root", Name: "POST /checkout", Service: "checkout-svc", Kind: "SERVER", DurationMs: 42.1}
	normalDB := SpanRow{SpanID: "c-SELECT users", Name: "SELECT users", Service: "checkout-svc", Kind: "CLIENT", DurationMs: 3.1, OffsetMs: 2.0, Scope: "pgx@5.5.0"}

	svcMap := buildServiceMap(repo.BlastRadiusSubgraph{
		Root: repo.BlastRadiusNode{Namespace: "default", Service: "checkout-svc"},
		Nodes: []repo.BlastRadiusNode{
			{Namespace: "default", Service: "checkout-svc"},
			{Namespace: "default", Service: "frontend"},
			{Namespace: "default", Service: "currency"},
			{Namespace: "default", Service: "shipping"},
			{Namespace: "", Service: "postgres:5432", IsVirtual: true},
		},
		Edges: []repo.BlastRadiusEdge{
			{ClientNamespace: "default", ClientService: "frontend", ServerNamespace: "default", ServerService: "checkout-svc", RequestCount: 2600, LastSeen: time.Now()},
			{ClientNamespace: "default", ClientService: "checkout-svc", ServerNamespace: "default", ServerService: "currency", RequestCount: 1300, LastSeen: time.Now()},
			{ClientNamespace: "default", ClientService: "checkout-svc", ServerNamespace: "default", ServerService: "shipping", RequestCount: 8800, FailedCount: 41, LastSeen: time.Now()},
			{ClientNamespace: "default", ClientService: "checkout-svc", ServerNamespace: "", ServerService: "postgres:5432", ConnectionType: "database", RequestCount: 3300, LastSeen: time.Now()},
		},
	})
	svcMap.Depth = serviceMapDepth

	data := map[string]any{
		"Active": "anomalies", "Flash": "", "Error": "",
		"ServiceMap": svcMap,
		"Issue": repo.AnomalyIssue{
			TransactionID: 42, Signature: "9f2cab", Occurrences: 15000, MaxScore: 62,
			TriggeredClasses: []repo.DeviationClass{repo.D6DBAccess, repo.D3Latency},
			Offending:        "d6_db_access=DELETE users", FirstSeen: time.Now().Add(-2 * time.Hour), LastSeen: time.Now(),
			LastTraceID: "9f2c…ab", Status: repo.AnomalyIssueOpen, Namespace: "default", Service: "checkout-svc",
		},
		"Transaction":     &repo.Transaction{ID: 42, Service: "checkout-svc", Operation: "POST /checkout"},
		"AnomalyRows":     []SpanRow{rootRow, dbRow},
		"BaselineRows":    []SpanRow{rootRow, normalDB},
		"BaselineTraceID": "1a3d…77",
		"Samples":         []sampleView{{TraceID: "9f2c…ab", ObservedAt: time.Now()}},
		"ChosenTrace":     "9f2c…ab",
		"Severity":        "high",
		"Findings": []engine.Finding{
			{Class: repo.D6DBAccess, Kind: engine.FindingStructural, Title: "New database access",
				Summary:        "The anomalous trace made a new access: DELETE on users; new query shape: DELETE [users].",
				SpanHighlights: []engine.SpanHighlight{{SpanID: "c-DELETE users", Severity: "high", Reason: "database access not seen during learning"}},
				AttrHighlights: []engine.AttrHighlight{{SpanID: "c-DELETE users", Key: "db.query.text", Value: "DELETE FROM users WHERE id = $1"}}},
			{Class: repo.D3Latency, Kind: engine.FindingMetric, Title: "Latency above baseline",
				Summary:        "Entry latency 42.1ms exceeded the learned p99 of 12.0ms.",
				Metric:         &engine.MetricComparison{Observed: 42100, Baseline: 12000, Unit: "us"},
				SpanHighlights: []engine.SpanHighlight{{SpanID: "root", Severity: "medium", Reason: "entry span slower than baseline p99"}}},
		},
	}
	var buf bytes.Buffer
	if err := tmpls["anomaly_compare"].ExecuteTemplate(&buf, "base.html", data); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write preview: %v", err)
	}
	t.Logf("wrote preview to %s", out)
}
