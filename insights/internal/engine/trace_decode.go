package engine

import (
	"encoding/hex"
	"strconv"
	"strings"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/odigos-io/odigos/insights/internal/engine/semconv"
)

// Span is the engine's flattened view of an OTLP span, keeping only the fields
// the scorers need so the rest of the engine stays free of proto types.
type Span struct {
	SpanID       string
	ParentSpanID string // empty if root
	Service      string
	Name         string
	Kind         tracepb.Span_SpanKind
	StartUnix    uint64
	EndUnix      uint64
	StatusCode   tracepb.Status_StatusCode
	// StatusMessage is the span's status message; it is application text.
	StatusMessage string
	// SpanAttrs holds the span's own attributes. ResourceAttrs is the resource
	// (service, k8s pod, ...) the span came from, shared read-only by every span
	// of that resource in the batch. Read both through Attr.
	//
	// They used to be merged into one map per span. A resource carries dozens
	// of attributes and a batch holds hundreds of spans from the same handful of
	// resources, so that merge copied the same entries hundreds of times per
	// request - the single largest allocation on the ingest path, and the GC
	// churn that came with it. Sharing the resource map removes it entirely.
	SpanAttrs     map[string]string
	ResourceAttrs map[string]string
	// SourceIdentity is the engine's SourceIdentityFunc applied to
	// ResourceAttrs: the Odigos source the span came from. Computed once per
	// resource on decode and shared the same way. Nil on the render path,
	// which decodes without a SourceIdentityFunc.
	SourceIdentity map[string]string
	ScopeName      string // OTel instrumentation library name (D5 input)
	ScopeVersion   string

	Children []*Span // built after grouping
}

// Attr returns one attribute by key, preferring the span's own value over the
// resource's, or "" when neither has it. Decoding never stores empty values, so
// "" always means absent.
func (s *Span) Attr(key string) string {
	if v, ok := s.SpanAttrs[key]; ok {
		return v
	}
	return s.ResourceAttrs[key]
}

// Trace is a complete trace as the engine sees it: the root span plus every
// span hanging off it, keyed by trace_id. DurationUs is the root span duration.
//
// RawOTLP() returns the proto-encoded TracesData containing ONLY this trace's
// spans (plus their resource/scope). The scoring engine doesn't need it; it's
// carried through so the observation store can persist a lossless copy.
// Non-empty for every Trace from DecodeRequest, which drops traces whose spans
// it cannot pick back out of the batch.
type Trace struct {
	TraceID string
	Root    *Span
	// Spans is every span under Root, Root included: for a decoded trace the
	// whole trace, for a sub-trace the entry's whole subtree. It is not limited
	// to Root's service. It can hold spans of downstream services, and spans
	// of Root's own service from other invocations (a later call back into the
	// same service, through the same or a different entry span).
	Spans []*Span
	// ServiceInvocationSpans is the part of Spans in Root's own service
	// invocation: Root and its descendants down to the next service entry.
	// Set by subTraces (see serviceEntrySpans).
	ServiceInvocationSpans []*Span
	DurationUs             int64

	// otlpSpans is this trace's spans, sliced out of the batch they arrived in.
	// Kept in decoded form because a transaction keeps only a handful of traces
	// - a few examples while it learns, one evidence trace per issue - so the
	// overwhelming majority are scored, learned from and dropped without anyone
	// ever opening them. Encoding them for storage is left to RawOTLP.
	otlpSpans *tracepb.TracesData
	// otlpSpansFn builds a nested entry's standalone view of the trace on
	// demand, for the same reason. Set by subTraces.
	otlpSpansFn func() *tracepb.TracesData
	// rawOTLP caches what RawOTLP encoded, so opening a trace twice doesn't
	// encode it twice. All three fields are unexported so a reader cannot
	// bypass the lazy build; a (sub-)trace belongs to the goroutine processing
	// it, so they need no lock.
	rawOTLP []byte

	// CallerService is the service that called this (sub-)transaction's entry
	// (the nearest enclosing span in a different service). Empty for the trace
	// root (external ingress) and set only for nested sub-transactions by
	// subTraces. Service-guardrail evaluation uses it to police "who may call me"
	// without a separate edges table.
	CallerService string
}

