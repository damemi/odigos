package schema

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNewFullEvidenceRequestSortsAndDefaultsProbes(t *testing.T) {
	req := NewFullEvidenceRequest(
		Service{Namespace: "ns", Kind: "Deployment", Name: "api", Container: "app"},
		nil,
		RequestTxn{ID: "GET /users", RootSpanName: "GET /users", RootSpanKind: "SERVER"},
		[]RequestCallEdge{
			{Caller: "com.example.B.b", Callee: "com.example.C.c"},
			{Caller: "com.example.A.a", Callee: "com.example.B.b"},
		},
	)
	if req.SchemaVersion != RequestVersion {
		t.Fatalf("schemaVersion %q", req.SchemaVersion)
	}
	if req.EvidenceMode != "full" {
		t.Fatalf("evidenceMode %q", req.EvidenceMode)
	}
	if req.ExistingProbes == nil || len(req.ExistingProbes) != 0 {
		t.Fatalf("existingProbes should be empty slice, got %#v", req.ExistingProbes)
	}
	if len(req.CallGraph) != 2 || req.CallGraph[0].Caller != "com.example.A.a" {
		t.Fatalf("graph order: %+v", req.CallGraph)
	}
}

func TestNewDeltaEvidenceRequest(t *testing.T) {
	req := NewDeltaEvidenceRequest(
		Service{Namespace: "ns", Kind: "Deployment", Name: "api", Container: "app"},
		nil,
		RequestTxn{ID: "GET /users"},
		[]RequestCallEdge{{Caller: "com.example.A.a", Callee: "com.example.New.method"}},
	)
	if req.EvidenceMode != "delta" {
		t.Fatalf("evidenceMode %q", req.EvidenceMode)
	}
	if len(req.AddedCallGraph) != 1 || len(req.CallGraph) != 0 {
		t.Fatalf("delta fields: %+v", req)
	}
}

func TestMarshalUserMessageIsDataOnly(t *testing.T) {
	req := NewFullEvidenceRequest(
		Service{Namespace: "ns", Kind: "Deployment", Name: "api", Container: "app"},
		[]ExistingProbe{{Language: "java", Target: "com.example.Foo.bar", RuleName: "r1"}},
		RequestTxn{ID: "GET /users"},
		[]RequestCallEdge{{Caller: "com.example.Foo.bar", Callee: "com.example.Foo.baz"}},
	)
	raw, err := MarshalUserMessage(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "You analyze") || strings.Contains(raw, "Recommend functions") {
		t.Fatalf("user message contains system instructions:\n%s", raw)
	}
	if strings.Contains(raw, "schemaVersion") && strings.Contains(strings.ToLower(raw), "recommendations") {
		t.Fatalf("user message looks like a response schema:\n%s", raw)
	}

	var got AnalysisRequest
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got.Transaction.ID != "GET /users" || got.Service.Name != "api" || len(got.ExistingProbes) != 1 {
		t.Fatalf("round trip: %+v", got)
	}
}

func TestSystemPromptAndResponseFormatAreStatic(t *testing.T) {
	if !strings.Contains(SystemPrompt, "AnalysisRequest") {
		t.Fatal("system prompt should describe the request payload")
	}
	if !strings.Contains(SystemPrompt, "evidenceMode") {
		t.Fatal("system prompt should describe delta/full evidence")
	}
	if !strings.Contains(SystemPrompt, "Justification (why)") {
		t.Fatal("system prompt should require evidence-grounded why")
	}
	if !strings.Contains(SystemPrompt, "caller→callee") {
		t.Fatal("system prompt should tell the model to cite call-graph edges")
	}
	if strings.Contains(SystemPrompt, `"recommendations"`) {
		t.Fatal("system prompt should not paste the output JSON")
	}
	rf := StrictResponseFormat()
	if rf["type"] != "json_schema" {
		t.Fatalf("type %v", rf["type"])
	}
	js, ok := rf["json_schema"].(map[string]any)
	if !ok {
		t.Fatal("json_schema missing")
	}
	if js["strict"] != true || js["name"] != "autopilot_analysis" {
		t.Fatalf("json_schema %+v", js)
	}
}
