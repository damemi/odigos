package learning

import (
	"fmt"
	"math"
	"sort"

	"github.com/odigos-io/odigos/common/urltemplate"
)

const (
	templatedSegmentName = "id"
	wildcardSegmentName  = "*"
)

// FindLiveTrafficLearningRules turns a path-observation trie into suggested URL
// templatization rules. It walks each first-segment child of the root via
// walkNode. Unset params use package defaults; see LiveTrafficLearningParams.
//
// Input:  PathTrie from Redis unmatched-path hashes (path → count), plus params.
// Output: []LearnedRule (Template, Reason, Segments/certainty).
func FindLiveTrafficLearningRules(trie *PathTrie, params LiveTrafficLearningParams) []LearnedRule {
	if trie == nil || trie.Root == nil {
		return nil
	}

	cfg := params.withDefaults()
	var rules []LearnedRule
	for _, entry := range sortedChildList(trie.Root) {
		segments := []Segment{staticSegment(entry.Segment)}
		childRules := walkNode(entry, segments, cfg)
		if len(childRules) > 0 {
			rules = append(rules, childRules...)
		}
	}
	return rules
}

// walkNode learns rules under one trie node: an optional static terminal rule, then
// child rules via walkChildren. Template is derived from segments.
//
// Children: require enough child traffic (>100), then either pattern-merge the
// same id/date/email into "{name}", or apply probability-scaled cardinality
// bands. The average child template probability p sets the "{id}" cutoff at
// minCardinalityForTemplating when p=1 and 50 when p=0. Recurse and
// moderate-"*" cutoffs keep the same fraction of that cutoff.
func walkNode(node *PathTrieNode, segments []Segment, cfg resolvedLearningParams) []LearnedRule {
	if node == nil {
		return nil
	}

	var newRules []LearnedRule

	// #2 Static terminal rule when an observed endpoint ends here.
	if node.Terminal {
		if rule, ok := terminalRule(node, segments, cfg); ok {
			newRules = append(newRules, rule)
		}
	}

	// #3 Children rules: learn rules from children.
	childrenRules := walkChildren(node, segments, cfg)
	if len(childrenRules) > 0 {
		newRules = append(newRules, childrenRules...)
	}

	return newRules
}

// walkChildren applies pattern-based and cardinality decisions under node.
func walkChildren(node *PathTrieNode, segments []Segment, cfg resolvedLearningParams) []LearnedRule {

	// #1 Gate: enough traffic under children before branching / templating.
	childrenObs := node.TotalObservations - node.TerminalObservationCount
	if childrenObs <= cfg.minObservationsForRule {
		return nil
	}

	// sort children for determinism and consistency between runs
	children := sortedChildList(node)
	n := len(children)
	if n == 0 {
		return nil
	}

	// #2 Pattern-based templatization: all children match the same id/date/email pattern.
	patternName, ok := childrenPatternBasedTemplateName(children)
	if ok {
		next := appendSegment(segments, SegmentTypeTemplate, patternName, children, RuleCertaintyHigh, cfg.maxTemplatedExamples)
		return walkMergedChildren(children, next, cfg, patternName)
	}

	// #3 Cardinality bands from the children's template probability.
	bands := cardinalityThresholdsFor(children, cfg)
	switch {

	case n <= bands.wildcardAt:
		var rules []LearnedRule
		for _, child := range children {
			childStaticSegment := appendStaticSegment(segments, child.Segment)
			childRules := walkNode(child, childStaticSegment, cfg)
			rules = append(rules, childRules...)
		}
		return rules

	case n < bands.templateAt:
		next := appendSegment(segments, SegmentTypeWildcard, "", children, RuleCertaintyHigh, cfg.maxTemplatedExamples)
		return walkMergedChildren(children, next, cfg, wildcardSegmentName)

	default:
		next := appendSegment(segments, SegmentTypeTemplate, templatedSegmentName, children, RuleCertaintyHigh, cfg.maxTemplatedExamples)
		return walkMergedChildren(children, next, cfg, templatedSegmentName)
	}
}

