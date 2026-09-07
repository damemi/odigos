package engine

import (
	"context"
	"testing"
	"time"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

// guardrailConfigFrom builds a GuardrailConfig from a set of guardrails via the
// shared repo.ScopeIndex - the same path the engine uses at runtime.
func guardrailConfigFrom(guardrails ...repo.ServiceGuardrail) *GuardrailConfig {
	idx := &repo.ScopeIndex[repo.ServiceGuardrail]{}
	for _, g := range guardrails {
		idx.Put(g.Scope, g.ScopeKey, g)
	}
	return newGuardrailConfig(idx)
}

// mkCallTrace builds a two-service trace: callerSvc's SERVER root calls into
// calleeSvc's SERVER entry, so the callee is its own sub-transaction with
// CallerService == callerSvc. The callee carries a namespace so its transaction
// scope key is "ns/<callee>".
func mkCallTrace(callerSvc, calleeSvc string) *Trace {
	root := &Span{
		SpanID: "root", Name: "POST /buy", Service: callerSvc,
		Kind: tracepb.Span_SPAN_KIND_SERVER,
	}
	callee := &Span{
		SpanID: "c1", ParentSpanID: "root", Name: "POST /charge", Service: calleeSvc,
		Kind:  tracepb.Span_SPAN_KIND_SERVER,
		Attrs: map[string]string{"service.namespace": "ns", "http.method": "POST"},
	}
	root.Children = []*Span{callee}
	return &Trace{TraceID: "tid", Root: root, Spans: []*Span{root, callee}}
}

func enforceCallersGuardrail(scopeKey string, allowed ...string) repo.ServiceGuardrail {
	return repo.ServiceGuardrail{
		Scope:     repo.ScopeService,
		ScopeKey:  scopeKey,
		RuleLists: map[repo.RuleKey][]string{risk.RuleAllowedCallers: allowed},
		RuleModes: map[repo.RuleKey]repo.RuleMode{risk.RuleAllowedCallers: repo.RuleModeEnforce},
	}
}

func TestEvalAllowedCallers(t *testing.T) {
	guardrail := enforceCallersGuardrail("ns/payments", "checkout", "cron")

	cases := []struct {
		name     string
		caller   string
		violated bool
	}{
		{"forbidden caller violates", "frontend", true},
		{"allowed caller passes", "checkout", false},
		{"allowed caller case-insensitive", "CheckOut", false},
		{"external ingress (no caller) not policed", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			offending := forbiddenCallers(&Trace{CallerService: c.caller}, guardrail)
			if (len(offending) > 0) != c.violated {
				t.Fatalf("offending = %v, want violated=%v", offending, c.violated)
			}
		})
	}
}

func TestForbiddenCalleesAndEgress(t *testing.T) {
	// A sub-trace rooted at checkout that calls payments (allowed) and ledger
	// (forbidden), and reaches api.stripe.com:443 (allowed) and
	// evil.example.com:4444 (forbidden).
	checkout := &Span{SpanID: "root", Name: "POST /pay", Service: "checkout", Kind: tracepb.Span_SPAN_KIND_SERVER}
	ledger := &Span{SpanID: "c2", ParentSpanID: checkout.SpanID, Name: "GET /post", Service: "ledger", Kind: tracepb.Span_SPAN_KIND_SERVER}
	payments := &Span{SpanID: "c3", ParentSpanID: checkout.SpanID, Name: "POST /charge", Service: "payments", Kind: tracepb.Span_SPAN_KIND_SERVER}
	egressOK := &Span{SpanID: "c4", ParentSpanID: checkout.SpanID, Name: "GET", Service: "checkout", Kind: tracepb.Span_SPAN_KIND_CLIENT,
		Attrs: map[string]string{"server.address": "api.stripe.com", "server.port": "443"}}
	egressBad := &Span{SpanID: "c5", ParentSpanID: checkout.SpanID, Name: "GET", Service: "checkout", Kind: tracepb.Span_SPAN_KIND_CLIENT,
		Attrs: map[string]string{"server.address": "evil.example.com", "server.port": "4444"}}
	checkout.Children = []*Span{ledger, payments, egressOK, egressBad}
	sub := &Trace{TraceID: "tid", Root: checkout, Spans: []*Span{checkout, ledger, payments, egressOK, egressBad}}

	calleeGuardrail := repo.ServiceGuardrail{RuleLists: map[repo.RuleKey][]string{risk.RuleAllowedCallees: {"payments"}}}
	if got := forbiddenCallees(sub, calleeGuardrail); len(got) != 1 || got[0] != "ledger" {
		t.Fatalf("forbiddenCallees = %v, want [ledger]", got)
	}

	egressGuardrail := repo.ServiceGuardrail{RuleLists: map[repo.RuleKey][]string{risk.RuleAllowedEgress: {"api.stripe.com"}}}
	if got := forbiddenEgress(sub, egressGuardrail, nil); len(got) != 1 || got[0] != "evil.example.com:4444" {
		t.Fatalf("forbiddenEgress = %v, want [evil.example.com:4444]", got)
	}
}

