package loop

import (
	"fmt"

	"github.com/odigos-io/odigos/insights/internal/autopilot/schema"
	"github.com/odigos-io/odigos/insights/internal/autopilot/store"
)

// Reasons an analysisPlan calls the model.
const (
	reasonNoPriorSuccess     = "no_prior_success"
	reasonUnreadableSnapshot = "unreadable_snapshot"
	reasonPendingEdges       = "pending_edges"
)

// analysisRevision scopes acknowledged evidence. Changing the prompt, request
// schema, or model makes earlier successes invisible, so every transaction
// gets one full analysis under the new revision.
func analysisRevision(modelName string) string {
	return fmt.Sprintf("prompt=%s/request=%s/model=%s", schema.PromptRevision, schema.RequestVersion, modelName)
}

// analysisPlan is the level-based decision for one transaction: compare the
// observed call graph with the graph the model has acknowledged, and send only
// what is pending.
type analysisPlan struct {
	// Reason is empty when nothing is pending and the model is not called.
	Reason string
	// Mode is "full" or "delta".
	Mode string
	// Send is the graph for the request: the whole observed graph in full
	// mode, the pending edges in delta mode.
	Send []CallGraphEdge
	// Commit is the acknowledged graph to persist once the call succeeds:
	// the previous acknowledged graph plus everything observed now.
	Commit AppEvidence
}

// planAnalysis compares observed evidence with lastSuccess, the newest
// successful run at the current revision (nil if none). Failed runs never
// reach here, so edges from a failed or skipped call stay pending.
func planAnalysis(lastSuccess *store.Run, observed AppEvidence) analysisPlan {
	if lastSuccess == nil {
		return fullPlan(reasonNoPriorSuccess, observed)
	}
	snap, ok := parseEvidenceSnapshot(lastSuccess.EvidenceSnapshot)
	if !ok {
		return fullPlan(reasonUnreadableSnapshot, observed)
	}
	acked := AppEvidence{CallGraph: snap.CallGraph}
	pending := diffEvidence(acked, observed)
	if len(pending) == 0 {
		return analysisPlan{}
	}
	return analysisPlan{
		Reason: reasonPendingEdges,
		Mode:   "delta",
		Send:   pending,
		Commit: unionEvidence(acked, observed),
	}
}

func fullPlan(reason string, observed AppEvidence) analysisPlan {
	return analysisPlan{
		Reason: reason,
		Mode:   "full",
		Send:   observed.CallGraph,
		Commit: observed,
	}
}

// unionEvidence returns the sorted union of both call graphs.
func unionEvidence(a, b AppEvidence) AppEvidence {
	edges := make(map[callGraphEdgeKey]struct{}, len(a.CallGraph)+len(b.CallGraph))
	for _, ev := range []AppEvidence{a, b} {
		for _, e := range ev.CallGraph {
			edges[callGraphEdgeKey{caller: e.Caller, callee: e.Callee}] = struct{}{}
		}
	}
	return AppEvidence{CallGraph: callGraphEdges(edges)}
}
