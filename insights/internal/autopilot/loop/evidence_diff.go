package loop

import (
	"encoding/json"
	"fmt"
	"strings"
)

// EvidenceSnapshot is persisted on tx_runs for delta payloads on later ticks.
type EvidenceSnapshot struct {
	CallGraph []CallGraphEdge `json:"callGraph"`
}

func marshalEvidenceSnapshot(ev AppEvidence) (string, error) {
	snap := EvidenceSnapshot{CallGraph: ev.CallGraph}
	raw, err := json.Marshal(snap)
	if err != nil {
		return "", fmt.Errorf("marshal evidence snapshot: %w", err)
	}
	return string(raw), nil
}

func parseEvidenceSnapshot(raw string) (EvidenceSnapshot, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return EvidenceSnapshot{}, false
	}
	var snap EvidenceSnapshot
	if err := json.Unmarshal([]byte(raw), &snap); err != nil {
		return EvidenceSnapshot{}, false
	}
	return snap, true
}

// diffEvidence returns edges present in curr but not in prev.
func diffEvidence(prev, curr AppEvidence) (addedEdges []CallGraphEdge) {
	prevEdge := map[callGraphEdgeKey]struct{}{}
	for _, e := range prev.CallGraph {
		prevEdge[callGraphEdgeKey{caller: e.Caller, callee: e.Callee}] = struct{}{}
	}
	for _, e := range curr.CallGraph {
		k := callGraphEdgeKey{caller: e.Caller, callee: e.Callee}
		if _, ok := prevEdge[k]; !ok {
			addedEdges = append(addedEdges, e)
		}
	}
	return addedEdges
}
