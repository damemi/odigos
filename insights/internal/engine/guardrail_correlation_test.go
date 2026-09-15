package engine

import (
	"testing"

	"github.com/odigos-io/odigos-enterprise/insights/internal/engine/semconv"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

// bolaTrace builds a trace like 2d2591d4: edge-gateway resolvePrincipal returns
// "client-1001" while account-service getAccount is called with account id
// "1002" - a confused-deputy / BOLA the equals correlation should catch once the
// numeric ids are extracted.
func bolaTrace(returnValue, accountArg string) *Trace {
	resolve := &Span{
		SpanID: "s1", Service: "edge-gateway", Name: "resolvePrincipal",
		SpanAttrs: map[string]string{semconv.ReturnValue: returnValue},
	}
	getAccount := &Span{
		SpanID: "s2", Service: "account-service", Name: "getAccount",
		SpanAttrs: map[string]string{semconv.ArgKey(0): accountArg},
	}
	return &Trace{TraceID: "tid", Root: resolve, Spans: []*Span{resolve, getAccount}}
}

func correlationGuardrail(mode repo.RuleMode, specs ...repo.CorrelationSpec) repo.ServiceGuardrail {
	return repo.ServiceGuardrail{
		Scope:            repo.ScopeTransaction,
		ScopeKey:         "42",
		RuleModes:        map[repo.RuleKey]repo.RuleMode{risk.RuleAttributeCorrelation: mode},
		RuleCorrelations: map[repo.RuleKey][]repo.CorrelationSpec{risk.RuleAttributeCorrelation: specs},
	}
}

func principalMatchesAccount(severity string) repo.CorrelationSpec {
	return repo.CorrelationSpec{
		Name:     "principal-matches-account",
		Left:     repo.CorrelationSelector{Service: "edge-gateway", Span: "resolvePrincipal", Attr: semconv.ReturnValue, Extract: `(\d+)`},
		Right:    repo.CorrelationSelector{Service: "account-service", Span: "getAccount", Attr: semconv.ArgKey(0), Extract: `(\d+)`},
		Relation: repo.CorrelationEquals,
		Severity: severity,
	}
}

func TestCorrelationViolations_BOLAFires(t *testing.T) {
	g := correlationGuardrail(repo.RuleModeEnforce, principalMatchesAccount(""))
	got := correlationViolations(bolaTrace("client-1001", "1002"), g)
	if len(got) != 1 {
		t.Fatalf("violations = %d, want 1", len(got))
	}
	if got[0].rule != risk.RuleAttributeCorrelation {
		t.Fatalf("rule = %q, want attribute_correlation", got[0].rule)
	}
	// Empty severity defaults to critical.
	if got[0].score != risk.ScoreForSeverity("") || got[0].score < 75 {
		t.Fatalf("score = %d, want critical (>=75)", got[0].score)
	}
}

func TestCorrelationViolations_EqualIDsPass(t *testing.T) {
	g := correlationGuardrail(repo.RuleModeEnforce, principalMatchesAccount(""))
	if got := correlationViolations(bolaTrace("client-1001", "1001"), g); len(got) != 0 {
		t.Fatalf("violations = %v, want none when ids match", got)
	}
}

// The attack calls getAccount twice, once for the caller's own account and
// once for the victim's; the violation fires whichever runs first.
func TestCorrelationViolations_AnyMatchingSpanBreaks(t *testing.T) {
	g := correlationGuardrail(repo.RuleModeEnforce, principalMatchesAccount(""))
	for _, order := range [][2]string{{"1001", "1002"}, {"1002", "1001"}} {
		tr := bolaTrace("client-1001", order[0])
		tr.Spans = append(tr.Spans, &Span{SpanID: "s3", Service: "account-service", Name: "getAccount", SpanAttrs: map[string]string{semconv.ArgKey(0): order[1]}})
		got := correlationViolations(tr, g)
		if len(got) != 1 {
			t.Fatalf("order %v: violations = %d, want 1", order, len(got))
		}
		res := evaluateCorrelation(tr, principalMatchesAccount(""))
		if res.held || res.left != "1001" || res.right != "1002" {
			t.Fatalf("order %v: result = %+v, want the breaking pair 1001/1002", order, res)
		}
	}
	// Two legitimate calls with the caller's own id still hold.
	tr := bolaTrace("client-1001", "1001")
	tr.Spans = append(tr.Spans, &Span{SpanID: "s3", Service: "account-service", Name: "getAccount", SpanAttrs: map[string]string{semconv.ArgKey(0): "1001"}})
	if got := correlationViolations(tr, g); len(got) != 0 {
		t.Fatalf("violations = %v, want none when every call agrees", got)
	}
	// not_equals breaks as soon as any pair is equal.
	ne := principalMatchesAccount("")
	ne.Relation = repo.CorrelationNotEquals
	tr = bolaTrace("client-1001", "1002")
	tr.Spans = append(tr.Spans, &Span{SpanID: "s3", Service: "account-service", Name: "getAccount", SpanAttrs: map[string]string{semconv.ArgKey(0): "1001"}})
	if res := evaluateCorrelation(tr, ne); !res.evaluated || res.held || res.right != "1001" {
		t.Fatalf("not_equals result = %+v, want broken on the equal pair", res)
	}
}

func TestCorrelationViolations_MissingSpanNoOp(t *testing.T) {
	// Only the resolvePrincipal span present; getAccount is absent, so the
	// assertion cannot be evaluated and nothing fires.
	resolve := &Span{SpanID: "s1", Service: "edge-gateway", Name: "resolvePrincipal",
		SpanAttrs: map[string]string{semconv.ReturnValue: "client-1001"}}
	trace := &Trace{TraceID: "tid", Root: resolve, Spans: []*Span{resolve}}
	g := correlationGuardrail(repo.RuleModeEnforce, principalMatchesAccount(""))
	if got := correlationViolations(trace, g); len(got) != 0 {
		t.Fatalf("violations = %v, want none when a span is missing", got)
	}
}

func TestCorrelationViolations_RegexExtract(t *testing.T) {
	// Without extraction "client-1001" != "1002" would fire on a name-vs-id
	// mismatch; with extraction the numeric ids drive the comparison, so equal
	// numbers must pass even when the raw strings differ.
	spec := principalMatchesAccount("")
	g := correlationGuardrail(repo.RuleModeEnforce, spec)
	if got := correlationViolations(bolaTrace("client-1001", "1001"), g); len(got) != 0 {
		t.Fatalf("regex extract should equalise ids, got %v", got)
	}
}

func TestCorrelationViolations_OnlyEnforceFires(t *testing.T) {
	spec := principalMatchesAccount("")
	for _, mode := range []repo.RuleMode{repo.RuleModeSuggest, repo.RuleModeOff} {
		g := correlationGuardrail(mode, spec)
		if got := correlationViolations(bolaTrace("client-1001", "1002"), g); len(got) != 0 {
			t.Fatalf("mode %q fired %v, want observe-only", mode, got)
		}
	}
}

func TestCorrelationViolations_SeverityRespected(t *testing.T) {
	cases := []struct {
		severity string
		want     int
	}{
		{"", risk.ScoreForSeverity("")}, // default critical
		{"critical", risk.ScoreForSeverity("critical")},
		{"high", risk.ScoreForSeverity("high")},
		{"medium", risk.ScoreForSeverity("medium")},
		{"low", risk.ScoreForSeverity("low")},
	}
	for _, c := range cases {
		g := correlationGuardrail(repo.RuleModeEnforce, principalMatchesAccount(c.severity))
		got := correlationViolations(bolaTrace("client-1001", "1002"), g)
		if len(got) != 1 || got[0].score != c.want {
			t.Fatalf("severity %q: score = %v, want %d", c.severity, got, c.want)
		}
	}
}

func TestCorrelationViolations_NotEqualsRelation(t *testing.T) {
	spec := principalMatchesAccount("")
	spec.Relation = repo.CorrelationNotEquals
	g := correlationGuardrail(repo.RuleModeEnforce, spec)
	// Required not-equals: equal ids now violate.
	if got := correlationViolations(bolaTrace("client-1001", "1001"), g); len(got) != 1 {
		t.Fatalf("not_equals should fire on equal ids, got %v", got)
	}
	// Differing ids satisfy not-equals.
	if got := correlationViolations(bolaTrace("client-1001", "1002"), g); len(got) != 0 {
		t.Fatalf("not_equals should pass on differing ids, got %v", got)
	}
}

// TestTransactionGuardrailDoesNotShadowService covers the additive-index
// invariant: a transaction-scoped guardrail must not shadow the service/global
// allowlist for that transaction. Resolve (the allowlist path) must still return
// the service guardrail, while ResolveTransactionGuardrail returns the txn one.
func TestTransactionGuardrailDoesNotShadowService(t *testing.T) {
	service := enforceCallersGuardrail("ns/payments", "checkout")
	txn := correlationGuardrail(repo.RuleModeEnforce, principalMatchesAccount("")) // scope_key "42"
	cfg := guardrailConfigFrom(service, txn)

	got, ok := cfg.Resolve(42, "ns/payments")
	if !ok {
		t.Fatal("Resolve returned no guardrail; service allowlist was shadowed")
	}
	if _, hasCallers := got.RuleLists[risk.RuleAllowedCallers]; !hasCallers {
		t.Fatal("Resolve returned the transaction guardrail, not the service allowlist")
	}

	txnGot, ok := cfg.ResolveTransactionGuardrail(42)
	if !ok || len(txnGot.RuleCorrelations[risk.RuleAttributeCorrelation]) != 1 {
		t.Fatalf("ResolveTransactionGuardrail = %+v, ok=%v; want the correlation guardrail", txnGot, ok)
	}
	if _, ok := cfg.ResolveTransactionGuardrail(99); ok {
		t.Fatal("ResolveTransactionGuardrail(99) should be absent")
	}
}
