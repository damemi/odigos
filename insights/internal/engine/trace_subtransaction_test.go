package engine

import (
	"bytes"
	"context"
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

func mustUnmarshalTraces(t *testing.T, raw []byte) *tracepb.TracesData {
	t.Helper()
	var td tracepb.TracesData
	if err := proto.Unmarshal(raw, &td); err != nil {
		t.Fatalf("unmarshal TracesData: %v", err)
	}
	return &td
}

// buyTrace builds frontend /buy -> coupon /apply-coupon -> <inner>, where the
// two HTTP server spans are service entries and <inner> is whatever egress the
// caller wants to vary between the "normal" and "attack" shapes.
func buyTrace(innerName, innerSvc string, innerKind tracepb.Span_SpanKind, innerAttrs map[string]string, innerScope string) *Trace {
	frontend := &Span{SpanID: "frontend", Name: "POST /buy", Service: "frontend", Kind: tracepb.Span_SPAN_KIND_SERVER, ScopeName: "nethttp"}
	coupon := &Span{SpanID: "coupon", ParentSpanID: "frontend", Name: "POST /apply-coupon", Service: "coupon", Kind: tracepb.Span_SPAN_KIND_SERVER, ScopeName: "nethttp"}
	inner := &Span{SpanID: "inner", ParentSpanID: "coupon", Name: innerName, Service: innerSvc, Kind: innerKind, Attrs: innerAttrs, ScopeName: innerScope}
	coupon.Children = []*Span{inner}
	frontend.Children = []*Span{coupon}
	return &Trace{TraceID: "tid", Root: frontend, Spans: []*Span{frontend, coupon, inner}}
}

func TestServiceEntrySpans_RootPlusNestedServers(t *testing.T) {
	tr := buyTrace("pricing.calc", "coupon", tracepb.Span_SPAN_KIND_CLIENT,
		map[string]string{"server.address": "pricing"}, "okhttp3")

	entries := serviceEntrySpans(tr)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (frontend, coupon), got %d", len(entries))
	}
	if entries[0].SpanID != "frontend" || entries[1].SpanID != "coupon" {
		t.Fatalf("expected pre-order [frontend, coupon], got [%s, %s]", entries[0].SpanID, entries[1].SpanID)
	}
}

func TestSubTraces_BuildsPerEntrySubtree(t *testing.T) {
	tr := buyTrace("pricing.calc", "coupon", tracepb.Span_SPAN_KIND_CLIENT,
		map[string]string{"server.address": "pricing"}, "okhttp3")

	subs := subTraces(tr)
	if len(subs) != 2 {
		t.Fatalf("expected 2 sub-traces, got %d", len(subs))
	}

	// Root sub-trace is the original trace verbatim (full subtree).
	if subs[0] != tr {
		t.Errorf("root sub-trace should reuse the original *Trace instance")
	}

	// Inner sub-trace is re-rooted at coupon and carries only coupon + inner.
	coupon := subs[1]
	if coupon.Root.SpanID != "coupon" {
		t.Fatalf("inner sub-trace root = %s, want coupon", coupon.Root.SpanID)
	}
	if len(coupon.Spans) != 2 {
		t.Fatalf("coupon subtree should have 2 spans (coupon, inner), got %d", len(coupon.Spans))
	}
	// Identity derives from the entry span, independent of the caller.
	id, ok := identifyTransaction(coupon)
	if !ok {
		t.Fatalf("coupon entry should be a valid transaction")
	}
	if id.Service != "coupon" || id.Operation != "POST /apply-coupon" || id.Kind != repo.KindHTTP {
		t.Errorf("unexpected coupon identity: %+v", id)
	}
}

