package loop

import (
	"testing"

	"github.com/odigos-io/odigos/api/k8sconsts"
	"github.com/odigos-io/odigos/insights/internal/autopilot/schema"
	"github.com/odigos-io/odigos/insights/internal/autopilot/store"
)

func TestAutopilotRuleLabels(t *testing.T) {
	source := store.SourceKey{
		Namespace:    "rowan",
		WorkloadKind: "Deployment",
		WorkloadName: "edge-gateway",
	}
	labels := autopilotRuleLabels(source)
	if labels[k8sconsts.OdigosProfilesManagedByLabel] != k8sconsts.OdigosAutopilotLoopManagedByValue {
		t.Fatalf("managed-by: %q", labels[k8sconsts.OdigosProfilesManagedByLabel])
	}
	if labels["odigos.io/autopilot-workload"] != "rowan.Deployment.edge-gateway" {
		t.Fatalf("workload label: %q", labels["odigos.io/autopilot-workload"])
	}
}

func TestWorkloadLabelValue(t *testing.T) {
	got := workloadLabelValue(store.SourceKey{
		Namespace:    "rowan",
		WorkloadKind: "Deployment",
		WorkloadName: "edge-gateway",
	})
	want := "rowan.Deployment.edge-gateway"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestSkipExistingProbes(t *testing.T) {
	recs := []schema.Recommendation{
		{
			Category: schema.CategoryObservability,
			Function: "com.example.UserService.getUser",
			Language: schema.LangJava,
			Probe:    schema.Probe{ClassName: "com.example.UserService", MethodName: "getUser"},
			Why:      "domain",
		},
		{
			Category: schema.CategorySecurity,
			Function: "com.example.Auth.check",
			Language: schema.LangJava,
			Probe:    schema.Probe{ClassName: "com.example.Auth", MethodName: "check"},
			Why:      "auth",
		},
	}
	existing := []customProbe{{Language: "java", Target: "com.example.UserService.getUser"}}
	got := skipExistingProbes(recs, existing)
	if len(got) != 1 || got[0].Function != "com.example.Auth.check" {
		t.Fatalf("got %+v", got)
	}
}

func TestProbeSlotsAvailable(t *testing.T) {
	existing := make([]customProbe, 18)
	for i := range existing {
		existing[i] = customProbe{Language: "java", Target: "t" + string(rune('a'+i))}
	}
	if got := probeSlotsAvailable(existing, 20); got != 2 {
		t.Fatalf("room: %d", got)
	}
	if got := probeSlotsAvailable(existing, 18); got != 0 {
		t.Fatalf("at cap: %d", got)
	}
	if got := probeSlotsAvailable(existing, 10); got != 0 {
		t.Fatalf("over cap: %d", got)
	}
}
