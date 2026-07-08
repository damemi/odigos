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
		t.Error("a destination without OTLP_GRPC_ENDPOINT must not resolve")
	}
	if _, ok := ResolveOTLPTarget(nil); ok {
		t.Error("no destinations must not resolve")
	}
}
