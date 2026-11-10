package engine

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/odigos-io/odigos/insights/internal/engine/semconv"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

// relationTrace is one learning-phase trace of the demo's "POST /accounts"
// transaction: the gateway resolves the caller to "client-<id>", the account
// service is asked for account <id> (also as a URL), a notification is
// published for the same id, and an audit span carries an unrelated id on odd
// traces. region is constant and http.route is transport plumbing; neither may
// become a rule.
func relationTrace(i int, id string) *Trace {
	tid := fmt.Sprintf("trace-%02d", i)
	root := &Span{
		SpanID: tid + "-root", Service: "edge-gateway", Name: "POST /accounts", Kind: tracepb.Span_SPAN_KIND_SERVER,
		SpanAttrs: map[string]string{semconv.HTTPRoute: "/accounts", "region": "eu"},
	}
	resolve := &Span{
		SpanID: tid + "-resolve", ParentSpanID: root.SpanID, Service: "edge-gateway", Name: "resolvePrincipal", Kind: tracepb.Span_SPAN_KIND_INTERNAL,
		SpanAttrs: map[string]string{semconv.ReturnValue: "client-" + id},
	}
	fetch := &Span{
		SpanID: tid + "-fetch", ParentSpanID: root.SpanID, Service: "edge-gateway", Name: "GET", Kind: tracepb.Span_SPAN_KIND_CLIENT,
		SpanAttrs: map[string]string{semconv.URLFull: fmt.Sprintf("http://accounts/api/accounts/%s?rid=r%d", id, 500+i*7)},
	}
	getAccount := &Span{
		SpanID: tid + "-get", ParentSpanID: fetch.SpanID, Service: "account-service", Name: "getAccount", Kind: tracepb.Span_SPAN_KIND_INTERNAL,
		SpanAttrs: map[string]string{semconv.ArgKey(0): id},
	}
	publish := &Span{
		SpanID: tid + "-pub", ParentSpanID: getAccount.SpanID, Service: "notification-api", Name: "publish", Kind: tracepb.Span_SPAN_KIND_INTERNAL,
		SpanAttrs: map[string]string{semconv.ArgKey(0): id},
	}
	auditID := id
	if i%2 == 1 {
		auditID = fmt.Sprintf("%d", 9000+i)
	}
	audit := &Span{
		SpanID: tid + "-audit", ParentSpanID: root.SpanID, Service: "edge-gateway", Name: "audit", Kind: tracepb.Span_SPAN_KIND_INTERNAL,
		SpanAttrs: map[string]string{semconv.ArgKey(0): auditID},
	}
	return &Trace{TraceID: tid, Root: root, Spans: []*Span{root, resolve, fetch, getAccount, publish, audit}}
}

func relationSamples(n int) []relationSample {
	out := make([]relationSample, 0, n)
	for i := 0; i < n; i++ {
		tr := relationTrace(i, fmt.Sprintf("%d", 1001+i))
		out = append(out, relationSample{full: tr, sub: tr, observedAt: time.Unix(int64(1_700_000_000+i), 0)})
	}
	return out
}

func testMiningConfig() relationMiningConfig {
	return relationMiningConfig{minDistinct: 5, minHoldPct: 98}
}

// Span names of the chain containing the position; nil when it chains with nothing.
func chainSpans(samples []relationSample, cfg relationMiningConfig, span, attr string) []string {
	columns := dedupeSpanColumns(collectAttrColumns(samples), cfg)
	for _, chain := range chainColumns(columns, cfg) {
		var spans []string
		hit := false
		for _, c := range chain {
			spans = append(spans, c.pos.Span)
			hit = hit || (c.pos.Span == span && c.pos.Attr == attr)
		}
		if hit {
			return spans
		}
	}
	return nil
}

func findCandidate(cands []relationCandidate, leftSpan, leftAttr, rightSpan, rightAttr string) (relationCandidate, bool) {
	for _, c := range cands {
		if c.spec.Left.Span == leftSpan && c.spec.Left.Attr == leftAttr && c.spec.Right.Span == rightSpan && c.spec.Right.Attr == rightAttr {
			return c, true
		}
	}
	return relationCandidate{}, false
}

