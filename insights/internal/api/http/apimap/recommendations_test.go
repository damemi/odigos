package apimap

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/engine"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
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
		"service", "namespace", "operation", "title", "summary", "why_it_matters", "spec", "transform", "confidence", "examples",
		"already_covered", "mined_at", "created_at", "updated_at")
	if out.Id != "rec-1" || out.Kind != apitypes.RecommendationKindAttributeCorrelation || out.State != apitypes.RecommendationStateOpen || !out.AlreadyCovered {
		t.Fatalf("identity fields = %+v", out)
	}
	if out.Scope != apitypes.PolicyScopeTransaction || out.ScopeKey != "42" || out.TransactionId != 42 {
		t.Fatalf("scope fields = %s/%s/%d", out.Scope, out.ScopeKey, out.TransactionId)
	}
	if out.Title != "edge-gateway/resolvePrincipal return.value matches account-service/getAccount arg.0" {
		t.Fatalf("title = %q", out.Title)
	}
	if out.Summary != "return.value on resolvePrincipal (edge-gateway) matches arg.0 on getAccount (account-service)." {
		t.Fatalf("summary = %q", out.Summary)
	}
	recContains := rec
	recContains.Transform = "contains"
	if got := FromRecommendation(engine.RecommendationView{Recommendation: recContains}).Summary; got != "return.value on resolvePrincipal (edge-gateway) is included in arg.0 on getAccount (account-service)." {
		t.Fatalf("contains summary = %q", got)
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

// A service_guardrail recommendation shares the envelope and omits the
// correlation-only fields; its confidence sums the rules' live counters and
// takes the weakest rule's level.
func TestFromRecommendation_ServiceGuardrailWireShape(t *testing.T) {
	since := time.Unix(1_700_000_000, 0)
	rec := repo.Recommendation{
		ID: "sg-1", Kind: repo.RecommendationKindServiceGuardrail, Scope: repo.ScopeService, ScopeKey: "bank/edge-gateway",
		State: repo.RecommendationOpen, Service: "edge-gateway", Namespace: "bank", SampleCount: 3,
		Rules: []repo.RecommendationRule{
			{Rule: risk.RuleAllowedCallees, Items: []string{"account-service"}, LiveChecked: 60, LiveSince: since, LiveLast: since.Add(time.Hour)},
			{Rule: risk.RuleAllowedEgress, LiveChecked: 10, LiveViolated: 1, LiveSince: since.Add(time.Minute), LiveLast: since.Add(2 * time.Hour)},
		},
	}
	out := FromRecommendation(engine.RecommendationView{Recommendation: rec})

	assertKeys(t, jsonKeys(t, out), "id", "kind", "state", "stale", "rank", "scope", "scope_key", "service", "namespace",
		"title", "summary", "why_it_matters", "confidence", "rules", "mined_at", "created_at", "updated_at")
	if out.Kind != apitypes.RecommendationKindServiceGuardrail || out.Scope != apitypes.PolicyScopeService || out.Spec != nil || out.TransactionId != 0 {
		t.Fatalf("envelope = %+v", out)
	}
	if out.Title != "Lock down edge-gateway" || len(out.Rules) != 2 || out.Rules[0].Label != "Allowed callees" {
		t.Fatalf("title/rules = %q %+v", out.Title, out.Rules)
	}
	if !strings.Contains(out.Summary, "covering allowed callees (1), allowed egress (0)") {
		t.Fatalf("summary = %q", out.Summary)
	}
	rec.State, rec.AppliedRules = repo.RecommendationApplied, []repo.RuleKey{risk.RuleAllowedEgress}
	applied := FromRecommendation(engine.RecommendationView{Recommendation: rec})
	if applied.Title != "edge-gateway locked down" || !strings.Contains(applied.Summary, "now covers allowed egress (0).") || strings.Contains(applied.Summary, "callees") {
		t.Fatalf("applied title/summary = %q %q", applied.Title, applied.Summary)
	}
	c := out.Confidence
	if c.Level != apitypes.RecommendationConfidenceLevelLow || c.SampleCount != 3 || c.LiveHeld != 69 || c.LiveBroken != 1 || !c.LiveSince.Equal(since) || !c.LiveLast.Equal(since.Add(2*time.Hour)) {
		t.Fatalf("confidence = %+v", c)
	}
	if !strings.Contains(strings.Join(c.Reasons, "\n"), "Allowed egress: 1 of 10 live traces") || !strings.Contains(strings.Join(c.Reasons, "\n"), "Allowed egress: nothing observed") {
		t.Fatalf("reasons = %v", c.Reasons)
	}
}

func TestServiceGuardrailConfidence_WeakestRuleAndEmpty(t *testing.T) {
	high := repo.Recommendation{
		Kind: repo.RecommendationKindServiceGuardrail, Service: "shop", SampleCount: 2,
		Rules: []repo.RecommendationRule{
			{Rule: risk.RuleAllowedCallees, Items: []string{"inventory"}, LiveChecked: serviceRuleHighLive},
			{Rule: risk.RuleAllowedEgress, Items: []string{"api.stripe.com"}, LiveChecked: serviceRuleHighLive + 10},
		},
	}
	if got := FromRecommendation(engine.RecommendationView{Recommendation: high}).Confidence; got.Level != apitypes.RecommendationConfidenceLevelHigh || got.LiveHeld != 2*serviceRuleHighLive+10 || got.LiveBroken != 0 {
		t.Fatalf("all-held high = %+v", got)
	}
	if reasons := strings.Join(FromRecommendation(engine.RecommendationView{Recommendation: high}).Confidence.Reasons, "\n"); !strings.Contains(reasons, "Checked on") || !strings.Contains(reasons, "none outside the lists") {
		t.Fatalf("held reasons = %s", reasons)
	}

	medium := high
	medium.Rules[1].LiveChecked = serviceRuleHighLive - 1
	if got := FromRecommendation(engine.RecommendationView{Recommendation: medium}).Confidence.Level; got != apitypes.RecommendationConfidenceLevelMedium {
		t.Fatalf("one under-threshold rule must pull the card to medium, got %s", got)
	}

	empty := repo.Recommendation{Kind: repo.RecommendationKindServiceGuardrail, Service: "shop", State: repo.RecommendationApplied}
	out := FromRecommendation(engine.RecommendationView{Recommendation: empty})
	if out.Confidence.Level != apitypes.RecommendationConfidenceLevelMedium {
		t.Fatalf("empty rules = %s", out.Confidence.Level)
	}
	if !strings.Contains(out.Summary, "now covers nothing yet") {
		t.Fatalf("applied coverage with no rules = %q", out.Summary)
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
		{Id: "sg-1", Rules: []string{" allowed_callees ", "", "allowed_egress"}},
	}})
	if len(items) != 3 || items[0].ID != "a" || items[0].Spec != nil || len(items[0].Rules) != 0 {
		t.Fatalf("items = %+v", items)
	}
	if items[1].Spec == nil || items[1].Spec.Name != "edited" || items[1].Spec.Relation != repo.CorrelationNotEquals || items[1].Spec.Left.Extract != `(\d+)` || items[1].Spec.Severity != "high" {
		t.Fatalf("edited spec = %+v", items[1].Spec)
	}
	if len(items[2].Rules) != 2 || items[2].Rules[0] != risk.RuleAllowedCallees || items[2].Rules[1] != risk.RuleAllowedEgress {
		t.Fatalf("service apply subset = %v", items[2].Rules)
	}

	preview := FromRecommendationPreview(engine.RecommendationPreview{SampleCount: 10, Observed: 8, Held: 6, Distinct: 5,
		CounterExamples: []repo.RecommendationExample{{TraceID: "bad", LeftValue: "1", RightValue: "2"}}})
	if preview.HoldRatio != 0.75 || len(preview.CounterExamples) != 1 || preview.CounterExamples[0].TraceId != "bad" {
		t.Fatalf("preview = %+v", preview)
	}
}
