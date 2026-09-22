package engine

import (
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/odigos-io/odigos-enterprise/insights/internal/engine/semconv"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

// d4ArgsReturns scores the arguments and return values captured by Odigos
// custom instrumentation against a learned per-callsite value profile. The
// input is method-level: the Java eBPF agent (and equivalents) attach the
// probed site (code.namespace + code.function) plus the stringified positional
// arguments (arg.0..arg.4) and the return value (return.value) to a span. For
// each (site, position) we learn the value's SHAPE - its format class(es), a
// bounded set of literal values when cardinality is low, and whether it can be
// null - then flag a promoted callsite whose argument/return takes on a new
// format, a value outside a learned small enum, or a call to a site never seen
// during learning. This turns "getUser(id)" always called with a numeric id
// into a detector for getUser("1 OR 1=1"), and a role parameter always seen as
// {"user","admin"} into a detector for role="superadmin".
//
// It is a set-style (non-additive) class: the baseline only grows (new sites,
// new formats, new enum values), so promotion converges on stability and the
// stored value is a canonical, union-safe decomposition (see the baseline
// codec's D4 members). D4's base signal is injection-weighted; the
// d4_injection_signature enricher escalates when a deviating value additionally
// carries an injection-like pattern (SQLi, traversal, command/template
// injection, SSRF).
type d4ArgsReturns struct{}

func (d4ArgsReturns) Class() repo.DeviationClass { return repo.D4ArgsReturns }

// d4EnumCap bounds how many distinct literal values a (site, position) keeps as
// an enforceable enum. Past this the position is considered open (free-valued)
// and only its format is enforced - this both avoids learning high-cardinality
// / PII values and keeps the stored baseline small.
const d4EnumCap = 12

// d4SampleMax caps how much of a deviating value is retained in evidence. The
// producer already truncates captures (~127 chars); this is a defensive bound.
const d4SampleMax = 256

// retPosition is the pseudo-position key used for a method's return value so it
// shares the argument profile machinery.
const retPosition = "ret"

// Format classes D4 infers for a captured value. A position's baseline is the
// SET of classes it was ever observed with; "freeform" is the catch-all and, if
// ever learned, disables format enforcement for that position (it has proven
// unstructured).
const (
	d4FmtNull     = "null"
	d4FmtBool     = "bool"
	d4FmtNumeric  = "numeric"
	d4FmtUUID     = "uuid"
	d4FmtEmail    = "email"
	d4FmtURL      = "url"
	d4FmtIP       = "ipaddr"
	d4FmtJSON     = "json"
	d4FmtPath     = "path"
	d4FmtToken    = "token" // bare identifier: letters/digits/_/- only
	d4FmtFreeform = "freeform"
)

// d4Profile is the learned shape of one (site, position): the set of observed
// format classes, an optional bounded enum of literal values, and whether the
// enum has been "opened" (too many distinct values to enforce).
type d4Profile struct {
	Formats  []string `json:"formats"`            // sorted set of format classes
	Values   []string `json:"values,omitempty"`   // sorted bounded enum; empty once EnumOpen
	EnumOpen bool     `json:"enumOpen,omitempty"` // distinct values exceeded d4EnumCap
}

// d4Site is one probed method's profiles: per positional-argument index (as a
// string key so the JSON is deterministic) plus an optional return profile.
type d4Site struct {
	Args map[string]d4Profile `json:"args,omitempty"`
	Ret  *d4Profile           `json:"ret,omitempty"`
}

// d4Data is the class baseline: probed sites keyed by "namespace.function".
//
// Seen records that the class has observed at least one trace, even one that
// carried nothing to profile (no custom-instrumentation captures). It is the
// D4 analogue of D7's zero-payload learning: without it, a transaction whose
// spans never expose arguments/returns - most commonly one whose service isn't
// custom-instrumented at all - would leave D4's baseline permanently empty, so
// it could never "grow once" and would stay in learning forever, blocking the
// whole transaction from promoting (see promotion.go's baselineGrewOnce gate).
// Setting Seen on the first observation lets the baseline grow off the empty
// seed and promote as a "zero value" while Score's cold-start guard (no sites =>
// no deviation) keeps a seen-but-empty baseline silent.
type d4Data struct {
	Sites map[string]d4Site `json:"sites,omitempty"`
	Seen  bool              `json:"seen,omitempty"`
}

// d4Observation is what one custom-instrumented span contributes: the site, the
// span it came from (for highlight attribution), the populated argument values
// by index, and the return value.
type d4Observation struct {
	Site   string
	SpanID string
	Args   map[int]string
	Ret    string
	HasRet bool
}

// d4Violation is one deviating value in the anomalous trace.
type d4Violation struct {
	Site     string   `json:"site"`
	SpanID   string   `json:"spanId,omitempty"`
	Kind     string   `json:"kind"`               // "new_site" | "arg" | "ret"
	Index    int      `json:"index,omitempty"`    // positional index for kind=arg
	Observed string   `json:"observed,omitempty"` // truncated offending value
	Format   string   `json:"format,omitempty"`   // observed format class
	Baseline []string `json:"baseline,omitempty"` // learned format classes at this position
	Enum     bool     `json:"enum,omitempty"`     // true => value violated a learned enum (format matched)
}

// d4Fingerprint is the stable, per-deviation identity that drives anomaly
// dedup. The engine's anomalySignature reads only evidence keys prefixed with
// "new_" (see offendingItems), folding firings that share the same items into
// one issue. It deliberately omits the per-trace span id and the raw offending
// value: repeated firings of the same deviation collapse into one issue, while
// genuinely different deviations stay distinct - a format change (getUser arg0
// -> freeform) and a traversal (getUser arg0 -> path) become two issues, as do
// arg vs return and different call sites. Two different SQLi payloads (both
// freeform) still dedup, which is the intended aggregation.
type d4Fingerprint struct {
	Site   string `json:"site"`
	Kind   string `json:"kind"`
	Index  int    `json:"index,omitempty"`
	Format string `json:"format,omitempty"`
	Enum   bool   `json:"enum,omitempty"`
}

type d4Evidence struct {
	Violations []d4Violation `json:"violations"`
	// NewDeviations mirrors Violations as dedup keys under the engine's "new_*"
	// evidence convention. Kept separate from Violations so Explain/EnrichRisk
	// keep the rich per-trace detail while the signature stays stable.
	NewDeviations []d4Fingerprint `json:"new_deviations,omitempty"`
}

// d4Fingerprints projects violations onto their stable dedup identities.
func d4Fingerprints(vs []d4Violation) []d4Fingerprint {
	if len(vs) == 0 {
		return nil
	}
	out := make([]d4Fingerprint, 0, len(vs))
	for _, v := range vs {
		out = append(out, d4Fingerprint{
			Site:   v.Site,
			Kind:   v.Kind,
			Index:  v.Index,
			Format: v.Format,
			Enum:   v.Enum,
		})
	}
	return out
}

// ---------- scoring ----------

func (s d4ArgsReturns) Score(t *Trace, current json.RawMessage) (bool, json.RawMessage, error) {
	return s.decodeBaseline(current).score(t)
}

// decodeBaseline implements baselineDecoder.
func (d4ArgsReturns) decodeBaseline(raw json.RawMessage) decodedBaseline {
	d, _ := decodeD4(raw)
	return d
}

// score implements decodedBaseline; it only reads base.
func (base d4Data) score(t *Trace) (bool, json.RawMessage, error) {
	if len(base.Sites) == 0 {
		return false, nil, nil // cold start: nothing learned, nothing to deviate from
	}
	obs := argSitesFromTrace(t)
	if len(obs) == 0 {
		return false, nil, nil
	}

	var violations []d4Violation
	for _, o := range obs {
		site, known := base.Sites[o.Site]
		if !known {
			violations = append(violations, d4Violation{Site: o.Site, SpanID: o.SpanID, Kind: "new_site"})
			continue
		}
		for _, i := range sortedIntKeys(o.Args) {
			p, ok := site.Args[strconv.Itoa(i)]
			if !ok {
				// A position never populated during learning: learn it rather
				// than flag it (optional/overloaded args would be pure noise).
				continue
			}
			if v, bad := checkD4Profile(p, o.Args[i]); bad {
				v.Site, v.SpanID, v.Kind, v.Index = o.Site, o.SpanID, "arg", i
				violations = append(violations, v)
			}
		}
		if o.HasRet && site.Ret != nil {
			if v, bad := checkD4Profile(*site.Ret, o.Ret); bad {
				v.Site, v.SpanID, v.Kind = o.Site, o.SpanID, "ret"
				violations = append(violations, v)
			}
		}
	}
	if len(violations) == 0 {
		return false, nil, nil
	}
	ev, err := json.Marshal(d4Evidence{Violations: violations, NewDeviations: d4Fingerprints(violations)})
	if err != nil {
		return true, nil, err
	}
	return true, ev, nil
}

// checkD4Profile reports whether a value deviates from a learned position
// profile, returning a partly-filled violation (the caller sets site/kind/idx).
// Format is enforced unless the position ever saw freeform (proven
// unstructured); an in-format value is then additionally checked against a
// bounded enum when one was learned.
func checkD4Profile(p d4Profile, raw string) (d4Violation, bool) {
	f := classifyD4Format(raw)
	formatOpen := containsStr(p.Formats, d4FmtFreeform)
	if !formatOpen && !containsStr(p.Formats, f) {
		return d4Violation{Observed: d4Sample(raw), Format: f, Baseline: p.Formats}, true
	}
	// Enum enforcement applies only to categorical (token) values, never to
	// naturally high-cardinality formats (ids, uuids, emails, urls…) that would
	// otherwise flag every unseen-but-normal value.
	if d4Enumerable(f) && !p.EnumOpen && len(p.Values) > 0 && !containsStr(p.Values, raw) {
		return d4Violation{Observed: d4Sample(raw), Format: f, Baseline: p.Formats, Enum: true}, true
	}
	return d4Violation{}, false
}

// d4Enumerable reports whether a format is low-cardinality enough to learn as a
// bounded enum (role/status/type strings). High-cardinality formats (numeric,
// uuid, email, url, ipaddr, json, path) are format-checked only.
func d4Enumerable(format string) bool { return format == d4FmtToken }

// ---------- learning ----------

func (d4ArgsReturns) Merge(t *Trace, current json.RawMessage) (json.RawMessage, error) {
	merged := mergeD4Data(mustDecodeD4(current), d4DataFromTrace(t))
	// Every observed trace marks the class as having seen traffic, so the first
	// observation grows the baseline off the empty seed (Seen flips {} into
	// {"seen":true}) and the class can promote even when no arguments/returns are
	// ever captured. Subsequent captureless traces re-marshal to the same bytes,
	// so the stability window still closes. See d4Data.Seen.
	merged.Seen = true
	return json.Marshal(merged)
}

// d4DataFromTrace folds one trace's captured values into a fresh baseline.
func d4DataFromTrace(t *Trace) d4Data {
	out := d4Data{Sites: map[string]d4Site{}}
	for _, o := range argSitesFromTrace(t) {
		site := out.Sites[o.Site]
		for i, v := range o.Args {
			if site.Args == nil {
				site.Args = map[string]d4Profile{}
			}
			key := strconv.Itoa(i)
			site.Args[key] = observeD4Profile(site.Args[key], v)
		}
		if o.HasRet {
			var rp d4Profile
			if site.Ret != nil {
				rp = *site.Ret
			}
			rp = observeD4Profile(rp, o.Ret)
			site.Ret = &rp
		}
		out.Sites[o.Site] = site
	}
	return out
}

// observeD4Profile folds one value into a position profile: add its format
// class, and (while the enum is still bounded) add the literal value, opening
// the enum once it exceeds the cap.
func observeD4Profile(p d4Profile, raw string) d4Profile {
	f := classifyD4Format(raw)
	p.Formats = addSortedStr(p.Formats, f)
	if d4Enumerable(f) && !p.EnumOpen {
		if !containsStr(p.Values, raw) {
			p.Values = addSortedStr(p.Values, raw)
		}
		if len(p.Values) > d4EnumCap {
			p.EnumOpen = true
			p.Values = nil
		}
	}
	return p
}

// mergeD4Data unions two baselines (sites, formats, enum values), honoring the
// enum cap. Used by both Merge (base + trace) and the classMerger (two stored
// baselines), so in-memory and DB-converged values stay byte-identical.
func mergeD4Data(a, b d4Data) d4Data {
	out := d4Data{Sites: map[string]d4Site{}, Seen: a.Seen || b.Seen}
	for site := range a.Sites {
		out.Sites[site] = d4Site{}
	}
	for site := range b.Sites {
		out.Sites[site] = d4Site{}
	}
	for site := range out.Sites {
		sa, sb := a.Sites[site], b.Sites[site]
		merged := d4Site{}
		positions := map[string]struct{}{}
		for k := range sa.Args {
			positions[k] = struct{}{}
		}
		for k := range sb.Args {
			positions[k] = struct{}{}
		}
		if len(positions) > 0 {
			merged.Args = map[string]d4Profile{}
			for k := range positions {
				merged.Args[k] = mergeD4Profile(sa.Args[k], sb.Args[k])
			}
		}
		if sa.Ret != nil || sb.Ret != nil {
			rp := mergeD4Profile(deref(sa.Ret), deref(sb.Ret))
			merged.Ret = &rp
		}
		out.Sites[site] = merged
	}
	return out
}

func mergeD4Profile(a, b d4Profile) d4Profile {
	out := d4Profile{Formats: mergeSortedStr(a.Formats, b.Formats)}
	out.EnumOpen = a.EnumOpen || b.EnumOpen
	if !out.EnumOpen {
		out.Values = mergeSortedStr(a.Values, b.Values)
		if len(out.Values) > d4EnumCap {
			out.EnumOpen = true
			out.Values = nil
		}
	}
	return out
}

// ---------- risk enrichment ----------

// d4InjectionKey is the editable per-scope signature list backing the
// d4_injection_signature enricher.
const d4InjectionKey repo.EnricherListKey = "injection_signatures"

// EnrichRisk implements RiskEnricher: it escalates a D4 deviation when a
// deviating value additionally matches a known injection signature (SQLi, path
// traversal, command/template injection, SSRF), turning "shape changed" into
// "shape changed AND looks like an attack payload".
func (d4ArgsReturns) EnrichRisk(evidence json.RawMessage, cfg enrichConfig) []risk.Signal {
	if len(evidence) == 0 {
		return nil
	}
	var ev d4Evidence
	if err := json.Unmarshal(evidence, &ev); err != nil {
		return nil
	}
	signatures := cfg.list(d4InjectionKey, risk.DefaultList(d4InjectionKey))
	var out []risk.Signal
	for _, v := range ev.Violations {
		if v.Observed == "" {
			continue
		}
		hay := strings.ToLower(v.Observed)
		for _, sig := range signatures {
			sig = strings.TrimSpace(sig)
			if sig == "" {
				continue
			}
			if strings.Contains(hay, strings.ToLower(sig)) {
				out = append(out, enricherSignal("d4_injection_signature", risk.CategoryInjection,
					fmt.Sprintf("%s carried an injection-like pattern (%q)", d4SiteLabel(v), sig)))
				break // one signal per deviating value
			}
		}
	}
	return out
}

// ---------- explain ----------

// Explain implements Explainer: it attributes each deviating value to the
// custom-instrumentation span it came from, tinting the span and pointing the
// UI at the specific arg.N / return.value attribute.
func (d4ArgsReturns) Explain(evidence json.RawMessage, spans []SpanView) Finding {
	f := Finding{
		Class:       repo.D4ArgsReturns,
		Kind:        FindingStructural,
		Title:       titleFor(repo.D4ArgsReturns),
		RawEvidence: evidence,
	}
	var ev d4Evidence
	_ = json.Unmarshal(evidence, &ev)
	for _, v := range ev.Violations {
		f.SpanHighlights = append(f.SpanHighlights, SpanHighlight{
			SpanID:   v.SpanID,
			Severity: "high",
			Reason:   d4ViolationReason(v),
		})
		if key, ok := d4AttrKey(v); ok {
			f.AttrHighlights = append(f.AttrHighlights, AttrHighlight{
				SpanID: v.SpanID,
				Key:    key,
				Value:  v.Observed,
			})
		}
	}
	f.Summary = summarizeD4(ev.Violations)
	return f
}

func d4ViolationReason(v d4Violation) string {
	switch v.Kind {
	case "new_site":
		return fmt.Sprintf("%s: method not seen during learning", v.Site)
	case "ret":
		if v.Enum {
			return fmt.Sprintf("%s return value outside learned set", v.Site)
		}
		return fmt.Sprintf("%s return value is %s, expected %s", v.Site, v.Format, strings.Join(v.Baseline, "/"))
	default: // arg
		if v.Enum {
			return fmt.Sprintf("%s arg %d outside learned set", v.Site, v.Index)
		}
		return fmt.Sprintf("%s arg %d is %s, expected %s", v.Site, v.Index, v.Format, strings.Join(v.Baseline, "/"))
	}
}

func d4SiteLabel(v d4Violation) string {
	switch v.Kind {
	case "ret":
		return v.Site + " return value"
	case "arg":
		return fmt.Sprintf("%s arg %d", v.Site, v.Index)
	default:
		return v.Site
	}
}

// d4AttrKey is the span attribute a violation points at, and whether one exists
// (a new_site violation has no specific attribute).
func d4AttrKey(v d4Violation) (string, bool) {
	switch v.Kind {
	case "ret":
		return semconv.ReturnValue, true
	case "arg":
		return semconv.ArgKey(v.Index), true
	default:
		return "", false
	}
}

func summarizeD4(vs []d4Violation) string {
	if len(vs) == 0 {
		return "An argument or return value deviated from the learned shape."
	}
	for _, v := range vs {
		if v.Kind == "new_site" {
			return "A custom-instrumented method was called that wasn't seen during learning."
		}
	}
	if len(vs) == 1 {
		return "A captured argument or return value deviated from the learned shape."
	}
	return "Captured arguments or return values deviated from the learned shape."
}

// ---------- trace extraction ----------

// argSitesFromTrace collects one observation per custom-instrumentation span: a
// span is one iff it carries a probed code site (the probe marker). Spans
// without any captured argument or return value are skipped - the probe fired
// but exposed nothing for D4 to profile.
func argSitesFromTrace(t *Trace) []d4Observation {
	if t == nil {
		return nil
	}
	var out []d4Observation
	for _, s := range t.Spans {
		if s == nil {
			continue
		}
		site := d4SiteOf(s)
		if site == "" {
			continue
		}
		o := d4Observation{Site: site, SpanID: s.SpanID, Args: map[int]string{}}
		for i := 0; i < semconv.MaxCapturedArgs; i++ {
			if v, ok := argAttr(s, i); ok {
				o.Args[i] = v
			}
		}
		if rv := s.Attr(semconv.ReturnValue); rv != "" {
			o.Ret = rv
			o.HasRet = true
		}
		if len(o.Args) == 0 && !o.HasRet {
			continue
		}
		out = append(out, o)
	}
	return out
}

// d4SiteOf derives the probed callsite identity from a span's attributes,
// spanning the OTel code-attribute deprecation. It prefers the current, already
// fully-qualified code.function.name; otherwise it reconstructs the same value
// from the deprecated code.namespace + code.function pair the Java eBPF agent
// emits today. Returns "" when the span carries no probe marker (not a
// custom-instrumentation span).
func d4SiteOf(s *Span) string {
	if fqn := s.Attr(semconv.CodeFunctionName); fqn != "" {
		return fqn
	}
	fn := s.Attr(semconv.CodeFunction)
	if fn == "" {
		return ""
	}
	if ns := s.Attr(semconv.CodeNamespace); ns != "" {
		return ns + "." + fn
	}
	return fn
}

// argAttr reads positional argument i, accepting both the eBPF spelling
// ("arg.0") and the dotless form ("arg0") some extensions emit.
func argAttr(s *Span, i int) (string, bool) {
	if v := s.Attr(semconv.ArgKey(i)); v != "" {
		return v, true
	}
	if v := s.Attr(semconv.LegacyArgKey(i)); v != "" {
		return v, true
	}
	return "", false
}

// ---------- format classification ----------

var (
	d4reNumeric = regexp.MustCompile(`^[+-]?\d+(\.\d+)?$`)
	d4reUUID    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	d4reEmail   = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	d4reToken   = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// classifyD4Format maps a captured string to a single primary format class.
// Order matters: the most specific structured shapes win, with token (bare
// identifier) and freeform as the fallbacks. A value that is unstructured
// (spaces, quotes, SQL, shell metacharacters) lands in freeform - which is
// exactly the shape a normal id/enum argument should never take.
func classifyD4Format(v string) string {
	switch {
	case v == "null":
		return d4FmtNull
	case v == "true" || v == "false":
		return d4FmtBool
	case d4reNumeric.MatchString(v):
		return d4FmtNumeric
	case d4reUUID.MatchString(v):
		return d4FmtUUID
	case d4reEmail.MatchString(v):
		return d4FmtEmail
	case isD4URL(v):
		return d4FmtURL
	case net.ParseIP(v) != nil:
		return d4FmtIP
	case isD4JSON(v):
		return d4FmtJSON
	case isD4Path(v):
		return d4FmtPath
	case d4reToken.MatchString(v):
		return d4FmtToken
	default:
		return d4FmtFreeform
	}
}

func isD4URL(v string) bool {
	i := strings.Index(v, "://")
	if i <= 0 {
		return false
	}
	for _, r := range v[:i] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '+' || r == '.' || r == '-') {
			return false
		}
	}
	return true
}

