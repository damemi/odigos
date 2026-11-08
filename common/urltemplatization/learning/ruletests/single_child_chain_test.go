package ruletests

import (
	"testing"

	learning "github.com/odigos-io/odigos/common/urltemplatization/learning"
)

func TestSingleChildChain(t *testing.T) {
	rules := learning.FindLiveTrafficLearningRules(learning.BuildPathTrie(map[string]string{
		"/api/v1/health": "101",
	}), learning.LiveTrafficLearningParams{})
	assertTemplates(t, templatesOf(rules), []string{"/api/v1/health"})
	assertSegments(t, rules[0].Segments, []wantSegment{
		{typ: learning.SegmentTypeStatic, name: "api"},
		{typ: learning.SegmentTypeStatic, name: "v1"},
		{typ: learning.SegmentTypeStatic, name: "health"},
	})
}

func TestSingleChildChainBelowObservationThreshold(t *testing.T) {
	rules := learning.FindLiveTrafficLearningRules(learning.BuildPathTrie(map[string]string{
		"/api/v1/health": "99",
	}), learning.LiveTrafficLearningParams{})
	if len(rules) != 0 {
		t.Fatalf("rules=%v, want none when leaf observations are below minObservationsForRule (100)", templatesOf(rules))
	}
}
