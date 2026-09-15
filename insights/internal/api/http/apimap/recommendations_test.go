package apimap

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/engine"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

func sampleRecommendation() repo.Recommendation {
	return repo.Recommendation{
		ID:            "rec-1",
		Kind:          repo.RecommendationKindAttributeCorrelation,
		Scope:         repo.ScopeTransaction,
		ScopeKey:      "42",
		TransactionID: 42,
		State:         repo.RecommendationOpen,
		Rank:          0,
		Service:       "edge-gateway",
		Namespace:     "bank",
		Operation:     "POST /accounts",
		TxKind:        repo.KindHTTP,
		Spec: repo.CorrelationSpec{
			Name:     "principal-matches-account",
			Left:     repo.CorrelationSelector{Service: "edge-gateway", Span: "resolvePrincipal", Attr: "return.value", Extract: `^client-(\d+)$`},
			Right:    repo.CorrelationSelector{Service: "account-service", Span: "getAccount", Attr: "arg.0"},
			Relation: repo.CorrelationEquals,
			Severity: "critical",
		},
		Transform:   "core",
		Observed:    40,
		Held:        40,
		Distinct:    31,
		SampleCount: 50,
		Examples:    []repo.RecommendationExample{{TraceID: "t1", LeftValue: "1001", RightValue: "1001", ObservedAt: time.Unix(1_700_000_000, 0)}},
		MinedAt:     time.Unix(1_700_000_100, 0),
	}
}

func TestFromRecommendation_WireShape(t *testing.T) {
	rec := sampleRecommendation()
	out := FromRecommendation(engine.RecommendationView{Recommendation: rec, AlreadyCovered: true})

	assertKeys(t, jsonKeys(t, out), "id", "kind", "state", "stale", "rank", "scope", "scope_key", "transaction_id", "transaction_kind",
		"service", "namespace", "operation", "title", "summary", "spec", "transform", "transport", "confidence", "examples",
		"already_covered", "mined_at", "created_at", "updated_at")
	if out.Id != "rec-1" || out.Kind != apitypes.AttributeCorrelation || out.State != apitypes.RecommendationStateOpen || !out.AlreadyCovered {
		t.Fatalf("identity fields = %+v", out)
	}
	if out.Scope != apitypes.PolicyScopeTransaction || out.ScopeKey != "42" || out.TransactionId != 42 {
		t.Fatalf("scope fields = %s/%s/%d", out.Scope, out.ScopeKey, out.TransactionId)
	}
	if out.Title != "edge-gateway/resolvePrincipal return.value matches account-service/getAccount arg.0" {
		t.Fatalf("title = %q", out.Title)
	}
	if !strings.HasPrefix(out.Summary, "In 40 of 40 sampled traces (31 distinct values), return.value on resolvePrincipal (edge-gateway) matched, once a constant prefix/suffix is removed,") {
		t.Fatalf("summary = %q", out.Summary)
	}
	if out.Spec.Left.Extract != `^client-(\d+)$` || out.Spec.Right.Attr != "arg.0" || out.Spec.Relation != apitypes.Equals {
		t.Fatalf("spec = %+v", out.Spec)
	}
	if len(out.Examples) != 1 || out.Examples[0].TraceId != "t1" || out.Examples[0].LeftValue != "1001" {
		t.Fatalf("examples = %+v", out.Examples)
	}
	// Same-service relations drop the service from the title.
	rec.Spec.Right.Service = "edge-gateway"
	if got := FromRecommendation(engine.RecommendationView{Recommendation: rec}).Title; got != "resolvePrincipal return.value matches getAccount arg.0" {
		t.Fatalf("same-service title = %q", got)
	}
}

