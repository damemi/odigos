package learning

import (
	"strconv"

	"github.com/odigos-io/odigos-enterprise/common-enterprise/urltemplatization/model"
	"github.com/odigos-io/odigos/common/urltemplate"
)

// PathTrieNode is one segment in a path trie.
// A node can be terminal (an observed endpoint ends here) and still have children
// from longer paths that share this prefix.
type PathTrieNode struct {
	// Segment is the path segment value for this node (no leading slash).
	Segment string

	// Children keyed by segment value.
	Children map[string]*PathTrieNode

	// Terminal is true when at least one full endpoint ends at this node.
	Terminal bool

	// TerminalObservationCount is the observation count for the endpoint that ends
	// at this node when Terminal is true. Zero when Terminal is false.
	TerminalObservationCount int64

	// TotalObservations is the sum of observation counts for all terminal endpoints
	// under this node (including this node when Terminal is true).
	TotalObservations int64

	// TemplateProbability is the URL-model probability (0 to 1) that this
	// segment is a parameter. Neighboring segments on each observed path are
	// used as context. When several paths share this node, the value is the
	// observation-weighted average of those path-specific probabilities.
	// Zero on the root, which has no segment.
	TemplateProbability float64
}

// PathTrie indexes URL paths by segment for URL templatization live traffic learning.
type PathTrie struct {
	Root *PathTrieNode
}

func NewPathTrie() *PathTrie {
	return &PathTrie{
		Root: &PathTrieNode{
			Children: make(map[string]*PathTrieNode),
		},
	}
}

// Insert walks path segment-by-segment, creating nodes as needed, and marks
// the final segment as terminal with the given observation count.
// If the same path is inserted again, observation counts are summed.
// TotalObservations is updated on the terminal node and all ancestors.
func (t *PathTrie) Insert(path string, count int64) {
	segments, _ := urltemplate.SplitPath(path)
	node := t.Root
	node.TotalObservations += count
	for i, segment := range segments {
		child, ok := node.Children[segment]
		if !ok {
			child = &PathTrieNode{
				Segment:  segment,
				Children: make(map[string]*PathTrieNode),
			}
			node.Children[segment] = child
		}
		node = child
		prevObservations := node.TotalObservations
		node.TotalObservations += count
		node.TemplateProbability = weightedTemplateProbability(
			node.TemplateProbability, prevObservations,
			model.Probability(segments, i), count,
		)
	}
	node.Terminal = true
	node.TerminalObservationCount += count
}

// BuildPathTrie builds a path trie from path -> count string pairs.
func BuildPathTrie(pathCounts map[string]string) *PathTrie {
	trie := NewPathTrie()
	for path, countStr := range pathCounts {
		count, err := strconv.ParseInt(countStr, 10, 64)
		if err != nil {
			continue
		}
		trie.Insert(path, count)
	}
	return trie
}

// weightedTemplateProbability combines two observation-weighted probabilities.
// A side with no observations does not move the result.
func weightedTemplateProbability(probA float64, obsA int64, probB float64, obsB int64) float64 {
	total := obsA + obsB
	if total <= 0 {
		return 0
	}
	return (probA*float64(obsA) + probB*float64(obsB)) / float64(total)
}

// mergeChildrenTrie collapses sibling child subtrees into one node named
// segmentName (e.g. "*" or "{id}"), summing terminal/total observations and merging
// grandchild paths that share the same segment.
func mergeChildrenTrie(children []*PathTrieNode, segmentName string) *PathTrieNode {
	merged := &PathTrieNode{
		Segment:  segmentName,
		Children: make(map[string]*PathTrieNode),
	}
	for _, child := range children {
		if child.Terminal {
			merged.Terminal = true
			merged.TerminalObservationCount += child.TerminalObservationCount
		}
		merged.TemplateProbability = weightedTemplateProbability(
			merged.TemplateProbability, merged.TotalObservations,
			child.TemplateProbability, child.TotalObservations,
		)
		merged.TotalObservations += child.TotalObservations
		for name, grandchild := range child.Children {
			if existing, ok := merged.Children[name]; ok {
				merged.Children[name] = mergeNodes(existing, grandchild)
			} else {
				merged.Children[name] = cloneNode(grandchild)
			}
		}
	}
	return merged
}

func mergeNodes(a, b *PathTrieNode) *PathTrieNode {
	out := &PathTrieNode{
		Segment:                  a.Segment,
		Children:                 make(map[string]*PathTrieNode),
		Terminal:                 a.Terminal || b.Terminal,
		TerminalObservationCount: a.TerminalObservationCount + b.TerminalObservationCount,
		TotalObservations:        a.TotalObservations + b.TotalObservations,
		TemplateProbability: weightedTemplateProbability(
			a.TemplateProbability, a.TotalObservations,
			b.TemplateProbability, b.TotalObservations,
		),
	}
	for name, child := range a.Children {
		out.Children[name] = cloneNode(child)
	}
	for name, child := range b.Children {
		if existing, ok := out.Children[name]; ok {
			out.Children[name] = mergeNodes(existing, child)
		} else {
			out.Children[name] = cloneNode(child)
		}
	}
	return out
}

func cloneNode(n *PathTrieNode) *PathTrieNode {
	if n == nil {
		return nil
	}
	out := &PathTrieNode{
		Segment:                  n.Segment,
		Children:                 make(map[string]*PathTrieNode, len(n.Children)),
		Terminal:                 n.Terminal,
		TerminalObservationCount: n.TerminalObservationCount,
		TotalObservations:        n.TotalObservations,
		TemplateProbability:      n.TemplateProbability,
	}
	for name, child := range n.Children {
		out.Children[name] = cloneNode(child)
	}
	return out
}