// TestReRootedOTLP_EntryBecomesRoot drives a real proto trace through
// DecodeRequest, decomposes it, and decodes the inner sub-trace's RawOTLP back
// out to prove the subtree was re-rooted: coupon is parent-less (a root) and
// the frontend ancestor is gone while the pricing descendant is kept.
func TestReRootedOTLP_EntryBecomesRoot(t *testing.T) {
	tid := []byte{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}
	frontendID := []byte{1, 1, 1, 1, 1, 1, 1, 1}
	couponID := []byte{2, 2, 2, 2, 2, 2, 2, 2}
	pricingID := []byte{3, 3, 3, 3, 3, 3, 3, 3}

	req := &tracepb.TracesData{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{kvStr("service.name", "demo")}},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Scope: &commonpb.InstrumentationScope{Name: "lib", Version: "1.0"},
				Spans: []*tracepb.Span{
					{TraceId: tid, SpanId: frontendID, Name: "POST /buy", Kind: tracepb.Span_SPAN_KIND_SERVER},
					{TraceId: tid, SpanId: couponID, ParentSpanId: frontendID, Name: "POST /apply-coupon", Kind: tracepb.Span_SPAN_KIND_SERVER},
					{TraceId: tid, SpanId: pricingID, ParentSpanId: couponID, Name: "pricing.calc", Kind: tracepb.Span_SPAN_KIND_CLIENT},
				},
			}},
		}},
	}

	traces := DecodeRequest(req)
	if len(traces) != 1 {
		t.Fatalf("expected 1 decoded trace, got %d", len(traces))
	}

	subs := subTraces(traces[0])
	if len(subs) != 2 {
		t.Fatalf("expected 2 sub-traces, got %d", len(subs))
	}
	coupon := subs[1]
	if len(coupon.RawOTLP()) == 0 {
		t.Fatalf("inner sub-trace RawOTLP is empty")
	}

	// Decode the re-rooted bytes back into a trace: coupon must now be the root.
	redecoded := DecodeRequest(mustUnmarshalTraces(t, coupon.RawOTLP()))
	if len(redecoded) != 1 {
		t.Fatalf("expected re-rooted OTLP to decode to 1 trace, got %d", len(redecoded))
	}
	root := redecoded[0].Root
	if root == nil || root.Name != "POST /apply-coupon" {
		t.Fatalf("re-rooted trace root = %+v, want coupon entry", root)
	}
	if len(redecoded[0].Spans) != 2 {
		t.Errorf("re-rooted trace should contain coupon + pricing (2 spans), got %d", len(redecoded[0].Spans))
	}
	for _, s := range redecoded[0].Spans {
		if s.Name == "POST /buy" {
			t.Errorf("frontend ancestor leaked into the re-rooted subtree")
		}
	}
}

// TestReRootedOTLP_InnerEntryDoesNotCorruptOuter covers the one way entries
// sharing a single decode can go wrong: detaching the innermost entry from its
// parent in place would also detach it inside the enclosing entry's stored
// trace, which would then open with two roots and a waterfall that no longer
// shows the call it is meant to explain.
func TestReRootedOTLP_InnerEntryDoesNotCorruptOuter(t *testing.T) {
	tid := []byte{4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4}
	frontendID := []byte{1, 1, 1, 1, 1, 1, 1, 1}
	couponID := []byte{2, 2, 2, 2, 2, 2, 2, 2}
	paymentID := []byte{3, 3, 3, 3, 3, 3, 3, 3}

	req := &tracepb.TracesData{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{kvStr("service.name", "demo")}},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Scope: &commonpb.InstrumentationScope{Name: "lib", Version: "1.0"},
				Spans: []*tracepb.Span{
					{TraceId: tid, SpanId: frontendID, Name: "POST /buy", Kind: tracepb.Span_SPAN_KIND_SERVER},
					{TraceId: tid, SpanId: couponID, ParentSpanId: frontendID, Name: "POST /apply-coupon", Kind: tracepb.Span_SPAN_KIND_SERVER},
					{TraceId: tid, SpanId: paymentID, ParentSpanId: couponID, Name: "POST /charge", Kind: tracepb.Span_SPAN_KIND_SERVER},
				},
			}},
		}},
	}

	traces := DecodeRequest(req)
	if len(traces) != 1 {
		t.Fatalf("expected 1 decoded trace, got %d", len(traces))
	}
	subs := subTraces(traces[0])
	if len(subs) != 3 {
		t.Fatalf("expected 3 sub-traces (frontend, coupon, payment), got %d", len(subs))
	}
	coupon, payment := subByRootName(t, subs, "POST /apply-coupon"), subByRootName(t, subs, "POST /charge")

	// Innermost entry first: it is the one whose parent gets cleared.
	if len(payment.RawOTLP()) == 0 {
		t.Fatal("payment sub-trace RawOTLP is empty")
	}
	couponTD := mustUnmarshalTraces(t, coupon.RawOTLP())

	if parent := spanParentIn(t, couponTD, couponID); len(parent) != 0 {
		t.Errorf("coupon entry should be parent-less in its own subtree, got parent %x", parent)
	}
	if parent := spanParentIn(t, couponTD, paymentID); !bytes.Equal(parent, couponID) {
		t.Errorf("payment parent in coupon's subtree = %x, want %x: the shared decode was mutated in place", parent, couponID)
	}
}

