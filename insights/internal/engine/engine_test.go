package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/odigos-io/odigos/common/collector/sourceidentity"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// -----------------------------------------------------------------------------
// Helpers: build small synthetic traces without going through OTLP proto.
// -----------------------------------------------------------------------------

func mkTrace(opts ...func(*Trace)) *Trace {
	t := &Trace{TraceID: "tid"}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// decodeRequest is DecodeRequest with the engine's default SourceIdentityFunc.
func decodeRequest(req *tracepb.TracesData) []*Trace {
	return DecodeRequest(req, sourceidentity.FromK8sResourceAttributesMap)
}

// k8sSourceIdentity is the source identity of k8sResourceAttrs(namespace).
func k8sSourceIdentity(namespace string) map[string]string {
	return sourceidentity.FromK8sResourceAttributesMap(k8sResourceAttrs(namespace))
}

// k8sResourceAttrs is a complete k8s source identity, which is what gives a
// transaction its namespace.
func k8sResourceAttrs(namespace string) map[string]string {
	return map[string]string{
		"k8s.namespace.name":   namespace,
		"k8s.container.name":   "app",
		"odigos.workload.kind": "Deployment",
		"odigos.workload.name": "workload",
	}
}

func withRoot(name, service string, kind tracepb.Span_SpanKind, attrs map[string]string) func(*Trace) {
	return func(t *Trace) {
		s := &Span{
			SpanID: "root", Name: name, Service: service, Kind: kind, SpanAttrs: attrs,
			ScopeName: "rootlib", ScopeVersion: "1.0",
		}
		t.Root = s
		t.Spans = append(t.Spans, s)
	}
}

func withChild(parent, name string, kind tracepb.Span_SpanKind, attrs map[string]string, scope string) func(*Trace) {
	return func(t *Trace) {
		var p *Span
		for _, s := range t.Spans {
			if s.SpanID == parent {
				p = s
				break
			}
		}
		if p == nil {
			panic("withChild: parent span " + parent + " not found")
		}
		c := &Span{
			SpanID: "c-" + name, ParentSpanID: parent, Name: name, Service: p.Service,
			Kind: kind, SpanAttrs: attrs, ScopeName: scope, ScopeVersion: "1.0",
		}
		p.Children = append(p.Children, c)
		t.Spans = append(t.Spans, c)
	}
}

// -----------------------------------------------------------------------------
// Scorer unit tests
// -----------------------------------------------------------------------------

func TestD1_NewEdgeDeviates(t *testing.T) {
	tr := mkTrace(
		withRoot("checkout", "user-svc", tracepb.Span_SPAN_KIND_SERVER, nil),
		withChild("root", "inventory.reserve", tracepb.Span_SPAN_KIND_CLIENT, nil, "okhttp3"),
	)
	baseline, err := (d1CallEdges{}).Merge(tr, nil)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	// Same trace against learned baseline: no deviation.
	dev, _, _ := (d1CallEdges{}).Score(tr, baseline)
	if dev {
		t.Errorf("expected no deviation on identical trace")
	}

	// Add a brand-new edge: should deviate.
	tr2 := mkTrace(
		withRoot("checkout", "user-svc", tracepb.Span_SPAN_KIND_SERVER, nil),
		withChild("root", "StringSubstitutor.replace", tracepb.Span_SPAN_KIND_INTERNAL, nil, "commons-text"),
	)
	dev, ev, _ := (d1CallEdges{}).Score(tr2, baseline)
	if !dev {
		t.Fatalf("expected deviation for new edge")
	}
	if !contains(string(ev), "StringSubstitutor.replace") {
		t.Errorf("evidence did not mention the new edge: %s", ev)
	}
}

// TestD1_NewServiceSameOpDeviates locks in the service-in-key behavior: a
// call to a DIFFERENT service is a new edge even when the operation name is one
// we've already learned elsewhere (lateral-movement detection).
func TestD1_NewServiceSameOpDeviates(t *testing.T) {
	mkEdgeTrace := func(childSvc string) *Trace {
		root := &Span{SpanID: "root", Name: "POST /checkout", Service: "frontend", Kind: tracepb.Span_SPAN_KIND_SERVER}
		child := &Span{SpanID: "c1", ParentSpanID: "root", Name: "charge", Service: childSvc, Kind: tracepb.Span_SPAN_KIND_CLIENT}
		root.Children = []*Span{child}
		return &Trace{TraceID: "t", Root: root, Spans: []*Span{root, child}}
	}

	baseline, err := (d1CallEdges{}).Merge(mkEdgeTrace("payments"), nil)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	// Same op name, same service: no deviation.
	if dev, _, _ := (d1CallEdges{}).Score(mkEdgeTrace("payments"), baseline); dev {
		t.Errorf("expected no deviation on identical (service, op) edge")
	}

	// Same op name "charge", different service: must deviate.
	dev, ev, _ := (d1CallEdges{}).Score(mkEdgeTrace("evil-svc"), baseline)
	if !dev {
		t.Fatalf("expected deviation for same op name on a new service")
	}
	if !contains(string(ev), "evil-svc") {
		t.Errorf("evidence did not mention the new service: %s", ev)
	}
}

func TestD2_NewPeerDeviates(t *testing.T) {
	tr := mkTrace(
		withRoot("checkout", "user-svc", tracepb.Span_SPAN_KIND_SERVER, nil),
		withChild("root", "stripe.charge", tracepb.Span_SPAN_KIND_CLIENT,
			map[string]string{"server.address": "stripe.com", "server.port": "443"}, "okhttp3"),
	)
	baseline, _ := (d2Egress{}).Merge(tr, nil)

	dev, _, _ := (d2Egress{}).Score(tr, baseline)
	if dev {
		t.Errorf("expected no deviation on identical trace")
	}

	tr2 := mkTrace(
		withRoot("checkout", "user-svc", tracepb.Span_SPAN_KIND_SERVER, nil),
		withChild("root", "ldap.lookup", tracepb.Span_SPAN_KIND_CLIENT,
			map[string]string{"server.address": "attacker.com", "server.port": "1389"}, "ldap"),
	)
	dev, ev, _ := (d2Egress{}).Score(tr2, baseline)
	if !dev {
		t.Fatalf("expected deviation for new peer")
	}
	if !contains(string(ev), "attacker.com") {
		t.Errorf("evidence did not mention the new peer: %s", ev)
	}
}

func TestD5_NewLibraryDeviates(t *testing.T) {
	tr := mkTrace(
		withRoot("checkout", "user-svc", tracepb.Span_SPAN_KIND_SERVER, nil),
		withChild("root", "db.query", tracepb.Span_SPAN_KIND_CLIENT, nil, "postgresql"),
	)
	baseline, _ := (d5Libraries{}).Merge(tr, nil)

	dev, _, _ := (d5Libraries{}).Score(tr, baseline)
	if dev {
		t.Errorf("expected no deviation on identical libs")
	}

	tr2 := mkTrace(
		withRoot("checkout", "user-svc", tracepb.Span_SPAN_KIND_SERVER, nil),
		withChild("root", "interp.eval", tracepb.Span_SPAN_KIND_INTERNAL, nil, "commons-text"),
	)
	dev, ev, _ := (d5Libraries{}).Score(tr2, baseline)
	if !dev {
		t.Fatalf("expected deviation for new library")
	}
	if !contains(string(ev), "commons-text") {
		t.Errorf("evidence did not mention new lib: %s", ev)
	}
}

// -----------------------------------------------------------------------------
// Engine integration tests with an in-memory repo
// -----------------------------------------------------------------------------

func TestEngine_LearnsThenFires(t *testing.T) {
	r := newFakeRepo()
	alerts := alertConfigFrom(repo.Policy{
		ID: 1, Name: "fire-at-1", Enabled: true, FireAtScore: 1,
	})
	eng := New(r, nil, countOnlyConfig(DefaultMinObservations), alerts)
	// Freeze the clock so the time-throttled example sampler (sampler.go) is
	// deterministic: every trace below lands inside one ExampleSampleInterval,
	// so exactly one "example" observation is kept. Count-only promotion
	// doesn't consult the clock, so freezing it doesn't affect graduation.
	eng.clock = func() time.Time { return time.Unix(0, 0).UTC() }

	normal := func() *Trace {
		return mkTrace(
			withRoot("POST /checkout", "user-svc", tracepb.Span_SPAN_KIND_SERVER,
				map[string]string{"http.method": "POST"}),
			withChild("root", "inventory.reserve", tracepb.Span_SPAN_KIND_CLIENT,
				map[string]string{"server.address": "inventory", "server.port": "8080"}, "okhttp3"),
			withChild("root", "stripe.charge", tracepb.Span_SPAN_KIND_CLIENT,
				map[string]string{"server.address": "stripe.com", "server.port": "443"}, "okhttp3"),
		)
	}

	// Send enough normal traces to promote every class.
	for i := int64(0); i < DefaultMinObservations; i++ {
		eng.Process(context.Background(), normal())
	}
	// Baselines live in memory until flushed; drain so the assertions below
	// (and confirm-on-anomaly for the attack) see the persisted state.
	if err := eng.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if len(r.transactions) != 1 {
		t.Fatalf("expected 1 transaction, got %d", len(r.transactions))
	}
	if len(r.anomalies) != 0 {
		t.Fatalf("expected no anomalies during learning, got %d", len(r.anomalies))
	}
	// The example sampler is time-throttled (sampler.go): with the clock
	// frozen, all DefaultMinObservations traces fall inside one
	// ExampleSampleInterval, so only the first is kept as an "example".
	reasons := countObservationReasons(r.observations)
	if want := 1; reasons["example"] != want {
		t.Errorf("example: got %d want %d", reasons["example"], want)
	}

	// Every class should be promoted now.
	for _, b := range r.baselines {
		if !b.Promoted {
			t.Errorf("class %s not promoted after %d traces", b.Class, DefaultMinObservations)
		}
	}

	// Attack trace: brand-new edge + brand-new peer + brand-new library = 3 dev classes.
	attack := mkTrace(
		withRoot("POST /checkout", "user-svc", tracepb.Span_SPAN_KIND_SERVER,
			map[string]string{"http.method": "POST"}),
		withChild("root", "StringSubstitutor.replace", tracepb.Span_SPAN_KIND_INTERNAL,
			nil, "commons-text"),
		withChild("root", "ldap.lookup", tracepb.Span_SPAN_KIND_CLIENT,
			map[string]string{"server.address": "attacker.com", "server.port": "1389"}, "ldap"),
	)
	eng.Process(context.Background(), attack)
	if err := eng.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if len(r.anomalies) != 1 {
		t.Fatalf("expected 1 anomaly, got %d", len(r.anomalies))
	}
	a := r.anomalies[0]
	if len(a.TriggeredClasses) != 3 {
		t.Errorf("expected 3 triggered classes (D1+D2+D5), got %v", a.TriggeredClasses)
	}
	if a.PolicyID != 1 {
		t.Errorf("expected policy id 1, got %d", a.PolicyID)
	}
}

func TestEngine_NoMergeWhenAnomalyFires(t *testing.T) {
	// Anomaly-flagged trace must NOT pollute the baseline (poisoning protection).
	r := newFakeRepo()
	alerts := alertConfigFrom(repo.Policy{
		ID: 1, Enabled: true, FireAtScore: 1,
	})
	eng := New(r, nil, countOnlyConfig(DefaultMinObservations), alerts)

	base := mkTrace(
		withRoot("/x", "svc", tracepb.Span_SPAN_KIND_SERVER, nil),
		withChild("root", "child-a", tracepb.Span_SPAN_KIND_CLIENT, nil, "lib-a"),
	)
	for i := int64(0); i < DefaultMinObservations; i++ {
		eng.Process(context.Background(), base)
	}
	if err := eng.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// Snapshot baseline data before the attack.
	pre := snapshotBaselines(r)

	attack := mkTrace(
		withRoot("/x", "svc", tracepb.Span_SPAN_KIND_SERVER, nil),
		withChild("root", "evil", tracepb.Span_SPAN_KIND_CLIENT, nil, "evil-lib"),
	)
	eng.Process(context.Background(), attack)

	post := snapshotBaselines(r)
	for k, v := range pre {
		if post[k] != v {
			t.Errorf("baseline %s changed after anomaly: %s -> %s", k, v, post[k])
		}
	}
}

// TestEngine_ResetClassRelearnsWhileSiblingFires is the regression for the
// re-learn starvation bug: after an operator resets one class, it must keep
// learning even while a DIFFERENT promoted class keeps firing on every trace.
// Before the fix the anomaly path returned before the learn loop, so the reset
// class never re-accumulated and vanished from the UI forever.
func TestEngine_ResetClassRelearnsWhileSiblingFires(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, countOnlyConfig(DefaultMinObservations),
		alertConfigFrom(repo.Policy{ID: 1, Enabled: true, FireAtScore: 1}))
	eng.clock = func() time.Time { return time.Unix(0, 0).UTC() }
	ctx := context.Background()

	// A "frontend /buy" trace with one internal edge and one external peer.
	// port picks the D2 peer; the D1 edge (root → known.call) is constant.
	trace := func(port string) *Trace {
		return mkTrace(
			withRoot("POST /buy", "frontend", tracepb.Span_SPAN_KIND_SERVER, nil),
			withChild("root", "known.call", tracepb.Span_SPAN_KIND_CLIENT,
				map[string]string{"server.address": "known", "server.port": port}, "okhttp3"),
		)
	}

	// Learn until every class promotes.
	for i := int64(0); i < DefaultMinObservations; i++ {
		eng.Process(ctx, trace("8080"))
	}
	if err := eng.Flush(ctx); err != nil {
		t.Fatalf("flush learn: %v", err)
	}
	if len(r.transactions) != 1 {
		t.Fatalf("expected 1 transaction after flush, got %d", len(r.transactions))
	}
	txID := r.transactions[0].ID
	classData := func(class repo.DeviationClass) (repo.BaselineClass, bool) {
		rows, _ := r.GetBaselineForTransaction(ctx, txID)
		for _, b := range rows {
			if b.Class == class {
				return b, true
			}
		}
		return repo.BaselineClass{}, false
	}
	if bc, ok := classData(repo.D1CallEdges); !ok || !bc.Promoted {
		t.Fatalf("D1 should be promoted after learning, got %+v (present=%v)", bc, ok)
	}

	// Operator resets D1. It's now gone and back in the learning phase.
	if err := eng.ResetBaselineClass(ctx, txID, repo.D1CallEdges); err != nil {
		t.Fatalf("reset D1: %v", err)
	}
	if _, ok := classData(repo.D1CallEdges); ok {
		t.Fatalf("D1 should be deleted immediately after reset")
	}

	// Now every trace hits a NEW peer (known:9090), so the still-promoted D2
	// fires on each one. D1's normal edge is present on every trace, so with the
	// fix it re-learns and re-promotes despite the sustained sibling anomaly.
	for i := int64(0); i < DefaultMinObservations; i++ {
		eng.Process(ctx, trace("9090"))
	}
	if err := eng.Flush(ctx); err != nil {
		t.Fatalf("flush relearn: %v", err)
	}

	// The sibling really did keep firing, and only D2 fired.
	if len(r.anomalies) == 0 {
		t.Fatalf("expected the promoted D2 sibling to keep firing")
	}
	if got := r.anomalies[0].TriggeredClasses; len(got) != 1 || got[0] != repo.D2Egress {
		t.Fatalf("expected only D2 to fire, got %v", got)
	}

	// The reset class re-learned and re-promoted, and its edge is back.
	bc, ok := classData(repo.D1CallEdges)
	if !ok {
		t.Fatalf("D1 must re-appear after re-learning; it stayed deleted (the bug)")
	}
	if !bc.Promoted {
		t.Errorf("D1 should re-promote after %d observations, got count=%d promoted=%v",
			DefaultMinObservations, bc.ObservationCount, bc.Promoted)
	}
	if !contains(string(bc.Data), "known.call") {
		t.Errorf("re-learned D1 baseline missing the normal edge: %s", bc.Data)
	}

	// Poisoning protection of the ESTABLISHED baseline is intact: the promoted
	// D2 never absorbed the anomalous peer it kept firing on.
	if d2, ok := classData(repo.D2Egress); ok && contains(string(d2.Data), "9090") {
		t.Errorf("promoted D2 must not learn the anomalous peer (poisoning): %s", d2.Data)
	}
}

