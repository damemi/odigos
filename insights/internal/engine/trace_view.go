package engine

import (
	"encoding/hex"
	"sort"
	"strconv"
	"strings"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// KV is one rendered attribute. Value is already string-coerced so consumers
// don't need to understand OTel AnyValue.
type KV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// SpanEvent is a span "log" annotation. OffsetMs is relative to the root span
// start, matching the waterfall bar's reference.
type SpanEvent struct {
	Name     string  `json:"name"`
	OffsetMs float64 `json:"offsetMs"`
	Attrs    []KV    `json:"attrs,omitempty"`
}

// SpanView is a backend-neutral, JSON-serialisable view of one span: enough for
// any UI to draw a waterfall and inspect a span with no OTLP/proto knowledge on
// the client. It carries raw timing (absolute nanos plus offset/duration
// relative to the root) so a renderer can compute its own bar geometry.
// SpansFromOTLP returns these in pre-order, each tagged with its tree Depth.
type SpanView struct {
	SpanID        string      `json:"spanId"`
	ParentSpanID  string      `json:"parentSpanId,omitempty"`
	Name          string      `json:"name"`
	Service       string      `json:"service"`
	Kind          string      `json:"kind"` // SERVER / CLIENT / INTERNAL / ...
	Depth         int         `json:"depth"`
	StartUnixNano uint64      `json:"startUnixNano"`
	EndUnixNano   uint64      `json:"endUnixNano"`
	OffsetMs      float64     `json:"offsetMs"` // start relative to root start
	DurationMs    float64     `json:"durationMs"`
	Scope         string      `json:"scope,omitempty"`
	StatusCode    string      `json:"statusCode,omitempty"` // OK | ERROR | ""
	StatusMsg     string      `json:"statusMsg,omitempty"`
	Attrs         []KV        `json:"attrs,omitempty"`
	Events        []SpanEvent `json:"events,omitempty"`
}

// AttrMap re-materialises the flat attribute map for a span view so
// evidence-matching code (e.g. the D7 explainer) can reuse the scorers'
// attribute-keyed helpers against a rendered span.
func (v SpanView) AttrMap() map[string]string {
	m := make(map[string]string, len(v.Attrs))
	for _, kv := range v.Attrs {
		m[kv.Key] = kv.Value
	}
	return m
}

// SpansFromOTLP proto-decodes persisted raw OTLP bytes into rich per-span views
// for the debug UI. Structural ordering goes through decodeTraceTrees - the
// same tree-building core the scoring engine uses - so what the UI shows matches
// what the scorers saw, and skips the span selection DecodeRequest does for the
// ingest path. Status, events, attributes, and scope are sideloaded from
// the original proto spans (engine.Span keeps only a flat string map). Returns
// nil for empty / unparseable input.
func SpansFromOTLP(raw []byte) []SpanView {
	if len(raw) == 0 {
		return nil
	}
	var td tracepb.TracesData
	if err := proto.Unmarshal(raw, &td); err != nil {
		return nil
	}

	type pctx struct {
		sp    *tracepb.Span
		scope *commonpb.InstrumentationScope
	}
	byID := map[string]pctx{}
	for _, rs := range td.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				byID[hex.EncodeToString(sp.GetSpanId())] = pctx{sp: sp, scope: ss.GetScope()}
			}
		}
	}

	traces := decodeTraceTrees(&td)
	if len(traces) == 0 || traces[0].Root == nil {
		return nil
	}
	root := traces[0].Root
	rootStartNs := root.StartUnix

	// Siblings are linked in OTLP payload order (grouped by resource/scope), so
	// two children from different instrumentation scopes come out in scope-block
	// order rather than execution order. Sort each parent's children by start
	// time so the waterfall reflects when spans actually ran. Render-only: these
	// Span values are freshly decoded here and not shared with the scoring pass.
	var sortChildren func(n *Span)
	sortChildren = func(n *Span) {
		sort.SliceStable(n.Children, func(i, j int) bool {
			return n.Children[i].StartUnix < n.Children[j].StartUnix
		})
		for _, c := range n.Children {
			sortChildren(c)
		}
	}
	sortChildren(root)

	var out []SpanView
	var walk func(n *Span, depth int)
	walk = func(n *Span, depth int) {
		if n == nil {
			return
		}
		var offsetNs uint64
		if n.StartUnix > rootStartNs {
			offsetNs = n.StartUnix - rootStartNs
		}
		widthNs := uint64(1)
		if n.EndUnix > n.StartUnix {
			widthNs = n.EndUnix - n.StartUnix
		}

		v := SpanView{
			SpanID:        n.SpanID,
			ParentSpanID:  n.ParentSpanID,
			Name:          n.Name,
			Service:       n.Service,
			Kind:          shortKind(n.Kind),
			Depth:         depth,
			StartUnixNano: n.StartUnix,
			EndUnixNano:   n.EndUnix,
			OffsetMs:      float64(offsetNs) / 1e6,
			DurationMs:    float64(widthNs) / 1e6,
		}
		if p, ok := byID[n.SpanID]; ok {
			v.StatusCode, v.StatusMsg = statusFrom(p.sp.GetStatus())
			v.Attrs = attrsFrom(p.sp.GetAttributes())
			v.Events = eventsFrom(p.sp.GetEvents(), rootStartNs)
			if p.scope != nil {
				v.Scope = p.scope.GetName()
				if ver := p.scope.GetVersion(); ver != "" {
					v.Scope += "@" + ver
				}
			}
		}
		out = append(out, v)
		for _, c := range n.Children {
			walk(c, depth+1)
		}
	}
	walk(root, 0)
	return out
}