// TestForbiddenEgressSkipsKnownService covers the cross-trace guard: a client
// call to an in-mesh service whose SERVER span didn't nest under the caller
// (broken context propagation) is marked External by peersFromTrace, but must
// NOT be policed as egress once the engine knows that host names a service.
func TestForbiddenEgressSkipsKnownService(t *testing.T) {
	// checkout calls "geolocation" (a real service) but its SERVER span is
	// absent from this trace, so the client span is a leaf → External=true.
	checkout := &Span{SpanID: "root", Name: "POST /pay", Service: "checkout", Kind: tracepb.Span_SPAN_KIND_SERVER}
	toGeo := &Span{SpanID: "c1", ParentSpanID: checkout.SpanID, Name: "GET", Service: "checkout", Kind: tracepb.Span_SPAN_KIND_CLIENT,
		Attrs: map[string]string{"server.address": "geolocation", "server.port": "8080"}}
	checkout.Children = []*Span{toGeo}
	sub := &Trace{TraceID: "tid", Root: checkout, Spans: []*Span{checkout, toGeo}}

	guardrail := repo.ServiceGuardrail{RuleLists: map[repo.RuleKey][]string{risk.RuleAllowedEgress: {}}}

	// Without the known-service set it looks like forbidden egress...
	if got := forbiddenEgress(sub, guardrail, nil); len(got) != 1 || got[0] != "geolocation:8080" {
		t.Fatalf("forbiddenEgress(nil) = %v, want [geolocation:8080]", got)
	}
	// ...but once geolocation is known to be in-mesh, it is not policed.
	known := map[string]struct{}{"geolocation": {}}
	if got := forbiddenEgress(sub, guardrail, known); len(got) != 0 {
		t.Fatalf("forbiddenEgress(known) = %v, want []", got)
	}
}

func TestEngine_GuardrailFires(t *testing.T) {
	cases := []struct {
		name      string
		guardrail repo.ServiceGuardrail
		caller    string
		want      int
	}{
		{"forbidden caller fires", enforceCallersGuardrail("ns/payments", "checkout"), "frontend", 1},
		{"allowed caller silent", enforceCallersGuardrail("ns/payments", "frontend"), "frontend", 0},
		{"suggest mode silent", repo.ServiceGuardrail{
			Scope: repo.ScopeService, ScopeKey: "ns/payments",
			RuleLists: map[repo.RuleKey][]string{risk.RuleAllowedCallers: {"checkout"}},
			RuleModes: map[repo.RuleKey]repo.RuleMode{risk.RuleAllowedCallers: repo.RuleModeSuggest},
		}, "frontend", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newFakeRepo()
			eng := New(r, nil, nil, nil)
			eng.clock = func() time.Time { return time.Unix(0, 0).UTC() }
			eng.SetGuardrailConfig(guardrailConfigFrom(c.guardrail))

			eng.Process(context.Background(), mkCallTrace(c.caller, "payments"))
			// Guardrail breaks are buffered in memory and persisted on flush, not
			// written per trace, so drain the buffer before asserting.
			if err := eng.Flush(context.Background()); err != nil {
				t.Fatalf("flush: %v", err)
			}

			// Guardrail breaks never create anomaly rows.
			if len(r.anomalies) != 0 {
				t.Fatalf("guardrails wrote %d anomaly rows, want 0", len(r.anomalies))
			}
			if len(r.guardrailHits) != c.want {
				t.Fatalf("violation groups = %d, want %d", len(r.guardrailHits), c.want)
			}
			if c.want == 1 {
				hit := r.guardrailHits[0]
				if hit.RuleKey != risk.RuleAllowedCallers || hit.Offending != "frontend" || hit.Occurrences != 1 {
					t.Fatalf("unexpected violation hit: %+v", hit)
				}
			}
		})
	}
}
