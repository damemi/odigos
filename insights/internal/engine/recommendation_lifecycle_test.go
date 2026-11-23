package engine

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/odigos-io/odigos/insights/internal/engine/semconv"
	"github.com/odigos-io/odigos/insights/internal/repo"
	"github.com/odigos-io/odigos/insights/internal/risk"
)

// otlpAccountTrace is relationTrace's shape as a real OTLP request, so the
// engine can keep it as a relation sample (RawOTLP) and the miner can decode
// it back. principal is what the gateway resolved, account what the backend
// was asked for; they match on legitimate traffic.
func otlpAccountTrace(i int, principal, account string) *Trace {
	tid := make([]byte, 16)
	binary.BigEndian.PutUint64(tid[8:], uint64(i+1))
	sid := func(n uint64) []byte {
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, uint64(i+1)<<8|n)
		return b
	}
	root, resolve, fetch, get, publish := sid(1), sid(2), sid(3), sid(4), sid(5)
	resource := func(service string, spans ...*tracepb.Span) *tracepb.ResourceSpans {
		return &tracepb.ResourceSpans{
			Resource:   &resourcepb.Resource{Attributes: []*commonpb.KeyValue{kvStr("service.name", service)}},
			ScopeSpans: []*tracepb.ScopeSpans{{Scope: &commonpb.InstrumentationScope{Name: "lib", Version: "1.0"}, Spans: spans}},
		}
	}
	req := &tracepb.TracesData{ResourceSpans: []*tracepb.ResourceSpans{
		resource("edge-gateway",
			&tracepb.Span{TraceId: tid, SpanId: root, Name: "POST /accounts", Kind: tracepb.Span_SPAN_KIND_SERVER,
				Attributes: []*commonpb.KeyValue{kvStr(semconv.LegacyHTTPMethod, "POST"), kvStr("region", "eu")}},
			&tracepb.Span{TraceId: tid, SpanId: resolve, ParentSpanId: root, Name: "resolvePrincipal", Kind: tracepb.Span_SPAN_KIND_INTERNAL,
				Attributes: []*commonpb.KeyValue{kvStr(semconv.ReturnValue, "client-"+principal)}},
			&tracepb.Span{TraceId: tid, SpanId: fetch, ParentSpanId: root, Name: "GET", Kind: tracepb.Span_SPAN_KIND_CLIENT,
				Attributes: []*commonpb.KeyValue{kvStr(semconv.URLFull, fmt.Sprintf("http://accounts/api/accounts/%s?rid=r%d", account, 500+i*7))}},
		),
		resource("account-service",
			&tracepb.Span{TraceId: tid, SpanId: get, ParentSpanId: fetch, Name: "getAccount", Kind: tracepb.Span_SPAN_KIND_INTERNAL,
				Attributes: []*commonpb.KeyValue{kvStr(semconv.ArgKey(0), account)}},
		),
		resource("notification-api",
			&tracepb.Span{TraceId: tid, SpanId: publish, ParentSpanId: get, Name: "publish", Kind: tracepb.Span_SPAN_KIND_INTERNAL,
				Attributes: []*commonpb.KeyValue{kvStr(semconv.ArgKey(0), account)}},
		),
	}}
	traces := decodeRequest(req)
	if len(traces) != 1 {
		panic(fmt.Sprintf("otlpAccountTrace decoded %d traces", len(traces)))
	}
	return traces[0]
}

// recommendationHarness is an engine over the fake repo with a clock that
// advances a minute per trace, so relation sampling's per-transaction interval
// never throttles a test trace.
type recommendationHarness struct {
	t    *testing.T
	r    *fakeRepo
	eng  *Engine
	now  time.Time
	txID int64
}