func TestMineRelations_FindsExactCoreAndContainment(t *testing.T) {
	cands := mineRelations(relationSamples(12), testMiningConfig())
	if len(cands) == 0 {
		t.Fatal("no candidates mined")
	}

	// The BOLA rule: the gateway's principal ("client-1001") is the account the
	// backend reads ("1001") once the constant prefix is stripped.
	core, ok := findCandidate(cands, "resolvePrincipal", semconv.ReturnValue, "getAccount", semconv.ArgKey(0))
	if !ok {
		t.Fatalf("missing resolvePrincipal/return.value -> getAccount/arg.0; got %+v", specsOf(cands))
	}
	if core.transform != relationTransformCore {
		t.Fatalf("transform = %q, want core", core.transform)
	}
	if core.spec.Left.Extract != `^client-(\d+)$` || core.spec.Right.Extract != "" {
		t.Fatalf("extracts = %q / %q, want ^client-(\\d+)$ / \"\"", core.spec.Left.Extract, core.spec.Right.Extract)
	}
	if core.observed != 12 || core.held != 12 || core.distinct != 12 {
		t.Fatalf("stats = observed %d held %d distinct %d, want 12/12/12", core.observed, core.held, core.distinct)
	}
	if core.spec.Severity != "critical" || core.spec.Relation != repo.CorrelationEquals || core.spec.Name == "" || core.spec.Why == "" {
		t.Fatalf("spec metadata incomplete: %+v", core.spec)
	}
	if len(core.examples) != maxRecommendationExamples || core.examples[0].LeftValue != "1001" || core.examples[0].RightValue != "1001" {
		t.Fatalf("examples = %+v", core.examples)
	}

	// The id is one value chain through four positions and two downstream
	// services, so it yields exactly one rule per downstream service, both
	// anchored on the origin; the gateway's outgoing URL is only a hop.
	pub, ok := findCandidate(cands, "resolvePrincipal", semconv.ReturnValue, "publish", semconv.ArgKey(0))
	if !ok {
		t.Fatalf("missing resolvePrincipal/return.value -> publish/arg.0; got %+v", specsOf(cands))
	}
	if pub.transform != relationTransformCore || pub.spec.Right.Service != "notification-api" {
		t.Fatalf("publish candidate = %+v", pub.spec)
	}
	if len(cands) != 2 {
		t.Fatalf("chain should collapse to one rule per downstream service, got %+v", specsOf(cands))
	}
	for _, c := range cands {
		if _, hit := findCandidate([]relationCandidate{c}, "getAccount", semconv.ArgKey(0), "publish", semconv.ArgKey(0)); hit {
			t.Fatalf("intermediate hop pair must not be its own rule: %+v", specsOf(cands))
		}
	}
}

// TestMineRelations_SameSpanCollapses checks that two attributes of one span
// carrying the same id (url.full and url.path on a client span) never pair
// with each other and count once in the chain, and that the rule is built on
// the more direct of the two.
func TestMineRelations_SameSpanCollapses(t *testing.T) {
	samples := relationSamples(12)
	for _, s := range samples {
		for _, sp := range s.full.Spans {
			if sp.Name == "GET" {
				full := sp.SpanAttrs[semconv.URLFull]
				sp.SpanAttrs[semconv.URLPath] = full[strings.Index(full, "/api/"):strings.Index(full, "?")]
			}
		}
	}
	cands := mineRelations(samples, testMiningConfig())
	// The extra attribute must not add rules: still one per downstream service.
	if len(cands) != 2 {
		t.Fatalf("same-span duplicate should collapse, got %+v", specsOf(cands))
	}
	for _, c := range cands {
		if c.spec.Left.Span == c.spec.Right.Span && c.spec.Left.Service == c.spec.Right.Service {
			t.Fatalf("same-span pair recommended: %+v", c.spec)
		}
		if c.spec.Left.Attr == semconv.URLFull || c.spec.Right.Attr == semconv.URLFull {
			t.Fatalf("url.full should have been dropped for url.path on the same span: %+v", c.spec)
		}
	}
	if _, ok := findCandidate(cands, "resolvePrincipal", semconv.ReturnValue, "getAccount", semconv.ArgKey(0)); !ok {
		t.Fatalf("principal -> getAccount missing: %+v", specsOf(cands))
	}
}

