package graph

import (
	"testing"

	redisclient "github.com/odigos-io/odigos/central-backend/internal/redis"
)

func TestParseClusterSnapshot_Disabled(t *testing.T) {
	// The proxy omits `disabled` when false, and proxies before the field never send it.
	data := []byte(`{
		"clusterId": "c1",
		"namespaces": [{
			"name": "app",
			"selected": true,
			"workloads": [
				{"namespace": "app", "name": "checkout", "kind": "Deployment", "selected": false, "disabled": true, "health": "disabled"},
				{"namespace": "app", "name": "cart", "kind": "Deployment", "selected": true, "health": "success"}
			]
		}]
	}`)

	got, err := parseClusterSnapshot(&redisclient.ClusterSnapshotData{ClusterID: "c1", Data: data})
	if err != nil {
		t.Fatalf("parseClusterSnapshot() error = %v", err)
	}

	workloads := got.Namespaces[0].Workloads
	want := map[string]bool{"checkout": true, "cart": false}
	for _, w := range workloads {
		if w.Disabled == nil {
			t.Fatalf("workload %s disabled is nil, want %v", w.Name, want[w.Name])
		}
		if *w.Disabled != want[w.Name] {
			t.Errorf("workload %s disabled = %v, want %v", w.Name, *w.Disabled, want[w.Name])
		}
	}
}
