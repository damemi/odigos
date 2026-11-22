package engine

import (
	"bytes"
	"context"
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// buyChainOTLP builds frontend POST /buy -> coupon POST /apply-coupon ->
// pricing POST /price -> <egress>: three nested service entries with the
// egress hanging off the innermost one, so a deviation there is inside all
// three subtrees. Real OTLP rather than a hand-built Trace because accept
// replays the stored bytes of the firing.
func buyChainOTLP(traceSeed byte, egressName, egressPeer, egressScope string) *tracepb.TracesData {
	tid := bytes.Repeat([]byte{traceSeed}, 16)
	frontendID := []byte{1, 1, 1, 1, 1, 1, 1, 1}
	couponID := []byte{2, 2, 2, 2, 2, 2, 2, 2}
	pricingID := []byte{3, 3, 3, 3, 3, 3, 3, 3}
	egressID := []byte{4, 4, 4, 4, 4, 4, 4, 4}

	svc := func(name, scope string, span *tracepb.Span) *tracepb.ResourceSpans {
		return &tracepb.ResourceSpans{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{kvStr("service.name", name)}},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Scope: &commonpb.InstrumentationScope{Name: scope, Version: "1.0"},
				Spans: []*tracepb.Span{span},
			}},
		}
	}
	return &tracepb.TracesData{ResourceSpans: []*tracepb.ResourceSpans{
		svc("frontend", "nethttp", &tracepb.Span{
			TraceId: tid, SpanId: frontendID, Name: "POST /buy", Kind: tracepb.Span_SPAN_KIND_SERVER,
		}),
		svc("coupon", "nethttp", &tracepb.Span{
			TraceId: tid, SpanId: couponID, ParentSpanId: frontendID,
			Name: "POST /apply-coupon", Kind: tracepb.Span_SPAN_KIND_SERVER,
		}),
		svc("pricing", "nethttp", &tracepb.Span{
			TraceId: tid, SpanId: pricingID, ParentSpanId: couponID,
			Name: "POST /price", Kind: tracepb.Span_SPAN_KIND_SERVER,
		}),
		svc("pricing", egressScope, &tracepb.Span{
			TraceId: tid, SpanId: egressID, ParentSpanId: pricingID,
			Name: egressName, Kind: tracepb.Span_SPAN_KIND_CLIENT,
			Attributes: []*commonpb.KeyValue{
				kvStr("server.address", egressPeer),
				kvStr("server.port", "8080"),
			},
		}),
	}}
}

// TestAcceptAnomalyIssue_WidensEnclosedSubTransactions is the counterpart to
// firing on the outermost entry only: coupon and pricing stayed silent because
// frontend reported for them, so accepting on frontend has to widen them too.
// Otherwise the accept just hands the deviation down and the operator gets the
// identical finding again on coupon, then on pricing.
func TestAcceptAnomalyIssue_WidensEnclosedSubTransactions(t *testing.T) {
	ctx := context.Background()
	r := newFakeRepo()
	alerts := alertConfigFrom(repo.Policy{ID: 1, Name: "fire-at-1", Enabled: true, FireAtScore: 1})
	eng := New(r, nil, countOnlyConfig(DefaultMinObservations), alerts)

	process := func(td *tracepb.TracesData) {
		t.Helper()
		for _, tr := range decodeRequest(td) {
			eng.Process(ctx, tr)
		}
		if err := eng.Flush(ctx); err != nil {
			t.Fatalf("flush: %v", err)
		}
	}

	for i := int64(0); i < DefaultMinObservations; i++ {
		process(buyChainOTLP(1, "rates.fetch", "rates", "okhttp3"))
	}

	ids := map[string]int64{}
	for _, tx := range r.transactions {
		ids[tx.Service] = tx.ID
	}
	for _, svc := range []string{"frontend", "coupon", "pricing"} {
		if ids[svc] == 0 {
			t.Fatalf("expected a %s transaction, got %+v", svc, r.transactions)
		}
	}
	if len(r.anomalies) != 0 {
		t.Fatalf("expected no anomalies during learning, got %+v", r.anomalies)
	}

	attack := func() { process(buyChainOTLP(2, "ldap.lookup", "attacker.com", "ldap")) }

	attack()
	if len(r.anomalies) != 1 {
		t.Fatalf("expected exactly one finding, got %+v", r.anomalies)
	}
	issue := r.anomalies[0]
	if issue.TransactionID != ids["frontend"] {
		t.Fatalf("expected the finding on frontend (outermost entry), got transaction %d", issue.TransactionID)
	}

	if err := eng.AcceptAnomalyIssue(ctx, issue.TransactionID, issue.Signature); err != nil {
		t.Fatalf("accept: %v", err)
	}

	attack()
	if len(r.anomalies) != 1 {
		t.Fatalf("accepted deviation resurfaced on an enclosed transaction: %+v", r.anomalies)
	}

	// Each transaction is widened with its own subtree, never the whole trace:
	// pricing must not come out of an accept knowing the callers above it.
	for _, b := range r.baselines {
		if b.TransactionID != ids["pricing"] {
			continue
		}
		if bytes.Contains(b.Data, []byte("frontend")) || bytes.Contains(b.Data, []byte("apply-coupon")) {
			t.Errorf("pricing %s baseline absorbed its callers: %s", b.Class, b.Data)
		}
	}
}