// RawOTLP returns the spans behind the trace a user opens from a finding - the
// waterfall shown next to the baseline, and what Accept folds back in - encoded
// for storage. This is the only place a trace is encoded, and it runs on the
// first ask, so traffic the engine scores and discards is never encoded at all.
// Returns nil when the bytes cannot be produced, and the UI falls back as it
// always has.
func (t *Trace) RawOTLP() []byte {
	if t == nil {
		return nil
	}
	if t.rawOTLP != nil {
		return t.rawOTLP
	}
	if t.otlpSpansFn != nil {
		t.otlpSpans = t.otlpSpansFn()
		t.otlpSpansFn = nil
	}
	t.rawOTLP = marshalOTLP(t.otlpSpans)
	return t.rawOTLP
}

// marshalOTLP encodes a trace's spans for storage. Returns nil when there is
// nothing to encode or encoding fails; callers treat that the same as a trace
// with no stored payload.
func marshalOTLP(td *tracepb.TracesData) []byte {
	if td == nil {
		return nil
	}
	out, err := proto.Marshal(td)
	if err != nil {
		return nil
	}
	return out
}

// DecodeRequest builds the traces (see decodeTraceTrees) and points each one at
// its own spans within the request, ready to be encoded if it turns out to be
// worth storing. It is the entry point for the ingest/scoring path (the OTLP
// receiver). Traces whose spans can't be picked out are dropped rather than
// stored as an empty observation row. Render-only callers should use
// decodeTraceTrees, which skips this step entirely. sourceIdentity sets each
// span's SourceIdentity; Engine.DecodeRequest passes the engine's.
func DecodeRequest(req *tracepb.TracesData, sourceIdentity SourceIdentityFunc) []*Trace {
	traces := decodeTraceTrees(req, sourceIdentity)
	byTrace := splitByTrace(req)
	out := traces[:0] // reuse backing array; we only ever drop, never add
	for _, t := range traces {
		var spans *tracepb.TracesData
		if raw, err := hex.DecodeString(t.TraceID); err == nil {
			spans = byTrace[string(raw)]
		}
		if spans == nil {
			// Defensive: re-selecting spans we just walked shouldn't fail, but
			// if it does (corrupt input, future proto incompat) drop the trace
			// rather than insert an empty observation row.
			continue
		}
		t.otlpSpans = spans
		out = append(out, t)
	}
	return out
}

// DecodeRequest is DecodeRequest with the engine's SourceIdentityFunc, so each
// span carries the source identity the engine identifies transactions by.
func (e *Engine) DecodeRequest(req *tracepb.TracesData) []*Trace {
	return DecodeRequest(req, e.sourceIdentity)
}

// decodeTraceTrees groups an OTLP request's spans by trace_id, links each
// trace's parent/child spans, and picks its root (copying service.name from the
// resource onto every span; the rest of the resource attributes are shared, not
// copied). sourceIdentity, when not nil, sets each span's SourceIdentity, once
// per resource. Traces with no identifiable root are dropped. It
// does not give a trace access to its own OTLP spans - DecodeRequest wraps it
// for that. Also used directly by the render path (SpansFromOTLP).
func decodeTraceTrees(req *tracepb.TracesData, sourceIdentity SourceIdentityFunc) []*Trace {
	if req == nil {
		return nil
	}
	bySpanID := map[string]*Span{}
	byTrace := map[string][]*Span{}

	for _, rs := range req.GetResourceSpans() {
		resourceAttrs := flattenAttrs(rs.GetResource().GetAttributes())
		serviceName := resourceAttrs[semconv.ServiceName]
		var identity map[string]string
		if sourceIdentity != nil {
			identity = sourceIdentity(resourceAttrs)
		}

		for _, ss := range rs.GetScopeSpans() {
			scope := ss.GetScope()
			scopeName, scopeVer := scope.GetName(), scope.GetVersion()

			for _, sp := range ss.GetSpans() {
				out := &Span{
					SpanID:         hex.EncodeToString(sp.GetSpanId()),
					ParentSpanID:   hex.EncodeToString(sp.GetParentSpanId()),
					Service:        serviceName,
					Name:           stripNUL(sp.GetName()),
					Kind:           sp.GetKind(),
					StartUnix:      sp.GetStartTimeUnixNano(),
					EndUnix:        sp.GetEndTimeUnixNano(),
					StatusCode:     sp.GetStatus().GetCode(),
					StatusMessage:  stripNUL(sp.GetStatus().GetMessage()),
					ScopeName:      scopeName,
					ScopeVersion:   scopeVer,
					SpanAttrs:      flattenAttrs(sp.GetAttributes()),
					ResourceAttrs:  resourceAttrs,
					SourceIdentity: identity,
				}
				tid := hex.EncodeToString(sp.GetTraceId())
				bySpanID[out.SpanID] = out
				byTrace[tid] = append(byTrace[tid], out)
			}
		}
	}

	// Link parent/child for every trace and identify roots.
	out := make([]*Trace, 0, len(byTrace))
	for tid, spans := range byTrace {
		var root *Span
		for _, sp := range spans {
			if sp.ParentSpanID == "" || sp.ParentSpanID == zeroSpanID {
				root = sp
				continue
			}
			if parent, ok := bySpanID[sp.ParentSpanID]; ok {
				parent.Children = append(parent.Children, sp)
			}
		}
		if root == nil {
			continue // orphan batch, no root to baseline against
		}
		out = append(out, &Trace{
			TraceID:    tid,
			Root:       root,
			Spans:      spans,
			DurationUs: spanDurationUs(root),
		})
	}
	return out
}