// cardinalityBands are the distinct-child cutoffs for one walkChildren decision.
type cardinalityBands struct {
	wildcardAt int
	templateAt int
}

// cardinalityThresholdsFor averages TemplateProbability across children, then
// sets the "{id}" cutoff on the line from 50 (p=0) to minCardinalityForTemplating
// (p=1). Recurse and moderate-"*" cutoffs stay the same fraction of that cutoff.
func cardinalityThresholdsFor(children []*PathTrieNode, cfg resolvedLearningParams) cardinalityBands {
	var sum float64
	for _, child := range children {
		sum += child.TemplateProbability
	}
	p := sum / float64(len(children))
	if p < 0 {
		p = 0
	} else if p > 1 {
		p = 1
	}
	certain := cfg.minCardinalityForTemplating
	templateAt := int(math.Round(float64(templateChildrenWhenStatic) - float64(templateChildrenWhenStatic-certain)*p))
	return cardinalityBands{
		wildcardAt: templateAt * cfg.maxRecurseEachChildren / certain,
		templateAt: templateAt,
	}
}

// walkMergedChildren collapses siblings into one dynamic segment and continues
// learning under that merged node. segments must already include the new segment.
// segmentName is used for trie merge and reason text ("*", "id", "date", …).
func walkMergedChildren(
	children []*PathTrieNode,
	segments []Segment,
	cfg resolvedLearningParams,
	segmentName string,
) []LearnedRule {
	merged := mergeChildrenTrie(children, segmentName)
	rules := walkNode(merged, segments, cfg)
	for i := range rules {
		rules[i].Reason = annotateDynamicReason(rules[i].Reason, segmentName, len(children))
	}
	return rules
}

// childrenPatternBasedTemplateName returns a template name when every child segment
// matches the same built-in pattern name; otherwise ("", false).
func childrenPatternBasedTemplateName(children []*PathTrieNode) (string, bool) {
	if len(children) == 0 {
		return "", false
	}
	first := urltemplate.SegmentTemplateName(children[0].Segment)
	if first == "" {
		return "", false
	}
	for _, child := range children {
		name := urltemplate.SegmentTemplateName(child.Segment)
		if name == "" || name != first {
			return "", false
		}
	}
	return first, true
}

func annotateDynamicReason(reason, segmentName string, childCount int) string {
	kind := "templated segment"
	display := "{" + segmentName + "}"
	if segmentName == wildcardSegmentName {
		kind = "wildcard segment"
		display = segmentName
	}
	return fmt.Sprintf("%s; %s %q from %d distinct child values", reason, kind, display, childCount)
}

// appendSegment appends a dynamic Wildcard or Template segment.
// For Template, name is the param name without braces (e.g. "id"); for Wildcard, name is unused.
func appendSegment(
	segments []Segment,
	typ SegmentType,
	name string,
	children []*PathTrieNode,
	certainty RuleCertainty,
	maxExamples int,
) []Segment {
	examples := make([]string, 0, len(children))
	for _, child := range children {
		examples = append(examples, child.Segment)
	}
	sort.Strings(examples)
	examples = sampleSparse(examples, maxExamples)
	out := make([]Segment, len(segments), len(segments)+1)
	copy(out, segments)
	out = append(out, Segment{
		Type:      typ,
		Name:      name,
		Certainty: certainty,
		Examples:  examples,
	})
	return out
}

// sampleSparse returns up to n values spaced evenly across items (already sorted).
// Prefer this over a prefix so examples are not clustered among similar neighbors.
func sampleSparse(items []string, n int) []string {
	if n <= 0 || len(items) == 0 {
		return nil
	}
	if len(items) <= n {
		return items
	}
	if n == 1 {
		return []string{items[len(items)/2]}
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		idx := i * (len(items) - 1) / (n - 1)
		out[i] = items[idx]
	}
	return out
}