// The demo's document flow: the gateway resolves the principal, notification-api
// only forwards the account id on an outgoing call, account-service acts on it.
func relayTrace(i int, id string) *Trace {
	tid := fmt.Sprintf("relay-%02d", i)
	root := &Span{SpanID: tid + "-root", Service: "edge-gateway", Name: "POST banking/documents", Kind: tracepb.Span_SPAN_KIND_SERVER, SpanAttrs: map[string]string{semconv.HTTPRoute: "/banking/documents"}}
	resolve := &Span{SpanID: tid + "-resolve", ParentSpanID: root.SpanID, Service: "edge-gateway", Name: "resolvePrincipal", Kind: tracepb.Span_SPAN_KIND_INTERNAL, SpanAttrs: map[string]string{semconv.ReturnValue: "client-" + id}}
	toNotify := &Span{SpanID: tid + "-c1", ParentSpanID: root.SpanID, Service: "edge-gateway", Name: "POST", Kind: tracepb.Span_SPAN_KIND_CLIENT, SpanAttrs: map[string]string{semconv.URLFull: "http://notification-api/documents"}}
	notify := &Span{SpanID: tid + "-n", ParentSpanID: toNotify.SpanID, Service: "notification-api", Name: "POST /documents", Kind: tracepb.Span_SPAN_KIND_SERVER, SpanAttrs: map[string]string{semconv.URLPath: "/documents"}}
	lookup := &Span{SpanID: tid + "-c2", ParentSpanID: notify.SpanID, Service: "notification-api", Name: "GET /accounts/{id}", Kind: tracepb.Span_SPAN_KIND_CLIENT, SpanAttrs: map[string]string{semconv.URLPath: "/accounts/" + id}}
	served := &Span{SpanID: tid + "-s", ParentSpanID: lookup.SpanID, Service: "account-service", Name: "GET /accounts/{id}", Kind: tracepb.Span_SPAN_KIND_SERVER, SpanAttrs: map[string]string{semconv.URLPath: "/accounts/" + id}}
	getAccount := &Span{SpanID: tid + "-g", ParentSpanID: served.SpanID, Service: "account-service", Name: "getAccount", Kind: tracepb.Span_SPAN_KIND_INTERNAL, SpanAttrs: map[string]string{semconv.ArgKey(0): id}}
	return &Trace{TraceID: tid, Root: root, Spans: []*Span{root, resolve, toNotify, notify, lookup, served, getAccount}}
}

// A service that only forwards the id gets no rule; the service acting on it does.
func TestMineRelations_RelayServiceIsAHop(t *testing.T) {
	var samples []relationSample
	for i := 0; i < 12; i++ {
		tr := relayTrace(i, fmt.Sprintf("%d", 1001+i))
		samples = append(samples, relationSample{full: tr, sub: tr, observedAt: time.Unix(int64(1_700_000_000+i), 0)})
	}
	cands := mineRelations(samples, testMiningConfig())
	if len(cands) != 1 {
		t.Fatalf("want one rule, got %+v", specsOf(cands))
	}
	c := cands[0]
	if c.spec.Left.Span != "resolvePrincipal" || c.spec.Right.Service != "account-service" || c.spec.Right.Attr != semconv.ArgKey(0) {
		t.Fatalf("rule = %+v", c.spec)
	}
	if c.transport || c.spec.Severity != "critical" {
		t.Fatalf("business rule flagged transport or wrong severity: %+v", c)
	}
}