func isD4JSON(v string) bool {
	v = strings.TrimSpace(v)
	if len(v) < 2 {
		return false
	}
	first, last := v[0], v[len(v)-1]
	return (first == '{' && last == '}') || (first == '[' && last == ']')
}

// isD4Path recognizes filesystem-ish paths without matching bare tokens: it
// requires a separator and no whitespace, so "/etc/passwd", "./x", "..\\y" and
// "a/b/c" qualify but "GET" does not.
func isD4Path(v string) bool {
	if v == "" || strings.ContainsAny(v, " \t\n") {
		return false
	}
	if !strings.ContainsAny(v, "/\\") {
		return false
	}
	return strings.HasPrefix(v, "/") || strings.HasPrefix(v, "./") ||
		strings.HasPrefix(v, "../") || strings.HasPrefix(v, ".\\") ||
		strings.HasPrefix(v, "..\\") || strings.Contains(v, "/") || strings.Contains(v, "\\")
}

// ---------- small helpers ----------

// ---------- baseline codec (union-safe members) ----------
//
// The ClickHouse backend merges set-class baselines by UNION of member strings
// (see Repo.ApplyBaselineDeltas), so D4 cannot store an aggregating profile as
// one opaque blob per position: two writers with different profiles for the
// same position would union to two conflicting members. Instead each atomic
// fact is its own member - one per (site, position, format), one per learned
// enum value, and a marker once the enum opens - which union correctly across
// writers and replicas. Decode folds them back into profiles, re-applying the
// enum cap so the reconstructed value is byte-identical to a fresh Merge.

