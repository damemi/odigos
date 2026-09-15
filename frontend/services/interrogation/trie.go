package interrogation

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
)

// buildCallTrie returns a flat list of call-path nodes from Redis hash maps.
// meta: nodeId -> parent\x1fframeMember ("name|frameType|sampleType")
// counts: nodeId -> sighting count string
// Roots have ParentID == "" (Redis parent "root"). Sorted by seenCount desc, then name.
func buildCallTrie(meta, counts map[string]string) []CallTrieNode {
	out := make([]CallTrieNode, 0, len(meta))
	for id, raw := range meta {
		parent, frame, ok := splitTrieMeta(raw)
		if !ok {
			continue
		}
		fn, ok := parseFunctionMember(frame)
		if !ok {
			continue
		}
		var seen int64
		if s, ok := counts[id]; ok {
			seen, _ = strconv.ParseInt(s, 10, 64)
		}
		parentID := parent
		if parentID == trieRootParent {
			parentID = ""
		}
		out = append(out, CallTrieNode{
			ID:         id,
			ParentID:   parentID,
			Name:       fn.Name,
			FrameType:  fn.FrameType,
			SampleType: fn.SampleType,
			SeenCount:  seen,
		})
	}
	sortCallTrieNodes(out)
	return out
}

func sortCallTrieNodes(nodes []CallTrieNode) {
	slices.SortFunc(nodes, func(a, b CallTrieNode) int {
		if c := cmp.Compare(b.SeenCount, a.SeenCount); c != 0 {
			return c
		}
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
}

func splitTrieMeta(raw string) (parent, frame string, ok bool) {
	i := strings.Index(raw, trieMetaSep)
	if i < 0 {
		return "", "", false
	}
	parent = raw[:i]
	frame = raw[i+len(trieMetaSep):]
	if parent == "" || frame == "" {
		return "", "", false
	}
	return parent, frame, true
}