func newRecommendationHarness(t *testing.T, promoteAfter int64) *recommendationHarness {
	h := &recommendationHarness{t: t, r: newFakeRepo(), now: time.Unix(1_700_000_000, 0).UTC()}
	h.eng = New(h.r, nil, countOnlyConfig(promoteAfter), nil)
	h.eng.clock = func() time.Time { return h.now }
	h.eng.SetGuardrailConfig(guardrailConfigFrom())
	id, ok := h.eng.subTransactionID(otlpAccountTrace(0, "1", "1"), nil)
	if !ok {
		t.Fatal("test trace has no transaction identity")
	}
	h.txID = id
	return h
}

func (h *recommendationHarness) process(i int, principal, account string) {
	h.now = h.now.Add(time.Minute)
	h.eng.Process(context.Background(), otlpAccountTrace(i, principal, account))
}

func (h *recommendationHarness) settle() {
	h.t.Helper()
	h.eng.samples.drain(context.Background())
	if err := h.eng.Flush(context.Background()); err != nil {
		h.t.Fatalf("flush: %v", err)
	}
}

func (h *recommendationHarness) mine() {
	h.t.Helper()
	h.eng.recommendations.mine(context.Background(), h.txID)
}

func (h *recommendationHarness) recommendation(leftSpan, rightSpan string) RecommendationView {
	h.t.Helper()
	views, err := h.eng.ListRecommendations(context.Background(), repo.RecommendationFilter{TransactionID: h.txID, IncludeStale: true})
	if err != nil {
		h.t.Fatalf("list recommendations: %v", err)
	}
	for _, v := range views {
		if v.Spec.Left.Span == leftSpan && v.Spec.Right.Span == rightSpan {
			return v
		}
	}
	h.t.Fatalf("no recommendation %s -> %s among %d", leftSpan, rightSpan, len(views))
	return RecommendationView{}
}

func (h *recommendationHarness) transactionGuardrail() (repo.ServiceGuardrail, bool) {
	h.t.Helper()
	g, ok, err := h.eng.findTransactionGuardrail(context.Background(), h.txID)
	if err != nil {
		h.t.Fatalf("find transaction guardrail: %v", err)
	}
	return g, ok
}

// TestRecommendations_PromotionQueuesMiningAndMinesStoredSamples is the
// learning half of the lifecycle: relation samples are kept while the
// transaction learns, promotion asks the miner to run, and mining those
// samples yields the BOLA rule with the evidence numbers the UI shows.
func TestRecommendations_PromotionQueuesMiningAndMinesStoredSamples(t *testing.T) {
	h := newRecommendationHarness(t, 12)
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("%d", 1001+i)
		h.process(i, id, id)
	}
	h.settle()

	// The promoting trace is not sampled (learning is over), the 11 before it are.
	counts, _ := h.r.CountObservationsByReason(context.Background(), h.txID)
	if counts[repo.RelationSampleReason] != 11 {
		t.Fatalf("relation samples = %d, want 11", counts[repo.RelationSampleReason])
	}
	select {
	case got := <-h.eng.recommendations.queue:
		if got != h.txID {
			t.Fatalf("queued transaction %d, want %d", got, h.txID)
		}
	default:
		t.Fatal("promotion did not queue the transaction for mining")
	}

	h.mine()
	core := h.recommendation("resolvePrincipal", "getAccount")
	if core.State != repo.RecommendationOpen || core.Transform != relationTransformCore || core.AlreadyCovered {
		t.Fatalf("mined recommendation = %+v", core)
	}
	if core.Scope != repo.ScopeTransaction || core.ScopeKey != repo.TransactionScopeKey(h.txID) {
		t.Fatalf("scope = %s/%s, want transaction/%d", core.Scope, core.ScopeKey, h.txID)
	}
	if core.SampleCount != 11 || core.Observed != 11 || core.Held != 11 || core.Distinct != 11 {
		t.Fatalf("evidence = samples %d observed %d held %d distinct %d, want 11 each", core.SampleCount, core.Observed, core.Held, core.Distinct)
	}
	if core.Spec.Left.Service != "edge-gateway" || core.Spec.Right.Service != "account-service" || core.Spec.Left.Extract != `^client-(\d+)$` {
		t.Fatalf("spec = %+v", core.Spec)
	}
	run := h.r.miningRuns[h.txID]
	if run.MinedAt.IsZero() || run.SampleCount != 11 || run.Candidates == 0 {
		t.Fatalf("mining run = %+v", run)
	}
}

