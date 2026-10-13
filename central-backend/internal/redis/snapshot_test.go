package redisclient

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/odigos-io/central-backend/internal/consts"
	"github.com/odigos-io/central-backend/internal/snapshotsummary"
)

func TestSetClusterSnapshot_StoresAndClearsSummary(t *testing.T) {
	mr, rc := setupTestRedis(t)
	defer mr.Close()
	ctx := context.Background()

	summary := &snapshotsummary.Summary{Workloads: 3, InstrumentedWorkloads: 2, WorkloadsByHealth: map[string]int{"Success": 2}, SnapshotTimestamp: 100}
	if err := rc.SetClusterSnapshot(ctx, "c1", "cluster-1", []byte(`{"namespaces":[]}`), 100, summary); err != nil {
		t.Fatalf("SetClusterSnapshot failed: %v", err)
	}

	got, err := rc.GetClusterSnapshotSummaries(ctx, []string{"c1", "no-snapshot"})
	if err != nil {
		t.Fatalf("GetClusterSnapshotSummaries failed: %v", err)
	}
	if len(got) != 1 || !reflect.DeepEqual(got["c1"], summary) {
		t.Fatalf("expected only the c1 summary %+v, got %+v", summary, got)
	}

	// A snapshot stored without a summary must not keep the previous one.
	if err := rc.SetClusterSnapshot(ctx, "c1", "cluster-1", []byte(`{"namespaces":[`), 200, nil); err != nil {
		t.Fatalf("SetClusterSnapshot failed: %v", err)
	}
	got, err = rc.GetClusterSnapshotSummaries(ctx, []string{"c1"})
	if err != nil {
		t.Fatalf("GetClusterSnapshotSummaries failed: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected the summary to be cleared, got %+v", got["c1"])
	}
	if data := mr.HGet(consts.GetClusterSnapshotKey("c1"), consts.SnapshotFieldData); data != `{"namespaces":[` {
		t.Fatalf("expected the new snapshot data to be stored, got %q", data)
	}
}

func TestSetClusterSnapshotSummaryIfMissing(t *testing.T) {
	mr, rc := setupTestRedis(t)
	defer mr.Close()
	ctx := context.Background()
	key := consts.GetClusterSnapshotKey("c1")

	backfilled := &snapshotsummary.Summary{Workloads: 1}

	written, err := rc.SetClusterSnapshotSummaryIfMissing(ctx, "c1", backfilled)
	if err != nil {
		t.Fatalf("SetClusterSnapshotSummaryIfMissing failed: %v", err)
	}
	if written || mr.Exists(key) {
		t.Fatal("expected no write, and no hash created, for a cluster without a snapshot")
	}

	mr.HSet(key, consts.SnapshotFieldData, `{"namespaces":[]}`)
	written, err = rc.SetClusterSnapshotSummaryIfMissing(ctx, "c1", backfilled)
	if err != nil {
		t.Fatalf("SetClusterSnapshotSummaryIfMissing failed: %v", err)
	}
	if !written {
		t.Fatal("expected the summary to be written for a snapshot without one")
	}

	written, err = rc.SetClusterSnapshotSummaryIfMissing(ctx, "c1", &snapshotsummary.Summary{Workloads: 99})
	if err != nil {
		t.Fatalf("SetClusterSnapshotSummaryIfMissing failed: %v", err)
	}
	if written {
		t.Fatal("expected an existing summary to be kept")
	}
	got, err := rc.GetClusterSnapshotSummaries(ctx, []string{"c1"})
	if err != nil {
		t.Fatalf("GetClusterSnapshotSummaries failed: %v", err)
	}
	if got["c1"] == nil || got["c1"].Workloads != 1 {
		t.Fatalf("expected the first summary to be kept, got %+v", got["c1"])
	}
}

