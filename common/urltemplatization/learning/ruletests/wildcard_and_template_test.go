package ruletests

import (
	"fmt"
	"testing"

	learning "github.com/odigos-io/odigos-enterprise/common-enterprise/urltemplatization/learning"
)

func setChildTemplateProbability(t *testing.T, trie *learning.PathTrie, segment string, p float64) {
	t.Helper()
	node := trie.Root.Children[segment]
	if node == nil {
		t.Fatalf("missing segment %q", segment)
	}
	for _, child := range node.Children {
		child.TemplateProbability = p
	}
}

func pathsUnder(prefix string, n int, count string) map[string]string {
	paths := make(map[string]string, n)
	for i := 0; i < n; i++ {
		paths[fmt.Sprintf("%s/seg%02d", prefix, i)] = count
	}
	return paths
}

func TestLowProbabilityRecursesBelowScaledThreshold(t *testing.T) {
	trie := learning.BuildPathTrie(pathsUnder("/items", 8, "101"))
	setChildTemplateProbability(t, trie, "items", 0)
	rules := learning.FindLiveTrafficLearningRules(trie, learning.LiveTrafficLearningParams{})
	assertTemplates(t, templatesOf(rules), []string{
		"/items/seg00", "/items/seg01", "/items/seg02", "/items/seg03",
		"/items/seg04", "/items/seg05", "/items/seg06", "/items/seg07",
	})
}

func TestLowProbabilityWildcardModerateBeforeTemplate(t *testing.T) {
	trie := learning.BuildPathTrie(pathsUnder("/items", 20, "10"))
	setChildTemplateProbability(t, trie, "items", 0)
	rules := learning.FindLiveTrafficLearningRules(trie, learning.LiveTrafficLearningParams{})
	assertTemplates(t, templatesOf(rules), []string{"/items/*"})
	assertSegments(t, rules[0].Segments, []wantSegment{
		{typ: learning.SegmentTypeStatic, name: "items"},
		{typ: learning.SegmentTypeWildcard, certainty: learning.RuleCertaintyModerate},
	})
}

func TestLowProbabilityTemplatesAt50Children(t *testing.T) {
	trie := learning.BuildPathTrie(pathsUnder("/users", 50, "5"))
	setChildTemplateProbability(t, trie, "users", 0)
	rules := learning.FindLiveTrafficLearningRules(trie, learning.LiveTrafficLearningParams{})
	assertTemplates(t, templatesOf(rules), []string{"/users/{id}"})
	assertSegments(t, rules[0].Segments, []wantSegment{
		{typ: learning.SegmentTypeStatic, name: "users"},
		{typ: learning.SegmentTypeTemplate, name: "id", certainty: learning.RuleCertaintyHigh, minExamples: 5},
	})
}

func TestCertainProbabilityTemplatesAtMinCardinality(t *testing.T) {
	trie := learning.BuildPathTrie(pathsUnder("/users", 20, "20"))
	setChildTemplateProbability(t, trie, "users", 1)
	rules := learning.FindLiveTrafficLearningRules(trie, learning.LiveTrafficLearningParams{})
	assertTemplates(t, templatesOf(rules), []string{"/users/{id}"})
	assertSegments(t, rules[0].Segments, []wantSegment{
		{typ: learning.SegmentTypeStatic, name: "users"},
		{typ: learning.SegmentTypeTemplate, name: "id", certainty: learning.RuleCertaintyHigh, minExamples: 5},
	})
}

func TestCertainProbabilityWildcardHighBelowTemplate(t *testing.T) {
	trie := learning.BuildPathTrie(pathsUnder("/items", 15, "20"))
	setChildTemplateProbability(t, trie, "items", 1)
	rules := learning.FindLiveTrafficLearningRules(trie, learning.LiveTrafficLearningParams{})
	assertTemplates(t, templatesOf(rules), []string{"/items/*"})
	assertSegments(t, rules[0].Segments, []wantSegment{
		{typ: learning.SegmentTypeStatic, name: "items"},
		{typ: learning.SegmentTypeWildcard, certainty: learning.RuleCertaintyHigh},
	})
}
