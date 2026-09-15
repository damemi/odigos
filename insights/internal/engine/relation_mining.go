package engine

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/odigos-io/odigos-enterprise/insights/internal/engine/semconv"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// Relation mining turns a promoted transaction's stored relation samples into
// attribute_correlation recommendations. The question it answers is: which two
// span attributes in this transaction always agree, across services and
// across many different values? Such a pair is an implicit invariant of the
// business flow (the account a caller was authorized for is the account the
// backend reads), and a trace where it breaks is exactly what a BOLA-style
// attack looks like. Everything here is pure and runs off the ingest path.

// Transform names how two correlated values relate.
const (
	relationTransformExact    = "exact"
	relationTransformCore     = "core"
	relationTransformDigits   = "digits"
	relationTransformContains = "contains"
)

const (
	// minRelationObserved is the fewest samples (with both values present) a
	// pair must have before it is even considered. Below this a 100% hold
	// ratio says nothing.
	minRelationObserved = 5
	// maxRelationValueLen skips blobs (bodies, stack traces) that are never ids.
	maxRelationValueLen = 256
	// maxRecommendationExamples is how many concrete value pairs are stored.
	maxRecommendationExamples = 3
	// maxRecommendationsPerTransaction caps what one mining run emits.
	maxRecommendationsPerTransaction = 20
)

// relationMiningConfig carries the operator thresholds a candidate must clear.
type relationMiningConfig struct {
	minDistinct int
	minHoldPct  int
}

func relationMiningConfigFrom(s repo.SystemSettings) relationMiningConfig {
	s = s.WithDefaults()
	return relationMiningConfig{minDistinct: s.RecommendationMinDistinct, minHoldPct: s.RecommendationMinHoldPercent}
}

// relationSample is one stored trace as the miner sees it: the full trace the
// guardrail will later evaluate against, and the sub-trace of the transaction
// being mined, which is where candidate attributes are taken from.
type relationSample struct {
	full       *Trace
	sub        *Trace
	observedAt time.Time
}

// attrPosition is one attribute at one span site, expressed exactly as a
// CorrelationSelector would locate it at runtime.
type attrPosition struct {
	Service string
	Span    string
	Attr    string
}

// attrColumn is one position's values across samples (sample index -> value)
// plus where the span sits in the call tree, used for ranking and to pick the
// origin and sink of a chain.
type attrColumn struct {
	pos   attrPosition
	depth int
	order int
	kind  tracepb.Span_SpanKind
	// Site of the parent span; identifies the two ends of one transport hop.
	parent attrPosition
	// Above the transaction's entry span (its caller's path). A value can be
	// established there, but this transaction never acts on it there.
	upstream bool
	values   map[int]string
	views    []columnView
}

// columnView is one way of reading a column's values: verbatim, the core
// between a constant prefix/suffix, or the single digit run. extract is the
// regex a selector needs to derive the view value from the raw attribute.
type columnView struct {
	name    string
	extract string
	values  map[int]string
}

// relationCandidate is one mined pair before it is turned into a recommendation.
type relationCandidate struct {
	spec      repo.CorrelationSpec
	transform string
	observed  int
	held      int
	distinct  int
	examples  []repo.RecommendationExample
	// Mirrored by the transport (same attribute on both ends of one hop).
	transport bool
	score     float64
}

// Attributes that carry the same value (under any transform) are chained, and
// each chain yields one rule per downstream service: from where the value was
// established to where it is acted on. A value passing through five positions
// would otherwise yield ten rules that all break on the same attack; a swap
// at any hop arrives changed at the sink, so origin -> sink covers them all.
func mineRelations(samples []relationSample, cfg relationMiningConfig) []relationCandidate {
	if len(samples) < minRelationObserved {
		return nil
	}
	columns := collectAttrColumns(samples)
	if len(columns) < 2 {
		return nil
	}
	columns = dedupeSpanColumns(columns, cfg)

	var out []relationCandidate
	for _, chain := range chainColumns(columns, cfg) {
		out = append(out, chainCandidates(chain, samples, cfg)...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].spec.Name < out[j].spec.Name
	})
	if len(out) > maxRecommendationsPerTransaction {
		out = out[:maxRecommendationsPerTransaction]
	}
	return out
}

// ---------- column collection ----------

