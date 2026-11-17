package loop

import (
	"reflect"
	"testing"

	"github.com/odigos-io/odigos/insights/internal/autopilot/db/clickhouse"
)

func TestBuildAppEvidenceMergesPaths(t *testing.T) {
	// Same HashMap.get under two parents — app filter drops JDK; use rowan types.
	ctrl := "com.rowan.notify.Controller.entry"
	svc := "void com.rowan.notify.Service.handle()"
	pub := "void com.rowan.notify.RenderMessageProducer.publish(java.lang.String, com.rowan.notify.DocumentRequest)"

	nodes := []clickhouse.CallTrieNode{
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

func TestBuildAppEvidenceIgnoresLibraryNodes(t *testing.T) {
	ctrl := "com.rowan.notify.Controller.entry"
	svc := "void com.rowan.notify.Service.handle()"
	repo := "void com.rowan.notify.Repo.load()"
	spring := "void org.springframework.web.servlet.DispatcherServlet.doDispatch()"
	tomcat := "void org.apache.catalina.core.StandardWrapperValve.invoke()"

	plain := []clickhouse.CallTrieNode{
		{ID: "1", Name: ctrl},
		{ID: "2", ParentID: "1", Name: svc},
		{ID: "3", ParentID: "2", Name: repo},
	}
	withLibs := []clickhouse.CallTrieNode{
		{ID: "1", Name: ctrl},
		{ID: "l1", ParentID: "1", Name: spring},
		{ID: "2", ParentID: "l1", Name: svc},
		{ID: "l2", ParentID: "2", Name: spring},
		{ID: "l3", ParentID: "l2", Name: tomcat},
		{ID: "3", ParentID: "l3", Name: repo},
	}
	want := BuildAppEvidence(plain)
	got := BuildAppEvidence(withLibs)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("evidence differs:\nwant %+v\ngot  %+v", want, got)
	}
	if hashEvidence(want) != hashEvidence(got) {
		t.Fatal("evidence hash differs with library nodes")
	}
	if len(got.CallGraph) != 2 {
		t.Fatalf("expected 2 edges, got %+v", got.CallGraph)
	}
}

func TestBuildAppEvidenceLibraryParentChainGuards(t *testing.T) {
	svc := "void com.rowan.notify.Service.handle()"
	spring := "void org.springframework.web.servlet.DispatcherServlet.doDispatch()"
	nodes := []clickhouse.CallTrieNode{
		{ID: "a", ParentID: "b", Name: spring}, // cycle of library nodes
		{ID: "b", ParentID: "a", Name: spring},
		{ID: "1", ParentID: "a", Name: svc},
		{ID: "2", ParentID: "missing", Name: svc},
		{ID: "3", ParentID: "l", Name: svc}, // library root, no app ancestor
		{ID: "l", Name: spring},
	}
	ev := BuildAppEvidence(nodes)
	if len(ev.CallGraph) != 0 {
		t.Fatalf("expected no edges, got %+v", ev.CallGraph)
	}
}
