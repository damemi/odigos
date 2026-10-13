package snapshotsummary

import (
	"reflect"
	"testing"
)

func TestCompute_CountsWorkloadsAndNamespaces(t *testing.T) {
	data := []byte(`{
		"clusterId": "c1",
		"timestamp": "2026-09-30T10:00:00Z",
		"namespaces": [
			{"name": "shop", "selected": true, "workloads": [
				{"namespace": "shop", "name": "cart", "kind": "Deployment", "selected": true, "health": "Success"},
				{"namespace": "shop", "name": "checkout", "kind": "Deployment", "selected": true, "health": "Error"},
				{"namespace": "shop", "name": "db", "kind": "StatefulSet", "selected": false, "health": "Disabled"}
			]},
			{"name": "infra", "selected": false, "workloads": [
				{"namespace": "infra", "name": "agent", "kind": "DaemonSet", "selected": true, "health": "Pending"},
				{"namespace": "infra", "name": "cron", "kind": "CronJob", "selected": false, "health": "Disabled"}
			]},
			{"name": "empty", "selected": false, "workloads": []}
		]
	}`)

	got, err := Compute(data, 1)
	if err != nil {
		t.Fatalf("Compute failed: %v", err)
	}

	want := &Summary{
		Workloads:                  5,
		InstrumentedWorkloads:      3,
		WorkloadsByHealth:          map[string]int{"Success": 1, "Error": 1, "Pending": 1},
		Namespaces:                 3,
		AutoInstrumentedNamespaces: 1,
		SnapshotTimestamp:          1790762400,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected summary:\n got: %+v\nwant: %+v", got, want)
	}
}

func TestSummary_HealthCategories(t *testing.T) {
	summary := &Summary{WorkloadsByHealth: map[string]int{
		"Error": 1, "Failure": 2, "Pending": 3, "Waiting": 4, "Success": 5, "Notice": 6, "Unsupported": 7,
	}}
	if got := summary.WorkloadsWithIssues(); got != 3 {
		t.Fatalf("WorkloadsWithIssues() = %d, want 3", got)
	}
	if got := summary.WorkloadsInProgress(); got != 7 {
		t.Fatalf("WorkloadsInProgress() = %d, want 7", got)
	}

	empty := &Summary{}
	if empty.WorkloadsWithIssues() != 0 || empty.WorkloadsInProgress() != 0 {
		t.Fatal("expected zero counts for a summary without health counts")
	}
}

func TestCompute_FallsBackToGivenTimestamp(t *testing.T) {
	got, err := Compute([]byte(`{"namespaces": []}`), 1234)
	if err != nil {
		t.Fatalf("Compute failed: %v", err)
	}
	if got.SnapshotTimestamp != 1234 {
		t.Fatalf("expected fallback timestamp 1234, got %d", got.SnapshotTimestamp)
	}
	if got.WorkloadsByHealth != nil {
		t.Fatalf("expected no health counts for an empty cluster, got %v", got.WorkloadsByHealth)
	}
}

func TestCompute_RejectsMalformedData(t *testing.T) {
	if _, err := Compute([]byte(`{"namespaces": [`), 1); err == nil {
		t.Fatal("expected an error for malformed snapshot data")
	}
}