// One column per attribute position, holding its value in each sample.
// Positions come from the transaction's own spans plus its caller's path in
// the full trace, so a nested transaction can be anchored on the principal
// its caller resolved. Constant, sparse, or non-identifier columns are
// dropped here.
func collectAttrColumns(samples []relationSample) []*attrColumn {
	byPos := map[attrPosition]*attrColumn{}
	record := func(i int, s *Span, parent *Span, depth, order int, upstream bool) {
		selector := spanSelectorName(s)
		if selector == "" {
			return
		}
		var parentPos attrPosition
		if parent != nil {
			parentPos = attrPosition{Service: parent.Service, Span: spanSelectorName(parent)}
		}
		for key, val := range s.SpanAttrs {
			if !relationAttrAllowed(key, val) {
				continue
			}
			pos := attrPosition{Service: s.Service, Span: selector, Attr: key}
			col := byPos[pos]
			if col == nil {
				col = &attrColumn{pos: pos, depth: depth, order: order, kind: s.Kind, parent: parentPos, upstream: upstream, values: map[int]string{}}
				byPos[pos] = col
			}
			// Seen inside the transaction in any sample: it is the transaction's.
			col.upstream = col.upstream && upstream
			// First value per sample is enough here; verification replays every span.
			if _, seen := col.values[i]; seen {
				continue
			}
			col.values[i] = val
			if depth < col.depth {
				col.depth, col.order, col.kind, col.parent = depth, order, s.Kind, parentPos
			}
		}
	}
	for i, sample := range samples {
		if sample.sub == nil {
			continue
		}
		byID := make(map[string]*Span, len(sample.full.Spans))
		for _, s := range sample.full.Spans {
			byID[s.SpanID] = s
		}
		depths := spanDepths(sample.sub)
		for order, s := range sample.sub.Spans {
			record(i, s, byID[s.ParentSpanID], depths[s.SpanID], order, false)
		}
		for _, u := range upstreamSpans(sample.full, sample.sub, byID) {
			record(i, u.span, byID[u.span.ParentSpanID], u.depth, u.depth, true)
		}
	}
	out := make([]*attrColumn, 0, len(byPos))
	for _, col := range byPos {
		if len(col.values) < minRelationObserved || distinctValues(col.values) < 2 {
			continue
		}
		col.views = buildColumnViews(col.values)
		out = append(out, col)
	}
	// Deterministic order so identical samples mine to identical output on
	// every replica.
	sort.Slice(out, func(i, j int) bool {
		if out[i].depth != out[j].depth {
			return out[i].depth < out[j].depth
		}
		if out[i].order != out[j].order {
			return out[i].order < out[j].order
		}
		return positionKey(out[i].pos) < positionKey(out[j].pos)
	})
	return out
}

func positionKey(p attrPosition) string { return p.Service + "/" + p.Span + "/" + p.Attr }

// A span above the transaction's entry; depth is negative so it sorts before
// the transaction's own spans.
type upstreamSpan struct {
	span  *Span
	depth int
}

// The caller context of a nested transaction: the ancestors of its entry span
// plus what each ancestor's own service did around them (the gateway resolves
// the principal in a sibling of the call down, not an ancestor). Other
// services' sub-trees are not pulled in. Empty for a root transaction.
func upstreamSpans(full, sub *Trace, byID map[string]*Span) []upstreamSpan {
	if sub.Root == nil || sub.Root.ParentSpanID == "" {
		return nil
	}
	children := make(map[string][]*Span, len(full.Spans))
	for _, s := range full.Spans {
		children[s.ParentSpanID] = append(children[s.ParentSpanID], s)
	}
	inSub := spanIDSet(sub.Spans)
	seen := map[string]bool{}
	var out []upstreamSpan
	hops := 0
	for cur := byID[sub.Root.ParentSpanID]; cur != nil && hops < len(full.Spans); cur = byID[cur.ParentSpanID] {
		hops++
		// The ancestor plus its own service's work beneath it.
		stack := []upstreamSpan{{span: cur, depth: -hops}}
		for len(stack) > 0 {
			u := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if seen[u.span.SpanID] {
				continue
			}
			seen[u.span.SpanID] = true
			out = append(out, u)
			for _, child := range children[u.span.SpanID] {
				if _, own := inSub[child.SpanID]; own || child.Service != cur.Service {
					continue
				}
				stack = append(stack, upstreamSpan{span: child, depth: u.depth})
			}
		}
	}
	return out
}

// spanSelectorName is the Span value a CorrelationSelector needs to match this
// span. Custom-instrumented spans use the short method name (the runtime
// matches it as a suffix of the fully-qualified site); every other span its name.
func spanSelectorName(s *Span) string {
	if site := d4SiteOf(s); site != "" {
		if i := strings.LastIndex(site, "."); i >= 0 && i < len(site)-1 {
			return site[i+1:]
		}
		return site
	}
	return s.Name
}

