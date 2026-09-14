package engine

import (
	"encoding/json"
	"testing"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// spanViewsFor builds a SpanView slice from a trace by round-tripping through
// the OTLP the decoder produces, mirroring what the explanation endpoint does.
func spanViewsWithDB(t *testing.T) []SpanView {
	t.Helper()
	// A trace with a DB span carrying a DELETE on users.
	tr := mkTrace(
		withRoot("POST /checkout", "checkout-svc", tracepb.Span_SPAN_KIND_SERVER, nil),
		withChild("root", "DELETE users", tracepb.Span_SPAN_KIND_CLIENT, map[string]string{
			"db.system":     "postgresql",
			"db.query.text": "DELETE FROM users WHERE id = 1",
		}, "pgx"),
	)
	// Build SpanViews directly from the engine spans (attrs preserved) so the
	// explainer can match. We fake minimal SpanViews here rather than proto.
	views := make([]SpanView, 0, len(tr.Spans))
	for _, s := range tr.Spans {
		attrs := make([]KV, 0, len(s.SpanAttrs))
		for k, v := range s.SpanAttrs {
			attrs = append(attrs, KV{Key: k, Value: v})
		}
		views = append(views, SpanView{SpanID: s.SpanID, Name: s.Name, Service: s.Service, Attrs: attrs})
	}
	return views
}

func TestExplainD7_HighlightsOffendingSpan(t *testing.T) {
	views := spanViewsWithDB(t)
	ev, _ := json.Marshal(map[string]any{
		"new_accesses":     []dbAccess{{System: "postgresql", Table: "users", Op: "DELETE"}},
		"new_query_shapes": []queryShape{{Op: "DELETE", Tables: "users"}},
	})

	f := Explain(repo.D7DBAccess, ev, views)
	if f.Kind != FindingStructural {
		t.Fatalf("D7 kind = %q, want structural", f.Kind)
	}
	if len(f.SpanHighlights) != 1 {
		t.Fatalf("expected 1 span highlight, got %d", len(f.SpanHighlights))
	}
	if f.SpanHighlights[0].SpanID != "c-DELETE users" {
		t.Errorf("highlighted wrong span: %s", f.SpanHighlights[0].SpanID)
	}
	if len(f.AttrHighlights) != 1 || f.AttrHighlights[0].Key != "db.query.text" {
		t.Errorf("expected db.query.text attr highlight, got %+v", f.AttrHighlights)
	}
	if !contains(f.Summary, "users") {
		t.Errorf("summary missing table: %s", f.Summary)
	}
}

func TestExplainD1_HighlightsNewEdgeChildSpan(t *testing.T) {
	// frontend/POST /checkout → payment/charge is the new edge; the existing
	// frontend/POST /checkout → cart/list edge must stay untinted.
	views := []SpanView{
		{SpanID: "root", Name: "POST /checkout", Service: "frontend"},
		{SpanID: "cart", ParentSpanID: "root", Name: "list", Service: "cart"},
		{SpanID: "pay", ParentSpanID: "root", Name: "charge", Service: "payment"},
	}
	ev, _ := json.Marshal(map[string]any{
		"new_edges": []d1Edge{{
			From: d1Node{Svc: "frontend", Op: "POST /checkout"},
			To:   d1Node{Svc: "payment", Op: "charge"},
		}},
	})

	f := Explain(repo.D1CallEdges, ev, views)
	if f.Kind != FindingStructural {
		t.Fatalf("D1 kind = %q, want structural", f.Kind)
	}
	if len(f.SpanHighlights) != 1 {
		t.Fatalf("expected 1 span highlight, got %d: %+v", len(f.SpanHighlights), f.SpanHighlights)
	}
	if f.SpanHighlights[0].SpanID != "pay" {
		t.Errorf("highlighted wrong span: %s (want pay)", f.SpanHighlights[0].SpanID)
	}
	if f.SpanHighlights[0].Severity != "high" {
		t.Errorf("severity = %q, want high", f.SpanHighlights[0].Severity)
	}
	if !contains(f.SpanHighlights[0].Reason, "payment/charge") {
		t.Errorf("highlight reason missing edge target: %s", f.SpanHighlights[0].Reason)
	}
	if !contains(f.Summary, "new call edge") {
		t.Errorf("unexpected summary: %s", f.Summary)
	}
}

func TestExplainD3_MetricComparison(t *testing.T) {
	views := []SpanView{{SpanID: "root", Name: "POST /buy"}}
	ev, _ := json.Marshal(map[string]any{"latency_us": 1_800_000, "latency_p99": 1_100_000})

	f := Explain(repo.D3Latency, ev, views)
	if f.Kind != FindingMetric {
		t.Fatalf("D3 kind = %q, want metric", f.Kind)
	}
	if f.Metric == nil || f.Metric.Observed != 1_800_000 || f.Metric.Baseline != 1_100_000 {
		t.Fatalf("unexpected metric: %+v", f.Metric)
	}
	if f.Metric.Unit != "us" {
		t.Errorf("unit = %q, want us", f.Metric.Unit)
	}
	if len(f.SpanHighlights) != 1 || f.SpanHighlights[0].SpanID != "root" {
		t.Errorf("expected root span highlight, got %+v", f.SpanHighlights)
	}
	if !contains(f.Summary, "1.80s") || !contains(f.Summary, "1.10s") {
		t.Errorf("summary missing human latencies: %s", f.Summary)
	}
}

func TestExplain_UnimplementedClassMinimal(t *testing.T) {
	f := Explain(repo.D6AttrRelations, json.RawMessage(`{"foo":1}`), nil)
	if f.Class != repo.D6AttrRelations {
		t.Fatalf("class = %s", f.Class)
	}
	if f.Title == "" || f.Summary == "" {
		t.Errorf("minimal finding should still carry title+summary")
	}
	if len(f.RawEvidence) == 0 {
		t.Errorf("minimal finding should carry raw evidence for UI fallback")
	}
}