// TestEngine_ScoreGate verifies the risk-score gate: the SAME deviation fires
// or not purely as a function of the policy threshold and per-class weight
// override. The attack deviates only D1 (catalog weight 20), an isolated signal
// that makes the exact-threshold math deterministic.
func TestEngine_ScoreGate(t *testing.T) {
	normal := func() *Trace {
		return mkTrace(
			withRoot("POST /checkout", "user-svc", tracepb.Span_SPAN_KIND_SERVER,
				map[string]string{"http.method": "POST"}),
			withChild("root", "inventory.reserve", tracepb.Span_SPAN_KIND_CLIENT,
				map[string]string{"server.address": "inventory", "server.port": "8080"}, "okhttp3"),
		)
	}
	// Adds a brand-new call edge (root → pricing.compute) with a known library
	// and no egress, so only D1 deviates.
	newEdgeAttack := func() *Trace {
		return mkTrace(
			withRoot("POST /checkout", "user-svc", tracepb.Span_SPAN_KIND_SERVER,
				map[string]string{"http.method": "POST"}),
			withChild("root", "inventory.reserve", tracepb.Span_SPAN_KIND_CLIENT,
				map[string]string{"server.address": "inventory", "server.port": "8080"}, "okhttp3"),
			withChild("root", "pricing.compute", tracepb.Span_SPAN_KIND_INTERNAL, nil, "okhttp3"),
		)
	}

	d1 := repo.D1CallEdges
	cases := []struct {
		name   string
		policy repo.Policy
		want   int // expected anomalies after the attack
	}{
		{"score at threshold fires", repo.Policy{ID: 1, Enabled: true, FireAtScore: 20}, 1},
		{"score below threshold suppressed", repo.Policy{ID: 1, Enabled: true, FireAtScore: 21}, 0},
		{"weight override drops below threshold", repo.Policy{ID: 1, Enabled: true, FireAtScore: 20, SignalWeights: map[repo.SignalSource]int{repo.SignalSource(d1): 19}}, 0},
		{"weight override raises to threshold", repo.Policy{ID: 1, Enabled: true, FireAtScore: 50, SignalWeights: map[repo.SignalSource]int{repo.SignalSource(d1): 50}}, 1},
		{"disabled never fires", repo.Policy{ID: 1, Enabled: false, FireAtScore: 1}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newFakeRepo()
			eng := New(r, nil, countOnlyConfig(DefaultMinObservations), alertConfigFrom(c.policy))
			eng.clock = func() time.Time { return time.Unix(0, 0).UTC() }
			for i := int64(0); i < DefaultMinObservations; i++ {
				eng.Process(context.Background(), normal())
			}
			if err := eng.Flush(context.Background()); err != nil {
				t.Fatalf("flush: %v", err)
			}
			eng.Process(context.Background(), newEdgeAttack())
			if err := eng.Flush(context.Background()); err != nil {
				t.Fatalf("flush: %v", err)
			}
			if len(r.anomalies) != c.want {
				t.Fatalf("anomalies = %d, want %d", len(r.anomalies), c.want)
			}
			// In a firing case, confirm the isolated signal really is just D1 -
			// this is what makes the exact-threshold math above valid.
			if c.want == 1 {
				got := r.anomalies[0].TriggeredClasses
				if len(got) != 1 || got[0] != d1 {
					t.Fatalf("expected only D1 to deviate, got %v", got)
				}
			}
		})
	}
}

