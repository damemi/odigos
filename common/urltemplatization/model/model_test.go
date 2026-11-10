package model

import (
	_ "embed"
	"encoding/json"
	"math"
	"testing"
)

//go:embed golden.json
var goldenJSON []byte

// Go must reproduce the Python model's probabilities, or hashing/features drifted.
func TestMatchesPython(t *testing.T) {
	var golden []struct {
		Path  string
		Probs []float64
	}
	if err := json.Unmarshal(goldenJSON, &golden); err != nil {
		t.Fatal(err)
	}
	for _, g := range golden {
		segs := Segments(g.Path)
		for i, want := range g.Probs {
			if got := Probability(segs, i); math.Abs(got-want) > 1e-4 {
				t.Errorf("%s seg %d (%q): go=%.5f python=%.5f", g.Path, i, segs[i], got, want)
			}
		}
	}
}