// d4KindSeen is the Kind of the site-less sentinel member that records
// d4Data.Seen (the class has observed traffic). It carries no Site/Pos, so it
// unions cleanly alongside real per-position members and survives the DB
// round-trip and cross-replica merge that back the "zero value" promotion.
const d4KindSeen = "seen"

// d4Member is one atomic, union-safe baseline fact. Kind is "f" (a format class
// at this position), "v" (a learned enum value), "o" (the enum has opened), or
// "seen" (the site-less Seen sentinel; no Site/Pos/X).
type d4Member struct {
	Site string `json:"s"`
	Pos  string `json:"p"` // "0".."4" for an arg, "ret" for the return value
	Kind string `json:"k"`
	X    string `json:"x,omitempty"` // format class (Kind=f) or enum value (Kind=v)
}

// encodeD4Members decomposes a baseline into union-safe member strings.
func encodeD4Members(d d4Data) ([]string, error) {
	var out []string
	if d.Seen {
		b, err := json.Marshal(d4Member{Kind: d4KindSeen})
		if err != nil {
			return nil, err
		}
		out = append(out, string(b))
	}
	emit := func(site, pos string, p d4Profile) error {
		for _, f := range p.Formats {
			b, err := json.Marshal(d4Member{Site: site, Pos: pos, Kind: "f", X: f})
			if err != nil {
				return err
			}
			out = append(out, string(b))
		}
		if p.EnumOpen {
			b, err := json.Marshal(d4Member{Site: site, Pos: pos, Kind: "o"})
			if err != nil {
				return err
			}
			out = append(out, string(b))
			return nil
		}
		for _, v := range p.Values {
			b, err := json.Marshal(d4Member{Site: site, Pos: pos, Kind: "v", X: v})
			if err != nil {
				return err
			}
			out = append(out, string(b))
		}
		return nil
	}
	for _, site := range sortedKeys(d.Sites) {
		s := d.Sites[site]
		for _, pos := range sortedKeys(s.Args) {
			if err := emit(site, pos, s.Args[pos]); err != nil {
				return nil, err
			}
		}
		if s.Ret != nil {
			if err := emit(site, retPosition, *s.Ret); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// decodeD4Members folds union-safe members back into a canonical baseline,
// re-applying the enum cap so the result matches a fresh Merge byte-for-byte.
func decodeD4Members(members []string) (d4Data, error) {
	type acc struct {
		formats map[string]struct{}
		values  map[string]struct{}
		open    bool
	}
	// keyed by site then position
	byKey := map[string]map[string]*acc{}
	seen := false
	for _, m := range members {
		var dm d4Member
		if err := json.Unmarshal([]byte(m), &dm); err != nil {
			return d4Data{}, err
		}
		if dm.Kind == d4KindSeen {
			seen = true
			continue
		}
		if dm.Site == "" || dm.Pos == "" {
			continue
		}
		positions := byKey[dm.Site]
		if positions == nil {
			positions = map[string]*acc{}
			byKey[dm.Site] = positions
		}
		a := positions[dm.Pos]
		if a == nil {
			a = &acc{formats: map[string]struct{}{}, values: map[string]struct{}{}}
			positions[dm.Pos] = a
		}
		switch dm.Kind {
		case "f":
			a.formats[dm.X] = struct{}{}
		case "v":
			a.values[dm.X] = struct{}{}
		case "o":
			a.open = true
		}
	}

	out := d4Data{Sites: map[string]d4Site{}, Seen: seen}
	for site, positions := range byKey {
		s := d4Site{}
		for pos, a := range positions {
			p := d4Profile{Formats: sortedSet(a.formats)}
			if a.open || len(a.values) > d4EnumCap {
				p.EnumOpen = true
			} else {
				p.Values = sortedSet(a.values)
			}
			if pos == retPosition {
				rp := p
				s.Ret = &rp
				continue
			}
			if s.Args == nil {
				s.Args = map[string]d4Profile{}
			}
			s.Args[pos] = p
		}
		out.Sites[site] = s
	}
	return out, nil
}

func decodeD4(raw json.RawMessage) (d4Data, error) {
	if len(raw) == 0 {
		return d4Data{}, nil
	}
	var d d4Data
	if err := json.Unmarshal(raw, &d); err != nil {
		return d4Data{}, err
	}
	return d, nil
}

func mustDecodeD4(raw json.RawMessage) d4Data {
	d, _ := decodeD4(raw)
	return d
}

func deref(p *d4Profile) d4Profile {
	if p == nil {
		return d4Profile{}
	}
	return *p
}

func d4Sample(v string) string {
	if len(v) > d4SampleMax {
		return v[:d4SampleMax]
	}
	return v
}

// sortedKeys returns a map's string keys in sorted order (deterministic
// iteration for canonical encoding).
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedSet returns a string set as a sorted slice, nil when empty.
func sortedSet(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedIntKeys(m map[int]string) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// addSortedStr returns s with v inserted if absent, keeping it sorted+unique.
func addSortedStr(s []string, v string) []string {
	if containsStr(s, v) {
		return s
	}
	s = append(s, v)
	sort.Strings(s)
	return s
}

// mergeSortedStr unions two string sets into a sorted, unique slice.
func mergeSortedStr(a, b []string) []string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(a)+len(b))
	for _, v := range a {
		set[v] = struct{}{}
	}
	for _, v := range b {
		set[v] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