// shortKind strips the "SPAN_KIND_" prefix the proto enum's String() emits.
func shortKind(k tracepb.Span_SpanKind) string {
	return strings.TrimPrefix(k.String(), "SPAN_KIND_")
}

func statusFrom(s *tracepb.Status) (string, string) {
	if s == nil {
		return "", ""
	}
	switch s.GetCode() {
	case tracepb.Status_STATUS_CODE_OK:
		return "OK", s.GetMessage()
	case tracepb.Status_STATUS_CODE_ERROR:
		return "ERROR", s.GetMessage()
	default:
		return "", ""
	}
}

func attrsFrom(kvs []*commonpb.KeyValue) []KV {
	if len(kvs) == 0 {
		return nil
	}
	out := make([]KV, 0, len(kvs))
	for _, kv := range kvs {
		if kv == nil {
			continue
		}
		out = append(out, KV{Key: kv.GetKey(), Value: anyValueDisplay(kv.GetValue())})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func eventsFrom(events []*tracepb.Span_Event, rootStartNs uint64) []SpanEvent {
	if len(events) == 0 {
		return nil
	}
	out := make([]SpanEvent, 0, len(events))
	for _, e := range events {
		var offsetMs float64
		if e.GetTimeUnixNano() > rootStartNs {
			offsetMs = float64(e.GetTimeUnixNano()-rootStartNs) / 1e6
		}
		out = append(out, SpanEvent{
			Name:     e.GetName(),
			OffsetMs: offsetMs,
			Attrs:    attrsFrom(e.GetAttributes()),
		})
	}
	return out
}

// anyValueDisplay collapses an OTel AnyValue to a display string for the trace
// viewer: scalars exact, arrays as `[a, b, c]`, kvlists as `{k=v, ...}`, bytes
// as a short hex prefix, matching Jaeger's tag column. Distinct from the
// scorer-oriented anyToString in trace_decode.go, which keeps only the first
// array element.
func anyValueDisplay(v *commonpb.AnyValue) string {
	if v == nil {
		return ""
	}
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue
	case *commonpb.AnyValue_BoolValue:
		return strconv.FormatBool(x.BoolValue)
	case *commonpb.AnyValue_IntValue:
		return strconv.FormatInt(x.IntValue, 10)
	case *commonpb.AnyValue_DoubleValue:
		return strconv.FormatFloat(x.DoubleValue, 'g', -1, 64)
	case *commonpb.AnyValue_ArrayValue:
		parts := make([]string, 0, len(x.ArrayValue.GetValues()))
		for _, el := range x.ArrayValue.GetValues() {
			parts = append(parts, anyValueDisplay(el))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *commonpb.AnyValue_KvlistValue:
		parts := make([]string, 0, len(x.KvlistValue.GetValues()))
		for _, kv := range x.KvlistValue.GetValues() {
			parts = append(parts, kv.GetKey()+"="+anyValueDisplay(kv.GetValue()))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case *commonpb.AnyValue_BytesValue:
		b := x.BytesValue
		if len(b) > 16 {
			return hex.EncodeToString(b[:16]) + "…"
		}
		return hex.EncodeToString(b)
	}
	return ""
}