// TestRecommendations_ShadowCountsThenApplyEnforces is the operator half: an
// open recommendation is counted on live traffic without firing, applying it
// creates the enforced transaction rule that then fires on the same traffic,
// and re-mining keeps the applied state.
func TestRecommendations_ShadowCountsThenApplyEnforces(t *testing.T) {
	h := newRecommendationHarness(t, 11)
	for i := 0; i < 11; i++ {
		id := fmt.Sprintf("%d", 1001+i)
		h.process(i, id, id)
	}
	h.settle()
	h.mine()
	core := h.recommendation("resolvePrincipal", "getAccount")

	// Live traffic: one legitimate trace, one confused-deputy trace.
	h.process(20, "2001", "2001")
	h.process(21, "2002", "2003")
	h.settle()
	if len(h.r.guardrailHits) != 0 {
		t.Fatalf("shadow evaluation fired %d violations, want none", len(h.r.guardrailHits))
	}
	core = h.recommendation("resolvePrincipal", "getAccount")
	if core.LiveHeld != 1 || core.LiveBroken != 1 {
		t.Fatalf("live counts = held %d broken %d, want 1/1", core.LiveHeld, core.LiveBroken)
	}
	for _, d := range h.r.liveDeltas {
		if d.Scope != repo.ScopeTransaction || d.ScopeKey != repo.TransactionScopeKey(h.txID) {
			t.Fatalf("live delta scope = %s/%s", d.Scope, d.ScopeKey)
		}
	}

	res := h.eng.ApplyRecommendations(context.Background(), []RecommendationApplyItem{{ID: core.ID}})
	if res.Done != 1 || len(res.Errors) != 0 {
		t.Fatalf("apply = %+v", res)
	}
	g, ok := h.transactionGuardrail()
	if !ok {
		t.Fatal("apply did not create the transaction guardrail")
	}
	specs := g.RuleCorrelations[risk.RuleAttributeCorrelation]
	if len(specs) != 1 || !sameCorrelationSelectors(specs[0], core.Spec) || specs[0].Left.Extract != core.Spec.Left.Extract {
		t.Fatalf("guardrail correlations = %+v", specs)
	}
	if g.RuleModes[risk.RuleAttributeCorrelation] != repo.RuleModeEnforce || g.RuleOrigins[risk.RuleAttributeCorrelation] != repo.GuardrailOriginRecommendation {
		t.Fatalf("mode/origin = %q/%q", g.RuleModes[risk.RuleAttributeCorrelation], g.RuleOrigins[risk.RuleAttributeCorrelation])
	}
	core = h.recommendation("resolvePrincipal", "getAccount")
	if core.State != repo.RecommendationApplied || !core.AlreadyCovered {
		t.Fatalf("after apply: state %s covered %v", core.State, core.AlreadyCovered)
	}

	// The applied rule now fires; the recommendation is no longer shadowed.
	h.process(22, "2002", "2003")
	h.settle()
	if len(h.r.guardrailHits) != 1 || h.r.guardrailHits[0].RuleKey != risk.RuleAttributeCorrelation {
		t.Fatalf("guardrail hits = %+v, want one attribute_correlation violation", h.r.guardrailHits)
	}
	if again := h.recommendation("resolvePrincipal", "getAccount"); again.LiveBroken != 1 {
		t.Fatalf("applied recommendation still shadow-counted: broken %d", again.LiveBroken)
	}

	h.mine()
	if core = h.recommendation("resolvePrincipal", "getAccount"); core.State != repo.RecommendationApplied || core.Stale {
		t.Fatalf("re-mining changed the applied recommendation: %+v", core)
	}

	// Revert removes the rule (the guardrail had nothing else) and reopens.
	res = h.eng.RevertRecommendations(context.Background(), []string{core.ID})
	if res.Done != 1 {
		t.Fatalf("revert = %+v", res)
	}
	if _, ok := h.transactionGuardrail(); ok {
		t.Fatal("revert left an empty transaction guardrail behind")
	}
	if core = h.recommendation("resolvePrincipal", "getAccount"); core.State != repo.RecommendationOpen || core.AlreadyCovered {
		t.Fatalf("after revert: %+v", core)
	}
}

