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
				SignalWeights: map[repo.SignalSource]int{repo.SignalSource(repo.D7DBAccess): 10, "d7_destructive_op": 0}, Enabled: true,
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

// TestRenderFindings executes the service-centric Findings feed against a
// representative data map (a service card with one policy violation and one
// transaction that expands to an inline anomaly), catching field/method
// mismatches in the nested drill-down the parse step can't.
func TestRenderFindings(t *testing.T) {
	tmpls := parseTemplates()
	cards := []findingsCard{{
		Namespace: "nodejs-db-permissions-leak",
		Service:   "db-permissions-leak",
		ScopeKey:  "nodejs-db-permissions-leak/db-permissions-leak",
		MaxScore:  45,
		Severity:  "high",
		Violations: []violationGroupView{{
			Service: "db-permissions-leak", Namespace: "nodejs-db-permissions-leak",
			ScopeKey: "nodejs-db-permissions-leak/db-permissions-leak",
			RuleKey:  "allowed_egress", RuleLabel: "allowed egress",
			Offending: "postgres:5432", Occurrences: 760, LastSeen: time.Now(),
			MaxScore: 25, Severity: "low", Status: "open",
		}},
		Transactions: []findingTxn{{
			TransactionID: 1, Service: "db-permissions-leak",
			Namespace: "nodejs-db-permissions-leak", Operation: "GET /api/users",
			Kind: repo.KindHTTP, MaxScore: 25, TabCount: 1, TotalIssues: 2,
			Issues: []repo.AnomalyIssue{{
				TransactionID: 1, Signature: "sig1", Occurrences: 12, MaxScore: 25,
				TriggeredClasses: []repo.DeviationClass{repo.D7DBAccess},
				Offending:        "d7_db_access", LastSeen: time.Now(), Status: repo.AnomalyIssueOpen,
			}},
		}},
		AnomalyCount: 1,
	}}
	rangeView := findingsTimeRangeViewFor("/debug/anomalies", nil, repo.FindingsTimeRange{
		From: time.Now().Add(-7 * 24 * time.Hour), Until: time.Now(), WindowHours: 168,
	})
	for _, tab := range []string{"open", "dismissed", "accepted"} {
		data := map[string]any{
			"Active": "anomalies", "Flash": "", "Error": "",
			"Cards": cards, "Tab": tab,
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
		if buf.Len() == 0 {
			t.Fatalf("empty render (tab=%s)", tab)
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
			TriggeredClasses: []repo.DeviationClass{repo.D7DBAccess, repo.D3Latency},
			Offending:        "d7_db_access=DELETE users", FirstSeen: time.Now().Add(-2 * time.Hour), LastSeen: time.Now(),
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
			{Class: repo.D7DBAccess, Kind: engine.FindingStructural, Title: "New database access",
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
