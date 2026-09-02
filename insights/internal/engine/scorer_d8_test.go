package engine

import (
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/odigos-io/odigos-enterprise/insights/internal/engine/histogram"
)

func httpTrace(attrs map[string]string) *Trace {
	return mkTrace(
		withRoot("POST /checkout", "checkout-svc", tracepb.Span_SPAN_KIND_SERVER, attrs),
	)
}

func learnD8(t *testing.T, traces ...*Trace) []byte {
	t.Helper()
	var raw []byte
	for _, tr := range traces {
		merged, err := (d8PayloadSize{}).Merge(tr, raw)
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		raw = merged
	}
	return raw
}

func TestD8_EmptyBaselineNeverDeviates(t *testing.T) {
	tr := httpTrace(map[string]string{"http.request.body.size": "100"})
	if dev, _, _ := (d8PayloadSize{}).Score(tr, nil); dev {
		t.Errorf("empty baseline must not deviate")
	}
}

func TestD8_RequestAboveP99Deviates(t *testing.T) {
	// 99 observations at 100 bytes, 1 at 127 bytes (same bucket) -> p99 upper bound 127.
	var traces []*Trace
	for i := 0; i < 99; i++ {
		traces = append(traces, httpTrace(map[string]string{"http.request.body.size": "100"}))
	}
	traces = append(traces, httpTrace(map[string]string{"http.request.body.size": "127"}))
	baseline := learnD8(t, traces...)

	if dev, _, _ := (d8PayloadSize{}).Score(httpTrace(map[string]string{"http.request.body.size": "127"}), baseline); dev {
		t.Errorf("request at p99 must not deviate")
	}

	dev, ev, _ := (d8PayloadSize{}).Score(httpTrace(map[string]string{"http.request.body.size": "128"}), baseline)
	if !dev {
		t.Fatalf("expected deviation for request above p99")
	}
	if !contains(string(ev), "128") || !contains(string(ev), "request_p99") {
		t.Errorf("evidence missing request size details: %s", ev)
	}
}

func TestD8_ResponseAboveP99Deviates(t *testing.T) {
	var traces []*Trace
	for i := 0; i < 99; i++ {
		traces = append(traces, httpTrace(map[string]string{
			"http.response.headers.content-length": "100",
		}))
	}
	traces = append(traces, httpTrace(map[string]string{
		"http.response.headers.content-length": "127",
	}))
	baseline := learnD8(t, traces...)

	if dev, _, _ := (d8PayloadSize{}).Score(httpTrace(map[string]string{
		"http.response.headers.content-length": "127",
	}), baseline); dev {
		t.Errorf("response at p99 must not deviate")
	}

	dev, ev, _ := (d8PayloadSize{}).Score(httpTrace(map[string]string{
		"http.response.headers.content-length": "128",
	}), baseline)
	if !dev {
		t.Fatalf("expected deviation for response above p99")
	}
	if !contains(string(ev), "128") || !contains(string(ev), "response_p99") {
		t.Errorf("evidence missing response size details: %s", ev)
	}
}

