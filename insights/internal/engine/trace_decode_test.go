package engine

import (
	"encoding/binary"
	"fmt"
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// TestDecodeRequest_SplitsBatchByTrace covers a batched export: several traces
// interleaved across resources and scopes. Each trace must get exactly its own
// spans, in arrival order, under the same resource and scope they arrived in.
func TestDecodeRequest_SplitsBatchByTrace(t *testing.T) {
	tidA := []byte{0xa, 0xa, 0xa, 0xa, 0xa, 0xa, 0xa, 0xa, 0xa, 0xa, 0xa, 0xa, 0xa, 0xa, 0xa, 0xa}
	tidB := []byte{0xb, 0xb, 0xb, 0xb, 0xb, 0xb, 0xb, 0xb, 0xb, 0xb, 0xb, 0xb, 0xb, 0xb, 0xb, 0xb}
	span := func(tid []byte, id byte, parent byte, name string) *tracepb.Span {
		sp := &tracepb.Span{TraceId: tid, SpanId: []byte{id, id, id, id, id, id, id, id}, Name: name}
		if parent != 0 {
			sp.ParentSpanId = []byte{parent, parent, parent, parent, parent, parent, parent, parent}
		}
		return sp
	}
	frontend := &resourcepb.Resource{Attributes: []*commonpb.KeyValue{kvStr("service.name", "frontend")}}
	backend := &resourcepb.Resource{Attributes: []*commonpb.KeyValue{kvStr("service.name", "backend")}}
	httpScope := &commonpb.InstrumentationScope{Name: "http"}
	dbScope := &commonpb.InstrumentationScope{Name: "db"}
	req := &tracepb.TracesData{ResourceSpans: []*tracepb.ResourceSpans{
		{Resource: frontend, ScopeSpans: []*tracepb.ScopeSpans{{Scope: httpScope, Spans: []*tracepb.Span{
			span(tidA, 1, 0, "a-root"),
			span(tidB, 5, 0, "b-root"),
			span(tidA, 2, 1, "a-call"),
		}}}},
		{Resource: backend, ScopeSpans: []*tracepb.ScopeSpans{
			{Scope: httpScope, Spans: []*tracepb.Span{
				span(tidB, 6, 5, "b-server"),
				span(tidA, 3, 2, "a-server"),
			}},
			{Scope: dbScope, Spans: []*tracepb.Span{
				span(tidA, 4, 3, "a-query"),
			}},
		}},
	}}

	traces := DecodeRequest(req)
	if len(traces) != 2 {
		t.Fatalf("expected 2 traces, got %d", len(traces))
	}
	type placed struct{ service, scope, span string }
	want := map[string][]placed{
		"a-root": {{"frontend", "http", "a-root"}, {"frontend", "http", "a-call"}, {"backend", "http", "a-server"}, {"backend", "db", "a-query"}},
		"b-root": {{"frontend", "http", "b-root"}, {"backend", "http", "b-server"}},
	}
	for _, tr := range traces {
		var got []placed
		for _, rs := range tr.otlpSpans.GetResourceSpans() {
			for _, ss := range rs.GetScopeSpans() {
				for _, sp := range ss.GetSpans() {
					got = append(got, placed{rs.GetResource().GetAttributes()[0].GetValue().GetStringValue(), ss.GetScope().GetName(), sp.GetName()})
				}
			}
		}
		if fmt.Sprint(got) != fmt.Sprint(want[tr.Root.Name]) {
			t.Errorf("trace %s: got %v, want %v", tr.Root.Name, got, want[tr.Root.Name])
		}
	}
}

// batchedRequest builds an export the way the gateway's batching sender does:
// traces appended one after another, each spanning several services.
func batchedRequest(traces, servicesPerTrace, spansPerService int) *tracepb.TracesData {
	req := &tracepb.TracesData{}
	spanID := uint64(0)
	for tr := 0; tr < traces; tr++ {
		tid := make([]byte, 16)
		binary.BigEndian.PutUint64(tid[8:], uint64(tr+1))
		var parent []byte
		for svc := 0; svc < servicesPerTrace; svc++ {
			ss := &tracepb.ScopeSpans{Scope: &commonpb.InstrumentationScope{Name: "lib"}}
			for i := 0; i < spansPerService; i++ {
				spanID++
				id := make([]byte, 8)
				binary.BigEndian.PutUint64(id, spanID)
				ss.Spans = append(ss.Spans, &tracepb.Span{TraceId: tid, SpanId: id, ParentSpanId: parent, Name: "op", EndTimeUnixNano: 1})
				parent = id
			}
			req.ResourceSpans = append(req.ResourceSpans, &tracepb.ResourceSpans{
				Resource:   &resourcepb.Resource{Attributes: []*commonpb.KeyValue{kvStr("service.name", fmt.Sprintf("svc-%d", svc))}},
				ScopeSpans: []*tracepb.ScopeSpans{ss},
			})
		}
	}
	return req
}

// BenchmarkDecodeRequest reports the per-trace cost of decoding as the number
// of traces per export grows, which is what gateway batching changes.
func BenchmarkDecodeRequest(b *testing.B) {
	for _, n := range []int{1, 55, 116, 950} {
		req := batchedRequest(n, 3, 3)
		b.Run(fmt.Sprintf("traces=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				DecodeRequest(req)
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*n), "ns/trace")
		})
	}
}
