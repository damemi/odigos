package engine

import (
	"testing"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// latencyTrace builds a minimal trace whose entry span has the given
// wall-clock duration in microseconds.
func latencyTrace(us int64) *Trace {
	s := &Span{
		SpanID:    "root",
		Name:      "POST /checkout",
		Service:   "checkout-svc",
		Kind:      tracepb.Span_SPAN_KIND_SERVER,
		StartUnix: 0,
		EndUnix:   uint64(us) * 1000, // ns
	}
	return &Trace{TraceID: "tid", Root: s, Spans: []*Span{s}}
}

func learnD3(t *testing.T, traces ...*Trace) []byte {
	t.Helper()
	var raw []byte
	for _, tr := range traces {
		merged, err := (d3Latency{}).Merge(tr, raw)
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		raw = merged
	}
	return raw
}

func TestD3_EmptyBaselineNeverDeviates(t *testing.T) {
	if dev, _, _ := (d3Latency{}).Score(latencyTrace(1000), nil); dev {
		t.Errorf("empty baseline must not deviate")
	}
}

func TestD3_AboveP99Deviates(t *testing.T) {
	// 99 observations at 100us (bucket 7 -> upper bound 127us), 1 at 500us.
	// p99 lands on bucket 7 whose inclusive upper bound is 127us.
	var traces []*Trace
	for i := 0; i < 99; i++ {
		traces = append(traces, latencyTrace(100))
	}
	traces = append(traces, latencyTrace(500))
	baseline := learnD3(t, traces...)

	// Up to 50% over p99 (190us) is noise.
	if dev, _, _ := (d3Latency{}).Score(latencyTrace(190), baseline); dev {
		t.Errorf("latency within 50%% of p99 must not deviate")
	}

	dev, ev, _ := (d3Latency{}).Score(latencyTrace(191), baseline)
	if !dev {
		t.Fatalf("expected deviation for latency over 50%% above p99")
	}
	if !contains(string(ev), "191") || !contains(string(ev), "latency_p99") {
		t.Errorf("evidence missing latency details: %s", ev)
	}
}

func TestD3_MergeObservesLatency(t *testing.T) {
	baseline := learnD3(t, latencyTrace(250), latencyTrace(300))

	d, err := decodeD3(baseline)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.Latency.Total() != 2 {
		t.Fatalf("expected 2 observations, got %d", d.Latency.Total())
	}
}

func TestD3_UntimedTraceIgnored(t *testing.T) {
	// A trace whose entry span carries no timing (start == end) is neither
	// learned nor scored.
	untimed := latencyTrace(0)
	if _, ok := entryLatencyMicros(untimed); ok {
		t.Fatalf("untimed trace should not yield a latency")
	}

	merged, err := (d3Latency{}).Merge(untimed, nil)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(merged) != 0 {
		t.Errorf("untimed trace should not change baseline, got %s", merged)
	}

	baseline := learnD3(t, latencyTrace(100))
	if dev, _, _ := (d3Latency{}).Score(untimed, baseline); dev {
		t.Errorf("untimed trace should not deviate")
	}
}

func TestD3_MergeHistogramsAdd(t *testing.T) {
	a := learnD3(t, latencyTrace(100))
	b := learnD3(t, latencyTrace(100), latencyTrace(100))

	merged, err := mergeD3Values(a, b)
	if err != nil {
		t.Fatalf("merge values: %v", err)
	}
	d, err := decodeD3(merged)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.Latency.Total() != 3 {
		t.Fatalf("expected 3 total after additive merge, got %d", d.Latency.Total())
	}
}