func snapshotBaselines(r *fakeRepo) map[string]string {
	out := map[string]string{}
	for _, b := range r.baselines {
		out[string(b.Class)] = string(b.Data)
	}
	return out
}

func countObservationReasons(obs []repo.Observation) map[string]int {
	out := map[string]int{}
	for _, o := range obs {
		out[o.SampleReason]++
	}
	return out
}

// TestSampler_StopsAfterCap proves example collection fills up to
// ExamplesPerTransaction and then stops (no rolling prune).
func TestSampler_StopsAfterCap(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, countOnlyConfig(1000), nil)

	s := repo.DefaultSystemSettings()
	s.ExamplesPerTransaction = 3
	s.ExampleSampleIntervalSeconds = 1
	_ = r.UpsertSystemSettings(context.Background(), s)
	_ = eng.ReloadSystemConfig(context.Background())

	now := time.Unix(0, 0).UTC()
	eng.clock = func() time.Time { return now }
	mk := func() *Trace {
		return mkTrace(withRoot("/x", "svc", tracepb.Span_SPAN_KIND_SERVER, nil))
	}
	// Examples are written behind by the sample writer; drain so the count
	// reflects what Process decided to keep.
	countExamples := func() int {
		eng.samples.drain(context.Background())
		n := 0
		for _, o := range r.observations {
			if o.SampleReason == repo.ExampleSampleReason {
				n++
			}
		}
		return n
	}

	for i := 0; i < 10; i++ {
		eng.Process(context.Background(), mk())
		now = now.Add(time.Second)
	}
	if got := countExamples(); got != 3 {
		t.Fatalf("after filling cap: got %d examples, want 3", got)
	}
}

// TestSampler_ExampleByInterval proves the example sampler is throttled by
// time, not trace count: a burst of traces within one ExampleSampleInterval
// yields a single example, and advancing the clock past the interval lets the
// next trace through.
func TestSampler_ExampleByInterval(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, nil, nil)

	now := time.Unix(0, 0).UTC()
	eng.clock = func() time.Time { return now }

	mk := func() *Trace {
		return mkTrace(
			withRoot("/x", "svc", tracepb.Span_SPAN_KIND_SERVER, nil),
			withChild("root", "child", tracepb.Span_SPAN_KIND_CLIENT, nil, "lib"),
		)
	}

	countExamples := func() int {
		eng.samples.drain(context.Background())
		n := 0
		for _, o := range r.observations {
			if o.SampleReason == "example" {
				n++
			}
		}
		return n
	}

	// A burst at the same instant: only the first trace is kept.
	for i := 0; i < 20; i++ {
		eng.Process(context.Background(), mk())
	}
	if got := countExamples(); got != 1 {
		t.Fatalf("burst within one interval: got %d examples, want 1", got)
	}

	// Still inside the interval: no new example.
	now = now.Add(ExampleSampleInterval - time.Nanosecond)
	eng.Process(context.Background(), mk())
	if got := countExamples(); got != 1 {
		t.Fatalf("just under the interval: got %d examples, want 1", got)
	}

	// Past the interval: the next trace is kept.
	now = now.Add(time.Nanosecond)
	eng.Process(context.Background(), mk())
	if got := countExamples(); got != 2 {
		t.Fatalf("past the interval: got %d examples, want 2", got)
	}
}

// TestSampler_StopsExamplesOnPromotion proves example collection freezes at
// promotion: while learning, throttled "example" samples fill up to the cap;
// once every class promotes the engine stops recording them so the compare
// base column stops drifting with live traffic.
func TestSampler_StopsExamplesOnPromotion(t *testing.T) {
	const minObs = 5
	r := newFakeRepo()
	eng := New(r, nil, countOnlyConfig(minObs), nil)

	now := time.Unix(0, 0).UTC()
	eng.clock = func() time.Time { return now }

	mk := func() *Trace {
		return mkTrace(
			withRoot("/x", "svc", tracepb.Span_SPAN_KIND_SERVER, nil),
			withChild("root", "child", tracepb.Span_SPAN_KIND_CLIENT, nil, "lib"),
		)
	}
	countExamples := func() int {
		n := 0
		for _, o := range r.observations {
			if o.SampleReason == repo.ExampleSampleReason {
				n++
			}
		}
		return n
	}

	for i := 0; i < minObs; i++ {
		eng.Process(context.Background(), mk())
		now = now.Add(ExampleSampleInterval)
	}
	if err := eng.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	for _, b := range r.baselines {
		if !b.Promoted {
			t.Fatalf("class %s not promoted after %d observations", b.Class, minObs)
		}
	}

	examplesAtPromotion := countExamples()
	if examplesAtPromotion == 0 {
		t.Fatalf("expected some example samples while learning")
	}

	for i := 0; i < 5; i++ {
		eng.Process(context.Background(), mk())
		now = now.Add(ExampleSampleInterval)
	}
	if got := countExamples(); got != examplesAtPromotion {
		t.Fatalf("examples kept collecting after promotion: got %d, want %d", got, examplesAtPromotion)
	}
}

// TestEngine_RelearnDeletesAnomalyIssues proves a re-learn drops findings scored
// against the wiped baseline so they cannot contradict the fresh one.
func TestEngine_RelearnDeletesAnomalyIssues(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, countOnlyConfig(5), nil)

	tx, err := r.UpsertTransaction(context.Background(), "svc", "ns", "/x", repo.KindHTTP, nil)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := r.RecordAnomalyOccurrences(context.Background(), []repo.AnomalyOccurrence{{
		TransactionID: tx.ID, Signature: "sig-old", PolicyID: 1,
		TriggeredClasses: []repo.DeviationClass{repo.D1CallEdges},
		Offending:        "d1_call_edges=x", Occurrences: 3, MaxScore: 40,
		FirstSeen: time.Now(), LastSeen: time.Now(),
	}}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if got := len(r.anomalies); got != 1 {
		t.Fatalf("precondition: got %d anomalies, want 1", got)
	}

	if err := eng.ResetTransactionBaselines(context.Background(), tx.ID); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got := len(r.anomalies); got != 0 {
		t.Fatalf("re-learn should delete anomaly issues: got %d, want 0", got)
	}
}

// TestEngine_TimeBasedPromotion proves a rare transaction graduates on the
// learning window alone: the min_observations condition is set unreachably
// high, so under "any" mode only the min_duration condition can promote it.
func TestEngine_TimeBasedPromotion(t *testing.T) {
	r := newFakeRepo()
	policies := map[repo.DeviationClass]repo.LearningPolicy{}
	for _, class := range allDeviationClasses {
		policies[class] = repo.LearningPolicy{
			Class: class,
			Mode:  repo.LearningModeAny,
			Conditions: []repo.LearningCondition{
				{Type: repo.ConditionMinObservations, MinObservations: 1_000_000},
				{Type: repo.ConditionMinDuration, MinDurationMinutes: 60},
			},
		}
	}
	eng := New(r, nil, newLearningConfig(policies), nil)

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := t0
	eng.clock = func() time.Time { return now }

	tr := func() *Trace {
		return mkTrace(
			withRoot("cron.weekly-rollup", "billing", tracepb.Span_SPAN_KIND_INTERNAL,
				map[string]string{"faas.trigger": "timer"}),
			withChild("root", "db.rollup", tracepb.Span_SPAN_KIND_CLIENT, nil, "postgresql"),
		)
	}

	// A few observations far under the count knob, all at t0: no class may
	// promote yet (count too low, zero time elapsed).
	for i := 0; i < 3; i++ {
		eng.Process(context.Background(), tr())
	}
	if err := eng.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	for _, b := range r.baselines {
		if b.Promoted {
			t.Fatalf("class %s promoted too early (count under threshold, no time elapsed)", b.Class)
		}
	}

	// Advance past the learning window; the next trace graduates every class
	// on the time knob alone.
	now = t0.Add(2 * time.Hour)
	eng.Process(context.Background(), tr())
	if err := eng.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	for _, b := range r.baselines {
		if !b.Promoted {
			t.Errorf("class %s not promoted after learning window elapsed", b.Class)
		}
	}
}

