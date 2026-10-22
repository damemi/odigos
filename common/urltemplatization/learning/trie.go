package learning

import (
	"maps"
	"strconv"

	"github.com/odigos-io/odigos-enterprise/common-enterprise/urltemplatization/model"
	"github.com/odigos-io/odigos/common/urltemplate"
)

// maxTerminalExamplePaths is how many full endpoints are kept on a terminal node.
const maxTerminalExamplePaths = 5

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

	// TerminalExamplePaths is the set of full endpoints that end at this node,
	// keyed by path. At most maxTerminalExamplePaths. Nil when Terminal is false.
	// Merges take turns across the source nodes so one node cannot fill the set.
	TerminalExamplePaths examplePaths

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
// The endpoint is recorded on that terminal node, up to maxTerminalExamplePaths.
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
	node.TerminalExamplePaths = addTerminalExamplePath(node.TerminalExamplePaths, path)
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
	merged := mergeNodeList(children)
	merged.Segment = segmentName
	return merged
}

// mergeNodeList collapses nodes that occupy the same trie position into one node.
// Terminal example paths take one new path from each source per pass, up to maxTerminalExamplePaths.
func mergeNodeList(nodes []*PathTrieNode) *PathTrieNode {
	if len(nodes) == 0 {
		return &PathTrieNode{Children: make(map[string]*PathTrieNode)}
	}
	if len(nodes) == 1 {
		return cloneNode(nodes[0])
	}
	out := &PathTrieNode{
		Segment:  nodes[0].Segment,
		Children: make(map[string]*PathTrieNode),
	}
	examplesByNode := make([]examplePaths, 0, len(nodes))
	childGroups := make(map[string][]*PathTrieNode)
	for _, n := range nodes {
		if n.Terminal {
			out.Terminal = true
			out.TerminalObservationCount += n.TerminalObservationCount
			if len(n.TerminalExamplePaths) > 0 {
				examplesByNode = append(examplesByNode, n.TerminalExamplePaths)
			}
		}
		out.TemplateProbability = weightedTemplateProbability(
			out.TemplateProbability, out.TotalObservations,
			n.TemplateProbability, n.TotalObservations,
		)
		out.TotalObservations += n.TotalObservations
		for name, child := range n.Children {
			childGroups[name] = append(childGroups[name], child)
		}
	}
	out.TerminalExamplePaths = mergeTerminalExamplePaths(examplesByNode)
	for name, group := range childGroups {
		out.Children[name] = mergeNodeList(group)
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
		TerminalExamplePaths:     maps.Clone(n.TerminalExamplePaths),
		TerminalObservationCount: n.TerminalObservationCount,
		TotalObservations:        n.TotalObservations,
		TemplateProbability:      n.TemplateProbability,
	}
	for name, child := range n.Children {
		out.Children[name] = cloneNode(child)
	}
	return out
}
