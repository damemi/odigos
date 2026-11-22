package engine

import (
	"encoding/binary"
	"fmt"
	"strings"
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/odigos-io/odigos/common/collector/sourceidentity"
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

	traces := decodeRequest(req)
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

func TestDecodeRequest_StripsNULFromNamesAndAttributes(t *testing.T) {
	tid := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	req := &tracepb.TracesData{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
			kvStr("service.name", "php"),
			kvStr("process.command", "php\x00-f\x00index.php"),
		}},
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{
			TraceId: tid,
			SpanId:  []byte{1, 1, 1, 1, 1, 1, 1, 1},
			Name:    "App\\Foo\x00anonymous",
			Attributes: []*commonpb.KeyValue{
				kvStr("http.route", "/users/\x00{id}"),
				{Key: "empty", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: ""}}},
				nil,
			},
		}}}},
	}}}

	traces := decodeRequest(req)
	if len(traces) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(traces))
	}
	sp := traces[0].Root
	if strings.ContainsRune(sp.Name, 0) || sp.Name != "App\\Fooanonymous" {
		t.Fatalf("span name kept NUL: %q", sp.Name)
	}
	if got := sp.Attr("http.route"); strings.ContainsRune(got, 0) || got != "/users/{id}" {
		t.Fatalf("span attr kept NUL: %q", got)
	}
	if got := sp.Attr("process.command"); strings.ContainsRune(got, 0) || got != "php-findex.php" {
		t.Fatalf("resource attr kept NUL: %q", got)
	}
	if got := sp.Attr("empty"); got != "" {
		t.Fatalf("empty string attr should be treated as absent, got %q", got)
	}
}

func TestSpanAttrPrefersSpanOverResource(t *testing.T) {
	tid := []byte{2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2}
	req := &tracepb.TracesData{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
			kvStr("service.name", "frontend"),
			kvStr("http.route", "/from-resource"),
			{Key: "retry", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 3}}},
		}},
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{
			TraceId: tid,
			SpanId:  []byte{2, 2, 2, 2, 2, 2, 2, 2},
			Name:    "GET",
			Attributes: []*commonpb.KeyValue{
				kvStr("http.route", "/from-span"),
				{Key: "ok", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: true}}},
				{Key: "sizes", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_ArrayValue{ArrayValue: &commonpb.ArrayValue{
					Values: []*commonpb.AnyValue{{Value: &commonpb.AnyValue_StringValue{StringValue: "first"}}, {Value: &commonpb.AnyValue_StringValue{StringValue: "second"}}},
				}}}},
			},
		}}}},
	}}}

	traces := decodeRequest(req)
	if len(traces) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(traces))
	}
	sp := traces[0].Root
	if got := sp.Attr("http.route"); got != "/from-span" {
		t.Fatalf("span attr should win, got %q", got)
	}
	if got := sp.Attr("retry"); got != "3" {
		t.Fatalf("resource int attr = %q, want 3", got)
	}
	if got := sp.Attr("ok"); got != "true" {
		t.Fatalf("bool attr = %q, want true", got)
	}
	if got := sp.Attr("sizes"); got != "first" {
		t.Fatalf("array attr should take the first element, got %q", got)
	}
	if got := sp.Attr("missing"); got != "" {
		t.Fatalf("absent attr = %q, want empty", got)
	}
}

// BenchmarkDecodeRequest reports the per-trace cost of decoding as the number
// of traces per export grows, which is what gateway batching changes.
func BenchmarkDecodeRequest(b *testing.B) {
	for _, n := range []int{1, 55, 116, 950} {
		req := batchedRequest(n, 3, 3)
		b.Run(fmt.Sprintf("traces=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				decodeRequest(req)
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*n), "ns/trace")
		})
	}
}

// TestDecodeTraceTrees_SetsSourceIdentityPerResource checks that every span
// carries its resource's source identity, and the render path (no
// SourceIdentityFunc) leaves it nil.
func TestDecodeTraceTrees_SetsSourceIdentityPerResource(t *testing.T) {
	tid := []byte{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}
	resource := func(workload string) *resourcepb.Resource {
		return &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
			kvStr("service.name", workload),
			kvStr("k8s.namespace.name", "shop"),
			kvStr("k8s.deployment.name", workload),
			kvStr("k8s.container.name", "app"),
		}}
	}
	req := &tracepb.TracesData{ResourceSpans: []*tracepb.ResourceSpans{
		{Resource: resource("checkout"), ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{
			{TraceId: tid, SpanId: []byte{1, 1, 1, 1, 1, 1, 1, 1}, Name: "GET /buy"},
			{TraceId: tid, SpanId: []byte{2, 2, 2, 2, 2, 2, 2, 2}, ParentSpanId: []byte{1, 1, 1, 1, 1, 1, 1, 1}, Name: "call pricing"},
		}}}},
		{Resource: resource("pricing"), ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{
			{TraceId: tid, SpanId: []byte{3, 3, 3, 3, 3, 3, 3, 3}, ParentSpanId: []byte{2, 2, 2, 2, 2, 2, 2, 2}, Name: "POST /price"},
		}}}},
	}}

	traces := decodeTraceTrees(req, sourceidentity.FromK8sResourceAttributesMap)
	if len(traces) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(traces))
	}
	for _, s := range traces[0].Spans {
		if got := s.SourceIdentity["k8s.deployment.name"]; got != s.Service {
			t.Errorf("span %q: source identity workload = %q, want %q", s.Name, got, s.Service)
		}
	}

	for _, s := range decodeTraceTrees(req, nil)[0].Spans {
		if s.SourceIdentity != nil {
			t.Errorf("span %q: source identity %v without a SourceIdentityFunc", s.Name, s.SourceIdentity)
		}
	}
}