func subByRootName(t *testing.T, subs []*Trace, name string) *Trace {
	t.Helper()
	for _, s := range subs {
		if s.Root != nil && s.Root.Name == name {
			return s
		}
	}
	t.Fatalf("no sub-trace rooted at %q", name)
	return nil
}

// spanParentIn returns the parent span id recorded for spanID in td.
func spanParentIn(t *testing.T, td *tracepb.TracesData, spanID []byte) []byte {
	t.Helper()
	for _, rs := range td.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				if bytes.Equal(sp.GetSpanId(), spanID) {
					return sp.GetParentSpanId()
				}
			}
		}
	}
	t.Fatalf("span %x not found in re-rooted payload", spanID)
	return nil
}

// TestEngine_NestedEntryFiresOwnAnomaly proves a sub-transaction is first-class:
// after both frontend and coupon learn, an anomaly that originates under coupon
// fires under coupon's own transaction.
func TestEngine_NestedEntryFiresOwnAnomaly(t *testing.T) {
	r := newFakeRepo()
	alerts := alertConfigFrom(repo.Policy{ID: 1, Name: "fire-at-1", Enabled: true, FireAtScore: 1})
	eng := New(r, nil, countOnlyConfig(DefaultMinObservations), alerts)

	normal := func() *Trace {
		return buyTrace("pricing.calc", "coupon", tracepb.Span_SPAN_KIND_CLIENT,
			map[string]string{"server.address": "pricing", "server.port": "8080"}, "okhttp3")
	}
	for i := int64(0); i < DefaultMinObservations; i++ {
		eng.Process(context.Background(), normal())
	}
	if err := eng.Flush(context.Background()); err != nil {
		t.Fatalf("flush learn: %v", err)
	}

	// One transaction per entry span: frontend + coupon.
	if len(r.transactions) != 2 {
		t.Fatalf("expected 2 transactions (frontend, coupon), got %d", len(r.transactions))
	}
	var couponID int64
	for _, tx := range r.transactions {
		if tx.Service == "coupon" {
			couponID = tx.ID
		}
	}
	if couponID == 0 {
		t.Fatalf("coupon transaction was never created")
	}
	if len(r.anomalies) != 0 {
		t.Fatalf("expected no anomalies during learning, got %d", len(r.anomalies))
	}

	// Attack: coupon suddenly egresses to a new peer over a new library with a
	// new edge - 3 deviation classes, all originating under coupon.
	attack := buyTrace("ldap.lookup", "coupon", tracepb.Span_SPAN_KIND_CLIENT,
		map[string]string{"server.address": "attacker.com", "server.port": "1389"}, "ldap")
	eng.Process(context.Background(), attack)
	if err := eng.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	var couponFired bool
	for _, a := range r.anomalies {
		if a.TransactionID == couponID {
			couponFired = true
			if len(a.TriggeredClasses) < 3 {
				t.Errorf("coupon anomaly: expected >=3 triggered classes, got %v", a.TriggeredClasses)
			}
		}
	}
	if !couponFired {
		t.Fatalf("expected coupon sub-transaction to fire its own anomaly; anomalies=%+v", r.anomalies)
	}
}

// TestSubTraces_FlatTraceUnchanged guards the backward-compatible path: a trace
// whose only entry is the root produces exactly one sub-trace (the original),
// so non-decomposable traffic behaves exactly as before.
func TestSubTraces_FlatTraceUnchanged(t *testing.T) {
	tr := mkTrace(
		withRoot("POST /checkout", "user-svc", tracepb.Span_SPAN_KIND_SERVER, nil),
		withChild("root", "inventory.reserve", tracepb.Span_SPAN_KIND_CLIENT, nil, "okhttp3"),
	)
	subs := subTraces(tr)
	if len(subs) != 1 || subs[0] != tr {
		t.Fatalf("flat trace should yield exactly the original trace, got %d sub-traces", len(subs))
	}
}
