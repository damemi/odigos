package engine

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

func TestMagnitudeConfidence(t *testing.T) {
	cases := []struct {
		name               string
		observed, p99      int64
		want               float64
		wantFull, wantZero bool
	}{
		{name: "at p99 is zero", observed: 100, p99: 100, wantZero: true},
		{name: "below p99 is zero", observed: 50, p99: 100, wantZero: true},
		{name: "10x p99 saturates", observed: 1000, p99: 100, wantFull: true},
		{name: "above 10x stays saturated", observed: 5000, p99: 100, wantFull: true},
		{name: "5x is mid-scale", observed: 500, p99: 100, want: (5.0 - 1) / (magnitudeSaturation - 1)},
		{name: "missing p99 is full confidence", observed: 200, p99: 0, wantFull: true},
		{name: "missing observed is full confidence", observed: 0, p99: 100, wantFull: true},
		{name: "negative observed is full confidence", observed: -1, p99: 100, wantFull: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := magnitudeConfidence(tc.observed, tc.p99)
			switch {
			case tc.wantFull:
				if got != 1 {
					t.Fatalf("got %v, want 1", got)
				}
			case tc.wantZero:
				if got != 0 {
					t.Fatalf("got %v, want 0", got)
				}
			default:
				if math.Abs(got-tc.want) > 1e-9 {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestConfidenceForClass(t *testing.T) {
	t.Run("set class is binary novelty", func(t *testing.T) {
		if got := confidenceForClass(repo.D1CallEdges, json.RawMessage(`{"new_edges":[]}`)); got != 1 {
			t.Fatalf("D1 = %v, want 1", got)
		}
	})

	t.Run("D3 reads latency_us vs p99", func(t *testing.T) {
		ev := json.RawMessage(`{"latency_us":500,"latency_p99":100}`)
		got := confidenceForClass(repo.D3Latency, ev)
		want := magnitudeConfidence(500, 100)
		if got != want {
			t.Fatalf("D3 = %v, want %v", got, want)
		}
	})

	t.Run("D7 takes the worse of request and response", func(t *testing.T) {
		// Request is 2x (low confidence); response is 10x (full).
		ev := json.RawMessage(`{"request_body_size":200,"request_p99":100,"response_body_size":1000,"response_p99":100}`)
		got := confidenceForClass(repo.D7PayloadSize, ev)
		if got != 1 {
			t.Fatalf("D7 = %v, want 1 from the 10x response side", got)
		}
	})

	t.Run("unknown class is full confidence", func(t *testing.T) {
		if got := confidenceForClass(repo.DeviationClass("D99"), nil); got != 1 {
			t.Fatalf("unknown = %v, want 1", got)
		}
	})
}