// TestEngine_WriteBehindIdentity proves transaction rows are not inserted on
// the hot path - only on Flush - and that delete+reprocess recreates them
// without an identity cache.
func TestEngine_WriteBehindIdentity(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, countOnlyConfig(1_000_000), nil)

	tr := mkTrace(
		withRoot("GET /health", "api", tracepb.Span_SPAN_KIND_SERVER, nil),
	)
	eng.Process(context.Background(), tr)
	if got := len(r.transactions); got != 0 {
		t.Fatalf("hot path must not upsert identity: got %d transactions", got)
	}

	if err := eng.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := len(r.transactions); got != 1 {
		t.Fatalf("flush should upsert identity: got %d transactions", got)
	}
	txID := r.transactions[0].ID
	if txID == 0 {
		t.Fatal("expected non-zero transaction id")
	}

	if err := eng.DeleteTransaction(context.Background(), txID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := len(r.transactions); got != 0 {
		t.Fatalf("delete should remove identity: got %d", got)
	}

	eng.Process(context.Background(), tr)
	if got := len(r.transactions); got != 0 {
		t.Fatalf("hot path after delete must not upsert: got %d", got)
	}
	if err := eng.Flush(context.Background()); err != nil {
		t.Fatalf("flush after recreate: %v", err)
	}
	if got := len(r.transactions); got != 1 {
		t.Fatalf("flush after delete+traffic should recreate identity: got %d", got)
	}
	if r.transactions[0].ID != txID {
		t.Fatalf("recreated id %d != original %d (hash must be stable)", r.transactions[0].ID, txID)
	}
}

// TestEngine_SweepPromotesWithoutNewTrace proves time-based promotion can
// graduate classes on the flush sweeper alone, without another trace arriving.
func TestEngine_SweepPromotesWithoutNewTrace(t *testing.T) {
	r := newFakeRepo()
	policies := map[repo.DeviationClass]repo.LearningPolicy{}
	for _, class := range allDeviationClasses {
		policies[class] = repo.LearningPolicy{
			Class: class,
			Mode:  repo.LearningModeAny,
			Conditions: []repo.LearningCondition{
				{Type: repo.ConditionMinObservations, MinObservations: 1_000_000},
				{Type: repo.ConditionMinDuration, MinDurationMinutes: 60},
			},
		}
	}
	eng := New(r, nil, newLearningConfig(policies), nil)

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := t0
	eng.clock = func() time.Time { return now }

	tr := func() *Trace {
		return mkTrace(
			withRoot("cron.weekly-rollup", "billing", tracepb.Span_SPAN_KIND_INTERNAL,
				map[string]string{"faas.trigger": "timer"}),
			withChild("root", "db.rollup", tracepb.Span_SPAN_KIND_CLIENT, nil, "postgresql"),
		)
	}

	for i := 0; i < 3; i++ {
		eng.Process(context.Background(), tr())
	}
	if err := eng.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	now = t0.Add(2 * time.Hour)
	eng.sweepPromotions(context.Background())
	if err := eng.Flush(context.Background()); err != nil {
		t.Fatalf("flush after sweep: %v", err)
	}
	for _, b := range r.baselines {
		if !b.Promoted {
			t.Errorf("class %s not promoted by sweeper after learning window elapsed", b.Class)
		}
	}
}

// TestEngine_SweepDoesNotPromoteEmptyStableDuration proves stable_duration alone
// does not promote a baseline that has never grown.
func TestEngine_SweepDoesNotPromoteEmptyStableDuration(t *testing.T) {
	r := newFakeRepo()
	policies := map[repo.DeviationClass]repo.LearningPolicy{}
	for _, class := range allDeviationClasses {
		policies[class] = repo.LearningPolicy{
			Class: class,
			Mode:  repo.LearningModeAny,
			Conditions: []repo.LearningCondition{
				{Type: repo.ConditionStableDuration, StableMinutes: 30},
			},
		}
	}
	eng := New(r, nil, newLearningConfig(policies), nil)

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	eng.clock = func() time.Time { return t0.Add(2 * time.Hour) }

	eng.Process(context.Background(), mkTrace(
		withRoot("GET /health", "api", tracepb.Span_SPAN_KIND_SERVER, nil),
	))
	if err := eng.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	eng.sweepPromotions(context.Background())
	for _, b := range r.baselines {
		if b.Promoted {
			t.Errorf("class %s promoted on stable_duration with empty baseline", b.Class)
		}
	}
}

// TestEngine_SweepColdPromotesEvictedTransaction promotes a transaction evicted
// from the in-memory store via the DB sweep path.
func TestEngine_SweepColdPromotesEvictedTransaction(t *testing.T) {
	r := newFakeRepo()
	policies := map[repo.DeviationClass]repo.LearningPolicy{}
	for _, class := range allDeviationClasses {
		policies[class] = repo.LearningPolicy{
			Class: class,
			Mode:  repo.LearningModeAny,
			Conditions: []repo.LearningCondition{
				{Type: repo.ConditionMinObservations, MinObservations: 1_000_000},
				{Type: repo.ConditionMinDuration, MinDurationMinutes: 60},
			},
		}
	}
	eng := New(r, nil, newLearningConfig(policies), nil)

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := t0
	eng.clock = func() time.Time { return now }

	tr := mkTrace(
		withRoot("cron.weekly-rollup", "billing", tracepb.Span_SPAN_KIND_INTERNAL,
			map[string]string{"faas.trigger": "timer"}),
		withChild("root", "db.rollup", tracepb.Span_SPAN_KIND_CLIENT, nil, "postgresql"),
	)
	eng.Process(context.Background(), tr)
	if err := eng.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	var txID int64
	if len(r.transactions) != 1 {
		t.Fatalf("precondition: got %d transactions, want 1", len(r.transactions))
	}
	txID = r.transactions[0].ID
	eng.store.removeEntry(txID)

	now = t0.Add(2 * time.Hour)
	eng.sweepPromotions(context.Background())
	for _, b := range r.baselines {
		if b.TransactionID == txID && !b.Promoted {
			t.Errorf("class %s not promoted by cold sweeper", b.Class)
		}
	}
}

// TestEngine_SweepRespectsAllMode proves min_duration alone cannot promote when
// match_mode=all and min_observations is not yet met.
func TestEngine_SweepRespectsAllMode(t *testing.T) {
	r := newFakeRepo()
	policies := map[repo.DeviationClass]repo.LearningPolicy{}
	for _, class := range allDeviationClasses {
		policies[class] = repo.LearningPolicy{
			Class: class,
			Mode:  repo.LearningModeAll,
			Conditions: []repo.LearningCondition{
				{Type: repo.ConditionMinObservations, MinObservations: 100},
				{Type: repo.ConditionMinDuration, MinDurationMinutes: 20},
			},
		}
	}
	eng := New(r, nil, newLearningConfig(policies), nil)

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	eng.clock = func() time.Time { return t0.Add(30 * time.Minute) }

	tr := mkTrace(
		withRoot("GET /health", "api", tracepb.Span_SPAN_KIND_SERVER, nil),
	)
	for i := 0; i < 3; i++ {
		eng.Process(context.Background(), tr)
	}
	if err := eng.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	eng.sweepPromotions(context.Background())
	for _, b := range r.baselines {
		if b.Promoted {
			t.Errorf("class %s promoted under mode=all with insufficient observations", b.Class)
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// -----------------------------------------------------------------------------
// Smoke test: real proto decode end-to-end.
// -----------------------------------------------------------------------------

func TestDecodeRequest_BuildsTrace(t *testing.T) {
	req := &tracepb.TracesData{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{kvStr("service.name", "demo")},
			},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Scope: &commonpb.InstrumentationScope{Name: "lib", Version: "1.0"},
				Spans: []*tracepb.Span{
					{
						TraceId: []byte{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
						SpanId:  []byte{1, 1, 1, 1, 1, 1, 1, 1},
						Name:    "root", Kind: tracepb.Span_SPAN_KIND_SERVER,
						StartTimeUnixNano: 0, EndTimeUnixNano: 1_000_000, // 1ms
					},
					{
						TraceId:      []byte{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
						SpanId:       []byte{2, 2, 2, 2, 2, 2, 2, 2},
						ParentSpanId: []byte{1, 1, 1, 1, 1, 1, 1, 1},
						Name:         "child", Kind: tracepb.Span_SPAN_KIND_CLIENT,
					},
				},
			}},
		}},
	}
	traces := decodeRequest(req)
	if len(traces) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(traces))
	}
	tr := traces[0]
	if tr.Root == nil || tr.Root.Name != "root" {
		t.Fatalf("expected root span 'root', got %+v", tr.Root)
	}
	if len(tr.Root.Children) != 1 || tr.Root.Children[0].Name != "child" {
		t.Errorf("expected one child 'child', got %+v", tr.Root.Children)
	}
	if tr.DurationUs != 1000 {
		t.Errorf("expected duration 1000us, got %d", tr.DurationUs)
	}
}

