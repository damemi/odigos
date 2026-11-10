package loop

import (
	"reflect"
	"testing"

	"github.com/odigos-io/odigos/insights/internal/interrogation/db/clickhouse"
)

func edges(pairs ...string) AppEvidence {
	var ev AppEvidence
	for i := 0; i+1 < len(pairs); i += 2 {
		ev.CallGraph = append(ev.CallGraph, CallGraphEdge{Caller: pairs[i], Callee: pairs[i+1]})
	}
	return ev
}

func successRun(t *testing.T, acked AppEvidence) *clickhouse.Run {
	t.Helper()
	snap, err := marshalEvidenceSnapshot(acked)
	if err != nil {
		t.Fatal(err)
	}
	return &clickhouse.Run{EvidenceSnapshot: snap, AnalysisStatus: clickhouse.AnalysisSucceeded}
}

func TestPlanAnalysisNoPriorSuccessSendsFull(t *testing.T) {
	observed := edges("a", "b", "b", "c")
	plan := planAnalysis(nil, observed)
	if plan.Reason != reasonNoPriorSuccess || plan.Mode != "full" {
		t.Fatalf("plan = %+v", plan)
	}
	if !reflect.DeepEqual(plan.Send, observed.CallGraph) || !reflect.DeepEqual(plan.Commit, observed) {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestPlanAnalysisNothingPendingSkips(t *testing.T) {
	acked := edges("a", "b", "b", "c")
	if plan := planAnalysis(successRun(t, acked), acked); plan.Reason != "" {
		t.Fatalf("plan = %+v, want skip", plan)
	}
	// Observed shrinking (e.g. a frame filter change) leaves nothing pending.
	if plan := planAnalysis(successRun(t, acked), edges("a", "b")); plan.Reason != "" {
		t.Fatalf("plan = %+v, want skip", plan)
	}
}

func TestPlanAnalysisSendsPendingAndCommitsUnion(t *testing.T) {
	acked := edges("a", "b", "x", "y")
	observed := edges("a", "b", "b", "c")
	plan := planAnalysis(successRun(t, acked), observed)
	if plan.Reason != reasonPendingEdges || plan.Mode != "delta" {
		t.Fatalf("plan = %+v", plan)
	}
	if want := edges("b", "c").CallGraph; !reflect.DeepEqual(plan.Send, want) {
		t.Fatalf("send = %v, want %v", plan.Send, want)
	}
	if want := edges("a", "b", "b", "c", "x", "y"); !reflect.DeepEqual(plan.Commit, want) {
		t.Fatalf("commit = %v, want %v", plan.Commit, want)
	}
}

// A failed call is never the lastSuccess, so edges from it stay pending even
// after more edges arrive.
func TestPlanAnalysisFailedEdgesStayPending(t *testing.T) {
	acked := edges("a", "b")
	// Tick 1 saw b->c but the call failed: nothing was acknowledged.
	// Tick 2 sees c->d as well.
	observed := edges("a", "b", "b", "c", "c", "d")
	plan := planAnalysis(successRun(t, acked), observed)
	if want := edges("b", "c", "c", "d").CallGraph; !reflect.DeepEqual(plan.Send, want) {
		t.Fatalf("send = %v, want %v", plan.Send, want)
	}
}

func TestPlanAnalysisUnreadableSnapshotSendsFull(t *testing.T) {
	observed := edges("a", "b")
	plan := planAnalysis(&clickhouse.Run{EvidenceSnapshot: "not json"}, observed)
	if plan.Reason != reasonUnreadableSnapshot || plan.Mode != "full" {
		t.Fatalf("plan = %+v", plan)
	}
}
