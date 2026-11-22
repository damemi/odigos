package loop

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/odigos-io/odigos/insights/internal/autopilot/schema"
	"github.com/odigos-io/odigos/insights/internal/autopilot/store"
)

func TestBuildAnalysisRequestIsEvidenceOnly(t *testing.T) {
	tx := transactionTrie{
		Transaction: store.Transaction{ID: "GET /users", RootSpanName: "GET", RootSpanKind: "SERVER"},
		Nodes: []store.CallTrieNode{
			{ID: "1", Name: "com.example.Foo.bar"},
		},
	}
	ev := BuildAppEvidence(tx.Nodes)
	req := buildFullAnalysisRequest(
		store.SourceKey{Namespace: "ns", WorkloadKind: "Deployment", WorkloadName: "api", ContainerName: "app"},
		[]customProbe{{RuleName: "r1", Language: "java", Target: "com.example.Foo.bar"}},
		tx,
		ev,
	)
	raw, err := schema.MarshalUserMessage(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "You analyze") || strings.Contains(raw, "## Response") {
		t.Fatalf("request contains prompt prose:\n%s", raw)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got["schemaVersion"] != schema.RequestVersion {
		t.Fatalf("schemaVersion %v", got["schemaVersion"])
	}
	if got["evidenceMode"] != "full" {
		t.Fatalf("evidenceMode %v", got["evidenceMode"])
	}
	if _, ok := got["recommendations"]; ok {
		t.Fatal("request must not include the response field recommendations")
	}
}