// TestDecodeRequest_EncodesOnlyWhenAsked guards the ingest path's cheapest
// saving: the engine scores every trace that arrives but stores only a handful
// per transaction, so a decoded trace should carry nothing but a pointer to its
// own spans and pay to encode them only if it turns out to be worth keeping.
// Encoding up front would change no behaviour at all - which is exactly why it
// needs a test - while costing about a tenth of the service's CPU.
func TestDecodeRequest_EncodesOnlyWhenAsked(t *testing.T) {
	tid := []byte{9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9}
	rootID := []byte{1, 1, 1, 1, 1, 1, 1, 1}
	req := &tracepb.TracesData{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{kvStr("service.name", "demo")}},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Scope: &commonpb.InstrumentationScope{Name: "lib", Version: "1.0"},
				Spans: []*tracepb.Span{
					{TraceId: tid, SpanId: rootID, Name: "POST /checkout", Kind: tracepb.Span_SPAN_KIND_SERVER},
					{TraceId: tid, SpanId: []byte{2, 2, 2, 2, 2, 2, 2, 2}, ParentSpanId: rootID,
						Name: "inventory.reserve", Kind: tracepb.Span_SPAN_KIND_CLIENT},
				},
			}},
		}},
	}

	traces := decodeRequest(req)
	if len(traces) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(traces))
	}
	tr := traces[0]
	if tr.rawOTLP != nil {
		t.Fatal("trace was encoded during decode; only RawOTLP should encode")
	}

	raw := tr.RawOTLP()
	if len(raw) == 0 {
		t.Fatal("RawOTLP returned nothing for a trace that has spans")
	}
	// Deferring must not change what a user ends up opening.
	reopened := decodeRequest(mustUnmarshalTraces(t, raw))
	if len(reopened) != 1 {
		t.Fatalf("stored bytes should reopen as 1 trace, got %d", len(reopened))
	}
	if root := reopened[0].Root; root == nil || root.Name != "POST /checkout" {
		t.Fatalf("stored bytes lost the root span, got %+v", root)
	}
	if len(reopened[0].Spans) != 2 {
		t.Errorf("stored bytes should carry both spans, got %d", len(reopened[0].Spans))
	}

	if second := tr.RawOTLP(); &second[0] != &raw[0] {
		t.Error("RawOTLP encoded a second time instead of reusing the first result")
	}
}

func kvStr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

// TestIdentifyTransaction_KindFromRoot covers the cross-product of OTel span
// kinds and rpc.system. The interesting case is row 3: Node.js gRPC client
// emits the root as kind=INTERNAL but still tags rpc.system=grpc, and we
// must classify that as GRPC instead of dumping it into INTERNAL.
func TestIdentifyTransaction_KindFromRoot(t *testing.T) {
	cases := []struct {
		name     string
		kind     tracepb.Span_SpanKind
		attrs    map[string]string
		wantKind repo.TransactionKind
	}{
		{"http server", tracepb.Span_SPAN_KIND_SERVER, nil, repo.KindHTTP},
		{"grpc server", tracepb.Span_SPAN_KIND_SERVER, map[string]string{"rpc.system": "grpc"}, repo.KindGRPC},
		{"grpc root with kind=internal (nodejs)", tracepb.Span_SPAN_KIND_INTERNAL, map[string]string{"rpc.system": "grpc"}, repo.KindGRPC},
		{"consumer", tracepb.Span_SPAN_KIND_CONSUMER, nil, repo.KindConsumer},
		{"producer", tracepb.Span_SPAN_KIND_PRODUCER, nil, repo.KindProducer},
		{"cron via faas.trigger=timer", tracepb.Span_SPAN_KIND_INTERNAL, map[string]string{"faas.trigger": "timer"}, repo.KindCron},
		{"plain internal", tracepb.Span_SPAN_KIND_INTERNAL, nil, repo.KindInternal},
		{"unspecified falls back to internal", tracepb.Span_SPAN_KIND_UNSPECIFIED, nil, repo.KindInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := identifyTransaction(&Trace{Root: &Span{
				Service:   "svc",
				Name:      "op",
				Kind:      tc.kind,
				SpanAttrs: tc.attrs,
			}}, nil)
			if !ok {
				t.Fatalf("identifyTransaction dropped a kind we expected to keep: %v", tc.kind)
			}
			if got.Kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", got.Kind, tc.wantKind)
			}
		})
	}
}

// TestIdentifyTransaction_DropsClientRoots covers both branches of
// discardableRoot:
//
//   - HTTP CLIENT root → DROPPED. A high-cardinality outbound URL (each ?id=N)
//     would otherwise mint its own INTERNAL transaction.
//   - RPC CLIENT root (rpc.system set) → KEPT and classified GRPC. Method names
//     are low-cardinality and a standalone RPC client is a legitimate trace
//     starter; dropping it would swallow client-only-instrumented gRPC traffic.
func TestIdentifyTransaction_DropsClientRoots(t *testing.T) {
	t.Run("http client root is dropped", func(t *testing.T) {
		_, ok := identifyTransaction(&Trace{Root: &Span{
			Service: "frontend",
			Name:    "GET http://shipping:8080/quote?id=21",
			Kind:    tracepb.Span_SPAN_KIND_CLIENT,
		}}, nil)
		if ok {
			t.Fatalf("expected HTTP CLIENT root span to be dropped, but identifyTransaction returned ok=true")
		}
	})

	t.Run("grpc client root is kept", func(t *testing.T) {
		got, ok := identifyTransaction(&Trace{Root: &Span{
			Service:   "cli-tool",
			Name:      "myapp.Greeter/SayHello",
			Kind:      tracepb.Span_SPAN_KIND_CLIENT,
			SpanAttrs: map[string]string{"rpc.system": "grpc"},
		}}, nil)
		if !ok {
			t.Fatalf("expected gRPC CLIENT root span to be kept, but identifyTransaction dropped it")
		}
		if got.Kind != repo.KindGRPC {
			t.Errorf("kind = %q, want %q", got.Kind, repo.KindGRPC)
		}
	})
}

// TestDecodeRequest_StripsNULBytes guards against a regression we hit in
// production: the Odigos eBPF Java agent reports `process.command` as
// NUL-separated argv ("java\x00-jar\x00/app/foo.jar\x00"). Postgres jsonb
// rejects \u0000 in strings (SQLSTATE 22P05), so we strip NULs at the
// decode boundary. Any non-empty result here means the regression is back.
func TestDecodeRequest_StripsNULBytes(t *testing.T) {
	req := &tracepb.TracesData{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{
					kvStr("service.name", "demo"),
					kvStr("process.command", "java\x00-jar\x00/app/foo.jar\x00"),
				},
			},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					TraceId: []byte{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
					SpanId:  []byte{1, 1, 1, 1, 1, 1, 1, 1},
					Name:    "root", Kind: tracepb.Span_SPAN_KIND_SERVER,
				}},
			}},
		}},
	}
	traces := decodeRequest(req)
	if len(traces) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(traces))
	}
	got := traces[0].Root.Attr("process.command")
	if got == "" {
		t.Fatalf("expected process.command to be present after stripping NULs, got empty")
	}
	if strings.ContainsRune(got, 0) {
		t.Fatalf("process.command still contains NUL: %q", got)
	}
	if want := "java-jar/app/foo.jar"; got != want {
		t.Errorf("process.command = %q, want %q", got, want)
	}
}

// -----------------------------------------------------------------------------
// fakeRepo - in-memory Repository for engine tests.
// -----------------------------------------------------------------------------

type fakeRepo struct {
	mu                       sync.Mutex
	transactions             []repo.Transaction
	baselines                []repo.BaselineClass
	observations             []repo.Observation
	policies                 []repo.Policy
	anomalies                []repo.AnomalyOccurrence // recorded via RecordAnomalyOccurrences (post-Flush)
	resolutions              []anomalyResolution      // recorded via ResolveAnomalyIssue
	learningPolicies         []repo.LearningPolicy
	guardrails               []repo.ServiceGuardrail
	guardrailHits            []repo.GuardrailViolationHit   // recorded via RecordGuardrailViolations
	profiles                 map[string]repo.ServiceProfile // "namespace/service" -> profile
	activity                 map[int64]int64                // transaction_id -> recorded volume
	serviceGraphEdges        []repo.ServiceGraphEdge        // recorded via UpsertServiceGraphEdges
	systemSettings           *repo.SystemSettings
	observationRetentionDays int

	// instrumentation for the write-behind tests.
	applyCalls       int   // number of ApplyBaselineDeltas invocations
	appliedDeltas    int   // total deltas successfully applied
	applyErr         error // when set, ApplyBaselineDeltas fails without applying
	getBaselineCalls int   // number of GetBaselineForTransaction invocations

	insertObservationErr  error // when set, InsertObservations fails without storing
	countObservationCalls int   // number of CountObservationsByReason invocations
	listStatsCalls        int   // number of ListTransactionStats invocations

	recommendations map[string]repo.Recommendation
	liveDeltas      []repo.RecommendationLiveDelta
	miningRuns      map[int64]repo.RecommendationMiningRun
}

// allDeviationClasses is the built-in class set, used by tests to build a
// policy snapshot covering every class.
var allDeviationClasses = repo.AllDeviationClasses

// countOnlyConfig builds a LearningConfig where every built-in class promotes
// purely on observation count (no time knob). Used by integration tests that
// want deterministic promotion after N traces under the real wall clock.
func countOnlyConfig(min int64) *LearningConfig {
	policies := map[repo.DeviationClass]repo.LearningPolicy{}
	for _, class := range allDeviationClasses {
		policies[class] = repo.LearningPolicy{
			Class:      class,
			Mode:       repo.LearningModeAny,
			Conditions: []repo.LearningCondition{{Type: repo.ConditionMinObservations, MinObservations: min}},
		}
	}
	return newLearningConfig(policies)
}

// alertConfigFrom builds an AlertConfig from a set of policies, indexing each by
// its scope via the shared repo.ScopeIndex - the same path the engine uses at
// runtime. Used by integration tests that assert firing behaviour.
func alertConfigFrom(policies ...repo.Policy) *AlertConfig {
	idx := &repo.ScopeIndex[repo.Policy]{}
	for _, p := range policies {
		idx.Put(p.Scope, p.ScopeKey, p)
	}
	return newAlertConfig(idx)
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{}
}

