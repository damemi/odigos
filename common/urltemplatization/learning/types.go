package learning

// RuleCertainty describes how confident live traffic learning is in a path segment.
type RuleCertainty string

const (
	RuleCertaintyHigh     RuleCertainty = "High"
	RuleCertaintyModerate RuleCertainty = "Moderate"
)

// SegmentType is the kind of one path segment in a learned rule.
type SegmentType string

const (
	SegmentTypeStatic   SegmentType = "Static"
	SegmentTypeWildcard SegmentType = "Wildcard"
	SegmentTypeTemplate SegmentType = "Template"
)

// Segment describes one path segment in a learned rule, left to right.
// Both static literals and dynamic (* / {name}) segments are included so
// Segments reconstructs the full Template path.
type Segment struct {
	// Type is Static, Wildcard, or Template.
	Type SegmentType `json:"type"`

	// Name is the literal value for Static (e.g. "users"), the param name for
	// Template (e.g. "id" for "{id}"), and empty for Wildcard.
	Name string `json:"name,omitempty"`

	// Certainty indicates how confident live traffic learning is in a dynamic
	// segment. For static segments this is High (the literal was observed).
	Certainty RuleCertainty `json:"certainty"`

	// Examples are concrete path-segment values observed for a dynamic segment.
	// Empty for static segments. Learning keeps up to 5 examples.
	Examples []string `json:"examples,omitempty"`
}

// LearnedRule is one learned URL templatization recommendation.
type LearnedRule struct {
	// Template is the recommended URL template rule (e.g. "/users/{id}/orders/{orderId}").
	Template string `json:"template"`

	// Reason is a human-readable explanation of why this rule was suggested.
	Reason string `json:"reason,omitempty"`

	// Examples are full endpoints observed for this template, up to 5.
	Examples []string `json:"examples,omitempty"`

	// Segments lists every path segment in this rule left to right, including
	// static literals and dynamic (* / {name}) segments.
	Segments []Segment `json:"segments,omitempty"`
}

// examplePaths is the set of full endpoints that end at one trie node, keyed by path.
type examplePaths map[string]any