// spanDepths returns each span's distance from the sub-trace root.
func spanDepths(t *Trace) map[string]int {
	byID := make(map[string]*Span, len(t.Spans))
	for _, s := range t.Spans {
		byID[s.SpanID] = s
	}
	depths := make(map[string]int, len(t.Spans))
	var depthOf func(s *Span, hops int) int
	depthOf = func(s *Span, hops int) int {
		if d, ok := depths[s.SpanID]; ok {
			return d
		}
		parent, ok := byID[s.ParentSpanID]
		// hops guards against a malformed parent cycle.
		if !ok || s.ParentSpanID == "" || hops > len(t.Spans) {
			depths[s.SpanID] = 0
			return 0
		}
		d := depthOf(parent, hops+1) + 1
		depths[s.SpanID] = d
		return d
	}
	for _, s := range t.Spans {
		depthOf(s, 0)
	}
	return depths
}

// relationAttrDenied lists attribute keys (exact or prefix) that never carry a
// business identifier worth correlating: transport plumbing, sizes and
// timings, propagation ids that are equal across a trace by construction, and
// classification enums that would correlate trivially.
var relationAttrDenied = []string{
	// Namespaces (prefix match).
	"thread.", "process.", "net.", "network.", "server.", "client.", "host.", "k8s.", "os.",
	"service.", "telemetry.", "otel.", "odigos.", "code.", "exception.",
	"http.request.header.", "http.response.header.", "user_agent.",
	// Exact keys.
	semconv.HTTPResponseStatusCode, semconv.LegacyHTTPStatusCode, semconv.HTTPRequestMethod, semconv.LegacyHTTPMethod,
	semconv.HTTPRoute, semconv.LegacyHTTPFlavor, semconv.LegacyHTTPScheme, semconv.URLScheme,
	semconv.HTTPRequestBodySize, semconv.HTTPResponseBodySize,
	semconv.LegacyHTTPRequestContentLength, semconv.LegacyHTTPResponseContentLength,
	semconv.DBSystem, semconv.LegacyDBOperation, semconv.LegacyDBName,
	semconv.RPCSystem, semconv.RPCService, semconv.RPCMethod, semconv.RPCGRPCStatusCode,
	semconv.MessagingSystem, semconv.LegacyMessagingOperation, semconv.PeerService,
	"span.kind", "traceparent", "tracestate", "trace_id", "span_id", "parent_id",
}

// relationAttrAllowed decides whether one attribute value is a plausible
// identifier: not on the denylist, short, and shaped like an id or a
// container of one (path, URL) rather than a flag, a blob, or free text.
func relationAttrAllowed(key, val string) bool {
	if val == "" || len(val) > maxRelationValueLen {
		return false
	}
	lower := strings.ToLower(key)
	for _, d := range relationAttrDenied {
		if strings.HasSuffix(d, ".") {
			if strings.HasPrefix(lower, d) {
				return false
			}
		} else if lower == d {
			return false
		}
	}
	for _, frag := range []string{"trace_id", "traceid", "span_id", "spanid", "request_id", "requestid", "duration", "timestamp", "_time", ".time", "size", "count", "length", "port"} {
		if strings.Contains(lower, frag) {
			return false
		}
	}
	switch classifyD4Format(val) {
	case d4FmtNull, d4FmtBool, d4FmtJSON, d4FmtFreeform, d4FmtIP:
		return false
	}
	return true
}

func distinctValues(values map[int]string) int {
	seen := map[string]struct{}{}
	for _, v := range values {
		seen[v] = struct{}{}
	}
	return len(seen)
}

// ---------- views ----------

var relationDigitRun = regexp.MustCompile(`\d+`)

// buildColumnViews derives the alternative readings of a column. Every view
// comes with the extract regex that produces it, so a match under a view is
// directly expressible as a CorrelationSelector.
func buildColumnViews(values map[int]string) []columnView {
	views := []columnView{{name: relationTransformExact, values: values}}
	if core, ok := coreView(values); ok {
		views = append(views, core)
	}
	if digits, ok := digitsView(values); ok {
		views = append(views, digits)
	}
	return views
}

// coreView strips the prefix and suffix every value shares ("client-1001" ->
// "1001"). It needs a non-empty constant frame and a non-empty, non-constant
// core, otherwise it is the exact view again.
func coreView(values map[int]string) (columnView, bool) {
	var all []string
	for _, v := range values {
		all = append(all, v)
	}
	prefix := commonPrefix(all)
	suffix := commonSuffix(all)
	if prefix == "" && suffix == "" {
		return columnView{}, false
	}
	out := make(map[int]string, len(values))
	for i, v := range values {
		if len(v) < len(prefix)+len(suffix) {
			return columnView{}, false
		}
		core := v[len(prefix) : len(v)-len(suffix)]
		if core == "" {
			return columnView{}, false
		}
		out[i] = core
	}
	if distinctValues(out) < 2 {
		return columnView{}, false
	}
	class := valueClass(out)
	return columnView{
		name:    relationTransformCore,
		extract: "^" + regexp.QuoteMeta(prefix) + "(" + class + ")" + regexp.QuoteMeta(suffix) + "$",
		values:  out,
	}, true
}