func (f *fakeRepo) UpsertTransaction(_ context.Context, service, namespace, operation string, kind repo.TransactionKind, sourceIdentity map[string]string) (repo.Transaction, error) {
	tx := repo.NewTransaction(service, namespace, operation, kind, sourceIdentity)
	if err := f.UpsertTransactions(context.Background(), []repo.Transaction{tx}); err != nil {
		return repo.Transaction{}, err
	}
	return tx, nil
}

func (f *fakeRepo) UpsertTransactions(_ context.Context, txs []repo.Transaction) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, tx := range txs {
		found := false
		for i, existing := range f.transactions {
			if existing.ID == tx.ID {
				f.transactions[i] = tx
				found = true
				break
			}
		}
		if !found {
			f.transactions = append(f.transactions, tx)
		}
	}
	return nil
}

func (f *fakeRepo) ListTransactions(_ context.Context, _ int) ([]repo.Transaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]repo.Transaction, len(f.transactions))
	copy(out, f.transactions)
	return out, nil
}

func (f *fakeRepo) ListTransactionRefs(_ context.Context) ([]repo.TransactionRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]repo.TransactionRef, 0, len(f.transactions))
	for _, tx := range f.transactions {
		out = append(out, repo.TransactionRef{ID: tx.ID, Namespace: tx.Namespace, Service: tx.Service})
	}
	return out, nil
}

func (f *fakeRepo) GetTransactionByID(_ context.Context, transactionID int64) (repo.Transaction, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, tx := range f.transactions {
		if tx.ID == transactionID {
			return tx, true, nil
		}
	}
	return repo.Transaction{}, false, nil
}

// RecordActivity accumulates per-transaction volume so write-behind tests can
// assert the flusher drained activity alongside baseline deltas.
func (f *fakeRepo) RecordActivity(_ context.Context, deltas []repo.ActivityDelta) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.activity == nil {
		f.activity = map[int64]int64{}
	}
	for _, d := range deltas {
		f.activity[d.TransactionID] += d.Volume
	}
	return nil
}

func (f *fakeRepo) ListServiceStats(_ context.Context, _ int) ([]repo.ServiceStat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	byKey := map[[2]string]*repo.ServiceStat{}
	var order [][2]string
	for _, tx := range f.transactions {
		k := [2]string{tx.Namespace, tx.Service}
		s := byKey[k]
		if s == nil {
			s = &repo.ServiceStat{Namespace: tx.Namespace, Service: tx.Service}
			byKey[k] = s
			order = append(order, k)
		}
		s.TransactionCount++
		s.Volume += f.activity[tx.ID]
	}
	out := make([]repo.ServiceStat, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	return out, nil
}

func (f *fakeRepo) ListServiceNames(_ context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[string]struct{}{}
	var out []string
	for _, tx := range f.transactions {
		if tx.Service == "" {
			continue
		}
		if _, ok := seen[tx.Service]; ok {
			continue
		}
		seen[tx.Service] = struct{}{}
		out = append(out, tx.Service)
	}
	return out, nil
}

func (f *fakeRepo) ListTransactionStats(_ context.Context, params repo.TransactionListParams) ([]repo.TransactionStat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listStatsCalls++
	var out []repo.TransactionStat
	for _, tx := range f.transactions {
		if params.Namespace != "" && params.Service != "" && (tx.Namespace != params.Namespace || tx.Service != params.Service) {
			continue
		}
		if params.Kind != "" && string(tx.Kind) != params.Kind {
			continue
		}
		total, promoted := 0, 0
		hasBaseline := false
		for _, b := range f.baselines {
			if b.TransactionID != tx.ID {
				continue
			}
			hasBaseline = true
			total++
			if b.Settled() {
				promoted++
			}
		}
		stat := repo.TransactionStat{
			Transaction: tx,
			Volume:      f.activity[tx.ID],
			HasBaseline: hasBaseline,
			Promoted:    hasBaseline && total > 0 && promoted == total,
		}
		for _, b := range f.baselines {
			if b.TransactionID == tx.ID && b.Saturated {
				stat.SaturatedClasses = append(stat.SaturatedClasses, b.Class)
			}
		}
		out = append(out, stat)
	}
	return out, nil
}

// DeleteTransaction mirrors the Postgres repo: findings and stored traces go
// with the transaction, not just its identity and baselines.
func (f *fakeRepo) DeleteTransaction(ctx context.Context, transactionID int64) error {
	if err := f.DeleteAnomalyIssues(ctx, transactionID); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.transactions[:0]
	for _, tx := range f.transactions {
		if tx.ID != transactionID {
			kept = append(kept, tx)
		}
	}
	f.transactions = kept
	delete(f.activity, transactionID)
	keptB := f.baselines[:0]
	for _, b := range f.baselines {
		if b.TransactionID != transactionID {
			keptB = append(keptB, b)
		}
	}
	f.baselines = keptB
	keptO := f.observations[:0]
	for _, o := range f.observations {
		if o.TransactionID != transactionID {
			keptO = append(keptO, o)
		}
	}
	f.observations = keptO
	return nil
}

func (f *fakeRepo) DeleteAllTransactions(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transactions = nil
	f.activity = map[int64]int64{}
	f.baselines = nil
	return nil
}

func (f *fakeRepo) GetBaselineForTransaction(_ context.Context, transactionID int64) ([]repo.BaselineClass, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getBaselineCalls++
	var out []repo.BaselineClass
	for _, b := range f.baselines {
		if b.TransactionID == transactionID {
			out = append(out, b)
		}
	}
	return out, nil
}

func (f *fakeRepo) ListUnpromotedBaselines(_ context.Context, filter repo.UnpromotedBaselineFilter) ([]repo.UnpromotedBaselineCandidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []repo.UnpromotedBaselineCandidate
	for _, b := range f.baselines {
		if b.Settled() {
			continue
		}
		timeMatch := false
		if filter.MinDurationEnabled && !b.LearningStartedAt.After(filter.MinDurationCutoff) {
			timeMatch = true
		}
		if filter.StableDurationEnabled && b.ObservationCountAtLastChange > 0 && !b.LastChangedAt.After(filter.StableDurationCutoff) {
			timeMatch = true
		}
		if !timeMatch {
			continue
		}
		out = append(out, repo.UnpromotedBaselineCandidate{
			TransactionID:                b.TransactionID,
			Class:                        b.Class,
			ObservationCount:             b.ObservationCount,
			LearningStartedAt:            b.LearningStartedAt,
			LastChangedAt:                b.LastChangedAt,
			ObservationCountAtLastChange: b.ObservationCountAtLastChange,
		})
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	return out, nil
}

func (f *fakeRepo) UpsertBaselineClass(_ context.Context, bc repo.BaselineClass) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, existing := range f.baselines {
		if existing.TransactionID == bc.TransactionID && existing.Class == bc.Class {
			f.baselines[i] = bc
			return nil
		}
	}
	if len(bc.Data) == 0 {
		bc.Data = json.RawMessage(`{}`)
	}
	f.baselines = append(f.baselines, bc)
	return nil
}

func (f *fakeRepo) PromoteBaselineClass(_ context.Context, transactionID int64, class repo.DeviationClass) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, b := range f.baselines {
		if b.TransactionID == transactionID && b.Class == class {
			f.baselines[i].Promoted = true
			return nil
		}
	}
	return errors.New("not found")
}

func (f *fakeRepo) ResetBaselineClass(_ context.Context, transactionID int64, class repo.DeviationClass) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.baselines[:0]
	for _, b := range f.baselines {
		if b.TransactionID == transactionID && b.Class == class {
			continue
		}
		kept = append(kept, b)
	}
	f.baselines = kept
	return nil
}

func (f *fakeRepo) ResetTransactionBaselines(_ context.Context, transactionID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.baselines[:0]
	for _, b := range f.baselines {
		if b.TransactionID == transactionID {
			continue
		}
		kept = append(kept, b)
	}
	f.baselines = kept
	return nil
}

// ApplyBaselineDeltas mirrors the postgres backend's additive semantics in
// memory: existing rows merge data + add count + monotonic promote, missing
// rows are seeded. Returns the post-merge rows for adopt-back.
func (f *fakeRepo) ApplyBaselineDeltas(_ context.Context, deltas []repo.BaselineDelta, merge repo.BaselineMerger) ([]repo.BaselineClass, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applyCalls++
	if f.applyErr != nil {
		return nil, f.applyErr
	}
	out := make([]repo.BaselineClass, 0, len(deltas))
	for _, d := range deltas {
		f.appliedDeltas++
		idx := -1
		for i, b := range f.baselines {
			if b.TransactionID == d.TransactionID && b.Class == d.Class {
				idx = i
				break
			}
		}
		if idx >= 0 {
			b := f.baselines[idx]
			if len(d.Contribution) > 0 && merge != nil {
				m, err := merge.MergeData(d.Class, b.Data, d.Contribution)
				if err != nil {
					return nil, err
				}
				b.Data = m
			}
			b.ObservationCount += d.CountDelta
			if d.Promote {
				b.Promoted = true
			}
			if d.Saturate {
				b.Saturated = true
			}
			if d.LastChangedAt.After(b.LastChangedAt) {
				b.LastChangedAt = d.LastChangedAt
			}
			if d.ObservationCountAtLastChange > b.ObservationCountAtLastChange {
				b.ObservationCountAtLastChange = d.ObservationCountAtLastChange
			}
			f.baselines[idx] = b
			out = append(out, b)
			continue
		}
		data := d.Contribution
		if len(data) == 0 {
			data = json.RawMessage(`{}`)
		}
		schema := d.DataSchemaVersion
		if schema == 0 {
			schema = 1
		}
		started := d.LearningStartedAt
		if started.IsZero() {
			started = time.Now()
		}
		b := repo.BaselineClass{
			TransactionID:                d.TransactionID,
			Class:                        d.Class,
			Data:                         data,
			DataSchemaVersion:            schema,
			ObservationCount:             d.CountDelta,
			Promoted:                     d.Promote,
			Saturated:                    d.Saturate,
			LearningStartedAt:            started,
			LastChangedAt:                d.LastChangedAt,
			ObservationCountAtLastChange: d.ObservationCountAtLastChange,
		}
		f.baselines = append(f.baselines, b)
		out = append(out, b)
	}
	return out, nil
}