// The service that owns the data gets the principal rule on its own nested
// transaction, anchored on the gateway span above its entry.
func TestMineRelations_UpstreamOrigin(t *testing.T) {
	var samples []relationSample
	for i := 0; i < 12; i++ {
		full := relayTrace(i, fmt.Sprintf("%d", 1001+i))
		var served *Span
		for _, s := range full.Spans {
			if s.Service == "account-service" && s.Kind == tracepb.Span_SPAN_KIND_SERVER {
				served = s
			}
		}
		sub := &Trace{TraceID: full.TraceID, Root: served, Spans: full.Spans[5:]}
		samples = append(samples, relationSample{full: full, sub: sub, observedAt: time.Unix(int64(1_700_000_000+i), 0)})
	}
	cands := mineRelations(samples, testMiningConfig())
	if len(cands) != 1 {
		t.Fatalf("want one rule, got %+v", specsOf(cands))
	}
	c := cands[0]
	if c.spec.Left.Service != "edge-gateway" || c.spec.Left.Span != "resolvePrincipal" || c.spec.Right.Span != "getAccount" {
		t.Fatalf("rule = %+v, want principal -> getAccount", c.spec)
	}
}

// One publish fanned out to two consumers, all carrying the message key; the
// consumers share the offset under two semconv spellings.
func kafkaTrace(i int, wire string) *Trace {
	tid := fmt.Sprintf("kafka-%02d", i)
	offset := fmt.Sprintf("%d", 21000+i)
	root := &Span{SpanID: tid + "-root", Service: "payments-api", Name: "POST /wires", Kind: tracepb.Span_SPAN_KIND_SERVER, SpanAttrs: map[string]string{semconv.HTTPRoute: "/wires"}}
	submit := &Span{SpanID: tid + "-sub", ParentSpanID: root.SpanID, Service: "payments-api", Name: "submitWire", Kind: tracepb.Span_SPAN_KIND_INTERNAL, SpanAttrs: map[string]string{semconv.ArgKey(0): wire}}
	publish := &Span{SpanID: tid + "-pub", ParentSpanID: submit.SpanID, Service: "payments-api", Name: "payments.submitted publish", Kind: tracepb.Span_SPAN_KIND_PRODUCER, SpanAttrs: map[string]string{semconv.MessagingKafkaMessageKey: "wire-" + wire}}
	settle := &Span{SpanID: tid + "-set", ParentSpanID: publish.SpanID, Service: "settlement-worker", Name: "payments.submitted receive", Kind: tracepb.Span_SPAN_KIND_CONSUMER, SpanAttrs: map[string]string{semconv.MessagingKafkaMessageKey: "wire-" + wire, semconv.MessagingKafkaOffset: offset}}
	sanction := &Span{SpanID: tid + "-san", ParentSpanID: publish.SpanID, Service: "sanctions-svc", Name: "payments.submitted receive", Kind: tracepb.Span_SPAN_KIND_CONSUMER, SpanAttrs: map[string]string{semconv.MessagingKafkaMessageKey: "wire-" + wire, semconv.LegacyMessagingKafkaMessageOffset: offset}}
	return &Trace{TraceID: tid, Root: root, Spans: []*Span{root, submit, publish, settle, sanction}}
}

// Transport-mirrored fields are kept as low-severity rules ranked after every
// business relation; the business relation itself is not flagged.
func TestMineRelations_TransportPairsAreLowAndLast(t *testing.T) {
	var samples []relationSample
	for i := 0; i < 12; i++ {
		tr := kafkaTrace(i, fmt.Sprintf("%d", 5001+i))
		samples = append(samples, relationSample{full: tr, sub: tr, observedAt: time.Unix(int64(1_700_000_000+i), 0)})
	}
	cands := mineRelations(samples, testMiningConfig())
	var business, transport []relationCandidate
	for _, c := range cands {
		if c.transport {
			transport = append(transport, c)
		} else {
			business = append(business, c)
		}
	}
	// The wire id chain: submitWire.arg.0 is the origin, one rule per consumer.
	if len(business) != 2 {
		t.Fatalf("business rules = %+v, want submitWire -> each consumer", specsOf(business))
	}
	for _, c := range business {
		if c.spec.Left.Span != "submitWire" || c.spec.Right.Attr != semconv.MessagingKafkaMessageKey {
			t.Fatalf("business rule = %+v", c.spec)
		}
		if c.spec.Severity != "critical" {
			t.Fatalf("business severity = %q", c.spec.Severity)
		}
	}
	// The offset chain (two spellings) is transport only.
	if len(transport) != 1 || mirrorKey(transport[0].spec.Left.Attr) != semconv.MessagingKafkaOffset || transport[0].spec.Severity != "low" {
		t.Fatalf("transport rules = %+v", specsOf(transport))
	}
	if !strings.Contains(transport[0].spec.Why, "tampering") {
		t.Fatalf("transport why = %q", transport[0].spec.Why)
	}
	for i := 1; i < len(cands); i++ {
		if cands[i-1].transport && !cands[i].transport {
			t.Fatalf("transport rule ranked above a business rule: %v", specsOf(cands))
		}
	}
	// With the business origin removed, producer -> consumer key is a transport hop.
	for _, s := range samples {
		delete(s.full.Spans[1].SpanAttrs, semconv.ArgKey(0))
	}
	cands = mineRelations(samples, testMiningConfig())
	for _, c := range cands {
		if !c.transport {
			t.Fatalf("expected only transport rules, got %+v", specsOf(cands))
		}
	}
	if len(cands) != 3 {
		t.Fatalf("want key -> 2 consumers + offset pair, got %+v", specsOf(cands))
	}
}