func TestD8_ResponseAndRequestHistogramsMerge(t *testing.T) {
	baseline := learnD8(t, httpTrace(map[string]string{
		"http.request.body.size":  "50",
		"http.response.body.size": "500",
	}))

	var d d8Data
	if err := decodeD8JSON(baseline, &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.Request.Total() != 1 || d.Response.Total() != 1 {
		t.Fatalf("expected both histograms updated, req=%d resp=%d", d.Request.Total(), d.Response.Total())
	}
}

func TestD8_BodyContentFallback(t *testing.T) {
	body := "hello" // 5 bytes
	baseline := learnD8(t, httpTrace(map[string]string{"http.request.body": body}))

	dev, _, _ := (d8PayloadSize{}).Score(httpTrace(map[string]string{
		"http.request.body": "much-longer-body",
	}), baseline)
	if !dev {
		t.Fatalf("expected deviation when captured body exceeds learned p99")
	}
}

func TestD8_ResponseHeadersContentLength(t *testing.T) {
	baseline := learnD8(t, httpTrace(map[string]string{
		"http.request.body.size":               "50",
		"http.response.headers.content-length": "500",
	}))

	var d d8Data
	if err := decodeD8JSON(baseline, &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.Response.Total() != 1 {
		t.Fatalf("expected response histogram updated, got %d", d.Response.Total())
	}
}

func TestD8_RequestHeadersContentLengthOnInternalRoot(t *testing.T) {
	tr := mkTrace(
		withRoot("POST /checkout", "checkout-svc", tracepb.Span_SPAN_KIND_INTERNAL, map[string]string{
			"http.request.headers.content-length": "256",
		}),
	)
	baseline := learnD8(t, tr)

	var d d8Data
	if err := decodeD8JSON(baseline, &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.Request.Total() != 1 {
		t.Fatalf("expected request histogram updated on INTERNAL root, got %d", d.Request.Total())
	}
}

func TestD8_SizesOnChildSpan(t *testing.T) {
	tr := mkTrace(
		withRoot("POST /checkout", "checkout-svc", tracepb.Span_SPAN_KIND_SERVER, nil),
		withChild("root", "handler", tracepb.Span_SPAN_KIND_INTERNAL, map[string]string{
			"http.response.headers.content-length": "1024",
		}, "net/http"),
	)
	baseline := learnD8(t, tr)

	var d d8Data
	if err := decodeD8JSON(baseline, &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.Response.Total() != 1 {
		t.Fatalf("expected response histogram from child span, got %d", d.Response.Total())
	}
}

func TestD8_DecodeRequestArrayHeaderValue(t *testing.T) {
	req := &tracepb.TracesData{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{kvStr("service.name", "checkout-svc")},
			},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					TraceId:           []byte{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
					SpanId:            []byte{1, 1, 1, 1, 1, 1, 1, 1},
					Name:              "POST /checkout",
					Kind:              tracepb.Span_SPAN_KIND_SERVER,
					StartTimeUnixNano: 0,
					EndTimeUnixNano:   1_000_000,
					Attributes: []*commonpb.KeyValue{
						kvStr("http.request.headers.content-length", "128"),
						kvStrArr("http.response.headers.content-length", "512"),
					},
				}},
			}},
		}},
	}
	traces := DecodeRequest(req)
	if len(traces) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(traces))
	}

	merged, err := (d8PayloadSize{}).Merge(traces[0], nil)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	var d d8Data
	if err := decodeD8JSON(merged, &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.Request.Total() != 1 || d.Response.Total() != 1 {
		t.Fatalf("expected both histograms updated from OTLP attrs, req=%d resp=%d",
			d.Request.Total(), d.Response.Total())
	}
}

func TestParseBodySizeValue(t *testing.T) {
	cases := map[string]int64{
		"3495":          3495,
		`["3495"]`:      3495,
		`["1234", "0"]`: 1234,
		"0":             0,
	}
	for in, want := range cases {
		got, ok := parseBodySizeValue(in)
		if !ok || got != want {
			t.Errorf("parseBodySizeValue(%q) = (%d, %v), want %d", in, got, ok, want)
		}
	}
}

func TestD8_MissingSizesTreatedAsZero(t *testing.T) {
	// A size-less trace (here non-HTTP) yields 0 sizes rather than being skipped,
	// so D8 still learns and promotes; the resulting all-zero baseline stays
	// silent instead of leaving the class stuck in "learning".
	tr := mkTrace(
		withRoot("checkout", "user-svc", tracepb.Span_SPAN_KIND_SERVER, nil),
		withChild("root", "query", tracepb.Span_SPAN_KIND_CLIENT,
			map[string]string{"db.system": "postgresql"}, "jdbc"),
	)
	req, resp, ok := httpPayloadSizesFromTrace(tr)
	if !ok || req != 0 || resp != 0 {
		t.Fatalf("missing sizes should be (0, 0, true), got (%d, %d, %v)", req, resp, ok)
	}
	merged, err := (d8PayloadSize{}).Merge(tr, nil)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(merged) == 0 {
		t.Fatalf("a size-less trace should still be observed (as 0), growing the baseline")
	}
	deviated, _, err := (d8PayloadSize{}).Score(tr, merged)
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	if deviated {
		t.Errorf("an all-zero baseline should not deviate on a size-less trace")
	}
}

func TestExpHistogram_Percentile(t *testing.T) {
	layout := histogram.DefaultLayout()
	h := histogram.New()
	for i := 0; i < 99; i++ {
		h.Observe(100, layout)
	}
	h.Observe(500, layout)

	p99, ok := h.Percentile(0.99, layout)
	if !ok {
		t.Fatal("expected p99")
	}
	if p99 != 127 { // bucket for [64,128)
		t.Errorf("p99 = %d, want 127", p99)
	}
}

func decodeD8JSON(raw []byte, d *d8Data) error {
	decoded, err := decodeD8(raw)
	if err != nil {
		return err
	}
	*d = decoded
	return nil
}

func kvStrArr(k string, vals ...string) *commonpb.KeyValue {
	avs := make([]*commonpb.AnyValue, len(vals))
	for i, v := range vals {
		avs[i] = &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}
	}
	return &commonpb.KeyValue{
		Key: k,
		Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_ArrayValue{
			ArrayValue: &commonpb.ArrayValue{Values: avs},
		}},
	}
}