// digitsView reads the single run of digits inside every value ("acct-1001"
// -> "1001"). Values with zero or several runs, or that are all digits already,
// have no digits view.
func digitsView(values map[int]string) (columnView, bool) {
	out := make(map[int]string, len(values))
	for i, v := range values {
		runs := relationDigitRun.FindAllString(v, 2)
		if len(runs) != 1 || runs[0] == v {
			return columnView{}, false
		}
		out[i] = runs[0]
	}
	if distinctValues(out) < 2 {
		return columnView{}, false
	}
	return columnView{name: relationTransformDigits, extract: `(\d+)`, values: out}, true
}

// valueClass is the regex character class that matches every value in the
// map, from the most specific shape they all share.
func valueClass(values map[int]string) string {
	allNumeric, allUUID, allToken := true, true, true
	for _, v := range values {
		switch classifyD4Format(v) {
		case d4FmtNumeric:
			allUUID = false
		case d4FmtUUID:
			allNumeric, allToken = false, false
		case d4FmtToken:
			allNumeric, allUUID = false, false
		default:
			allNumeric, allUUID, allToken = false, false, false
		}
	}
	switch {
	case allNumeric:
		return `\d+`
	case allUUID:
		return `[0-9a-fA-F-]{36}`
	case allToken:
		return `[A-Za-z0-9_-]+`
	default:
		return `[^/?&#\s]+`
	}
}

// commonPrefix is the longest prefix every value shares, backed off to a token
// boundary: sequential ids ("1001", "1002", ...) share leading digits that are
// part of the id, not of the constant frame, so a prefix never ends inside a
// run of digits or letters that continues in some value.
func commonPrefix(values []string) string {
	if len(values) == 0 {
		return ""
	}
	prefix := values[0]
	for _, v := range values[1:] {
		for !strings.HasPrefix(v, prefix) {
			prefix = prefix[:len(prefix)-1]
			if prefix == "" {
				return ""
			}
		}
	}
	for prefix != "" && splitsToken(prefix[len(prefix)-1], values, len(prefix)) {
		prefix = prefix[:len(prefix)-1]
	}
	return prefix
}

// commonSuffix is the longest shared suffix, backed off to a token boundary
// (see commonPrefix).
func commonSuffix(values []string) string {
	if len(values) == 0 {
		return ""
	}
	suffix := values[0]
	for _, v := range values[1:] {
		for !strings.HasSuffix(v, suffix) {
			suffix = suffix[1:]
			if suffix == "" {
				return ""
			}
		}
	}
	for suffix != "" {
		split := false
		for _, v := range values {
			if i := len(v) - len(suffix) - 1; i >= 0 && sameCharClass(v[i], suffix[0]) {
				split = true
				break
			}
		}
		if !split {
			break
		}
		suffix = suffix[1:]
	}
	return suffix
}

// splitsToken reports whether the frame character c, sitting just before
// offset at in some value, is followed there by a character of the same class.
func splitsToken(c byte, values []string, at int) bool {
	for _, v := range values {
		if len(v) > at && sameCharClass(c, v[at]) {
			return true
		}
	}
	return false
}

