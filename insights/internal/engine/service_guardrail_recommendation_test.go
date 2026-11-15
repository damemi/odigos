package engine

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/odigos-io/odigos/insights/internal/repo"
	"github.com/odigos-io/odigos/insights/internal/risk"
)

// seedPromotedService creates the given transactions for ns/svc with every
// class promoted, so the service counts as fully learned.
func seedPromotedService(t *testing.T, r *fakeRepo, namespace, service string, operations ...string) {
	t.Helper()
	for _, op := range operations {
		tx, err := r.UpsertTransaction(context.Background(), service, namespace, op, repo.KindHTTP, nil)
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
	if _, err := r.UpsertTransaction(ctx, "shop", "ns", "GET /cart", repo.KindHTTP, nil); err != nil {
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

// TestServiceGuardrailRecommendation_RevertKeepsOperatorEntries: a card extends
// the allowlist the operator drafted rather than replacing it, so reverting must
// withdraw only what the card contributed - the draft, and anything added after
// applying, are the operator's and stay.
func TestServiceGuardrailRecommendation_RevertKeepsOperatorEntries(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, nil, nil)
	eng.SetGuardrailConfig(guardrailConfigFrom())
	eng.SetSystemConfig(newSystemConfig(repo.SystemSettings{}))
	ctx := context.Background()

	seedPromotedService(t, r, "ns", "checkout", "POST /pay")
	r.profiles = map[string]repo.ServiceProfile{"ns/checkout": {
		Namespace: "ns", Service: "checkout", Egress: []string{"api.stripe.com:443"},
	}}
	// The operator has been drafting an egress allowlist by hand, observe-only.
	if err := r.UpsertServiceGuardrail(ctx, repo.ServiceGuardrail{
		Scope: repo.ScopeService, ScopeKey: "ns/checkout",
		RuleModes: map[repo.RuleKey]repo.RuleMode{risk.RuleAllowedEgress: repo.RuleModeSuggest},
		RuleLists: map[repo.RuleKey][]string{risk.RuleAllowedEgress: {"vault.internal:8200"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.refreshServiceGuardrailRecommendation(ctx, "ns", "checkout"); err != nil {
		t.Fatal(err)
	}
	id := repo.ServiceGuardrailRecommendationID("ns", "checkout")
	if res := eng.ApplyRecommendations(ctx, []RecommendationApplyItem{{ID: id}}); res.Done != 1 {
		t.Fatalf("apply: %+v", res)
	}
	g, _ := eng.loadOrInitServiceGuardrail(ctx, "ns/checkout")
	if got := g.RuleLists[risk.RuleAllowedEgress]; !slices.Equal(got, []string{"api.stripe.com:443", "vault.internal:8200"}) {
		t.Fatalf("apply should enforce the merged list, got %v", got)
	}
	// The operator adds one more host through the guardrail editor.
	g.RuleLists[risk.RuleAllowedEgress] = append(g.RuleLists[risk.RuleAllowedEgress], "backup.corp:443")
	if err := r.UpsertServiceGuardrail(ctx, g); err != nil {
		t.Fatal(err)
	}

	if res := eng.RevertRecommendations(ctx, []string{id}); res.Done != 1 || len(res.Errors) != 0 {
		t.Fatalf("revert: %+v", res)
	}
	g, _ = eng.loadOrInitServiceGuardrail(ctx, "ns/checkout")
	if got := g.RuleLists[risk.RuleAllowedEgress]; !slices.Equal(got, []string{"vault.internal:8200", "backup.corp:443"}) {
		t.Fatalf("revert must withdraw only the card's own entries, got %v", got)
	}
	if g.RuleModes[risk.RuleAllowedEgress] != repo.RuleModeSuggest {
		t.Fatalf("the rule should be back to the mode it had before applying: %v", g.RuleModes)
	}
	if g.RuleOrigins[risk.RuleAllowedEgress] != "" {
		t.Fatalf("the recommendation no longer owns the rule: %v", g.RuleOrigins)
	}
	// Applying and reverting again is idempotent - the draft is still not the
	// card's to remove.
	if _, err := eng.refreshServiceGuardrailRecommendation(ctx, "ns", "checkout"); err != nil {
		t.Fatal(err)
	}
	if res := eng.ApplyRecommendations(ctx, []RecommendationApplyItem{{ID: id}}); res.Done != 1 {
		t.Fatalf("re-apply: %+v", res)
	}
	if res := eng.RevertRecommendations(ctx, []string{id}); res.Done != 1 {
		t.Fatalf("re-revert: %+v", res)
	}
	g, _ = eng.loadOrInitServiceGuardrail(ctx, "ns/checkout")
	if got := g.RuleLists[risk.RuleAllowedEgress]; !slices.Equal(got, []string{"backup.corp:443", "vault.internal:8200"}) {
		t.Fatalf("second revert = %v", got)
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

func newServiceRecEngine() (*fakeRepo, *Engine) {
	r := newFakeRepo()
	eng := New(r, nil, nil, nil)
	eng.SetGuardrailConfig(guardrailConfigFrom())
	eng.SetSystemConfig(newSystemConfig(repo.SystemSettings{}))
	return r, eng
}

func seedShopCard(t *testing.T, r *fakeRepo, eng *Engine) string {
	t.Helper()
	seedPromotedService(t, r, "ns", "shop", "GET /products")
	r.profiles = map[string]repo.ServiceProfile{"ns/shop": {
		Namespace: "ns", Service: "shop",
		Callers: []string{"gateway"}, Callees: []string{"inventory"}, Egress: []string{"api.stripe.com"},
	}}
	if _, err := eng.refreshServiceGuardrailRecommendation(context.Background(), "ns", "shop"); err != nil {
		t.Fatal(err)
	}
	return repo.ServiceGuardrailRecommendationID("ns", "shop")
}

func mustGetRec(t *testing.T, r *fakeRepo, id string) repo.Recommendation {
	t.Helper()
	rec, ok, err := r.GetRecommendation(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("get %s: ok=%v err=%v", id, ok, err)
	}
	return rec
}

func applyErrContains(t *testing.T, res RecommendationBulkResult, substr string) {
	t.Helper()
	if res.Done != 0 || len(res.Errors) != 1 {
		t.Fatalf("want one apply error, got %+v", res)
	}
	if !strings.Contains(res.Errors[0].Err.Error(), substr) {
		t.Fatalf("error %q does not contain %q", res.Errors[0].Err, substr)
	}
}

// TestServiceGuardrailRecommendation_StaleWhenNothingLeft: once every offered
// rule is enforced or the service is learning again, the open card is hidden
// (stale) rather than deleted. Apply refuses it; remine un-stales when a rule
// is offerable again.
func TestServiceGuardrailRecommendation_StaleWhenNothingLeft(t *testing.T) {
	r, eng := newServiceRecEngine()
	ctx := context.Background()
	created := time.Unix(1_700_000_000, 0).UTC()
	now := created
	eng.clock = func() time.Time { return now }
	id := seedShopCard(t, r, eng)
	rec := mustGetRec(t, r, id)
	if rec.CreatedAt != created {
		t.Fatalf("CreatedAt = %v", rec.CreatedAt)
	}

	if err := r.UpsertServiceGuardrail(ctx, repo.ServiceGuardrail{
		Scope: repo.ScopeService, ScopeKey: "ns/shop",
		RuleModes: map[repo.RuleKey]repo.RuleMode{
			risk.RuleAllowedCallers:      repo.RuleModeEnforce,
			risk.RuleAllowedCallees:      repo.RuleModeEnforce,
			risk.RuleAllowedEgress:       repo.RuleModeOff,
			risk.RuleAllowedTransactions: repo.RuleModeEnforce,
		},
	}); err != nil {
		t.Fatal(err)
	}
	now = created.Add(time.Hour)
	written, err := eng.refreshServiceGuardrailRecommendation(ctx, "ns", "shop")
	if err != nil || !written {
		t.Fatalf("should mark stale: written=%v err=%v", written, err)
	}
	rec = mustGetRec(t, r, id)
	if !rec.Stale || rec.State != repo.RecommendationOpen {
		t.Fatalf("stale open card = %+v", rec)
	}
	open, err := eng.ListRecommendations(ctx, repo.RecommendationFilter{Kind: repo.RecommendationKindServiceGuardrail})
	if err != nil || len(open) != 0 {
		t.Fatalf("stale card must be hidden from the default list: %d err=%v", len(open), err)
	}
	if _, ok, err := eng.GetRecommendation(ctx, id); err != nil || !ok {
		t.Fatalf("Get must still return the stale row: ok=%v err=%v", ok, err)
	}
	if err := eng.ReloadGuardrailConfig(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := eng.guardrails.config.Load().shadowService("ns/shop"); ok {
		t.Fatal("stale card must not be shadow-evaluated")
	}
	applyErrContains(t, eng.ApplyRecommendations(ctx, []RecommendationApplyItem{{ID: id}}), "stale")

	written, err = eng.refreshServiceGuardrailRecommendation(ctx, "ns", "shop")
	if err != nil || written {
		t.Fatalf("already-stale with nothing to offer must not rewrite: written=%v err=%v", written, err)
	}

	// A rule becomes offerable again: remine un-stales the same row.
	g, _ := eng.loadOrInitServiceGuardrail(ctx, "ns/shop")
	g.RuleModes[risk.RuleAllowedCallees] = repo.RuleModeSuggest
	if err := r.UpsertServiceGuardrail(ctx, g); err != nil {
		t.Fatal(err)
	}
	now = created.Add(2 * time.Hour)
	written, err = eng.refreshServiceGuardrailRecommendation(ctx, "ns", "shop")
	if err != nil || !written {
		t.Fatalf("un-stale: written=%v err=%v", written, err)
	}
	rec = mustGetRec(t, r, id)
	if rec.Stale || rec.CreatedAt != created || rec.MinedAt != now {
		t.Fatalf("un-staled card = stale=%v created=%v mined=%v", rec.Stale, rec.CreatedAt, rec.MinedAt)
	}
	if len(rec.Rules) != 1 || rec.Rules[0].Rule != risk.RuleAllowedCallees {
		t.Fatalf("only the now-offerable rule should be on the card: %+v", rec.Rules)
	}
}

// TestServiceGuardrailRecommendation_ApplyAllSkipsNowEnforced: empty selected
// means "every remaining rule". A rule enforced or switched off since mining
// is skipped so apply never overrides a decision made elsewhere; if nothing
// is left, apply fails closed.
func TestServiceGuardrailRecommendation_ApplyAllSkipsNowEnforced(t *testing.T) {
	r, eng := newServiceRecEngine()
	ctx := context.Background()
	id := seedShopCard(t, r, eng)

	if err := r.UpsertServiceGuardrail(ctx, repo.ServiceGuardrail{
		Scope: repo.ScopeService, ScopeKey: "ns/shop",
		RuleModes: map[repo.RuleKey]repo.RuleMode{
			risk.RuleAllowedCallers: repo.RuleModeEnforce,
			risk.RuleAllowedEgress:  repo.RuleModeOff,
		},
		RuleOrigins: map[repo.RuleKey]string{risk.RuleAllowedCallers: "manual"},
		RuleLists:   map[repo.RuleKey][]string{risk.RuleAllowedCallers: {"admin"}},
	}); err != nil {
		t.Fatal(err)
	}
	res := eng.ApplyRecommendations(ctx, []RecommendationApplyItem{{ID: id}})
	if res.Done != 1 || len(res.Errors) != 0 {
		t.Fatalf("apply all: %+v", res)
	}
	g, _ := eng.loadOrInitServiceGuardrail(ctx, "ns/shop")
	if g.RuleModes[risk.RuleAllowedCallers] != repo.RuleModeEnforce || g.RuleOrigins[risk.RuleAllowedCallers] != "manual" || !slices.Equal(g.RuleLists[risk.RuleAllowedCallers], []string{"admin"}) {
		t.Fatalf("already-enforced callers must be left alone: %+v", g)
	}
	if g.RuleModes[risk.RuleAllowedEgress] != repo.RuleModeOff {
		t.Fatalf("off egress must stay off: %v", g.RuleModes)
	}
	if g.RuleModes[risk.RuleAllowedCallees] != repo.RuleModeEnforce || g.RuleOrigins[risk.RuleAllowedCallees] != repo.GuardrailOriginRecommendation {
		t.Fatalf("remaining rules should be applied: %+v", g)
	}
	if g.RuleModes[risk.RuleAllowedTransactions] != repo.RuleModeEnforce || g.RuleFingerprints[risk.RuleAllowedTransactions] != eng.system.IdentityFingerprint() {
		t.Fatalf("transactions should be applied with a live fingerprint: %+v", g)
	}
	rec := mustGetRec(t, r, id)
	if rec.State != repo.RecommendationApplied || !slices.Equal(rec.AppliedRules, []repo.RuleKey{risk.RuleAllowedCallees, risk.RuleAllowedTransactions}) {
		t.Fatalf("AppliedRules should be what apply actually turned on: %+v", rec)
	}

	applyErrContains(t, eng.ApplyRecommendations(ctx, []RecommendationApplyItem{{ID: id, Rules: []repo.RuleKey{risk.RuleAllowedCallees}}}), "applied")
}

func TestServiceGuardrailRecommendation_ApplyRejectsDismissedAndNothingLeft(t *testing.T) {
	r, eng := newServiceRecEngine()
	ctx := context.Background()
	id := seedShopCard(t, r, eng)

	res := eng.DismissRecommendations(ctx, []string{id})
	if res.Done != 1 {
		t.Fatalf("dismiss: %+v", res)
	}
	applyErrContains(t, eng.ApplyRecommendations(ctx, []RecommendationApplyItem{{ID: id}}), "dismissed")

	res = eng.RestoreRecommendations(ctx, []string{id})
	if res.Done != 1 {
		t.Fatalf("restore: %+v", res)
	}
	select {
	case got := <-eng.recommendations.services:
		if got.namespace != "ns" || got.service != "shop" {
			t.Fatalf("restore queued %+v", got)
		}
	default:
		t.Fatal("restore of a service card must queue a remine")
	}

	if err := r.UpsertServiceGuardrail(ctx, repo.ServiceGuardrail{
		Scope: repo.ScopeService, ScopeKey: "ns/shop",
		RuleModes: map[repo.RuleKey]repo.RuleMode{
			risk.RuleAllowedCallers:      repo.RuleModeEnforce,
			risk.RuleAllowedCallees:      repo.RuleModeOff,
			risk.RuleAllowedEgress:       repo.RuleModeEnforce,
			risk.RuleAllowedTransactions: repo.RuleModeOff,
		},
	}); err != nil {
		t.Fatal(err)
	}
	applyErrContains(t, eng.ApplyRecommendations(ctx, []RecommendationApplyItem{{ID: id}}), "nothing to apply")
}

// TestServiceGuardrailRecommendation_RevertDeletesEmptyAndSurvivesMissing:
// reverting the last rec-owned rules deletes the empty guardrail so auto-enable
// is not blocked by a hollow row. A guardrail deleted out of band still reopens
// the card so the operator is not stuck on applied.
func TestServiceGuardrailRecommendation_RevertDeletesEmptyAndSurvivesMissing(t *testing.T) {
	r, eng := newServiceRecEngine()
	ctx := context.Background()
	id := seedShopCard(t, r, eng)

	res := eng.ApplyRecommendations(ctx, []RecommendationApplyItem{{ID: id}})
	if res.Done != 1 {
		t.Fatalf("apply: %+v", res)
	}
	res = eng.RevertRecommendations(ctx, []string{id})
	if res.Done != 1 {
		t.Fatalf("revert: %+v", res)
	}
	if g, found, err := eng.findServiceGuardrail(ctx, "ns/shop"); err != nil || found {
		t.Fatalf("empty rec-owned guardrail should be deleted: found=%v g=%+v err=%v", found, g, err)
	}
	rec := mustGetRec(t, r, id)
	if rec.State != repo.RecommendationOpen || len(rec.AppliedRules) != 0 {
		t.Fatalf("reverted card = %+v", rec)
	}
	select {
	case got := <-eng.recommendations.services:
		if got.service != "shop" {
			t.Fatalf("revert queued %+v", got)
		}
	default:
		t.Fatal("revert must queue a remine so the card's lists refresh")
	}

	res = eng.ApplyRecommendations(ctx, []RecommendationApplyItem{{ID: id}})
	if res.Done != 1 {
		t.Fatalf("re-apply: %+v", res)
	}
	if err := r.DeleteServiceGuardrail(ctx, repo.ScopeService, "ns/shop"); err != nil {
		t.Fatal(err)
	}
	res = eng.RevertRecommendations(ctx, []string{id})
	if res.Done != 1 {
		t.Fatalf("revert after out-of-band delete: %+v", res)
	}
	rec = mustGetRec(t, r, id)
	if rec.State != repo.RecommendationOpen {
		t.Fatalf("missing guardrail must still reopen the card: %s", rec.State)
	}
}

func TestServiceGuardrailRecommendation_DismissedNotRemined(t *testing.T) {
	r, eng := newServiceRecEngine()
	ctx := context.Background()
	id := seedShopCard(t, r, eng)
	if res := eng.DismissRecommendations(ctx, []string{id}); res.Done != 1 {
		t.Fatalf("dismiss: %+v", res)
	}
	r.profiles["ns/shop"] = repo.ServiceProfile{Namespace: "ns", Service: "shop", Callees: []string{"ledger"}}
	written, err := eng.refreshServiceGuardrailRecommendation(ctx, "ns", "shop")
	if err != nil || written {
		t.Fatalf("dismissed card is the operator's decision: written=%v err=%v", written, err)
	}
	rec := mustGetRec(t, r, id)
	if rec.State != repo.RecommendationDismissed {
		t.Fatalf("state = %s", rec.State)
	}
	if _, offered := ruleByKey(rec, risk.RuleAllowedCallees); !offered {
		t.Fatal("dismissed card must keep its mined lists")
	}
}

func TestServiceGuardrailRecommendation_AlreadyCoveredIsFalse(t *testing.T) {
	r, eng := newServiceRecEngine()
	ctx := context.Background()
	id := seedShopCard(t, r, eng)
	view, ok, err := eng.GetRecommendation(ctx, id)
	if err != nil || !ok || view.AlreadyCovered {
		t.Fatalf("service cards are never already-covered (that is a correlation join): %+v ok=%v err=%v", view, ok, err)
	}
}

func TestServiceGuardrailRecommendation_RecomputeQueuesAndRejectsUnknown(t *testing.T) {
	r, eng := newServiceRecEngine()
	ctx := context.Background()
	if err := eng.RecomputeServiceGuardrailRecommendation(ctx, "ns", "missing"); err == nil {
		t.Fatal("unknown service must fail")
	}
	seedPromotedService(t, r, "ns", "shop", "GET /products")
	if err := eng.RecomputeServiceGuardrailRecommendation(ctx, "ns", "shop"); err != nil {
		t.Fatalf("recompute: %v", err)
	}
	select {
	case got := <-eng.recommendations.services:
		if got.namespace != "ns" || got.service != "shop" {
			t.Fatalf("queued %+v", got)
		}
	default:
		t.Fatal("recompute did not queue the service")
	}
}

func TestServiceGuardrailRecommendation_RequestMineServiceDropsWhenFull(t *testing.T) {
	_, eng := newServiceRecEngine()
	m := eng.recommendations
	for i := 0; i < minerQueueSize; i++ {
		m.requestMineService("ns", "shop")
	}
	done := make(chan struct{})
	go func() {
		m.requestMineService("ns", "other")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a full service queue must drop, not block ingest")
	}
	if got := len(m.services); got != minerQueueSize {
		t.Fatalf("queue grew to %d after a drop", got)
	}
}

// TestServiceGuardrailRecommendation_ShadowCallersEmptyListAndFingerprint:
// callers are only counted when an in-mesh parent exists; an empty proposed
// list is "forbid any"; a transactions list mined under another identity
// scheme is skipped, not counted as a hold.
func TestServiceGuardrailRecommendation_ShadowCallersEmptyListAndFingerprint(t *testing.T) {
	r, eng := newServiceRecEngine()
	ctx := context.Background()

	seedPromotedService(t, r, "ns", "checkout", "POST /pay")
	r.profiles = map[string]repo.ServiceProfile{"ns/checkout": {
		Namespace: "ns", Service: "checkout", Callers: []string{"gateway"},
	}}
	if _, err := eng.refreshServiceGuardrailRecommendation(ctx, "ns", "checkout"); err != nil {
		t.Fatal(err)
	}
	id := repo.ServiceGuardrailRecommendationID("ns", "checkout")
	rec := mustGetRec(t, r, id)
	for i := range rec.Rules {
		if rec.Rules[i].Rule == risk.RuleAllowedTransactions {
			rec.Rules[i].Fingerprint = "not-this-scheme"
		}
	}
	if err := r.UpsertRecommendations(ctx, []repo.Recommendation{rec}); err != nil {
		t.Fatal(err)
	}
	if err := eng.ReloadGuardrailConfig(ctx); err != nil {
		t.Fatal(err)
	}

	// gateway → checkout: callers holds (gateway is on the list). checkout
	// also reaches evil.example.com; egress items are empty, so that is a break.
	gateway := &Span{SpanID: "gw", Name: "POST /entry", Service: "gateway", Kind: tracepb.Span_SPAN_KIND_SERVER,
		SpanAttrs: map[string]string{"service.namespace": "ns", "http.method": "POST"}}
	checkout := &Span{SpanID: "pay", ParentSpanID: "gw", Name: "POST /pay", Service: "checkout", Kind: tracepb.Span_SPAN_KIND_SERVER,
		SpanAttrs: map[string]string{"service.namespace": "ns", "http.method": "POST"}}
	egress := &Span{SpanID: "out", ParentSpanID: "pay", Name: "GET", Service: "checkout", Kind: tracepb.Span_SPAN_KIND_CLIENT,
		SpanAttrs: map[string]string{"server.address": "evil.example.com", "server.port": "443"}}
	gateway.Children = []*Span{checkout}
	checkout.Children = []*Span{egress}
	eng.Process(ctx, &Trace{TraceID: "t-hold", Root: gateway, Spans: []*Span{gateway, checkout, egress}})
	if err := eng.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	held, broken := map[repo.RuleKey]int64{}, map[repo.RuleKey]int64{}
	for _, d := range r.liveDeltas {
		if d.ID != id {
			continue
		}
		held[d.Rule] += d.Held
		broken[d.Rule] += d.Broken
	}
	if held[risk.RuleAllowedCallers] != 1 || broken[risk.RuleAllowedCallers] != 0 {
		t.Fatalf("nested caller on the list should hold: held=%v broken=%v", held, broken)
	}
	if broken[risk.RuleAllowedEgress] != 1 {
		t.Fatalf("empty egress list forbids any destination: %v", broken)
	}
	if _, counted := held[risk.RuleAllowedTransactions]; counted {
		t.Fatalf("stale-fingerprint transactions must not be counted: %v", held)
	}
	if _, counted := broken[risk.RuleAllowedTransactions]; counted {
		t.Fatalf("stale-fingerprint transactions must not be counted broken: %v", broken)
	}
	if len(r.guardrailHits) != 0 {
		t.Fatalf("shadow must not fire findings: %+v", r.guardrailHits)
	}
}
