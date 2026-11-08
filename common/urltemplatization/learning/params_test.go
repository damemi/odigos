package learning

import (
	"reflect"
	"testing"
)

func TestWithDefaultsMinObservationsForRule(t *testing.T) {
	resolved := LiveTrafficLearningParams{}.withDefaults()
	if resolved.minObservationsForRule != defaultMinObservationsForRule {
		t.Fatalf("default minObservationsForRule=%d, want %d",
			resolved.minObservationsForRule, defaultMinObservationsForRule)
	}

	minObs := int64(50)
	resolved = LiveTrafficLearningParams{MinObservationsForRule: &minObs}.withDefaults()
	if resolved.minObservationsForRule != 50 {
		t.Fatalf("configured minObservationsForRule=%d, want 50", resolved.minObservationsForRule)
	}
}

func TestCardinalityThresholdsFromChildProbability(t *testing.T) {
	children := make([]*PathTrieNode, 4)
	for i := range children {
		children[i] = &PathTrieNode{}
	}

	cfg := LiveTrafficLearningParams{}.withDefaults()
	static := cardinalityThresholdsFor(children, cfg)
	if static.templateAt != templateChildrenWhenStatic || static.wildcardAt != 12 {
		t.Fatalf("p=0 thresholds=%+v, want wildcard 12 template 50", static)
	}

	for _, child := range children {
		child.TemplateProbability = 1
	}
	certain := cardinalityThresholdsFor(children, cfg)
	if certain.templateAt != defaultMinCardinalityForTemplating || certain.wildcardAt != 5 {
		t.Fatalf("p=1 thresholds=%+v, want wildcard 5 template 20", certain)
	}

	for _, child := range children {
		child.TemplateProbability = 0.5
	}
	mid := cardinalityThresholdsFor(children, cfg)
	if mid.templateAt != 35 || mid.wildcardAt != 8 {
		t.Fatalf("p=0.5 thresholds=%+v, want wildcard 8 template 35", mid)
	}

	recurse := 10
	wildcard := 15
	templating := 30
	custom := LiveTrafficLearningParams{
		MaxRecurseEachChildren:      &recurse,
		MaxWildcardLowChildren:      &wildcard,
		MinCardinalityForTemplating: &templating,
	}.withDefaults()
	scaled := cardinalityThresholdsFor(children, custom)
	if scaled.templateAt != 40 || scaled.wildcardAt != 13 {
		t.Fatalf("custom params thresholds=%+v, want wildcard 13 template 40", scaled)
	}
}

func TestSampleSparse(t *testing.T) {
	items := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	got := sampleSparse(items, 5)
	want := []string{"a", "c", "e", "g", "j"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sampleSparse=%v, want %v", got, want)
	}
	if got := sampleSparse(items[:3], 5); !reflect.DeepEqual(got, items[:3]) {
		t.Fatalf("short list should return all: got %v", got)
	}
}