func sameCharClass(a, b byte) bool {
	isDigit := func(c byte) bool { return c >= '0' && c <= '9' }
	isAlpha := func(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
	return (isDigit(a) && isDigit(b)) || (isAlpha(a) && isAlpha(b))
}

// ---------- same-span duplicates ----------

// dedupeSpanColumns keeps one column per value per span. url.full and url.path
// on the same client span carry the same id by construction, so pairing them
// with each other says nothing and pairing both with a third position says the
// same thing twice. The survivor is the attribute carrying the value most
// directly (shortest values, e.g. url.path over url.full, arg.0 over a
// formatted message), so the rule needs the least extraction.
func dedupeSpanColumns(columns []*attrColumn, cfg relationMiningConfig) []*attrColumn {
	dropped := map[*attrColumn]bool{}
	for i := 0; i < len(columns); i++ {
		for j := i + 1; j < len(columns); j++ {
			a, b := columns[i], columns[j]
			if dropped[a] || dropped[b] || !sameSpan(a, b) || !columnsRelated(a, b, cfg) {
				continue
			}
			if moreDirect(b, a) {
				dropped[a] = true
			} else {
				dropped[b] = true
			}
		}
	}
	out := make([]*attrColumn, 0, len(columns))
	for _, c := range columns {
		if !dropped[c] {
			out = append(out, c)
		}
	}
	return out
}

func sameSpan(a, b *attrColumn) bool {
	return a.pos.Service == b.pos.Service && a.pos.Span == b.pos.Span
}

// moreDirect reports whether a carries its value with less wrapping than b:
// shorter raw values, then by name for determinism.
func moreDirect(a, b *attrColumn) bool {
	la, lb := totalLen(a.values), totalLen(b.values)
	if la != lb {
		return la < lb
	}
	return positionKey(a.pos) < positionKey(b.pos)
}

func totalLen(values map[int]string) int {
	n := 0
	for _, v := range values {
		n += len(v)
	}
	return n
}

// ---------- value chains ----------

// chainColumns unions columns that carry the same value under any supported
// transform (verbatim, framed, digits, embedded) into value chains: the set of
// positions one identifier passes through within the transaction. Two
// attributes of one span never relate - a chain is a path through spans.
func chainColumns(columns []*attrColumn, cfg relationMiningConfig) [][]*attrColumn {
	parent := make([]int, len(columns))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	for i := 0; i < len(columns); i++ {
		for j := i + 1; j < len(columns); j++ {
			if !sameSpan(columns[i], columns[j]) && columnsRelated(columns[i], columns[j], cfg) {
				parent[find(i)] = find(j)
			}
		}
	}
	byRoot := map[int][]*attrColumn{}
	var order []int
	for i, col := range columns {
		root := find(i)
		if _, seen := byRoot[root]; !seen {
			order = append(order, root)
		}
		byRoot[root] = append(byRoot[root], col)
	}
	out := make([][]*attrColumn, 0, len(order))
	for _, root := range order {
		if len(byRoot[root]) > 1 {
			out = append(out, byRoot[root])
		}
	}
	return out
}

// columnsRelated reports whether two columns agree, on enough shared samples
// and distinct values, under some pair of views or by one value being embedded
// in the other. It is the cheap in-memory test; pairCandidate later replays
// the chosen pair through the runtime selectors.
func columnsRelated(a, b *attrColumn, cfg relationMiningConfig) bool {
	for i := range a.views {
		for j := range b.views {
			both, held, distinct := agreement(a.views[i].values, b.views[j].values)
			if both >= minRelationObserved && holds(held, both, cfg) && distinct >= cfg.minDistinct {
				return true
			}
		}
	}
	if _, ok := containmentSpec(a, b, cfg); ok {
		return true
	}
	_, ok := containmentSpec(b, a, cfg)
	return ok
}

// One chain becomes one rule per downstream service that acts on the value:
// origin (where it was established) -> sink (where it is used). A service that
// only forwards the value on outgoing calls is a relay, not a sink: a swap
// there is caught at the next real sink. Upstream positions are never sinks.
// A chain inside one service yields a single origin -> deepest rule.
func chainCandidates(chain []*attrColumn, samples []relationSample, cfg relationMiningConfig) []relationCandidate {
	origins := append([]*attrColumn(nil), chain...)
	sort.SliceStable(origins, func(i, j int) bool { return originBefore(origins[i], origins[j]) })
	origin := origins[0]

	sinksByService := map[string][]*attrColumn{}
	actsOn := map[string]bool{}
	var services []string
	for _, c := range chain {
		if c.upstream || strings.EqualFold(c.pos.Service, origin.pos.Service) {
			continue
		}
		if _, seen := sinksByService[c.pos.Service]; !seen {
			services = append(services, c.pos.Service)
		}
		sinksByService[c.pos.Service] = append(sinksByService[c.pos.Service], c)
		if !forwardsOnly(c) {
			actsOn[c.pos.Service] = true
		}
	}
	sort.Strings(services)
	if len(actsOn) > 0 {
		kept := services[:0]
		for _, svc := range services {
			if actsOn[svc] {
				kept = append(kept, svc)
			}
		}
		services = kept
	}
	if len(services) == 0 {
		// In-service chain: the far end of the flow is the sink.
		var own []*attrColumn
		for _, c := range chain {
			if !c.upstream {
				own = append(own, c)
			}
		}
		sinksByService[origin.pos.Service] = own
		services = []string{origin.pos.Service}
	}

	var out []relationCandidate
	for _, svc := range services {
		sinks := sinksByService[svc]
		sort.SliceStable(sinks, func(i, j int) bool { return sinkBefore(sinks[i], sinks[j]) })
		if cand, ok := firstVerifiedPair(origins, sinks, samples, cfg); ok {
			out = append(out, cand)
		}
	}
	return out
}

// An outgoing call or publish: the value is passing through, not acted on.
func forwardsOnly(c *attrColumn) bool {
	return c.kind == tracepb.Span_SPAN_KIND_CLIENT || c.kind == tracepb.Span_SPAN_KIND_PRODUCER
}

// The best-ranked origin/sink pair that replays cleanly on the samples.
func firstVerifiedPair(origins, sinks []*attrColumn, samples []relationSample, cfg relationMiningConfig) (relationCandidate, bool) {
	for _, o := range origins {
		for _, s := range sinks {
			if o == s || sameSpan(o, s) {
				continue
			}
			if cand, ok := pairCandidate(o, s, samples, cfg); ok {
				return cand, true
			}
		}
	}
	return relationCandidate{}, false
}

// The same attribute on both ends of one hop (client/server, producer/consumer)
// or on sibling consumers of one message. The transport copies it, so the
// pair holds by construction; a break means in-flight tampering or
// misattribution, not an authorization bypass. Kept at low severity and
// ranked after every business relation.
func transportPair(a, b *attrColumn) bool {
	if semconv.IsProbeAttr(a.pos.Attr) || semconv.IsProbeAttr(b.pos.Attr) || mirrorKey(a.pos.Attr) != mirrorKey(b.pos.Attr) {
		return false
	}
	site := func(c *attrColumn) attrPosition { return attrPosition{Service: c.pos.Service, Span: c.pos.Span} }
	hop := func(from, to *attrColumn) bool {
		if to.parent != site(from) {
			return false
		}
		return (from.kind == tracepb.Span_SPAN_KIND_CLIENT && to.kind == tracepb.Span_SPAN_KIND_SERVER) ||
			(from.kind == tracepb.Span_SPAN_KIND_PRODUCER && to.kind == tracepb.Span_SPAN_KIND_CONSUMER)
	}
	if hop(a, b) || hop(b, a) {
		return true
	}
	siblings := a.parent == b.parent && a.parent != (attrPosition{})
	return siblings && a.kind == b.kind &&
		(a.kind == tracepb.Span_SPAN_KIND_CONSUMER || a.kind == tracepb.Span_SPAN_KIND_SERVER)
}

// Spellings of one transport field across semconv versions, current first.
var transportFieldAliases = [][]string{
	{semconv.MessagingKafkaMessageKey},
	{semconv.MessagingKafkaOffset, semconv.LegacyMessagingKafkaMessageOffset},
	{semconv.MessagingDestinationPartitionID, semconv.LegacyMessagingKafkaPartition},
	{semconv.MessagingMessageID},
	{semconv.MessagingMessageConversationID},
}

// A producer on an old instrumentation and a consumer on a new one still pair.
func mirrorKey(attr string) string {
	lower := strings.ToLower(attr)
	for _, aliases := range transportFieldAliases {
		if slices.Contains(aliases, lower) {
			return aliases[0]
		}
	}
	return lower
}

// originBefore prefers the position where a value is established: a captured
// return value (the result of resolving the caller), then the transaction's
// entry span, then whatever sits closest to the root.
func originBefore(a, b *attrColumn) bool {
	ra, rb := originRank(a), originRank(b)
	if ra != rb {
		return ra < rb
	}
	return anchorBefore(a, b)
}

func originRank(c *attrColumn) int {
	switch {
	case c.pos.Attr == semconv.ReturnValue:
		return 0
	case c.depth == 0:
		return 1
	default:
		return 2
	}
}

// sinkBefore prefers the position where a value is acted on: a captured
// argument (what the code actually used), then the deepest span.
func sinkBefore(a, b *attrColumn) bool {
	pa, pb := semconv.IsCapturedArg(a.pos.Attr), semconv.IsCapturedArg(b.pos.Attr)
	if pa != pb {
		return pa
	}
	if a.depth != b.depth {
		return a.depth > b.depth
	}
	if a.order != b.order {
		return a.order > b.order
	}
	return positionKey(a.pos) < positionKey(b.pos)
}

// anchorBefore reports whether a sits closer to the root than b (then earlier
// in span order, then by name for determinism).
func anchorBefore(a, b *attrColumn) bool {
	if a.depth != b.depth {
		return a.depth < b.depth
	}
	if a.order != b.order {
		return a.order < b.order
	}
	return positionKey(a.pos) < positionKey(b.pos)
}

// agreement counts samples where both columns have a value, how many agree,
// and how many distinct agreeing values there are.
func agreement(a, b map[int]string) (both, held, distinct int) {
	seen := map[string]struct{}{}
	for i, va := range a {
		vb, ok := b[i]
		if !ok {
			continue
		}
		both++
		if va == vb {
			held++
			seen[va] = struct{}{}
		}
	}
	return both, held, len(seen)
}

func holds(held, both int, cfg relationMiningConfig) bool {
	return both > 0 && held*100 >= both*cfg.minHoldPct
}

// ---------- pairing ----------

// pairCandidate tries every supported transform between two columns, in
// simplest-first order, and returns the first one that replays successfully
// through the runtime selector logic on the full traces.
func pairCandidate(a, b *attrColumn, samples []relationSample, cfg relationMiningConfig) (relationCandidate, bool) {
	type combo struct {
		va, vb *columnView
	}
	var combos []combo
	for i := range a.views {
		for j := range b.views {
			combos = append(combos, combo{&a.views[i], &b.views[j]})
		}
	}
	// Exact/exact first, then the fewest transforms.
	sort.SliceStable(combos, func(i, j int) bool {
		return viewCost(combos[i].va)+viewCost(combos[i].vb) < viewCost(combos[j].va)+viewCost(combos[j].vb)
	})
	for _, c := range combos {
		both, held, distinct := agreement(c.va.values, c.vb.values)
		if both < minRelationObserved || !holds(held, both, cfg) || distinct < cfg.minDistinct {
			continue
		}
		spec := repo.CorrelationSpec{
			Left:     repo.CorrelationSelector{Service: a.pos.Service, Span: a.pos.Span, Attr: a.pos.Attr, Extract: c.va.extract},
			Right:    repo.CorrelationSelector{Service: b.pos.Service, Span: b.pos.Span, Attr: b.pos.Attr, Extract: c.vb.extract},
			Relation: repo.CorrelationEquals,
		}
		transform := transformName(c.va, c.vb)
		if cand, ok := verifyCandidate(spec, transform, a, b, samples, cfg); ok {
			return cand, true
		}
	}
	// No view agrees: maybe one value is embedded in the other (an id inside a
	// URL or path).
	if spec, ok := containmentSpec(a, b, cfg); ok {
		if cand, ok := verifyCandidate(spec, relationTransformContains, a, b, samples, cfg); ok {
			return cand, true
		}
	}
	if spec, ok := containmentSpec(b, a, cfg); ok {
		// Keep the anchor on the left even when the anchor is the embedded value.
		spec.Left, spec.Right = spec.Right, spec.Left
		if cand, ok := verifyCandidate(spec, relationTransformContains, a, b, samples, cfg); ok {
			return cand, true
		}
	}
	return relationCandidate{}, false
}

func viewCost(v *columnView) int {
	switch v.name {
	case relationTransformExact:
		return 0
	case relationTransformCore:
		return 1
	default:
		return 2
	}
}

func transformName(va, vb *columnView) string {
	switch {
	case va.name == relationTransformExact && vb.name == relationTransformExact:
		return relationTransformExact
	case va.name == relationTransformDigits || vb.name == relationTransformDigits:
		return relationTransformDigits
	default:
		return relationTransformCore
	}
}

// containmentSpec derives a rule for "inner's value appears inside container's
// value" - e.g. an account id inside url.full. The extract regex is built from
// the text that always surrounds the embedded value across samples, so it
// pins the position rather than matching the id anywhere in the string.
func containmentSpec(container, inner *attrColumn, cfg relationMiningConfig) (repo.CorrelationSpec, bool) {
	var befores, afters []string
	both, held := 0, 0
	for i, outer := range container.values {
		in, ok := inner.values[i]
		if !ok {
			continue
		}
		both++
		if len(in) < 3 {
			continue
		}
		idx := strings.Index(outer, in)
		if idx < 0 {
			continue
		}
		held++
		befores = append(befores, outer[:idx])
		afters = append(afters, outer[idx+len(in):])
	}
	if both < minRelationObserved || !holds(held, both, cfg) {
		return repo.CorrelationSpec{}, false
	}
	leftCtx := commonSuffix(befores)
	rightCtx := commonPrefix(afters)
	if leftCtx == "" && rightCtx == "" {
		return repo.CorrelationSpec{}, false
	}
	pattern := regexp.QuoteMeta(leftCtx) + "(" + valueClass(inner.values) + ")" + regexp.QuoteMeta(rightCtx)
	if allEmpty(befores) {
		pattern = "^" + pattern
	}
	if allEmpty(afters) {
		pattern += "$"
	}
	return repo.CorrelationSpec{
		Left:     repo.CorrelationSelector{Service: container.pos.Service, Span: container.pos.Span, Attr: container.pos.Attr, Extract: pattern},
		Right:    repo.CorrelationSelector{Service: inner.pos.Service, Span: inner.pos.Span, Attr: inner.pos.Attr},
		Relation: repo.CorrelationEquals,
	}, true
}

func allEmpty(values []string) bool {
	for _, v := range values {
		if v != "" {
			return false
		}
	}
	return true
}

// Replays the rule exactly as the guardrail would enforce it, so the numbers
// shown to the operator are what applying it means. Fills in name, rationale
// and examples.
func verifyCandidate(spec repo.CorrelationSpec, transform string, a, b *attrColumn, samples []relationSample, cfg relationMiningConfig) (relationCandidate, bool) {
	stats := replaySpec(spec, samples)
	if stats.observed < minRelationObserved || !holds(stats.held, stats.observed, cfg) || stats.distinct < cfg.minDistinct {
		return relationCandidate{}, false
	}
	transport := transportPair(a, b)
	spec.Name = relationName(spec)
	spec.Severity = relationSeverity(a, b, transport)
	spec.Why = relationWhy(spec, transform, transport, stats.observed, stats.distinct)
	return relationCandidate{
		spec:      spec,
		transform: transform,
		observed:  stats.observed,
		held:      stats.held,
		distinct:  stats.distinct,
		examples:  stats.examples,
		transport: transport,
		score:     relationScore(a, b, transform, transport, stats),
	}, true
}

// replayStats is what a spec does on stored samples.
type replayStats struct {
	observed        int
	held            int
	distinct        int
	examples        []repo.RecommendationExample
	counterExamples []repo.RecommendationExample
}

// Hold counts and a few concrete examples of the rule on the stored samples.
func replaySpec(spec repo.CorrelationSpec, samples []relationSample) replayStats {
	var st replayStats
	seen := map[string]struct{}{}
	for _, sample := range samples {
		if sample.full == nil {
			continue
		}
		res := evaluateCorrelation(sample.full, spec)
		if !res.evaluated {
			continue
		}
		st.observed++
		ex := repo.RecommendationExample{TraceID: sample.full.TraceID, LeftValue: res.left, RightValue: res.right, ObservedAt: sample.observedAt}
		if res.held {
			st.held++
			seen[res.left] = struct{}{}
			if len(st.examples) < maxRecommendationExamples {
				st.examples = append(st.examples, ex)
			}
		} else if len(st.counterExamples) < maxRecommendationExamples {
			st.counterExamples = append(st.counterExamples, ex)
		}
	}
	st.distinct = len(seen)
	return st
}

// Large enough to rank every transport pair after every business relation.
const transportScorePenalty = 100

// Ranking: a cross-service, root-anchored relation on captured arguments is
// the strongest security signal; a call seen from its client and server side
// is the weakest.
func relationScore(a, b *attrColumn, transform string, transport bool, st replayStats) float64 {
	score := 0.0
	if transport {
		score -= transportScorePenalty
	}
	if !strings.EqualFold(a.pos.Service, b.pos.Service) {
		score += 3
	}
	if a.depth <= 1 {
		score += 2
	}
	if semconv.IsProbeAttr(a.pos.Attr) || semconv.IsProbeAttr(b.pos.Attr) {
		score += 1
	}
	switch transform {
	case relationTransformExact:
		score += 1
	case relationTransformContains:
		score -= 1
	}
	if sameCallPair(a, b) {
		score -= 2
	}
	if st.observed > 0 {
		score += float64(st.held) / float64(st.observed)
	}
	d := st.distinct
	if d > 20 {
		d = 20
	}
	score += float64(d) / 20
	return score
}

// sameCallPair is a CLIENT span and the SERVER span one hop below it: the two
// ends of one RPC, whose attributes mirror each other by definition.
func sameCallPair(a, b *attrColumn) bool {
	if a.depth+1 != b.depth && b.depth+1 != a.depth {
		return false
	}
	return (a.kind == tracepb.Span_SPAN_KIND_CLIENT && b.kind == tracepb.Span_SPAN_KIND_SERVER) ||
		(b.kind == tracepb.Span_SPAN_KIND_CLIENT && a.kind == tracepb.Span_SPAN_KIND_SERVER)
}

// Crossing a service boundary is the classic authorization-bypass surface;
// in-service starts a notch lower; transport pairs are integrity tripwires
// and start low. Editable before applying.
func relationSeverity(a, b *attrColumn, transport bool) string {
	switch {
	case transport:
		return "low"
	case !strings.EqualFold(a.pos.Service, b.pos.Service):
		return "critical"
	default:
		return "high"
	}
}

// relationName is the short rule name shown in the guardrail editor and in
// violation findings.
func relationName(spec repo.CorrelationSpec) string {
	return fmt.Sprintf("%s %s matches %s %s", spec.Left.Span, spec.Left.Attr, spec.Right.Span, spec.Right.Attr)
}

// Rationale stored on the applied rule: what was learned, from how much
// evidence, and what a violation means.
func relationWhy(spec repo.CorrelationSpec, transform string, transport bool, observed, distinct int) string {
	how := "carried the same value"
	switch transform {
	case relationTransformCore:
		how = "carried the same value once the constant prefix/suffix is removed"
	case relationTransformDigits:
		how = "carried the same numeric id"
	case relationTransformContains:
		how = "embedded the same identifier"
	}
	consequence := "A trace where they differ means the value acted on downstream is not the one established upstream."
	if transport {
		consequence = "The transport copies this field between the two ends, so a trace where they differ means " +
			"in-flight tampering or misattribution (a rewriting proxy, a broker or client fault), not a business-logic bypass."
	}
	return fmt.Sprintf("Learned from %d traces with %d distinct values: %s/%s %s and %s/%s %s always %s. %s",
		observed, distinct,
		spec.Left.Service, spec.Left.Span, spec.Left.Attr,
		spec.Right.Service, spec.Right.Span, spec.Right.Attr, how, consequence)
}
