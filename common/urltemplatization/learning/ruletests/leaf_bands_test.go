package ruletests

import (
	"testing"

	learning "github.com/odigos-io/odigos-enterprise/common-enterprise/urltemplatization/learning"
)

func TestLeafEmittedAboveThreshold(t *testing.T) {
	rules := learning.FindLiveTrafficLearningRules(learning.BuildPathTrie(map[string]string{
		"/health": "101",
	}), learning.LiveTrafficLearningParams{})
	assertTemplates(t, templatesOf(rules), []string{"/health"})
	assertSegments(t, rules[0].Segments, []wantSegment{
		{typ: learning.SegmentTypeStatic, name: "health"},
	})
}

func TestLeafAtThresholdNotEmitted(t *testing.T) {
	rules := learning.FindLiveTrafficLearningRules(learning.BuildPathTrie(map[string]string{
		"/health": "100",
	}), learning.LiveTrafficLearningParams{})
	if len(rules) != 0 {
		t.Fatalf("rules=%v, want none", templatesOf(rules))
	}
}

func TestLeafUnderThresholdNotEmittedEvenIfSiblingTrafficHigh(t *testing.T) {
	rules := learning.FindLiveTrafficLearningRules(learning.BuildPathTrie(map[string]string{
		"/users":       "50",
		"/users/extra": "60",
	}), learning.LiveTrafficLearningParams{})
	if len(rules) != 0 {
		t.Fatalf("rules=%v, want none", templatesOf(rules))
	}
}