func TestGetClusterSnapshotIDs(t *testing.T) {
	mr, rc := setupTestRedis(t)
	defer mr.Close()
	ctx := context.Background()

	for _, id := range []string{"a", "b"} {
		if err := rc.SetClusterSnapshot(ctx, id, id, []byte(`{}`), 1, nil); err != nil {
			t.Fatalf("SetClusterSnapshot failed: %v", err)
		}
	}
	mr.HSet(consts.GetProxyMetadataKey("a"), consts.ProxyMetaFieldPlatformName, "a")

	ids, err := rc.GetClusterSnapshotIDs(ctx)
	if err != nil {
		t.Fatalf("GetClusterSnapshotIDs failed: %v", err)
	}
	sort.Strings(ids)
	if !reflect.DeepEqual(ids, []string{"a", "b"}) {
		t.Fatalf("expected [a b], got %v", ids)
	}
}

func TestDeleteProxyConnection_RemovesSnapshot(t *testing.T) {
	mr, rc := setupTestRedis(t)
	defer mr.Close()
	ctx := context.Background()

	if err := rc.SetProxyMetadata(ctx, "p1", "cluster-1", "k8s", "v1.38.0", ""); err != nil {
		t.Fatalf("SetProxyMetadata failed: %v", err)
	}
	if err := rc.SetClusterSnapshot(ctx, "p1", "cluster-1", []byte(`{}`), 1, &snapshotsummary.Summary{}); err != nil {
		t.Fatalf("SetClusterSnapshot failed: %v", err)
	}

	if err := rc.DeleteProxyConnection(ctx, "p1"); err != nil {
		t.Fatalf("DeleteProxyConnection failed: %v", err)
	}
	if mr.Exists(consts.GetProxyMetadataKey("p1")) || mr.Exists(consts.GetClusterSnapshotKey("p1")) {
		t.Fatal("expected both the proxy metadata and the snapshot to be deleted")
	}
}

func TestWorkloadSummaryWithoutClusterSnapshot(t *testing.T) {
	mr, rc := setupTestRedis(t)
	defer mr.Close()
	ctx := context.Background()
	summary := &snapshotsummary.Summary{Workloads: 7, InstrumentedWorkloads: 4, WorkloadsByHealth: map[string]int{"Success": 2, "Error": 1, "Waiting": 1}, SnapshotTimestamp: 100}
	if err := rc.SetWorkloadSummary(ctx, "vm-1", summary); err != nil {
		t.Fatal(err)
	}
	lastSeen, err := rc.client.HGet(ctx, consts.GetProxyMetadataKey("vm-1"), consts.ProxyMetaFieldLastSeenAt).Int64()
	if err != nil || lastSeen <= 0 {
		t.Fatalf("summary did not update lastSeenAt: %d, %v", lastSeen, err)
	}
	got, err := rc.GetClusterSnapshotSummaries(ctx, []string{"vm-1", "unknown"})
	if err != nil || !reflect.DeepEqual(got["vm-1"], summary) {
		t.Fatalf("summaries = %+v, err = %v", got, err)
	}
	snapshot, err := rc.GetClusterSnapshot(ctx, "vm-1")
	if err != nil || snapshot != nil {
		t.Fatalf("summary-only agent produced a snapshot: %+v, %v", snapshot, err)
	}
	snapshots, err := rc.GetAllClusterSnapshots(ctx)
	if err != nil || len(snapshots) != 0 {
		t.Fatalf("summary-only agent leaked into cluster snapshots: %+v, %v", snapshots, err)
	}
	invalid := *summary
	invalid.InstrumentedWorkloads = 8
	if err := rc.SetWorkloadSummary(ctx, "vm-1", &invalid); err == nil {
		t.Fatal("accepted invalid summary")
	}
	if err := rc.DeleteProxyConnection(ctx, "vm-1"); err != nil {
		t.Fatal(err)
	}
	got, err = rc.GetClusterSnapshotSummaries(ctx, []string{"vm-1"})
	if err != nil || len(got) != 0 {
		t.Fatalf("deleted agent retained stats: %+v, %v", got, err)
	}
}