func TestRecommendations_ApplyWithEditedSpec(t *testing.T) {
	h := newRecommendationHarness(t, 11)
	for i := 0; i < 11; i++ {
		id := fmt.Sprintf("%d", 1001+i)
		h.process(i, id, id)
	}
	h.settle()
	h.mine()
	core := h.recommendation("resolvePrincipal", "getAccount")

	// Edits may rename, re-rank severity or change the extract...
	edited := core.Spec
	edited.Name, edited.Severity, edited.Left.Extract = "principal must own account", "high", `(\d+)`
	res := h.eng.ApplyRecommendations(context.Background(), []RecommendationApplyItem{{ID: core.ID, Spec: &edited}})
	if res.Done != 1 {
		t.Fatalf("apply edited = %+v", res)
	}
	g, _ := h.transactionGuardrail()
	if got := g.RuleCorrelations[risk.RuleAttributeCorrelation][0]; got.Name != edited.Name || got.Severity != "high" || got.Left.Extract != `(\d+)` {
		t.Fatalf("stored spec = %+v", got)
	}

	// ...but not point the rule at other attributes, and a bad regex is refused.
	other := core.Spec
	other.Right.Attr = semconv.ArgKey(1)
	res = h.eng.ApplyRecommendations(context.Background(), []RecommendationApplyItem{{ID: core.ID, Spec: &other}})
	if res.Done != 0 || len(res.Errors) != 1 {
		t.Fatalf("apply with moved selector = %+v", res)
	}
	bad := core.Spec
	bad.Left.Extract = `(`
	res = h.eng.ApplyRecommendations(context.Background(), []RecommendationApplyItem{{ID: core.ID, Spec: &bad}})
	if res.Done != 0 || len(res.Errors) != 1 {
		t.Fatalf("apply with invalid regex = %+v", res)
	}
	res = h.eng.ApplyRecommendations(context.Background(), []RecommendationApplyItem{{ID: "missing"}})
	if len(res.Errors) != 1 || res.Errors[0].Err != ErrRecommendationNotFound {
		t.Fatalf("apply unknown id = %+v", res)
	}
}

