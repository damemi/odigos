package engine

import (
	"bytes"
	"encoding/json"
	"testing"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// d4Trace builds a trace whose probed child span carries the given custom
// instrumentation attributes (code.namespace / code.function / arg.N /
// return.value), mirroring what the Java eBPF agent emits.
func d4Trace(attrs map[string]string) *Trace {
	return mkTrace(
		withRoot("GET /users", "users-svc", tracepb.Span_SPAN_KIND_SERVER, nil),
		withChild("root", "getUser", tracepb.Span_SPAN_KIND_INTERNAL, attrs, "ebpf-java"),
	)
}

func probe(ns, fn string, kv ...string) map[string]string {
	m := map[string]string{"code.namespace": ns, "code.function": fn}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

func learnD4(t *testing.T, traces ...*Trace) json.RawMessage {
	t.Helper()
	var raw json.RawMessage
	for _, tr := range traces {
		merged, err := (d4ArgsReturns{}).Merge(tr, raw)
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		raw = merged
	}
	return raw
}

func TestD4_EmptyBaselineNeverDeviates(t *testing.T) {
	tr := d4Trace(probe("com.acme.UserService", "getUser", "arg.0", "42"))
	if dev, _, _ := (d4ArgsReturns{}).Score(tr, nil); dev {
		t.Errorf("empty baseline must not deviate")
	}
}

func TestD4_KnownShapeNoDeviation(t *testing.T) {
	baseline := learnD4(t,
		d4Trace(probe("com.acme.UserService", "getUser", "arg.0", "42")),
		d4Trace(probe("com.acme.UserService", "getUser", "arg.0", "1001")),
	)
	dev, _, _ := (d4ArgsReturns{}).Score(
		d4Trace(probe("com.acme.UserService", "getUser", "arg.0", "77")), baseline)
	if dev {
		t.Errorf("a numeric id against a numeric baseline must not deviate")
	}
}

// TestD4_DistinctDeviationsDistinctSignatures guards the anomaly-dedup contract:
// D4 evidence must expose its deviations under a "new_*" key so the engine's
// anomalySignature tells genuinely different attacks apart. A SQLi payload
// (numeric -> freeform) and a traversal payload (numeric -> path) on the same
// call site are distinct issues; two SQLi payloads (both freeform) fold into
// one. Before this, all D4 firings for a transaction collapsed into a single
// issue and only the newest showed.
func TestD4_DistinctDeviationsDistinctSignatures(t *testing.T) {
	baseline := learnD4(t,
		d4Trace(probe("com.acme.UserService", "getUser", "arg.0", "42")),
		d4Trace(probe("com.acme.UserService", "getUser", "arg.0", "1001")),
	)
	score := func(id string) json.RawMessage {
		dev, ev, err := (d4ArgsReturns{}).Score(
			d4Trace(probe("com.acme.UserService", "getUser", "arg.0", id)), baseline)
		if err != nil || !dev {
			t.Fatalf("expected deviation for %q (dev=%v err=%v)", id, dev, err)
		}
		return ev
	}
	sqli := score("1 OR 1=1")           // freeform
	sqli2 := score("2 OR 2=2")          // freeform (different payload, same shape)
	traversal := score("../../etc/pwd") // path

	const txID = int64(7)
	sig := func(ev json.RawMessage) string {
		s, _ := anomalySignature(txID, []repo.DeviationClass{repo.D4ArgsReturns},
			map[string]json.RawMessage{string(repo.D4ArgsReturns): ev})
		return s
	}
	if sig(sqli) == sig(traversal) {
		t.Errorf("SQLi (freeform) and traversal (path) must have distinct signatures")
	}
	if sig(sqli) != sig(sqli2) {
		t.Errorf("two freeform payloads on the same site must dedup to one signature")
	}
}

func TestD4_FormatViolationDeviates(t *testing.T) {
	baseline := learnD4(t,
		d4Trace(probe("com.acme.UserService", "getUser", "arg.0", "42")),
		d4Trace(probe("com.acme.UserService", "getUser", "arg.0", "1001")),
	)
	dev, ev, _ := (d4ArgsReturns{}).Score(
		d4Trace(probe("com.acme.UserService", "getUser", "arg.0", "1 OR 1=1")), baseline)
	if !dev {
		t.Fatalf("expected deviation when a numeric arg becomes freeform")
	}
	if !contains(string(ev), "getUser") || !contains(string(ev), "freeform") {
		t.Errorf("evidence missing site/format details: %s", ev)
	}
}

func TestD4_EnumViolationDeviates(t *testing.T) {
	// A role argument only ever seen as user/admin (both token format): a new
	// in-format value must still deviate as an enum violation.
	baseline := learnD4(t,
		d4Trace(probe("com.acme.Authz", "checkRole", "arg.0", "user")),
		d4Trace(probe("com.acme.Authz", "checkRole", "arg.0", "admin")),
		d4Trace(probe("com.acme.Authz", "checkRole", "arg.0", "user")),
	)
	dev, ev, _ := (d4ArgsReturns{}).Score(
		d4Trace(probe("com.acme.Authz", "checkRole", "arg.0", "superadmin")), baseline)
	if !dev {
		t.Fatalf("expected deviation for a value outside the learned enum")
	}
	if !contains(string(ev), `"enum":true`) {
		t.Errorf("expected enum violation flagged: %s", ev)
	}
}

func TestD4_NewCallSiteDeviates(t *testing.T) {
	baseline := learnD4(t, d4Trace(probe("com.acme.UserService", "getUser", "arg.0", "42")))
	tr := mkTrace(
		withRoot("GET /users", "users-svc", tracepb.Span_SPAN_KIND_SERVER, nil),
		withChild("root", "dropTable", tracepb.Span_SPAN_KIND_INTERNAL,
			probe("com.acme.Admin", "dropTable", "arg.0", "users"), "ebpf-java"),
	)
	dev, ev, _ := (d4ArgsReturns{}).Score(tr, baseline)
	if !dev {
		t.Fatalf("expected deviation for a callsite not seen during learning")
	}
	if !contains(string(ev), "new_site") || !contains(string(ev), "com.acme.Admin.dropTable") {
		t.Errorf("evidence missing new_site details: %s", ev)
	}
}

func TestD4_ReturnShapeChangeDeviates(t *testing.T) {
	baseline := learnD4(t,
		d4Trace(probe("com.acme.UserService", "isAdmin", "return.value", "false")),
		d4Trace(probe("com.acme.UserService", "isAdmin", "return.value", "true")),
	)
	dev, ev, _ := (d4ArgsReturns{}).Score(
		d4Trace(probe("com.acme.UserService", "isAdmin", "return.value", "{\"role\":\"root\"}")), baseline)
	if !dev {
		t.Fatalf("expected deviation when a boolean return becomes json")
	}
	if !contains(string(ev), `"kind":"ret"`) {
		t.Errorf("expected a return-value violation: %s", ev)
	}
}

func TestD4_UnprofiledArgIsLearnedNotFlagged(t *testing.T) {
	// arg.1 never appears during learning; when it later shows up it should be
	// learned (no baseline profile), not flagged.
	baseline := learnD4(t, d4Trace(probe("com.acme.UserService", "getUser", "arg.0", "42")))
	dev, _, _ := (d4ArgsReturns{}).Score(
		d4Trace(probe("com.acme.UserService", "getUser", "arg.0", "43", "arg.1", "extra")), baseline)
	if dev {
		t.Errorf("a never-learned arg position must not deviate on first sighting")
	}
}

func TestD4_EmptyCaptureMergeGrowsOnceThenStable(t *testing.T) {
	// A trace with no captured args/returns (e.g. a service with no custom
	// instrumentation) must still let D4 promote, like D7's zero-payload
	// learning: the FIRST such observation grows the baseline off the empty
	// seed (so baselineGrewOnce becomes true), but subsequent captureless
	// observations are byte-stable so the stability window can close instead of
	// the class learning forever.
	seed := json.RawMessage(`{}`)
	tr := mkTrace(withRoot("GET /users", "users-svc", tracepb.Span_SPAN_KIND_SERVER, nil))

	first, err := (d4ArgsReturns{}).Merge(tr, seed)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if bytes.Equal(first, seed) {
		t.Fatalf("first empty-capture merge must grow off the seed so the class can promote, got %q", first)
	}
	if !baselineGrew(d4ArgsReturns{}, seed, first) {
		t.Fatalf("first empty-capture merge must register as growth, seed=%q first=%q", seed, first)
	}

	second, err := (d4ArgsReturns{}).Merge(tr, first)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !bytes.Equal(second, first) {
		t.Errorf("subsequent empty-capture merges must be byte-stable, got %q then %q", first, second)
	}
	if baselineGrew(d4ArgsReturns{}, first, second) {
		t.Errorf("a repeat empty-capture merge must not register as growth")
	}
}

func TestD4_SeenEmptyBaselineNeverDeviates(t *testing.T) {
	// The safety property for customers NOT using custom instrumentation: a
	// baseline that has only ever seen captureless traffic promotes as a "zero
	// value" and must never flag anything - not even a later probed span with an
	// injection-looking argument - because no site was ever learned.
	empty := mkTrace(withRoot("GET /users", "users-svc", tracepb.Span_SPAN_KIND_SERVER, nil))
	baseline := learnD4(t, empty, empty, empty)
	if !contains(string(baseline), `"seen":true`) {
		t.Fatalf("seen-but-empty baseline should be marked seen, got %s", baseline)
	}
	probed := d4Trace(probe("com.acme.UserService", "getUser", "arg.0", "1 OR 1=1"))
	if dev, _, _ := (d4ArgsReturns{}).Score(probed, baseline); dev {
		t.Errorf("a seen-but-empty baseline (no sites learned) must never deviate")
	}
}

func TestD4_SeenEmptyCodecRoundTrip(t *testing.T) {
	// The Seen marker must survive the database member round-trip (and the
	// cross-replica union) so the zero-value promotion isn't lost on reload.
	empty := mkTrace(withRoot("GET /users", "users-svc", tracepb.Span_SPAN_KIND_SERVER, nil))
	baseline := learnD4(t, empty)
	codec := NewBaselineCodec()
	members, counters, err := codec.Encode(repo.D4ArgsReturns, baseline)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(members) != 1 || len(counters) != 0 {
		t.Fatalf("seen-empty baseline should encode to exactly one member and no counters, got members=%v counters=%d", members, len(counters))
	}
	got, err := codec.Decode(repo.D4ArgsReturns, members, nil)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(got, baseline) {
		t.Errorf("seen-empty codec round trip changed the baseline:\n in: %s\nout: %s", baseline, got)
	}
}

func TestD4_EnumOpensPastCap(t *testing.T) {
	var traces []*Trace
	for i := 0; i < d4EnumCap+5; i++ {
		traces = append(traces, d4Trace(probe("com.acme.Svc", "tag", "arg.0", "tag-"+string(rune('a'+i)))))
	}
	baseline := learnD4(t, traces...)

	var d d4Data
	if err := json.Unmarshal(baseline, &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	p := d.Sites["com.acme.Svc.tag"].Args["0"]
	if !p.EnumOpen || len(p.Values) != 0 {
		t.Fatalf("expected enum opened past cap, got open=%v values=%v", p.EnumOpen, p.Values)
	}
	// A brand-new (but same-format) value must NOT deviate once the enum opened.
	dev, _, _ := (d4ArgsReturns{}).Score(
		d4Trace(probe("com.acme.Svc", "tag", "arg.0", "tag-zzz")), baseline)
	if dev {
		t.Errorf("an open enum should not enforce membership")
	}
}

func TestD4_CodecRoundTrip(t *testing.T) {
	baseline := learnD4(t,
		d4Trace(probe("com.acme.UserService", "getUser", "arg.0", "42", "return.value", "ok")),
		d4Trace(probe("com.acme.Authz", "checkRole", "arg.0", "user")),
		d4Trace(probe("com.acme.Authz", "checkRole", "arg.0", "admin")),
	)
	codec := NewBaselineCodec()
	members, counters, err := codec.Encode(repo.D4ArgsReturns, baseline)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(counters) != 0 {
		t.Fatalf("D4 is a set class, expected no counters, got %d", len(counters))
	}
	got, err := codec.Decode(repo.D4ArgsReturns, members, nil)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(got, baseline) {
		t.Errorf("codec round trip changed the baseline:\n in: %s\nout: %s", baseline, got)
	}
}

func TestD4_MembersUnionMerge(t *testing.T) {
	// Two writers learn the same site with different formats; the union of their
	// members must reconstruct a single merged profile (formats unioned).
	a := learnD4(t, d4Trace(probe("com.acme.Svc", "f", "arg.0", "42")))      // numeric
	b := learnD4(t, d4Trace(probe("com.acme.Svc", "f", "arg.0", "abc-123"))) // token

	codec := NewBaselineCodec()
	ma, _, err := codec.Encode(repo.D4ArgsReturns, a)
	if err != nil {
		t.Fatalf("encode a: %v", err)
	}
	mb, _, err := codec.Encode(repo.D4ArgsReturns, b)
	if err != nil {
		t.Fatalf("encode b: %v", err)
	}
	merged, err := codec.Decode(repo.D4ArgsReturns, append(ma, mb...), nil)
	if err != nil {
		t.Fatalf("decode union: %v", err)
	}
	var d d4Data
	if err := json.Unmarshal(merged, &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := d.Sites["com.acme.Svc.f"].Args["0"].Formats
	if len(got) != 2 || !containsStr(got, d4FmtNumeric) || !containsStr(got, d4FmtToken) {
		t.Errorf("expected unioned formats [numeric token], got %v", got)
	}
}

func TestD4_EnrichRiskDetectsInjection(t *testing.T) {
	ev, _ := json.Marshal(d4Evidence{Violations: []d4Violation{
		{Site: "com.acme.UserService.getUser", Kind: "arg", Index: 0, Observed: "1' OR 1=1--", Format: d4FmtFreeform},
	}})
	sigs := (d4ArgsReturns{}).EnrichRisk(ev, enrichConfig{})
	if len(sigs) == 0 {
		t.Fatalf("expected an injection-signature signal for a SQLi-looking value")
	}
	if sigs[0].Source != "d4_injection_signature" {
		t.Errorf("unexpected signal source %q", sigs[0].Source)
	}
	if sigs[0].Weight == 0 {
		t.Errorf("injection enricher signal should carry a non-zero weight")
	}
}

func TestD4_EnrichRiskIgnoresBenignShapeChange(t *testing.T) {
	// A format change with no injection markers should NOT add an enricher
	// signal (the base D4 signal already covers "shape changed").
	ev, _ := json.Marshal(d4Evidence{Violations: []d4Violation{
		{Site: "com.acme.Svc.f", Kind: "arg", Index: 0, Observed: "hello world", Format: d4FmtFreeform},
	}})
	if sigs := (d4ArgsReturns{}).EnrichRisk(ev, enrichConfig{}); len(sigs) != 0 {
		t.Errorf("benign shape change must not emit an injection signal, got %+v", sigs)
	}
}

func TestD4_ExplainHighlightsSpanAndAttr(t *testing.T) {
	ev, _ := json.Marshal(d4Evidence{Violations: []d4Violation{
		{Site: "com.acme.UserService.getUser", SpanID: "c-getUser", Kind: "arg", Index: 0,
			Observed: "1 OR 1=1", Format: d4FmtFreeform, Baseline: []string{d4FmtNumeric}},
	}})
	spans := []SpanView{{SpanID: "c-getUser", Name: "getUser", Service: "users-svc"}}
	f := (d4ArgsReturns{}).Explain(ev, spans)
	if f.Class != repo.D4ArgsReturns || f.Summary == "" {
		t.Fatalf("unexpected finding: %+v", f)
	}
	if len(f.SpanHighlights) != 1 || f.SpanHighlights[0].SpanID != "c-getUser" {
		t.Errorf("expected span highlight on c-getUser, got %+v", f.SpanHighlights)
	}
	if len(f.AttrHighlights) != 1 || f.AttrHighlights[0].Key != "arg.0" {
		t.Errorf("expected arg.0 attr highlight, got %+v", f.AttrHighlights)
	}
}

func TestClassifyD4Format(t *testing.T) {
	cases := map[string]string{
		"42":                                   d4FmtNumeric,
		"-3.14":                                d4FmtNumeric,
		"true":                                 d4FmtBool,
		"null":                                 d4FmtNull,
		"3f2504e0-4f89-41d3-9a0c-0305e82c3301": d4FmtUUID,
		"alice@example.com":                    d4FmtEmail,
		"https://example.com/x":                d4FmtURL,
		"10.0.0.1":                             d4FmtIP,
		`{"a":1}`:                              d4FmtJSON,
		"/etc/passwd":                          d4FmtPath,
		"../../secret":                         d4FmtPath,
		"user_role-1":                          d4FmtToken,
		"1 OR 1=1":                             d4FmtFreeform,
		"'; DROP TABLE users; --":              d4FmtFreeform,
	}
	for in, want := range cases {
		if got := classifyD4Format(in); got != want {
			t.Errorf("classifyD4Format(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestD4_CodeFunctionNamePreferredAndCompatible(t *testing.T) {
	// Site identity must be stable across the OTel code-attribute deprecation:
	// the new fully-qualified code.function.name and the deprecated
	// code.namespace + code.function pair resolve to the same site, and the new
	// key wins when both are present.
	const want = "com.acme.UserService.getUser"
	if got := d4SiteOf(&Span{SpanAttrs: map[string]string{"code.function.name": want}}); got != want {
		t.Fatalf("code.function.name site = %q, want %q", got, want)
	}
	if got := d4SiteOf(&Span{SpanAttrs: map[string]string{"code.namespace": "com.acme.UserService", "code.function": "getUser"}}); got != want {
		t.Fatalf("deprecated pair site = %q, want %q", got, want)
	}
	if got := d4SiteOf(&Span{SpanAttrs: map[string]string{"code.function.name": want, "code.function": "other", "code.namespace": "wrong"}}); got != want {
		t.Fatalf("code.function.name must win, got %q", got)
	}

	// A baseline learned via the deprecated pair must not deviate when the same
	// call arrives under the new key (same site, numeric arg), but a broken
	// value under the new key still deviates.
	baseline := learnD4(t,
		d4Trace(probe("com.acme.UserService", "getUser", "arg.0", "42")),
		d4Trace(probe("com.acme.UserService", "getUser", "arg.0", "1001")),
	)
	ok := d4Trace(map[string]string{"code.function.name": want, "arg.0": "77"})
	if dev, _, _ := (d4ArgsReturns{}).Score(ok, baseline); dev {
		t.Errorf("same site via code.function.name must not deviate against a numeric baseline")
	}
	bad := d4Trace(map[string]string{"code.function.name": want, "arg.0": "1 OR 1=1"})
	if dev, _, _ := (d4ArgsReturns{}).Score(bad, baseline); !dev {
		t.Errorf("SQLi under code.function.name must deviate")
	}
}

func TestD4_ArgDotlessAlias(t *testing.T) {
	// Some extension instrumentations emit "arg0" rather than "arg.0".
	baseline := learnD4(t, d4Trace(probe("com.acme.Svc", "f", "arg0", "42")))
	dev, _, _ := (d4ArgsReturns{}).Score(
		d4Trace(probe("com.acme.Svc", "f", "arg0", "not-a-number!!")), baseline)
	if !dev {
		t.Errorf("expected deviation via the dotless arg alias")
	}
}
