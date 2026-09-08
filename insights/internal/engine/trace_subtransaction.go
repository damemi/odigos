package engine

import (
	"encoding/hex"
	"time"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// Sub-transaction decomposition.
//
// A single trace is a *tree* of transactions: every service-entry span (the
// root plus any inbound SERVER / CONSUMER span deeper in the trace) anchors its
// own transaction, learned/scored independently against its own baselines. Each
// entry learns the FULL subtree rooted at it, so context falls out organically.
// For a trace
//
//	frontend /buy -> coupon /apply-coupon -> pricing /calc
//
// the engine produces three transactions:
//
//	frontend /buy          learns {frontend, coupon, pricing}
//	coupon /apply-coupon   learns {coupon, pricing}
//	pricing /calc          learns {pricing}
//
// An entry keeps a single caller-independent identity while its baseline carries
// only what lives at or below it - a transaction's own baseline IS its context,
// with no edges table or union view. Outer and inner subtrees overlap on
// purpose, so one deep deviation is visible to several transactions; it fires
// on the outermost one that flagged it and is suppressed below (see
// anomaly_claims.go), which is why entries are returned outermost-first.

// maxSubtransactionDepth bounds how many nested service-entry levels become
// transactions (root = level 0), capping write amplification since a span is
// learned once per enclosing entry. Deeper entries are still traversed for
// their ancestors' context; they just don't get their own transaction.
const maxSubtransactionDepth = 8

// isServiceEntry reports whether a span is an inbound service entry (SERVER or
// CONSUMER) that should anchor its own transaction. CLIENT / PRODUCER /
// INTERNAL spans are captured within their enclosing entry's subtree instead.
// The trace root is always treated as an entry by serviceEntrySpans regardless
// of kind.
func isServiceEntry(s *Span) bool {
	if s == nil {
		return false
	}
	switch s.Kind {
	case tracepb.Span_SPAN_KIND_SERVER, tracepb.Span_SPAN_KIND_CONSUMER:
		return true
	default:
		return false
	}
}

// serviceEntrySpans returns a trace's entry spans in pre-order: the root first,
// then every nested SERVER / CONSUMER span down to maxSubtransactionDepth
// levels. Non-entry spans are walked through (so an entry nested under a CLIENT
// span is still found) but never returned. Used by subTraces.
func serviceEntrySpans(t *Trace) []*Span {
	if t == nil || t.Root == nil {
		return nil
	}
	entries := []*Span{t.Root} // root is always entry level 0
	var walk func(s *Span, level int)
	walk = func(s *Span, level int) {
		for _, c := range s.Children {
			if isServiceEntry(c) {
				childLevel := level + 1
				if childLevel < maxSubtransactionDepth {
					entries = append(entries, c)
				}
				walk(c, childLevel)
				continue
			}
			walk(c, level)
		}
	}
	walk(t.Root, 0)
	return entries
}

// subtreeSpans returns the entry span plus every descendant, flattened - the
// sub-trace's Spans slice that the flat-iterating scorers (D2/D5/D7/D8) consume.
func subtreeSpans(entry *Span) []*Span {
	var out []*Span
	var walk func(s *Span)
	walk = func(s *Span) {
		if s == nil {
			return
		}
		out = append(out, s)
		for _, c := range s.Children {
			walk(c)
		}
	}
	walk(entry)
	return out
}

// spanIDSet is the set of span IDs in a subtree, used to filter the stored OTLP
// down to that subtree.
func spanIDSet(spans []*Span) map[string]struct{} {
	ids := make(map[string]struct{}, len(spans))
	for _, s := range spans {
		ids[s.SpanID] = struct{}{}
	}
	return ids
}

// subTraces decomposes a decoded trace into one *Trace per service-entry span,
// as called by Engine.Process. The root entry reuses the original trace
// verbatim; every nested entry gets a sub-trace rooted at the entry, carrying
// its subtree and a re-rooted view of the trace so its observation renders as a
// standalone waterfall. Building that view is deferred to the first RawOTLP()
// call: every trace is scored, but only the few the engine decides to keep are
// ever shown to anyone. Each entry re-roots the parent's already-decoded spans,
// so decomposing a trace costs no decoding at all.
func subTraces(t *Trace) []*Trace {
	if t == nil || t.Root == nil {
		return nil
	}
	entries := serviceEntrySpans(t)
	// Index every span so a nested entry can walk up to its caller (the nearest
	// ancestor in a different service). Built once and shared across entries.
	byID := make(map[string]*Span, len(t.Spans))
	for _, s := range t.Spans {
		byID[s.SpanID] = s
	}
	out := make([]*Trace, 0, len(entries))
	for _, e := range entries {
		if e == t.Root {
			out = append(out, t) // root: external ingress, no in-trace caller
			continue
		}
		spans := subtreeSpans(e)
		entryID, ids := e.SpanID, spanIDSet(spans)
		out = append(out, &Trace{
			TraceID:       t.TraceID,
			Root:          e,
			Spans:         spans,
			DurationMs:    spanDurationMs(e),
			otlpSpansFn:   func() *tracepb.TracesData { return reRootedOTLP(t.otlpSpans, entryID, ids) },
			CallerService: callerService(e, byID),
		})
	}
	return out
}

// callerService returns the service that called entry: the nearest ancestor (up
// the parent-id chain) whose service differs from the entry's. Normally one hop
// (the caller's CLIENT span), but the loop tolerates in-process spans between.
// Empty when no different-service ancestor exists.
func callerService(entry *Span, byID map[string]*Span) string {
	for cur := byID[entry.ParentSpanID]; cur != nil; cur = byID[cur.ParentSpanID] {
		if cur.Service != "" && cur.Service != entry.Service {
			return cur.Service
		}
	}
	return ""
}

// spanDurationMs mirrors the root-duration math in DecodeRequest for an
// arbitrary entry span, guarding against unsigned underflow on a clock-skewed
// end-before-start span.
func spanDurationMs(s *Span) int64 {
	if s == nil || s.EndUnix < s.StartUnix {
		return 0
	}
	return int64((s.EndUnix - s.StartUnix) / uint64(time.Millisecond))
}

// reRootedOTLP rebuilds a TracesData with only the spans in ids, preserving the
// Resource/Scope hierarchy, and clears the entry span's parent so it becomes
// the root of its own trace. Without this the subtree would have no identifiable
// root (DecodeRequest only treats parent-less spans as roots) and the UI would
// drop it. Returns nil when there's nothing to extract; the UI falls back
// gracefully, as with extractTraceSpans. Used by subTraces.
//
// td is the enclosing trace's spans, shared with its other entries and with the
// batch they arrived in, so treat it as read-only.
func reRootedOTLP(td *tracepb.TracesData, entryID string, ids map[string]struct{}) *tracepb.TracesData {
	if td == nil || len(ids) == 0 {
		return nil
	}
	var rsOut []*tracepb.ResourceSpans
	for _, rs := range td.GetResourceSpans() {
		var ssOut []*tracepb.ScopeSpans
		for _, ss := range rs.GetScopeSpans() {
			var spansOut []*tracepb.Span
			for _, sp := range ss.GetSpans() {
				id := hex.EncodeToString(sp.GetSpanId())
				if _, ok := ids[id]; !ok {
					continue
				}
				if id == entryID {
					// Re-parent a copy: these spans are shared with this
					// trace's other entries and with the batch they arrived in,
					// and detaching the span in place would leave an enclosing
					// entry's stored trace with two roots - a broken waterfall
					// for whoever opens it.
					cp := proto.Clone(sp).(*tracepb.Span)
					cp.ParentSpanId = nil
					spansOut = append(spansOut, cp)
					continue
				}
				spansOut = append(spansOut, sp)
			}
			if len(spansOut) == 0 {
				continue
			}
			ssOut = append(ssOut, &tracepb.ScopeSpans{
				Scope:     ss.GetScope(),
				Spans:     spansOut,
				SchemaUrl: ss.GetSchemaUrl(),
			})
		}
		if len(ssOut) == 0 {
			continue
		}
		rsOut = append(rsOut, &tracepb.ResourceSpans{
			Resource:   rs.GetResource(),
			ScopeSpans: ssOut,
			SchemaUrl:  rs.GetSchemaUrl(),
		})
	}
	if len(rsOut) == 0 {
		return nil
	}
	return &tracepb.TracesData{ResourceSpans: rsOut}
}
