package destination

import "testing"

func TestResolveOTLPTarget(t *testing.T) {
	// A traces-only destination (e.g. Jaeger) → only Traces true; TLS enabled → not insecure.
	tracesOnly := []Destination{{
		Type:    "otlp",
		Config:  map[string]any{"OTLP_GRPC_ENDPOINT": "jaeger:4317", "OTLP_GRPC_TLS_ENABLED": "true"},
		Signals: []string{"TRACES"},
	}}
	got, ok := ResolveOTLPTarget(tracesOnly)
	if !ok || got.Endpoint != "jaeger:4317" || got.Insecure || !got.Traces || got.Metrics || got.Logs {
		t.Fatalf("traces-only: got %+v ok=%v", got, ok)
	}

	// Full backend with TLS off → insecure, all signals.
	full := []Destination{{
		Type:    "otlp",
		Config:  map[string]any{"OTLP_GRPC_ENDPOINT": "collector:4317"},
		Signals: []string{"TRACES", "METRICS", "LOGS"},
	}}
	got, ok = ResolveOTLPTarget(full)
	if !ok || !got.Insecure || !got.Traces || !got.Metrics || !got.Logs {
		t.Fatalf("full: got %+v ok=%v", got, ok)
	}

	// No signals recorded → fall back to traces-only.
	noSignals := []Destination{{Type: "otlp", Config: map[string]any{"OTLP_GRPC_ENDPOINT": "c:4317"}}}
	got, _ = ResolveOTLPTarget(noSignals)
	if !got.Traces || got.Metrics || got.Logs {
		t.Errorf("no-signals fallback should be traces-only, got %+v", got)
	}

	// No OTLP endpoint → not found.
	if _, ok := ResolveOTLPTarget([]Destination{{Type: "otlp", Config: map[string]any{}}}); ok {
		t.Error("a destination without an OTLP endpoint must not resolve")
	}
	if _, ok := ResolveOTLPTarget(nil); ok {
		t.Error("no destinations must not resolve")
	}
}

func TestResolveOTLPTargetHTTP(t *testing.T) {
	// An otlphttp-only destination resolves with a normalized HTTP endpoint and no gRPC endpoint.
	httpOnly := []Destination{{
		Type:    "otlphttp",
		Config:  map[string]any{"OTLP_HTTP_ENDPOINT": "collector"},
		Signals: []string{"TRACES", "METRICS"},
	}}
	got, ok := ResolveOTLPTarget(httpOnly)
	if !ok || got.Endpoint != "" || got.HTTPEndpoint != "http://collector:4318" || !got.Traces || !got.Metrics || got.Logs {
		t.Fatalf("http-only: got %+v ok=%v", got, ok)
	}

	// A destination carrying both endpoints exposes both.
	both := []Destination{{
		Type: "otlp",
		Config: map[string]any{
			"OTLP_GRPC_ENDPOINT": "collector:4317",
			"OTLP_HTTP_ENDPOINT": "https://collector:4318",
		},
		Signals: []string{"TRACES"},
	}}
	got, ok = ResolveOTLPTarget(both)
	if !ok || got.Endpoint != "collector:4317" || got.HTTPEndpoint != "https://collector:4318" {
		t.Fatalf("both: got %+v ok=%v", got, ok)
	}
}

func TestNormalizeOTLPHTTPEndpoint(t *testing.T) {
	cases := map[string]string{
		"":                             "",
		"collector":                    "http://collector:4318",   // bare host: default scheme + port
		"collector:9099":               "http://collector:9099",   // explicit port preserved
		"https://collector":            "https://collector:4318",  // scheme preserved, port defaulted
		"https://collector:4318":       "https://collector:4318",  // fully specified passthrough
		"http://collector:4318/v1":     "http://collector:4318/v1", // a path implies a deliberate endpoint
	}
	for in, want := range cases {
		if got := normalizeOTLPHTTPEndpoint(in); got != want {
			t.Errorf("normalizeOTLPHTTPEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}