func TestRecommendationConfidence_Levels(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*repo.Recommendation)
		want   apitypes.RecommendationConfidenceLevel
		reason string
	}{
		{"many distinct, no live yet", func(*repo.Recommendation) {}, apitypes.RecommendationConfidenceLevelHigh, "Not yet checked against live traffic"},
		{"few distinct", func(r *repo.Recommendation) { r.Distinct = 6 }, apitypes.RecommendationConfidenceLevelMedium, "Not yet checked against live traffic"},
		{"few distinct but live holds", func(r *repo.Recommendation) {
			r.Distinct = 6
			r.LiveHeld = 80
			r.LiveSince = time.Unix(1_700_000_000, 0)
		}, apitypes.RecommendationConfidenceLevelHigh, "Held on all 80 live traces"},
		{"live break", func(r *repo.Recommendation) {
			r.LiveHeld = 500
			r.LiveBroken = 1
			r.LiveSince = time.Unix(1_700_000_000, 0)
		}, apitypes.RecommendationConfidenceLevelLow, "Broke on 1 of 501 live traces"},
		{"sample miss", func(r *repo.Recommendation) { r.Held = 39 }, apitypes.RecommendationConfidenceLevelMedium, "Held on 39 of 40 stored samples"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := sampleRecommendation()
			c.mutate(&rec)
			conf := FromRecommendation(engine.RecommendationView{Recommendation: rec}).Confidence
			if conf.Level != c.want {
				t.Fatalf("level = %s, want %s (%+v)", conf.Level, c.want, conf)
			}
			if conf.HoldRatio != float64(rec.Held)/float64(rec.Observed) {
				t.Fatalf("hold ratio = %v", conf.HoldRatio)
			}
			found := false
			for _, r := range conf.Reasons {
				if strings.Contains(r, c.reason) {
					found = true
				}
			}
			if !found {
				t.Fatalf("reasons %v lack %q", conf.Reasons, c.reason)
			}
			if last := conf.Reasons[len(conf.Reasons)-1]; last != "Crosses a service boundary" {
				t.Fatalf("cross-service reason missing: %v", conf.Reasons)
			}
		})
	}
}

func TestRecommendationBulkAndApplyMapping(t *testing.T) {
	res := FromRecommendationBulkResult(engine.RecommendationBulkResult{
		Done:   2,
		Errors: []engine.RecommendationItemError{{ID: "x", Err: errors.New("boom")}},
	})
	if res.Done != 2 || res.Failed != 1 || res.Errors[0].Id != "x" || res.Errors[0].Error != "boom" {
		t.Fatalf("bulk result = %+v", res)
	}

	items := ToRecommendationApplyItems(apitypes.RecommendationApplyRequest{Items: []apitypes.RecommendationApplyItem{
		{Id: " a "},
		{Id: "b", Spec: &apitypes.CorrelationSpec{
			Name:     "edited",
			Left:     apitypes.CorrelationSelector{Service: "s", Span: "p", Attr: "x", Extract: `(\d+)`},
			Right:    apitypes.CorrelationSelector{Service: "s", Span: "q", Attr: "y"},
			Relation: apitypes.NotEquals,
			Severity: apitypes.SeverityHigh,
		}},
	}})
	if len(items) != 2 || items[0].ID != "a" || items[0].Spec != nil {
		t.Fatalf("items = %+v", items)
	}
	if items[1].Spec == nil || items[1].Spec.Name != "edited" || items[1].Spec.Relation != repo.CorrelationNotEquals || items[1].Spec.Left.Extract != `(\d+)` || items[1].Spec.Severity != "high" {
		t.Fatalf("edited spec = %+v", items[1].Spec)
	}

	preview := FromRecommendationPreview(engine.RecommendationPreview{SampleCount: 10, Observed: 8, Held: 6, Distinct: 5,
		CounterExamples: []repo.RecommendationExample{{TraceID: "bad", LeftValue: "1", RightValue: "2"}}})
	if preview.HoldRatio != 0.75 || len(preview.CounterExamples) != 1 || preview.CounterExamples[0].TraceId != "bad" {
		t.Fatalf("preview = %+v", preview)
	}
}
