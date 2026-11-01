package destination

import (
	"reflect"
	"testing"
)

func TestResolveOTLPHeadersReadsTheTransportsOwnKey(t *testing.T) {
	cases := map[string]struct {
		dest Destination
		want map[string]string
	}{
		"grpc destination reads OTLP_GRPC_HEADERS": {
			dest: Destination{Type: "otlp", Signals: []string{SignalTraces}, Config: map[string]any{
				"OTLP_GRPC_ENDPOINT": "collector:4317",
				"OTLP_GRPC_HEADERS":  `[{"key":"authorization","value":"Bearer tok"}]`,
				// Present but must be ignored: this destination exports over gRPC, and sending the HTTP
				// destination's credential to it would hand a token to the wrong backend.
				"OTLP_HTTP_HEADERS": `[{"key":"x-api-key","value":"WRONG"}]`,
			}},
			want: map[string]string{"authorization": "Bearer tok"},
		},
		"http-only destination reads OTLP_HTTP_HEADERS": {
			dest: Destination{Type: "otlphttp", Signals: []string{SignalTraces}, Config: map[string]any{
				"OTLP_HTTP_ENDPOINT": "https://collector:4318",
				"OTLP_HTTP_HEADERS":  `[{"key":"x-api-key","value":"abc"}]`,
			}},
			want: map[string]string{"x-api-key": "abc"},
		},
		"no headers configured": {
			dest: Destination{Type: "otlp", Signals: []string{SignalTraces}, Config: map[string]any{
				"OTLP_GRPC_ENDPOINT": "collector:4317",
			}},
			want: nil,
		},
		// Malformed must not fail instrumentation — see parseOTLPHeaders.
		"malformed json is ignored": {
			dest: Destination{Type: "otlp", Signals: []string{SignalTraces}, Config: map[string]any{
				"OTLP_GRPC_ENDPOINT": "collector:4317",
				"OTLP_GRPC_HEADERS":  `{"authorization":"Bearer tok"}`,
			}},
			want: nil,
		},
		"entry with an empty key is skipped": {
			dest: Destination{Type: "otlp", Signals: []string{SignalTraces}, Config: map[string]any{
				"OTLP_GRPC_ENDPOINT": "collector:4317",
				"OTLP_GRPC_HEADERS":  `[{"key":"","value":"x"},{"key":"a","value":"b"}]`,
			}},
			want: map[string]string{"a": "b"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := ResolveOTLPHeaders([]Destination{c.dest}); !reflect.DeepEqual(got, c.want) {
				t.Errorf("ResolveOTLPHeaders = %v, want %v", got, c.want)
			}
		})
	}
}

// TestResolveOTLPHeadersTracksTargetSelection is the guard the duplicated selection rule needs. If
// ResolveOTLPTarget ever changes WHICH destination it picks, this fails rather than letting the connector
// export to one destination while sending another's credential.
func TestResolveOTLPHeadersTracksTargetSelection(t *testing.T) {
	dests := []Destination{
		// Skipped by both: no endpoint of either kind. Its headers must never be chosen.
		{Name: "no-endpoint", Type: "otlp", Signals: []string{SignalTraces}, Config: map[string]any{
			"OTLP_GRPC_HEADERS": `[{"key":"authorization","value":"WRONG-first"}]`,
		}},
		{Name: "chosen", Type: "otlp", Signals: []string{SignalTraces}, Config: map[string]any{
			"OTLP_GRPC_ENDPOINT": "chosen:4317",
			"OTLP_GRPC_HEADERS":  `[{"key":"authorization","value":"RIGHT"}]`,
		}},
		{Name: "later", Type: "otlp", Signals: []string{SignalTraces}, Config: map[string]any{
			"OTLP_GRPC_ENDPOINT": "later:4317",
			"OTLP_GRPC_HEADERS":  `[{"key":"authorization","value":"WRONG-later"}]`,
		}},
	}
	target, found := ResolveOTLPTarget(dests)
	if !found {
		t.Fatal("test setup: no target resolved")
	}
	if target.Endpoint != "chosen:4317" {
		t.Fatalf("test setup: ResolveOTLPTarget picked %q", target.Endpoint)
	}
	got := ResolveOTLPHeaders(dests)
	if want := map[string]string{"authorization": "RIGHT"}; !reflect.DeepEqual(got, want) {
		t.Errorf("headers came from a different destination than the export target: got %v, want %v", got, want)
	}
}

// TestResolveOTLPHeadersSkipDisabledAndManagedCollector is the Disabled /
// managed-collector counterpart of TestResolveOTLPHeadersTracksTargetSelection.
// Both resolvers must skip the same entries so a paused destination's credential
// is never sent to the next vendor, and a routing marker never supplies headers.
func TestResolveOTLPHeadersSkipDisabledAndManagedCollector(t *testing.T) {
	dests := []Destination{
		{Name: "paused", Type: "otlp", Disabled: true, Signals: []string{SignalTraces}, Config: map[string]any{
			"OTLP_GRPC_ENDPOINT": "paused:4317",
			"OTLP_GRPC_HEADERS":  `[{"key":"authorization","value":"WRONG-disabled"}]`,
		}},
		{Name: "marker", Type: CatalogTypeManagedCollector, Region: "us-east-1", Signals: []string{SignalTraces}, Config: map[string]any{
			"OTLP_GRPC_HEADERS": `[{"key":"authorization","value":"WRONG-marker"}]`,
		}},
		{Name: "on", Type: "otlp", Signals: []string{SignalTraces}, Config: map[string]any{
			"OTLP_GRPC_ENDPOINT": "right:4317",
			"OTLP_GRPC_HEADERS":  `[{"key":"authorization","value":"RIGHT"}]`,
		}},
	}
	target, found := ResolveOTLPTarget(dests)
	if !found || target.Endpoint != "right:4317" {
		t.Fatalf("ResolveOTLPTarget picked %+v found=%v", target, found)
	}
	got := ResolveOTLPHeaders(dests)
	if want := map[string]string{"authorization": "RIGHT"}; !reflect.DeepEqual(got, want) {
		t.Errorf("headers came from a skipped destination: got %v, want %v", got, want)
	}
}
