package ruletests

import (
	"testing"

	learning "github.com/odigos-io/odigos/common/urltemplatization/learning"
)

func TestNumericChildrenTemplatedAsID(t *testing.T) {
	rules := learning.FindLiveTrafficLearningRules(learning.BuildPathTrie(map[string]string{
		"/users/101": "40",
		"/users/202": "40",
		"/users/303": "40",
	}), learning.LiveTrafficLearningParams{})
	assertTemplates(t, templatesOf(rules), []string{"/users/{id}"})
	assertSegments(t, rules[0].Segments, []wantSegment{
		{typ: learning.SegmentTypeStatic, name: "users"},
		{typ: learning.SegmentTypeTemplate, name: "id"},
	})
}

func TestDateChildrenTemplatedAsDate(t *testing.T) {
	rules := learning.FindLiveTrafficLearningRules(learning.BuildPathTrie(map[string]string{
		"/reports/2025-01-01": "40",
		"/reports/2025-01-02": "40",
		"/reports/2025-01-03": "40",
	}), learning.LiveTrafficLearningParams{})
	assertTemplates(t, templatesOf(rules), []string{"/reports/{date}"})
	assertSegments(t, rules[0].Segments, []wantSegment{
		{typ: learning.SegmentTypeStatic, name: "reports"},
		{typ: learning.SegmentTypeTemplate, name: "date"},
	})
}

func TestSinglePatternMatchingChildTemplatedAsID(t *testing.T) {
	rules := learning.FindLiveTrafficLearningRules(learning.BuildPathTrie(map[string]string{
		"/users/550e8400-e29b-41d4-a716-446655440000": "101",
	}), learning.LiveTrafficLearningParams{})
	assertTemplates(t, templatesOf(rules), []string{"/users/{id}"})
	assertSegments(t, rules[0].Segments, []wantSegment{
		{typ: learning.SegmentTypeStatic, name: "users"},
		{typ: learning.SegmentTypeTemplate, name: "id"},
	})
}

// Mixed siblings: numeric IDs plus one non-pattern literal. Children total clears
// the walk gate (120 > 100), but pattern-based merge requires every child to
// agree, so /users/{id} is not emitted. Individual leaves stay at 40 and do not
// clear the leaf observation gate either.
func TestMixedPatternAndNonPatternChildrenNoTemplate(t *testing.T) {
	rules := learning.FindLiveTrafficLearningRules(learning.BuildPathTrie(map[string]string{
		"/users/101":   "40",
		"/users/202":   "40",
		"/users/alice": "40",
	}), learning.LiveTrafficLearningParams{})
	if len(rules) != 0 {
		t.Fatalf("rules=%v, want none (no pattern merge; leaves below observation threshold)", templatesOf(rules))
	}
}
