package loop

import (
	"reflect"
	"testing"

	"github.com/odigos-io/odigos/insights/internal/autopilot/store"
)

func TestBuildAppEvidenceMergesPaths(t *testing.T) {
	// Same HashMap.get under two parents — app filter drops JDK; use rowan types.
	ctrl := "com.rowan.notify.Controller.entry"
	svc := "void com.rowan.notify.Service.handle()"
	pub := "void com.rowan.notify.RenderMessageProducer.publish(java.lang.String, com.rowan.notify.DocumentRequest)"

	nodes := []store.CallTreeNode{
		{ID: "1", ParentID: "", Name: ctrl},
		{ID: "2", ParentID: "1", Name: svc},
		{ID: "3", ParentID: "1", Name: pub},
		{ID: "4", ParentID: "2", Name: pub}, // publish also under service path
		{ID: "5", ParentID: "", Name: "java.lang.Object java.util.HashMap.get(java.lang.Object)"},
	}
	ev := BuildAppEvidence(nodes)
	if len(ev.CallGraph) != 3 {
		t.Fatalf("call graph: %+v", ev.CallGraph)
	}
	want := []CallGraphEdge{
		{Caller: ctrl, Callee: pub},
		{Caller: ctrl, Callee: svc},
		{Caller: svc, Callee: pub},
	}
	for i, edge := range want {
		if ev.CallGraph[i] != edge {
			t.Fatalf("edge %d: got %+v want %+v", i, ev.CallGraph[i], edge)
		}
	}
}

// Runtime and Odigos agent frames are not application code, so evidence skips
// over them. Libraries count as application code (see autopilot.FrameKindApp).
func TestBuildAppEvidenceIgnoresNonAppNodes(t *testing.T) {
	ctrl := "com.rowan.notify.Controller.entry"
	svc := "void com.rowan.notify.Service.handle()"
	repo := "void com.rowan.notify.Repo.load()"
	runtime := "void java.lang.reflect.Method.invoke(java.lang.Object, java.lang.Object[])"
	agent := "void io.odigos.spring.servlet.SpringServletAdvice.detach(long)"

	plain := []store.CallTreeNode{
		{ID: "1", Name: ctrl},
		{ID: "2", ParentID: "1", Name: svc},
		{ID: "3", ParentID: "2", Name: repo},
	}
	withNonApp := []store.CallTreeNode{
		{ID: "1", Name: ctrl},
		{ID: "r1", ParentID: "1", Name: runtime},
		{ID: "2", ParentID: "r1", Name: svc},
		{ID: "r2", ParentID: "2", Name: runtime},
		{ID: "a1", ParentID: "r2", Name: agent},
		{ID: "3", ParentID: "a1", Name: repo},
	}
	want := BuildAppEvidence(plain)
	got := BuildAppEvidence(withNonApp)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("evidence differs:\nwant %+v\ngot  %+v", want, got)
	}
	if hashEvidence(want) != hashEvidence(got) {
		t.Fatal("evidence hash differs with non-app nodes")
	}
	if len(got.CallGraph) != 2 {
		t.Fatalf("expected 2 edges, got %+v", got.CallGraph)
	}
}

func TestBuildAppEvidenceNonAppParentChainGuards(t *testing.T) {
	svc := "void com.rowan.notify.Service.handle()"
	runtime := "void java.lang.reflect.Method.invoke(java.lang.Object, java.lang.Object[])"
	nodes := []store.CallTreeNode{
		{ID: "a", ParentID: "b", Name: runtime}, // cycle of non-app nodes
		{ID: "b", ParentID: "a", Name: runtime},
		{ID: "1", ParentID: "a", Name: svc},
		{ID: "2", ParentID: "missing", Name: svc},
		{ID: "3", ParentID: "r", Name: svc}, // non-app root, no app ancestor
		{ID: "r", Name: runtime},
	}
	ev := BuildAppEvidence(nodes)
	if len(ev.CallGraph) != 0 {
		t.Fatalf("expected no edges, got %+v", ev.CallGraph)
	}
}