func TestRecommendations_DismissRestoreAndPreview(t *testing.T) {
	h := newRecommendationHarness(t, 11)
	for i := 0; i < 11; i++ {
		id := fmt.Sprintf("%d", 1001+i)
		h.process(i, id, id)
	}
	h.settle()
	h.mine()
	core := h.recommendation("resolvePrincipal", "getAccount")
	ctx := context.Background()

	if res := h.eng.DismissRecommendations(ctx, []string{core.ID}); res.Done != 1 {
		t.Fatalf("dismiss = %+v", res)
	}
	if got := h.recommendation("resolvePrincipal", "getAccount"); got.State != repo.RecommendationDismissed {
		t.Fatalf("state after dismiss = %s", got.State)
	}
	// Dismissed recommendations are not shadow-evaluated.
	if specs := h.eng.guardrails.config.Load().shadowSpecs(h.txID); len(specs) != 0 {
		for _, s := range specs {
			if s.id == core.ID {
				t.Fatal("dismissed recommendation still shadow-evaluated")
			}
		}
	}
	if res := h.eng.DismissRecommendations(ctx, []string{core.ID}); res.Done != 0 || len(res.Errors) != 1 {
		t.Fatalf("second dismiss = %+v, want an error", res)
	}
	// Sticky across re-mining.
	h.mine()
	if got := h.recommendation("resolvePrincipal", "getAccount"); got.State != repo.RecommendationDismissed {
		t.Fatalf("re-mining reopened a dismissed recommendation: %s", got.State)
	}
	if res := h.eng.RestoreRecommendations(ctx, []string{core.ID}); res.Done != 1 {
		t.Fatalf("restore = %+v", res)
	}
	if got := h.recommendation("resolvePrincipal", "getAccount"); got.State != repo.RecommendationOpen {
		t.Fatalf("state after restore = %s", got.State)
	}

	// Preview replays a spec on the stored samples: the mined spec holds on
	// all of them, the same spec without extraction on none.
	preview, err := h.eng.PreviewRecommendation(ctx, h.txID, core.Spec)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.SampleCount != 10 || preview.Observed != 10 || preview.Held != 10 || len(preview.CounterExamples) != 0 {
		t.Fatalf("preview of mined spec = %+v", preview)
	}
	raw := core.Spec
	raw.Left.Extract = ""
	preview, err = h.eng.PreviewRecommendation(ctx, h.txID, raw)
	if err != nil {
		t.Fatalf("preview raw: %v", err)
	}
	if preview.Held != 0 || preview.Observed != 10 || len(preview.CounterExamples) == 0 {
		t.Fatalf("preview of raw spec = %+v", preview)
	}
	if _, err := h.eng.PreviewRecommendation(ctx, h.txID, repo.CorrelationSpec{}); err == nil {
		t.Fatal("preview accepted an empty spec")
	}
}

func TestRecommendations_ReminingMarksVanishedRelationsStale(t *testing.T) {
	h := newRecommendationHarness(t, 11)
	for i := 0; i < 11; i++ {
		id := fmt.Sprintf("%d", 1001+i)
		h.process(i, id, id)
	}
	h.settle()
	h.mine()
	core := h.recommendation("resolvePrincipal", "getAccount")

	// The samples the relation was mined from are gone (retention), and the
	// transaction shows a relation the earlier run did not.
	h.r.mu.Lock()
	h.r.observations = nil
	h.r.mu.Unlock()
	h.mine()
	if got := h.recommendation("resolvePrincipal", "getAccount"); !got.Stale || got.ID != core.ID {
		t.Fatalf("recommendation without samples should be stale: %+v", got)
	}
	open, err := h.eng.ListRecommendations(context.Background(), repo.RecommendationFilter{TransactionID: h.txID})
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 0 {
		t.Fatalf("stale recommendations listed by default: %d", len(open))
	}
}

func TestRecommendations_RecomputeQueuesTransaction(t *testing.T) {
	h := newRecommendationHarness(t, 100)
	ctx := context.Background()
	if err := h.eng.RecomputeRecommendations(ctx, h.txID); err == nil {
		t.Fatal("recompute of an unknown transaction should fail")
	}
	h.process(0, "1", "1")
	h.settle()
	if err := h.eng.RecomputeRecommendations(ctx, h.txID); err != nil {
		t.Fatalf("recompute: %v", err)
	}
	select {
	case got := <-h.eng.recommendations.queue:
		if got != h.txID {
			t.Fatalf("queued %d, want %d", got, h.txID)
		}
	default:
		t.Fatal("recompute did not queue the transaction")
	}
	if err := h.eng.RecomputeRecommendations(ctx, 0); err != nil {
		t.Fatalf("recompute all: %v", err)
	}
	select {
	case <-h.eng.recommendations.wake:
	default:
		t.Fatal("recompute all did not wake the sweep")
	}
}
