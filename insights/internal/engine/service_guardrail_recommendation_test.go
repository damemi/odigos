package engine

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

// seedPromotedService creates the given transactions for ns/svc with every
// class promoted, so the service counts as fully learned.
func seedPromotedService(t *testing.T, r *fakeRepo, namespace, service string, operations ...string) {
	t.Helper()
	for _, op := range operations {
		tx, err := r.UpsertTransaction(context.Background(), service, namespace, op, repo.KindHTTP)
		if err != nil {
			t.Fatal(err)
		}
		for _, class := range ScoredClasses() {
			if err := r.UpsertBaselineClass(context.Background(), repo.BaselineClass{
				TransactionID: tx.ID, Class: class, Data: json.RawMessage(`{}`), Promoted: true,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func ruleByKey(rec repo.Recommendation, key repo.RuleKey) (repo.RecommendationRule, bool) {
	for _, r := range rec.Rules {
		if r.Rule == key {
			return r, true
		}
	}
	return repo.RecommendationRule{}, false
}

func TestServiceGuardrailRecommendation_BuildSkipsEnforcedAndOff(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, nil, nil)
	eng.SetGuardrailConfig(guardrailConfigFrom())
	eng.SetSystemConfig(newSystemConfig(repo.SystemSettings{}))
	ctx := context.Background()

	seedPromotedService(t, r, "ns", "shop", "GET /products", "POST /orders")
	r.profiles = map[string]repo.ServiceProfile{"ns/shop": {
		Namespace: "ns", Service: "shop",
		Callers: []string{"gateway"}, Callees: []string{"Payments", "inventory"}, Egress: []string{"api.stripe.com:443"},
	}}
	// Callees already enforced by hand, egress switched off, callers drafted
	// in suggest mode with an entry the profile does not have.
	if err := r.UpsertServiceGuardrail(ctx, repo.ServiceGuardrail{
		Scope: repo.ScopeService, ScopeKey: "ns/shop",
		RuleModes: map[repo.RuleKey]repo.RuleMode{
			risk.RuleAllowedCallees: repo.RuleModeEnforce,
			risk.RuleAllowedEgress:  repo.RuleModeOff,
			risk.RuleAllowedCallers: repo.RuleModeSuggest,
		},
		RuleLists: map[repo.RuleKey][]string{
			risk.RuleAllowedCallees: {"inventory"},
			risk.RuleAllowedCallers: {"admin-console", "Gateway"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	written, err := eng.refreshServiceGuardrailRecommendation(ctx, "ns", "shop")
	if err != nil || !written {
		t.Fatalf("refresh: written=%v err=%v", written, err)
	}
	rec, ok, err := r.GetRecommendation(ctx, repo.ServiceGuardrailRecommendationID("ns", "shop"))
	if err != nil || !ok {
		t.Fatalf("card missing: ok=%v err=%v", ok, err)
	}
	if rec.Kind != repo.RecommendationKindServiceGuardrail || rec.Scope != repo.ScopeService || rec.ScopeKey != "ns/shop" || rec.State != repo.RecommendationOpen {
		t.Fatalf("card = %+v", rec)
	}
	if len(rec.Rules) != 2 || rec.Rules[0].Rule != risk.RuleAllowedCallers || rec.Rules[1].Rule != risk.RuleAllowedTransactions {
		t.Fatalf("enforced/off rules must not be offered: %+v", rec.Rules)
	}
	// The drafted list is extended, deduplicated case-insensitively, sorted.
	if got := rec.Rules[0].Items; !slices.Equal(got, []string{"Gateway", "admin-console"}) {
		t.Fatalf("callers = %v", got)
	}
	txRule := rec.Rules[1]
	if txRule.Fingerprint != eng.system.IdentityFingerprint() || len(txRule.Items) != 2 {
		t.Fatalf("transactions rule = %+v", txRule)
	}

	// Same world, same rules: nothing rewritten.
	written, err = eng.refreshServiceGuardrailRecommendation(ctx, "ns", "shop")
	if err != nil || written {
		t.Fatalf("unchanged card should not be rewritten: written=%v err=%v", written, err)
	}
}

func TestServiceGuardrailRecommendation_NotOfferedWhileLearning(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, nil, nil)
	eng.SetGuardrailConfig(guardrailConfigFrom())
	eng.SetSystemConfig(newSystemConfig(repo.SystemSettings{}))
	ctx := context.Background()

	seedPromotedService(t, r, "ns", "shop", "GET /products")
	if _, err := r.UpsertTransaction(ctx, "shop", "ns", "GET /cart", repo.KindHTTP); err != nil {
		t.Fatal(err)
	}
	written, err := eng.refreshServiceGuardrailRecommendation(ctx, "ns", "shop")
	if err != nil || written {
		t.Fatalf("a service with a learning transaction gets no card: written=%v err=%v", written, err)
	}
}

func TestServiceGuardrailRecommendation_ApplySubsetExtendsAndRevertRestores(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, nil, nil)
	eng.SetGuardrailConfig(guardrailConfigFrom())
	eng.SetSystemConfig(newSystemConfig(repo.SystemSettings{AutoTransactionGuardrail: true}))
	ctx := context.Background()

	seedPromotedService(t, r, "ns", "shop", "GET /products")
	r.profiles = map[string]repo.ServiceProfile{"ns/shop": {
		Namespace: "ns", Service: "shop", Callers: []string{"gateway"}, Callees: []string{"inventory"}, Egress: []string{"api.stripe.com:443"},
	}}
	// The automatic transaction guardrail already owns allowed_transactions.
	if err := eng.maybeAutoEnableTransactionGuardrail(ctx, "ns", "shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.refreshServiceGuardrailRecommendation(ctx, "ns", "shop"); err != nil {
		t.Fatal(err)
	}
	id := repo.ServiceGuardrailRecommendationID("ns", "shop")
	rec, _, _ := r.GetRecommendation(ctx, id)
	if _, offered := ruleByKey(rec, risk.RuleAllowedTransactions); offered || len(rec.Rules) != 3 {
		t.Fatalf("auto-enforced allowed_transactions must not be on the card: %+v", rec.Rules)
	}

	// Apply everything but egress.
	res := eng.ApplyRecommendations(ctx, []RecommendationApplyItem{{ID: id, Rules: []repo.RuleKey{risk.RuleAllowedCallers, risk.RuleAllowedCallees}}})
	if res.Done != 1 || len(res.Errors) != 0 {
		t.Fatalf("apply: %+v", res)
	}
	g, _ := eng.loadOrInitServiceGuardrail(ctx, "ns/shop")
	for _, rule := range []repo.RuleKey{risk.RuleAllowedCallers, risk.RuleAllowedCallees} {
		if g.RuleModes[rule] != repo.RuleModeEnforce || g.RuleOrigins[rule] != repo.GuardrailOriginRecommendation {
			t.Fatalf("%s should be enforced by the recommendation: modes=%v origins=%v", rule, g.RuleModes, g.RuleOrigins)
		}
	}
	if g.RuleModes[risk.RuleAllowedEgress] != "" {
		t.Fatalf("deselected egress must be left alone: %v", g.RuleModes)
	}
	if g.RuleModes[risk.RuleAllowedTransactions] != repo.RuleModeEnforce || g.RuleOrigins[risk.RuleAllowedTransactions] != repo.GuardrailOriginAutoTransactionGuardrail {
		t.Fatalf("the automatic transaction guardrail must be untouched: modes=%v origins=%v", g.RuleModes, g.RuleOrigins)
	}
	if got := g.RuleLists[risk.RuleAllowedCallees]; !slices.Equal(got, []string{"inventory"}) {
		t.Fatalf("callees list = %v", got)
	}
	rec, _, _ = r.GetRecommendation(ctx, id)
	if rec.State != repo.RecommendationApplied || !slices.Equal(rec.AppliedRules, []repo.RuleKey{risk.RuleAllowedCallers, risk.RuleAllowedCallees}) {
		t.Fatalf("applied card = state %s rules %v", rec.State, rec.AppliedRules)
	}
	// An applied card is the operator's decision: the miner leaves it be.
	if written, err := eng.refreshServiceGuardrailRecommendation(ctx, "ns", "shop"); err != nil || written {
		t.Fatalf("applied card must not be re-mined: written=%v err=%v", written, err)
	}

	// The operator takes callers over; revert must keep it and drop callees only.
	g.RuleOrigins[risk.RuleAllowedCallers] = "manual"
	if err := r.UpsertServiceGuardrail(ctx, g); err != nil {
		t.Fatal(err)
	}
	res = eng.RevertRecommendations(ctx, []string{id})
	if res.Done != 1 || len(res.Errors) != 0 {
		t.Fatalf("revert: %+v", res)
	}
	g, _ = eng.loadOrInitServiceGuardrail(ctx, "ns/shop")
	if g.RuleModes[risk.RuleAllowedCallees] != "" || g.RuleLists[risk.RuleAllowedCallees] != nil {
		t.Fatalf("callees should be removed on revert: %v", g.RuleModes)
	}
	if g.RuleModes[risk.RuleAllowedCallers] != repo.RuleModeEnforce || g.RuleModes[risk.RuleAllowedTransactions] != repo.RuleModeEnforce {
		t.Fatalf("manually owned and auto rules must survive revert: %v", g.RuleModes)
	}
	rec, _, _ = r.GetRecommendation(ctx, id)
	if rec.State != repo.RecommendationOpen || len(rec.AppliedRules) != 0 {
		t.Fatalf("reverted card = state %s rules %v", rec.State, rec.AppliedRules)
	}
}

func TestServiceGuardrailRecommendation_ApplyRejectsUnknownRuleAndStaleFingerprint(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, nil, nil)
	eng.SetGuardrailConfig(guardrailConfigFrom())
	eng.SetSystemConfig(newSystemConfig(repo.SystemSettings{}))
	ctx := context.Background()

	seedPromotedService(t, r, "ns", "shop", "GET /products")
	if _, err := eng.refreshServiceGuardrailRecommendation(ctx, "ns", "shop"); err != nil {
		t.Fatal(err)
	}
	id := repo.ServiceGuardrailRecommendationID("ns", "shop")

	res := eng.ApplyRecommendations(ctx, []RecommendationApplyItem{{ID: id, Rules: []repo.RuleKey{"allowed_planets"}}})
	if res.Done != 0 || len(res.Errors) != 1 {
		t.Fatalf("unknown rule must fail: %+v", res)
	}

	// The identity scheme changes after mining: the transactions list is no
	// longer what enforcement would compare against.
	eng.SetSystemConfig(newSystemConfig(repo.SystemSettings{
		TransactionIdentityDimensions: []repo.TransactionIdentityDimension{{Key: "http.response.status_code", Enabled: true}},
	}))
	res = eng.ApplyRecommendations(ctx, []RecommendationApplyItem{{ID: id, Rules: []repo.RuleKey{risk.RuleAllowedTransactions}}})
	if res.Done != 0 || len(res.Errors) != 1 {
		t.Fatalf("stale fingerprint must fail: %+v", res)
	}
	res = eng.ApplyRecommendations(ctx, []RecommendationApplyItem{{ID: id, Rules: []repo.RuleKey{risk.RuleAllowedCallers}}})
	if res.Done != 1 {
		t.Fatalf("rules without a fingerprint still apply: %+v", res)
	}
}

// TestServiceGuardrailRecommendation_ShadowCountsPerRule: an open card is
// checked on live traffic rule by rule, silently - counts, no findings.
func TestServiceGuardrailRecommendation_ShadowCountsPerRule(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, nil, nil)
	eng.SetGuardrailConfig(guardrailConfigFrom())
	eng.SetSystemConfig(newSystemConfig(repo.SystemSettings{}))
	ctx := context.Background()

	seedPromotedService(t, r, "ns", "checkout", "POST /pay")
	r.profiles = map[string]repo.ServiceProfile{"ns/checkout": {
		Namespace: "ns", Service: "checkout", Callees: []string{"payments"}, Egress: []string{"api.stripe.com"},
	}}
	if _, err := eng.refreshServiceGuardrailRecommendation(ctx, "ns", "checkout"); err != nil {
		t.Fatal(err)
	}
	if err := eng.ReloadGuardrailConfig(ctx); err != nil {
		t.Fatal(err)
	}
	id := repo.ServiceGuardrailRecommendationID("ns", "checkout")
	if _, ok := eng.guardrails.config.Load().shadowService("ns/checkout"); !ok {
		t.Fatal("open card should be loaded for shadow evaluation")
	}

	// checkout calls payments (on the list) and ledger (not), and reaches
	// evil.example.com (not on the egress list).
	checkout := &Span{SpanID: "root", Name: "POST /pay", Service: "checkout", Kind: tracepb.Span_SPAN_KIND_SERVER,
		SpanAttrs: map[string]string{"service.namespace": "ns", "http.method": "POST"}}
	payments := &Span{SpanID: "c1", ParentSpanID: "root", Name: "POST /charge", Service: "payments", Kind: tracepb.Span_SPAN_KIND_SERVER}
	ledger := &Span{SpanID: "c2", ParentSpanID: "root", Name: "GET /post", Service: "ledger", Kind: tracepb.Span_SPAN_KIND_SERVER}
	egress := &Span{SpanID: "c3", ParentSpanID: "root", Name: "GET", Service: "checkout", Kind: tracepb.Span_SPAN_KIND_CLIENT,
		SpanAttrs: map[string]string{"server.address": "evil.example.com", "server.port": "4444"}}
	checkout.Children = []*Span{payments, ledger, egress}
	eng.Process(ctx, &Trace{TraceID: "t1", Root: checkout, Spans: []*Span{checkout, payments, ledger, egress}})
	if err := eng.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	if len(r.guardrailHits) != 0 {
		t.Fatalf("shadow evaluation must not fire findings, got %+v", r.guardrailHits)
	}
	broken := map[repo.RuleKey]int64{}
	for _, d := range r.liveDeltas {
		if d.ID != id {
			continue
		}
		if d.Rule == "" || d.Scope != repo.ScopeService || d.ScopeKey != "ns/checkout" {
			t.Fatalf("service card deltas are per rule and service-scoped: %+v", d)
		}
		broken[d.Rule] += d.Broken
	}
	if broken[risk.RuleAllowedCallees] != 1 || broken[risk.RuleAllowedEgress] != 1 {
		t.Fatalf("callees and egress should each be counted broken once: %v", broken)
	}
	if _, counted := broken[risk.RuleAllowedCallers]; counted {
		t.Fatalf("root ingress has no caller to check; allowed_callers must not be counted: %v", broken)
	}
	rec, _, _ := r.GetRecommendation(ctx, id)
	callees, _ := ruleByKey(rec, risk.RuleAllowedCallees)
	if callees.LiveChecked != 1 || callees.LiveViolated != 1 {
		t.Fatalf("callees live counts = %d/%d", callees.LiveChecked, callees.LiveViolated)
	}
}

func TestMergeAllowlist(t *testing.T) {
	got := mergeAllowlist(true, []string{" B ", "a", ""}, []string{"b", "C"})
	if !slices.Equal(got, []string{"B", "C", "a"}) {
		t.Fatalf("folded merge = %v", got)
	}
	got = mergeAllowlist(false, []string{"HTTP\tGET /x"}, []string{"HTTP\tget /x"})
	if len(got) != 2 {
		t.Fatalf("identities are case-sensitive: %v", got)
	}
}