func (f *fakeRepo) InsertObservations(_ context.Context, obs []repo.Observation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.insertObservationErr != nil {
		return f.insertObservationErr
	}
	f.observations = append(f.observations, obs...)
	return nil
}

func (f *fakeRepo) ListObservationsByTransaction(_ context.Context, transactionID int64, reason string, _ int) ([]repo.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []repo.Observation
	for _, o := range f.observations {
		if o.TransactionID == transactionID && (reason == "" || o.SampleReason == reason) {
			out = append(out, o)
		}
	}
	return out, nil
}

func (f *fakeRepo) CountObservationsByReason(_ context.Context, transactionID int64) (map[string]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.countObservationCalls++
	out := map[string]int{}
	seen := map[string]struct{}{}
	for _, o := range f.observations {
		if o.TransactionID != transactionID {
			continue
		}
		key := o.SampleReason + "\x00" + o.TraceID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out[o.SampleReason]++
	}
	return out, nil
}

func (f *fakeRepo) ListObservationsWithRaw(ctx context.Context, transactionID int64, reason string, limit int) ([]repo.Observation, error) {
	out, err := f.ListObservationsByTransaction(ctx, transactionID, reason, limit)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func (f *fakeRepo) UpsertRecommendations(_ context.Context, recs []repo.Recommendation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recommendations == nil {
		f.recommendations = map[string]repo.Recommendation{}
	}
	for _, rec := range recs {
		f.recommendations[rec.ID] = rec
	}
	return nil
}

func (f *fakeRepo) ListRecommendations(_ context.Context, filter repo.RecommendationFilter) ([]repo.Recommendation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []repo.Recommendation
	for _, rec := range f.recommendations {
		if !filter.IncludeStale && rec.Stale {
			continue
		}
		if filter.Kind != "" && rec.Kind != filter.Kind {
			continue
		}
		if filter.State != "" && rec.State != filter.State {
			continue
		}
		if filter.TransactionID != 0 && rec.TransactionID != filter.TransactionID {
			continue
		}
		if scope, _ := repo.NormalizeRecommendationScope(rec.Scope, rec.ScopeKey, rec.TransactionID); filter.Scope != "" && scope != filter.Scope {
			continue
		}
		if rec.TransactionID != 0 {
			for _, tx := range f.transactions {
				if tx.ID == rec.TransactionID {
					rec.Service, rec.Namespace, rec.Operation, rec.TxKind = tx.Service, tx.Namespace, tx.Operation, tx.Kind
				}
			}
		}
		if filter.Namespace != "" && rec.Namespace != filter.Namespace {
			continue
		}
		if filter.Service != "" && rec.Service != filter.Service {
			continue
		}
		rec.Rules = append([]repo.RecommendationRule(nil), rec.Rules...)
		for _, d := range f.liveDeltas {
			if d.ID != rec.ID {
				continue
			}
			if d.Rule == "" {
				rec.LiveHeld += d.Held
				rec.LiveBroken += d.Broken
				continue
			}
			for i := range rec.Rules {
				if rec.Rules[i].Rule == d.Rule {
					rec.Rules[i].LiveChecked += d.Held + d.Broken
					rec.Rules[i].LiveViolated += d.Broken
				}
			}
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TransactionID != out[j].TransactionID {
			return out[i].TransactionID < out[j].TransactionID
		}
		return out[i].Rank < out[j].Rank
	})
	return out, nil
}

func (f *fakeRepo) GetRecommendation(ctx context.Context, id string) (repo.Recommendation, bool, error) {
	all, err := f.ListRecommendations(ctx, repo.RecommendationFilter{IncludeStale: true})
	if err != nil {
		return repo.Recommendation{}, false, err
	}
	for _, rec := range all {
		if rec.ID == id {
			return rec, true, nil
		}
	}
	return repo.Recommendation{}, false, nil
}

func (f *fakeRepo) SetRecommendationState(_ context.Context, id string, state repo.RecommendationState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.recommendations[id]
	if !ok {
		return fmt.Errorf("recommendation %s not found", id)
	}
	rec.State = state
	f.recommendations[id] = rec
	return nil
}

func (f *fakeRepo) AddRecommendationLiveCounts(_ context.Context, deltas []repo.RecommendationLiveDelta) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.liveDeltas = append(f.liveDeltas, deltas...)
	return nil
}

func (f *fakeRepo) DeleteRecommendations(_ context.Context, transactionID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, rec := range f.recommendations {
		if transactionID == 0 || rec.TransactionID == transactionID {
			delete(f.recommendations, id)
		}
	}
	if transactionID == 0 {
		f.miningRuns = nil
	} else {
		delete(f.miningRuns, transactionID)
	}
	return nil
}

func (f *fakeRepo) ListRecommendationMiningCandidates(_ context.Context, _ int, _ int) ([]repo.RecommendationMiningCandidate, error) {
	return nil, nil
}

func (f *fakeRepo) RecordRecommendationMiningRun(_ context.Context, run repo.RecommendationMiningRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.miningRuns == nil {
		f.miningRuns = map[int64]repo.RecommendationMiningRun{}
	}
	f.miningRuns[run.TransactionID] = run
	return nil
}

func (f *fakeRepo) GetObservationByTrace(_ context.Context, transactionID int64, traceID string) (repo.Observation, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range f.observations {
		if o.TransactionID == transactionID && o.TraceID == traceID {
			return o, true, nil
		}
	}
	return repo.Observation{}, false, nil
}

func (f *fakeRepo) GetLatestObservationByReason(_ context.Context, sampleReason string) (repo.Observation, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var (
		best  repo.Observation
		found bool
	)
	for _, o := range f.observations {
		if o.SampleReason == sampleReason && (!found || o.ObservedAt.After(best.ObservedAt)) {
			best, found = o, true
		}
	}
	return best, found, nil
}

func (f *fakeRepo) ListEnabledPolicies(_ context.Context) ([]repo.Policy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []repo.Policy
	for _, p := range f.policies {
		if p.Enabled {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeRepo) ListAlertPolicies(_ context.Context) ([]repo.Policy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]repo.Policy, len(f.policies))
	copy(out, f.policies)
	return out, nil
}

func (f *fakeRepo) UpsertAlertPolicy(_ context.Context, p repo.Policy) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	scope := repo.NormalizePolicyScope(p.Scope)
	for i := range f.policies {
		if repo.NormalizePolicyScope(f.policies[i].Scope) == scope && f.policies[i].ScopeKey == p.ScopeKey {
			f.policies[i] = p
			return nil
		}
	}
	f.policies = append(f.policies, p)
	return nil
}

func (f *fakeRepo) DeleteAlertPolicy(_ context.Context, scope repo.PolicyScope, scopeKey string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	scope = repo.NormalizePolicyScope(scope)
	out := f.policies[:0]
	for _, p := range f.policies {
		if repo.NormalizePolicyScope(p.Scope) == scope && p.ScopeKey == scopeKey {
			continue
		}
		out = append(out, p)
	}
	f.policies = out
	return nil
}

type anomalyResolution struct {
	transactionID int64
	signature     string
	resolution    repo.AnomalyResolution
}

func (f *fakeRepo) RecordAnomalyOccurrences(_ context.Context, occs []repo.AnomalyOccurrence) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.anomalies = append(f.anomalies, occs...)
	return nil
}

// aggregateIssues folds recorded occurrences into per-(txn, signature) issues,
// mirroring the upsert merge the Postgres repo does.
func (f *fakeRepo) aggregateIssues() map[string]*repo.AnomalyIssue {
	txByID := map[int64]repo.Transaction{}
	for _, tx := range f.transactions {
		txByID[tx.ID] = tx
	}
	issues := map[string]*repo.AnomalyIssue{}
	for _, o := range f.anomalies {
		key := fmt.Sprintf("%d\x00%s", o.TransactionID, o.Signature)
		g := issues[key]
		if g == nil {
			tx := txByID[o.TransactionID]
			g = &repo.AnomalyIssue{
				TransactionID:    o.TransactionID,
				Signature:        o.Signature,
				Service:          tx.Service,
				Namespace:        tx.Namespace,
				Operation:        tx.Operation,
				Kind:             tx.Kind,
				TriggeredClasses: o.TriggeredClasses,
				Offending:        o.Offending,
				Evidence:         o.Evidence,
				PolicyID:         o.PolicyID,
				FirstSeen:        o.FirstSeen,
				LastSeen:         o.LastSeen,
				Status:           repo.AnomalyIssueOpen,
			}
			issues[key] = g
		}
		g.Occurrences += o.Occurrences
		if o.MaxScore > g.MaxScore {
			g.MaxScore = o.MaxScore
		}
		if o.LastSeen.After(g.LastSeen) {
			g.LastSeen = o.LastSeen
			g.LastTraceID = o.LastTraceID
		}
	}
	for _, res := range f.resolutions {
		key := fmt.Sprintf("%d\x00%s", res.transactionID, res.signature)
		if g := issues[key]; g != nil {
			switch res.resolution {
			case repo.AnomalyResolveAccept:
				g.Status = repo.AnomalyIssueAccepted
			case repo.AnomalyResolveDismiss:
				g.Status = repo.AnomalyIssueDismissed
			case repo.AnomalyResolveReopen:
				g.Status = repo.AnomalyIssueOpen
			}
		}
	}
	return issues
}

