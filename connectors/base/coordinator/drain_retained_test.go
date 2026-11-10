package coordinator

import (
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// TestCountRetainedManualInstrumentation covers what a drain reports as left behind. Getting this wrong is
// worse than not reporting at all: undercounting tells a customer their account is clean while agents keep
// exporting, and overcounting sends them hunting for instrumentation that is not there.
func TestCountRetainedManualInstrumentation(t *testing.T) {
	cases := []struct {
		name      string
		workloads []api.DiscoveredWorkload
		want      int
		wantNames []string
	}{
		{
			name: "manual and instrumented is retained",
			workloads: []api.DiscoveredWorkload{
				{Kind: "gcp.cloud-run", ServiceName: "checkout", ManagementMode: spec.ModeManual, Instrumented: true},
			},
			want:      1,
			wantNames: []string{"gcp.cloud-run/checkout"},
		},
		{
			// The drain disables every source before counting, so source state must NOT be part of the rule —
			// requiring an enabled source here would always count zero and silently defeat the whole report.
			name: "counted even though the drain already disabled its source",
			workloads: []api.DiscoveredWorkload{
				{Kind: "aws.lambda", ServiceName: "orders", ManagementMode: spec.ModeManual, Instrumented: true,
					SourceMatched: true, SourceEnabled: false},
			},
			want:      1,
			wantNames: []string{"aws.lambda/orders"},
		},
		{
			// Automatic mode is what the drain actually rolled back. If it is still flagged instrumented the
			// rollback failed, and that path already fails the drain loudly — counting it here would relabel a
			// real failure as "the customer's to clean up".
			name: "automatic mode is not retained",
			workloads: []api.DiscoveredWorkload{
				{Kind: "aws.lambda", ServiceName: "orders", ManagementMode: spec.ModeAutomatic, Instrumented: true},
			},
			want: 0,
		},
		{
			name: "manual but never instrumented is nothing to clean up",
			workloads: []api.DiscoveredWorkload{
				{Kind: "gcp.cloud-run", ServiceName: "checkout", ManagementMode: spec.ModeManual, Instrumented: false},
			},
			want: 0,
		},
		{
			// A type not enabled for instrumentation in any mode: an unrelated customer OpenTelemetry setup
			// could make the provider flag it, and it is not ours to report on.
			name: "no mode resolved is not retained",
			workloads: []api.DiscoveredWorkload{
				{Kind: "aws.ecs-cluster", ServiceName: "legacy", ManagementMode: "", Instrumented: true},
			},
			want: 0,
		},
		{
			name:      "nothing discovered",
			workloads: nil,
			want:      0,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, names := countRetainedManualInstrumentation(c.workloads)
			if got != c.want {
				t.Errorf("count = %d, want %d", got, c.want)
			}
			if len(names) != len(c.wantNames) {
				t.Fatalf("names = %v, want %v", names, c.wantNames)
			}
			for i := range names {
				if names[i] != c.wantNames[i] {
					t.Errorf("names[%d] = %q, want %q", i, names[i], c.wantNames[i])
				}
			}
		})
	}
}

// TestDrainShouldComplete pins the withhold rule: automatic rollback alone is not enough when manual-mode
// workloads are still instrumented — shutdownComplete must wait until the retained count is zero.
func TestDrainShouldComplete(t *testing.T) {
	if !drainShouldComplete(0) {
		t.Fatal("retained=0 must complete the drain")
	}
	if drainShouldComplete(1) {
		t.Fatal("retained>0 must withhold shutdownComplete")
	}
	if drainShouldComplete(3) {
		t.Fatal("retained>0 must withhold shutdownComplete")
	}
}

// TestRetainedNamesAreCapped keeps the sample bounded: it rides in a CRD status blob and a Kubernetes event
// message, both of which have size limits, while the count stays exact.
func TestRetainedNamesAreCapped(t *testing.T) {
	var workloads []api.DiscoveredWorkload
	for i := 0; i < maxRetainedWorkloadNames*3; i++ {
		workloads = append(workloads, api.DiscoveredWorkload{
			Kind:           "gcp.cloud-run",
			ServiceName:    "svc",
			ManagementMode: spec.ModeManual,
			Instrumented:   true,
		})
	}
	count, names := countRetainedManualInstrumentation(workloads)
	if count != len(workloads) {
		t.Errorf("count = %d, want the exact total %d — the count must not be capped", count, len(workloads))
	}
	if len(names) != maxRetainedWorkloadNames {
		t.Errorf("names = %d, want the cap %d", len(names), maxRetainedWorkloadNames)
	}
}

// TestMergeAwaitingManual ensures heartbeat health carries the withhold signal the controller reads.
func TestMergeAwaitingManual(t *testing.T) {
	c := &Coordinator{}
	health := map[string]any{"discoveryReady": true}
	c.mergeAwaitingManual(health)
	if _, ok := health["awaitingManualCleanup"]; ok {
		t.Fatal("expected no awaiting fields when retained is zero")
	}

	c.setAwaitingManual(2, []string{"gcp.cloud-run/checkout"})
	c.mergeAwaitingManual(health)
	if health["awaitingManualCleanup"] != true {
		t.Fatalf("awaitingManualCleanup = %v, want true", health["awaitingManualCleanup"])
	}
	if health["manualInstrumentationRetained"] != 2 {
		t.Fatalf("retained = %v, want 2", health["manualInstrumentationRetained"])
	}
	names, ok := health["manualInstrumentationWorkloads"].([]string)
	if !ok || len(names) != 1 || names[0] != "gcp.cloud-run/checkout" {
		t.Fatalf("workloads = %v", health["manualInstrumentationWorkloads"])
	}

	c.clearAwaitingManual()
	health2 := map[string]any{}
	c.mergeAwaitingManual(health2)
	if len(health2) != 0 {
		t.Fatalf("cleared awaiting should merge nothing, got %v", health2)
	}
}

