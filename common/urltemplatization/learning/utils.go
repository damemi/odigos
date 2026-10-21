package learning

import (
	"sort"
	"strings"
)

// copySegments snapshots the current walk's segment list onto an emitted rule.
// The walker keeps growing `segments` as it recurses into children (including
// after a terminal rule is emitted on a node that also has descendants). Without a
// copy, those later appends could reuse the same backing array and mutate the
// Segments already stored on earlier rules.
func copySegments(segments []Segment) []Segment {
	if len(segments) == 0 {
		return nil
	}
	out := make([]Segment, len(segments))
	copy(out, segments)
	return out
}

// sortedChildList returns children in segment-name order. Learning decisions only
// care about child count/patterns; sorting is for stable rule and example order
// because map iteration is randomized.
func sortedChildList(n *PathTrieNode) []*PathTrieNode {
	if n == nil || len(n.Children) == 0 {
		return nil
	}
	names := make([]string, 0, len(n.Children))
	for name := range n.Children {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]*PathTrieNode, 0, len(names))
	for _, name := range names {
		out = append(out, n.Children[name])
	}
	return out
}

func staticSegment(literal string) Segment {
	return Segment{
		Type:      SegmentTypeStatic,
		Name:      literal,
		Certainty: RuleCertaintyHigh,
	}
}

func appendStaticSegment(segments []Segment, literal string) []Segment {
	out := make([]Segment, len(segments), len(segments)+1)
	copy(out, segments)
	return append(out, staticSegment(literal))
}

// templateFromSegments rebuilds the Template path string from the walk's segments
// (static literals, "*", or "{name}" for templated segments).
func templateFromSegments(segments []Segment) string {
	if len(segments) == 0 {
		return ""
	}
	var b strings.Builder
	for _, seg := range segments {
		b.WriteByte('/')
		switch seg.Type {
		case SegmentTypeStatic:
			b.WriteString(seg.Name)
		case SegmentTypeWildcard:
			b.WriteString(wildcardSegmentName)
		default:
			b.WriteByte('{')
			b.WriteString(seg.Name)
			b.WriteByte('}')
		}
	}
	return b.String()
}