// splitByTrace groups a batch's spans by raw trace_id in one pass, keeping each
// span under its original resource and scope (what trace_observations.raw_otlp
// stores). The results share spans with req, so treat them as read-only (see
// reRootedOTLP).
func splitByTrace(req *tracepb.TracesData) map[string]*tracepb.TracesData {
	byTrace := map[string]*tracepb.TracesData{}
	for _, rs := range req.GetResourceSpans() {
		rsByTrace := map[string]*tracepb.ResourceSpans{}
		for _, ss := range rs.GetScopeSpans() {
			ssByTrace := map[string]*tracepb.ScopeSpans{}
			for _, sp := range ss.GetSpans() {
				id := string(sp.GetTraceId())
				ssOut := ssByTrace[id]
				if ssOut == nil {
					rsOut := rsByTrace[id]
					if rsOut == nil {
						rsOut = &tracepb.ResourceSpans{Resource: rs.GetResource(), SchemaUrl: rs.GetSchemaUrl()}
						rsByTrace[id] = rsOut
						td := byTrace[id]
						if td == nil {
							td = &tracepb.TracesData{}
							byTrace[id] = td
						}
						td.ResourceSpans = append(td.ResourceSpans, rsOut)
					}
					ssOut = &tracepb.ScopeSpans{Scope: ss.GetScope(), SchemaUrl: ss.GetSchemaUrl()}
					ssByTrace[id] = ssOut
					rsOut.ScopeSpans = append(rsOut.ScopeSpans, ssOut)
				}
				ssOut.Spans = append(ssOut.Spans, sp)
			}
		}
	}
	return byTrace
}

// zeroSpanID is what hex.EncodeToString returns for a fresh 8-byte zero array.
const zeroSpanID = "0000000000000000"

// flattenAttrs converts OTLP KeyValues into a string map. Lossy for
// array/kvlist/bytes values (rare) - the scorers only need scalar attributes.
func flattenAttrs(kvs []*commonpb.KeyValue) map[string]string {
	if len(kvs) == 0 {
		return nil
	}
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		if kv == nil || kv.Value == nil {
			continue
		}
		if s := anyToString(kv.Value); s != "" {
			out[kv.Key] = s
		}
	}
	return out
}

func anyToString(v *commonpb.AnyValue) string {
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return stripNUL(x.StringValue)
	case *commonpb.AnyValue_BoolValue:
		return strconv.FormatBool(x.BoolValue)
	case *commonpb.AnyValue_IntValue:
		return strconv.FormatInt(x.IntValue, 10)
	case *commonpb.AnyValue_DoubleValue:
		return strconv.FormatFloat(x.DoubleValue, 'g', -1, 64)
	case *commonpb.AnyValue_ArrayValue:
		// Header attrs (e.g. http.response.headers.content-length) arrive as
		// string[]; take the first element.
		arr := x.ArrayValue
		if arr == nil || len(arr.GetValues()) == 0 {
			return ""
		}
		return anyToString(arr.GetValues()[0])
	}
	return ""
}

// stripNUL removes NUL bytes from a string because Postgres jsonb refuses to
// store JSON strings containing \u0000 (SQLSTATE 22P05). Real sources include
// the Odigos eBPF Java agent's NUL-separated process.command argv and PHP
// anonymous-class names with an embedded NUL. Sanitizing both attribute values
// AND span names here keeps every downstream JSON write safe by construction.
func stripNUL(s string) string {
	if !strings.ContainsRune(s, 0) {
		return s
	}
	return strings.ReplaceAll(s, "\x00", "")
}