func (f *fakeRepo) ListAnomalyIssues(_ context.Context, filter repo.FindingListFilter) ([]repo.AnomalyIssue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	issues := f.aggregateIssues()
	out := make([]repo.AnomalyIssue, 0, len(issues))
	for _, g := range issues {
		if g.LastSeen.Before(filter.Since) {
			continue
		}
		if filter.Service != "" && g.Service != filter.Service {
			continue
		}
		if filter.Namespace != "" && g.Namespace != filter.Namespace {
			continue
		}
		if filter.Status != "" && string(g.Status) != filter.Status {
			continue
		}
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Occurrences > out[j].Occurrences })
	return pageSlice(out, filter.Limit, 0), nil
}

func (f *fakeRepo) ListAnomalyIssuesByTransaction(_ context.Context, transactionID int64, filter repo.FindingListFilter) ([]repo.AnomalyIssue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]repo.AnomalyIssue, 0)
	for _, g := range f.aggregateIssues() {
		if g.TransactionID != transactionID || g.LastSeen.Before(filter.Since) {
			continue
		}
		if filter.Status != "" && string(g.Status) != filter.Status {
			continue
		}
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Occurrences > out[j].Occurrences })
	return pageSlice(out, filter.Limit, 0), nil
}

func (f *fakeRepo) GetAnomalyIssue(_ context.Context, transactionID int64, signature string) (repo.AnomalyIssue, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := fmt.Sprintf("%d\x00%s", transactionID, signature)
	if g := f.aggregateIssues()[key]; g != nil {
		return *g, true, nil
	}
	return repo.AnomalyIssue{}, false, nil
}

func (f *fakeRepo) ResolveAnomalyIssue(_ context.Context, transactionID int64, signature string, resolution repo.AnomalyResolution) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolutions = append(f.resolutions, anomalyResolution{transactionID, signature, resolution})
	return nil
}

func (f *fakeRepo) DeleteAnomalyIssues(_ context.Context, transactionID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.anomalies[:0]
	for _, o := range f.anomalies {
		if o.TransactionID == transactionID {
			continue
		}
		kept = append(kept, o)
	}
	f.anomalies = kept
	return nil
}

func (f *fakeRepo) GetSystemSettings(_ context.Context) (repo.SystemSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.systemSettings != nil {
		return *f.systemSettings, nil
	}
	return repo.DefaultSystemSettings(), nil
}

func (f *fakeRepo) UpsertSystemSettings(_ context.Context, s repo.SystemSettings) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := s.WithDefaults()
	f.systemSettings = &cp
	return nil
}

func (f *fakeRepo) SetObservationRetentionDays(_ context.Context, days int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observationRetentionDays = days
	return nil
}

func (f *fakeRepo) RecordGuardrailViolations(_ context.Context, hits []repo.GuardrailViolationHit) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.guardrailHits = append(f.guardrailHits, hits...)
	return nil
}

func (f *fakeRepo) ListGuardrailViolations(_ context.Context, _ repo.FindingListFilter) ([]repo.GuardrailViolationGroup, error) {
	return nil, nil
}

func (f *fakeRepo) GetGuardrailViolation(_ context.Context, _ string, _ repo.RuleKey, _ string) (repo.GuardrailViolationGroup, bool, error) {
	return repo.GuardrailViolationGroup{}, false, nil
}

func (f *fakeRepo) ResolveGuardrailViolation(_ context.Context, _ repo.PolicyScope, _ string, _ repo.RuleKey, _ string, _ repo.GuardrailResolution) error {
	return nil
}

func (f *fakeRepo) DeleteGuardrailViolationsForRule(_ context.Context, _ repo.RuleKey) error {
	return nil
}

// pageSlice applies limit/offset to a slice the way the SQL repos do, so the
// fake matches paginated callers. limit<=0 means "no limit".
func pageSlice[T any](in []T, limit, offset int) []T {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(in) {
		return nil
	}
	in = in[offset:]
	if limit > 0 && limit < len(in) {
		in = in[:limit]
	}
	return in
}

func (f *fakeRepo) ListLearningPolicies(_ context.Context) ([]repo.LearningPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]repo.LearningPolicy, len(f.learningPolicies))
	copy(out, f.learningPolicies)
	return out, nil
}

func (f *fakeRepo) UpsertLearningPolicy(_ context.Context, p repo.LearningPolicy) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, ex := range f.learningPolicies {
		if ex.Class == p.Class && ex.Scope == p.Scope && ex.ScopeKey == p.ScopeKey {
			f.learningPolicies[i] = p
			return nil
		}
	}
	f.learningPolicies = append(f.learningPolicies, p)
	return nil
}

func (f *fakeRepo) DeleteLearningPolicy(_ context.Context, class repo.DeviationClass, scope repo.PolicyScope, scopeKey string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.learningPolicies[:0]
	for _, ex := range f.learningPolicies {
		if ex.Class == class && ex.Scope == scope && ex.ScopeKey == scopeKey {
			continue
		}
		kept = append(kept, ex)
	}
	f.learningPolicies = kept
	return nil
}

func (f *fakeRepo) ListServiceGuardrails(_ context.Context) ([]repo.ServiceGuardrail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]repo.ServiceGuardrail(nil), f.guardrails...), nil
}

func (f *fakeRepo) UpsertServiceGuardrail(_ context.Context, g repo.ServiceGuardrail) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, existing := range f.guardrails {
		if existing.Scope == g.Scope && existing.ScopeKey == g.ScopeKey {
			f.guardrails[i] = g
			return nil
		}
	}
	f.guardrails = append(f.guardrails, g)
	return nil
}

func (f *fakeRepo) DeleteServiceGuardrail(_ context.Context, scope repo.PolicyScope, scopeKey string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.guardrails[:0]
	for _, g := range f.guardrails {
		if g.Scope == scope && g.ScopeKey == scopeKey {
			continue
		}
		out = append(out, g)
	}
	f.guardrails = out
	return nil
}

func (f *fakeRepo) GetServiceProfile(_ context.Context, namespace, service string) (repo.ServiceProfile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.profiles[namespace+"/"+service]; ok {
		return p, nil
	}
	return repo.ServiceProfile{Namespace: namespace, Service: service}, nil
}

func (f *fakeRepo) GetStorageHealth(_ context.Context) (repo.StorageHealth, error) {
	return repo.StorageHealth{Status: repo.StorageStatusHealthy, Connection: repo.StorageConnection{Reachable: true}}, nil
}
func (f *fakeRepo) UpsertServiceGraphEdges(_ context.Context, edges []repo.ServiceGraphEdge) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.serviceGraphEdges = append(f.serviceGraphEdges, edges...)
	return nil
}

func (f *fakeRepo) QueryBlastRadius(_ context.Context, namespace, service string, _ int) (repo.BlastRadiusSubgraph, error) {
	return repo.BlastRadiusSubgraph{Root: repo.BlastRadiusNode{Namespace: namespace, Service: service}}, nil
}

func (f *fakeRepo) ListIntegrations(_ context.Context) ([]repo.Integration, error) { return nil, nil }
func (f *fakeRepo) GetIntegration(_ context.Context, _ string) (repo.Integration, bool, error) {
	return repo.Integration{}, false, nil
}
func (f *fakeRepo) UpsertIntegration(_ context.Context, _ repo.Integration) error { return nil }
func (f *fakeRepo) DeleteIntegration(_ context.Context, _ string) error           { return nil }
func (f *fakeRepo) ListNotificationDispatch(_ context.Context, _ repo.DispatchListFilter) ([]repo.NotificationDispatch, error) {
	return nil, nil
}
func (f *fakeRepo) UpsertNotificationDispatch(_ context.Context, _ []repo.NotificationDispatch) error {
	return nil
}
func (f *fakeRepo) ResetNotificationDispatch(_ context.Context, _ string, _ []string) error {
	return nil
}
func (f *fakeRepo) RecordNotificationAttempts(_ context.Context, _ []repo.NotificationAttempt) error {
	return nil
}
func (f *fakeRepo) ListNotificationAttempts(_ context.Context, _ string, _ time.Time, _ int) ([]repo.NotificationAttempt, error) {
	return nil, nil
}
func (f *fakeRepo) ListIntegrationHealth(_ context.Context) (map[string]repo.IntegrationHealth, error) {
	return nil, nil
}
func (f *fakeRepo) GetNotificationCursor(_ context.Context, _ string) (repo.NotificationCursor, bool, error) {
	return repo.NotificationCursor{}, false, nil
}
func (f *fakeRepo) SetNotificationCursor(_ context.Context, _ repo.NotificationCursor) error {
	return nil
}

func (f *fakeRepo) Ping(_ context.Context) error { return nil }
func (f *fakeRepo) Close() error                 { return nil }
