package loop

import (
	"testing"

	"github.com/odigos-io/odigos/insights/internal/autopilot/store"
)

func TestHashEvidenceStable(t *testing.T) {
	base := BuildAppEvidence([]store.CallTrieNode{
		{ID: "1", ParentID: "", Name: "com.example.A.a"},
		{ID: "2", ParentID: "1", Name: "com.example.B.b"},
	})
	h1 := hashEvidence(base)
	reordered := BuildAppEvidence([]store.CallTrieNode{
		{ID: "2", ParentID: "1", Name: "com.example.B.b"},
		{ID: "1", ParentID: "", Name: "com.example.A.a"},
	})
	if h1 != hashEvidence(reordered) {
		t.Fatal("node order must not change structural evidence hash")
	}
	withNew := BuildAppEvidence([]store.CallTrieNode{
		{ID: "1", ParentID: "", Name: "com.example.A.a"},
		{ID: "2", ParentID: "1", Name: "com.example.B.b"},
		{ID: "3", ParentID: "2", Name: "com.example.C.c"},
	})
	if h1 == hashEvidence(withNew) {
		t.Fatal("new function/edge should change hash")
	}
}

func TestDiffEvidence(t *testing.T) {
	prev := AppEvidence{
		CallGraph: []CallGraphEdge{{Caller: "com.example.A.a", Callee: "com.example.B.b"}},
	}
	curr := AppEvidence{
		CallGraph: []CallGraphEdge{
			{Caller: "com.example.A.a", Callee: "com.example.B.b"},
			{Caller: "com.example.B.b", Callee: "com.example.C.c"},
		},
	}
	addedEdges := diffEvidence(prev, curr)
	if len(addedEdges) != 1 || addedEdges[0].Callee != "com.example.C.c" {
		t.Fatalf("added edges: %+v", addedEdges)
	}
}
