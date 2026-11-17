package loop

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/odigos-io/odigos/common/autopilot"
	"github.com/odigos-io/odigos/insights/internal/autopilot/db/clickhouse"
)

// CallGraphEdge is a merged caller→callee link between application functions.
type CallGraphEdge struct {
	Caller string
	Callee string
}

// AppEvidence is loop-ready evidence: application call-graph edges, without path-identity explosion.
type AppEvidence struct {
	CallGraph []CallGraphEdge
}

// BuildAppEvidence rolls path trie nodes into a merged application call graph.
// Library nodes are skipped: an edge's caller is the nearest application ancestor, so
// app → library → app paths yield the same evidence as app → app.
func BuildAppEvidence(nodes []clickhouse.CallTrieNode) AppEvidence {
	if len(nodes) == 0 {
		return AppEvidence{}
	}

	byID := make(map[string]clickhouse.CallTrieNode, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}

	edges := map[callGraphEdgeKey]struct{}{}
	for _, n := range nodes {
		if !autopilot.IsApplicationFrame(n.Name) || n.ParentID == "" {
			continue
		}
		caller, ok := nearestAppAncestor(byID, n)
		if !ok {
			continue
		}
		edges[callGraphEdgeKey{caller: caller, callee: n.Name}] = struct{}{}
	}

	return AppEvidence{CallGraph: callGraphEdges(edges)}
}

// nearestAppAncestor walks ParentID through non-app nodes. It stops on a missing
// parent or a cycle.
func nearestAppAncestor(byID map[string]clickhouse.CallTrieNode, n clickhouse.CallTrieNode) (string, bool) {
	seen := map[string]struct{}{n.ID: {}}
	for id := n.ParentID; id != ""; {
		if _, dup := seen[id]; dup {
			return "", false
		}
		seen[id] = struct{}{}
		p, ok := byID[id]
		if !ok {
			return "", false
		}
		if autopilot.IsApplicationFrame(p.Name) {
			return p.Name, true
		}
		id = p.ParentID
	}
	return "", false
}

// hashEvidence fingerprints application call-graph edges.
func hashEvidence(ev AppEvidence) string {
	edges := append([]CallGraphEdge(nil), ev.CallGraph...)
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Caller != edges[j].Caller {
			return edges[i].Caller < edges[j].Caller
		}
		return edges[i].Callee < edges[j].Callee
	})

	h := sha256.New()
	for _, e := range edges {
		fmt.Fprintf(h, "E\x00%s\x00%s\n", e.Caller, e.Callee)
	}
	return hex.EncodeToString(h.Sum(nil))
}

type callGraphEdgeKey struct {
	caller, callee string
}

func callGraphEdges(edges map[callGraphEdgeKey]struct{}) []CallGraphEdge {
	out := make([]CallGraphEdge, 0, len(edges))
	for k := range edges {
		out = append(out, CallGraphEdge{Caller: k.caller, Callee: k.callee})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Caller != out[j].Caller {
			return out[i].Caller < out[j].Caller
		}
		return out[i].Callee < out[j].Callee
	})
	return out
}
