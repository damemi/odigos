package apimap

import (
	"testing"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// A call path has the same stored node id in the events and the samples trees;
// the wire ids must still be unique within the transaction.
func TestAutopilotCallTreeListScopesNodeIDsBySampleType(t *testing.T) {
	node := func(sampleType, nodeID, parent string) repo.CallTreeNode {
		return repo.CallTreeNode{SampleType: sampleType, NodeID: nodeID, Parent: parent, FunctionName: "f", FrameType: "jvm"}
	}
	got := AutopilotCallTreeList([]repo.CallTreeTransaction{{
		TransactionID: 7,
		Operation:     "POST /claims",
		EntryKind:     "HTTP",
		Nodes: []repo.CallTreeNode{
			node("events", "a", "root"), node("events", "b", "a"),
			node("samples", "a", "root"), node("samples", "b", "a"),
		},
	}})
	if got.Count != 1 || got.Items[0].TransactionId != 7 || got.Items[0].Operation != "POST /claims" {
		t.Fatalf("list = %+v", got)
	}
	parents := map[string]string{}
	for _, n := range got.Items[0].Nodes {
		if _, dup := parents[n.Id]; dup {
			t.Fatalf("node id %q repeats", n.Id)
		}
		parents[n.Id] = n.ParentId
	}
	want := map[string]string{"events:a": "", "events:b": "events:a", "samples:a": "", "samples:b": "samples:a"}
	for id, parent := range want {
		if got, ok := parents[id]; !ok || got != parent {
			t.Fatalf("node %q parent = %q (present %v), want %q", id, got, ok, parent)
		}
	}
}

// The operation name and identity dimensions come from the stored operation, as
// on the other transaction endpoints, and stay empty until it is stored.
func TestAutopilotCallTreeListSplitsTheOperation(t *testing.T) {
	got := AutopilotCallTreeList([]repo.CallTreeTransaction{
		{TransactionID: 1, Operation: "POST /buy [http.response.status_code=500]", EntryKind: "HTTP"},
		{TransactionID: 2},
	})
	stored := got.Items[0]
	if stored.OperationName != "POST /buy" {
		t.Fatalf("operation name = %q", stored.OperationName)
	}
	if len(stored.IdentityDimensions) != 1 || stored.IdentityDimensions[0].Key != "http.response.status_code" || stored.IdentityDimensions[0].Value != "500" {
		t.Fatalf("identity dimensions = %+v", stored.IdentityDimensions)
	}
	if missing := got.Items[1]; missing.OperationName != "" || len(missing.IdentityDimensions) != 0 {
		t.Fatalf("unstored transaction = %+v", missing)
	}
}