// TestAcceptAnomalyIssue_SkipsSiblingBranches covers the shape where the trace
// forks: frontend calls both coupon and moshe, and only coupon's branch reaches
// the accepted deviation. moshe is a sub-transaction of the same trace but the
// behaviour is not in its subtree, so accepting must neither teach it the new
// peer nor set its learning progress back as though it had just grown.
func TestAcceptAnomalyIssue_SkipsSiblingBranches(t *testing.T) {
	ctx := context.Background()
	r := newFakeRepo()
	alerts := alertConfigFrom(repo.Policy{ID: 1, Name: "fire-at-1", Enabled: true, FireAtScore: 1})
	eng := New(r, nil, countOnlyConfig(DefaultMinObservations), alerts)

	process := func(td *tracepb.TracesData) {
		t.Helper()
		for _, tr := range decodeRequest(td) {
			eng.Process(ctx, tr)
		}
		if err := eng.Flush(ctx); err != nil {
			t.Fatalf("flush: %v", err)
		}
	}

	for i := int64(0); i < DefaultMinObservations; i++ {
		process(withMosheBranch(buyChainOTLP(1, "rates.fetch", "rates", "okhttp3")))
	}
	ids := map[string]int64{}
	for _, tx := range r.transactions {
		ids[tx.Service] = tx.ID
	}
	if ids["moshe"] == 0 {
		t.Fatalf("expected a moshe transaction, got %+v", r.transactions)
	}

	before := map[repo.DeviationClass]repo.BaselineClass{}
	for _, b := range r.baselines {
		if b.TransactionID == ids["moshe"] {
			before[b.Class] = b
		}
	}

	process(withMosheBranch(buyChainOTLP(2, "ldap.lookup", "attacker.com", "ldap")))
	if len(r.anomalies) != 1 {
		t.Fatalf("expected exactly one finding, got %+v", r.anomalies)
	}
	issue := r.anomalies[0]
	if err := eng.AcceptAnomalyIssue(ctx, issue.TransactionID, issue.Signature); err != nil {
		t.Fatalf("accept: %v", err)
	}

	for _, b := range r.baselines {
		if b.TransactionID != ids["moshe"] {
			continue
		}
		if bytes.Contains(b.Data, []byte("attacker.com")) {
			t.Errorf("moshe never called attacker.com but its %s baseline learned it: %s", b.Class, b.Data)
		}
		if prev, ok := before[b.Class]; ok && !b.LastChangedAt.Equal(prev.LastChangedAt) {
			t.Errorf("accept reset moshe's %s stability clock without its baseline growing", b.Class)
		}
	}
}

// withMosheBranch hangs a second service entry off the frontend root, a sibling
// of coupon that makes its own unrelated egress.
func withMosheBranch(td *tracepb.TracesData) *tracepb.TracesData {
	tid := td.GetResourceSpans()[0].GetScopeSpans()[0].GetSpans()[0].GetTraceId()
	frontendID := []byte{1, 1, 1, 1, 1, 1, 1, 1}
	mosheID := []byte{5, 5, 5, 5, 5, 5, 5, 5}
	mosheEgressID := []byte{6, 6, 6, 6, 6, 6, 6, 6}

	td.ResourceSpans = append(td.ResourceSpans, &tracepb.ResourceSpans{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{kvStr("service.name", "moshe")}},
		ScopeSpans: []*tracepb.ScopeSpans{{
			Scope: &commonpb.InstrumentationScope{Name: "nethttp", Version: "1.0"},
			Spans: []*tracepb.Span{
				{TraceId: tid, SpanId: mosheID, ParentSpanId: frontendID, Name: "POST /moshe", Kind: tracepb.Span_SPAN_KIND_SERVER},
				{
					TraceId: tid, SpanId: mosheEgressID, ParentSpanId: mosheID,
					Name: "inventory.read", Kind: tracepb.Span_SPAN_KIND_CLIENT,
					Attributes: []*commonpb.KeyValue{
						kvStr("server.address", "inventory"),
						kvStr("server.port", "8080"),
					},
				},
			},
		}},
	})
	return td
}

// TestAcceptAnomalyIssue_LeavesLearningSubTransactionsAlone keeps accept from
// freezing a baseline that never finished learning: an enclosed transaction
// still in its learning window absorbs the behaviour from live traffic anyway,
// so accept must not seed and promote it.
func TestAcceptAnomalyIssue_LeavesLearningSubTransactionsAlone(t *testing.T) {
	ctx := context.Background()
	r := newFakeRepo()
	alerts := alertConfigFrom(repo.Policy{ID: 1, Name: "fire-at-1", Enabled: true, FireAtScore: 1})
	eng := New(r, nil, countOnlyConfig(DefaultMinObservations), alerts)

	process := func(td *tracepb.TracesData) {
		t.Helper()
		for _, tr := range decodeRequest(td) {
			eng.Process(ctx, tr)
		}
		if err := eng.Flush(ctx); err != nil {
			t.Fatalf("flush: %v", err)
		}
	}

	for i := int64(0); i < DefaultMinObservations; i++ {
		process(buyChainOTLP(1, "rates.fetch", "rates", "okhttp3"))
	}
	ids := map[string]int64{}
	for _, tx := range r.transactions {
		ids[tx.Service] = tx.ID
	}

	// Send pricing back to learning, as if it had just been re-learned.
	if err := eng.ResetTransactionBaselines(ctx, ids["pricing"]); err != nil {
		t.Fatalf("reset pricing: %v", err)
	}

	process(buyChainOTLP(2, "ldap.lookup", "attacker.com", "ldap"))
	if len(r.anomalies) != 1 {
		t.Fatalf("expected exactly one finding, got %+v", r.anomalies)
	}
	issue := r.anomalies[0]
	if err := eng.AcceptAnomalyIssue(ctx, issue.TransactionID, issue.Signature); err != nil {
		t.Fatalf("accept: %v", err)
	}

	for _, b := range r.baselines {
		if b.TransactionID == ids["pricing"] && b.Promoted {
			t.Errorf("accept promoted pricing's %s baseline while it was still learning", b.Class)
		}
	}
}