// TestMineRelations_InServiceChain: a chain that never leaves the service
// yields a single origin -> deepest rule.
func TestMineRelations_InServiceChain(t *testing.T) {
	var samples []relationSample
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("%d", 1001+i)
		tid := fmt.Sprintf("t-%02d", i)
		root := &Span{SpanID: tid + "-r", Service: "svc", Name: "GET /orders/{id}", Kind: tracepb.Span_SPAN_KIND_SERVER, SpanAttrs: map[string]string{semconv.URLPath: "/orders/" + id}}
		load := &Span{SpanID: tid + "-l", ParentSpanID: root.SpanID, Service: "svc", Name: "loadOrder", Kind: tracepb.Span_SPAN_KIND_INTERNAL, SpanAttrs: map[string]string{semconv.ArgKey(0): id}}
		price := &Span{SpanID: tid + "-p", ParentSpanID: load.SpanID, Service: "svc", Name: "priceOrder", Kind: tracepb.Span_SPAN_KIND_INTERNAL, SpanAttrs: map[string]string{semconv.ArgKey(0): id}}
		tr := &Trace{TraceID: tid, Root: root, Spans: []*Span{root, load, price}}
		samples = append(samples, relationSample{full: tr, sub: tr, observedAt: time.Unix(int64(1_700_000_000+i), 0)})
	}
	cands := mineRelations(samples, testMiningConfig())
	if len(cands) != 1 {
		t.Fatalf("want one rule for an in-service chain, got %+v", specsOf(cands))
	}
	c := cands[0]
	if c.spec.Left.Span != "GET /orders/{id}" || c.spec.Right.Span != "priceOrder" {
		t.Fatalf("rule = %+v", c.spec)
	}
	if spans := chainSpans(samples, testMiningConfig(), "loadOrder", semconv.ArgKey(0)); len(spans) != 3 {
		t.Fatalf("loadOrder should be a hop of the same chain, got %v", spans)
	}
}

func TestMineRelations_RejectsNoise(t *testing.T) {
	cands := mineRelations(relationSamples(12), testMiningConfig())
	for _, c := range cands {
		for _, sel := range []repo.CorrelationSelector{c.spec.Left, c.spec.Right} {
			switch sel.Attr {
			case semconv.HTTPRoute:
				t.Fatalf("denylisted attribute recommended: %+v", c.spec)
			case "region":
				t.Fatalf("constant attribute recommended: %+v", c.spec)
			}
			if sel.Span == "audit" {
				t.Fatalf("50%% relation recommended: %+v (held %d of %d)", c.spec, c.held, c.observed)
			}
		}
	}
}

func TestMineRelations_ThresholdsAndMinimums(t *testing.T) {
	// Too few samples: nothing, however clean.
	if got := mineRelations(relationSamples(minRelationObserved-1), testMiningConfig()); len(got) != 0 {
		t.Fatalf("mined %d candidates from %d samples, want none", len(got), minRelationObserved-1)
	}
	// Enough samples but not enough distinct values.
	cfg := testMiningConfig()
	cfg.minDistinct = 20
	if got := mineRelations(relationSamples(12), cfg); len(got) != 0 {
		t.Fatalf("mined %d candidates below minDistinct, want none", len(got))
	}
	// Lowering the hold threshold admits the 50% audit relation.
	cfg = testMiningConfig()
	cfg.minHoldPct = 40
	// audit is on the origin's own service, so it joins the principal's chain
	// as a hop rather than becoming a rule of its own.
	spans := chainSpans(relationSamples(12), cfg, "audit", semconv.ArgKey(0))
	if len(spans) < 2 || !slices.Contains(spans, "resolvePrincipal") {
		t.Fatalf("audit should join the principal chain at 40%% hold; got %v", spans)
	}
	if spans := chainSpans(relationSamples(12), testMiningConfig(), "audit", semconv.ArgKey(0)); spans != nil {
		t.Fatalf("audit should be in no chain at 98%% hold; got %v", spans)
	}
}

func TestMineRelations_DeterministicAndRanked(t *testing.T) {
	a := mineRelations(relationSamples(12), testMiningConfig())
	b := mineRelations(relationSamples(12), testMiningConfig())
	if !reflect.DeepEqual(specsOf(a), specsOf(b)) {
		t.Fatalf("mining is not deterministic:\n%v\n%v", specsOf(a), specsOf(b))
	}
	for i := 1; i < len(a); i++ {
		if a[i-1].score < a[i].score {
			t.Fatalf("candidates not sorted by score: %v", a)
		}
	}
	// The account read is the strongest rule of the chain and ranks first.
	if a[0].spec.Right.Span != "getAccount" {
		t.Fatalf("expected the getAccount rule first, got %v", specsOf(a))
	}
}

func TestCommonFrame_StopsAtTokenBoundary(t *testing.T) {
	// Sequential ids share leading digits; the frame must not absorb them.
	if got := commonPrefix([]string{"client-1001", "client-1002", "client-1012"}); got != "client-" {
		t.Fatalf("commonPrefix = %q, want client-", got)
	}
	if got := commonPrefix([]string{"1001", "1002"}); got != "" {
		t.Fatalf("commonPrefix of bare ids = %q, want empty", got)
	}
	if got := commonSuffix([]string{"1001-eu", "2001-eu"}); got != "-eu" {
		t.Fatalf("commonSuffix = %q, want -eu", got)
	}
	if got := commonSuffix([]string{"/tenant-17/", "/tenant-27/"}); got != "/" {
		t.Fatalf("commonSuffix = %q, want /", got)
	}
	// A real constant frame is kept whole.
	if got := commonPrefix([]string{"http://a/x/1", "http://a/x/2"}); got != "http://a/x/" {
		t.Fatalf("commonPrefix = %q, want http://a/x/", got)
	}
}

func TestRecommendationID_StableAcrossMetadata(t *testing.T) {
	a := principalMatchesAccount("critical")
	b := principalMatchesAccount("low")
	b.Name, b.Why = "renamed", "different rationale"
	if repo.RecommendationID(7, repo.RecommendationKindAttributeCorrelation, a) != repo.RecommendationID(7, repo.RecommendationKindAttributeCorrelation, b) {
		t.Fatal("id must not depend on name, severity, or why")
	}
	c := a
	c.Right.Attr = semconv.ArgKey(1)
	if repo.RecommendationID(7, repo.RecommendationKindAttributeCorrelation, a) == repo.RecommendationID(7, repo.RecommendationKindAttributeCorrelation, c) {
		t.Fatal("id must change when a selector changes")
	}
	if repo.RecommendationID(7, repo.RecommendationKindAttributeCorrelation, a) == repo.RecommendationID(8, repo.RecommendationKindAttributeCorrelation, a) {
		t.Fatal("id must be scoped to the transaction")
	}
}

func specsOf(cands []relationCandidate) []string {
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, fmt.Sprintf("%s/%s/%s[%s] -> %s/%s/%s[%s] %s %d/%d d=%d",
			c.spec.Left.Service, c.spec.Left.Span, c.spec.Left.Attr, c.spec.Left.Extract,
			c.spec.Right.Service, c.spec.Right.Span, c.spec.Right.Attr, c.spec.Right.Extract,
			c.transform, c.held, c.observed, c.distinct))
	}
	return out
}
