package snapshotworker

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/odigos-io/central-backend/internal/config"
	"github.com/odigos-io/central-backend/internal/consts"
	"github.com/odigos-io/central-backend/internal/logger"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
	"github.com/odigos-io/central-backend/internal/snapshotsummary"
)

func setupTestPool(t *testing.T) (*miniredis.Miniredis, *redisclient.RedisClient, *WorkerPool) {
	t.Helper()
	logger.InitLogger()

	mr := miniredis.RunT(t)
	config.Config.RedisAddr = mr.Addr()
	rc, err := redisclient.NewRedisClient(context.Background())
	if err != nil {
		t.Fatalf("failed to create redis client: %v", err)
	}
	return mr, rc, NewWorkerPool(rc, 1, 1)
}

func TestProcessJob_StoresSummaryWithSnapshot(t *testing.T) {
	_, rc, pool := setupTestPool(t)
	ctx := context.Background()

	pool.processJob(0, SnapshotJob{
		ClusterID:   "c1",
		ClusterName: "cluster-1",
		Timestamp:   100,
		Data:        []byte(`{"namespaces":[{"name":"a","selected":true,"workloads":[{"selected":true,"health":"Failure"},{"selected":false,"health":"Disabled"}]}]}`),
	})

	summaries, err := rc.GetClusterSnapshotSummaries(ctx, []string{"c1"})
	if err != nil {
		t.Fatalf("GetClusterSnapshotSummaries failed: %v", err)
	}
	got := summaries["c1"]
	if got == nil {
		t.Fatal("expected a summary to be stored with the snapshot")
	}
	if got.Workloads != 2 || got.InstrumentedWorkloads != 1 || got.WorkloadsByHealth["Failure"] != 1 || got.AutoInstrumentedNamespaces != 1 || got.SnapshotTimestamp != 100 {
		t.Fatalf("unexpected summary: %+v", got)
	}
}

func TestBackfillSummaries_FillsOnlyMissingSummaries(t *testing.T) {
	mr, rc, pool := setupTestPool(t)
	ctx := context.Background()

	// Stored before summaries existed.
	mr.HSet(consts.GetClusterSnapshotKey("old"),
		consts.SnapshotFieldData, `{"namespaces":[{"name":"a","selected":false,"workloads":[{"selected":true,"health":"Success"},{"selected":false,"health":"Disabled"}]}]}`,
		consts.SnapshotFieldTimestamp, "100",
		consts.SnapshotFieldClusterName, "old",
	)
	// Already summarized; the backfill must not touch it.
	if err := rc.SetClusterSnapshot(ctx, "new", "new", []byte(`{"namespaces":[]}`), 200, &snapshotsummary.Summary{Workloads: 42, SnapshotTimestamp: 200}); err != nil {
		t.Fatalf("SetClusterSnapshot failed: %v", err)
	}
	// Unparseable data is skipped without failing the rest.
	mr.HSet(consts.GetClusterSnapshotKey("broken"), consts.SnapshotFieldData, `{"namespaces":[`)

	pool.backfillSummaries()

	summaries, err := rc.GetClusterSnapshotSummaries(ctx, []string{"old", "new", "broken"})
	if err != nil {
		t.Fatalf("GetClusterSnapshotSummaries failed: %v", err)
	}
	if old := summaries["old"]; old == nil || old.Workloads != 2 || old.InstrumentedWorkloads != 1 || old.SnapshotTimestamp != 100 {
		t.Fatalf("expected the old snapshot to be summarized, got %+v", old)
	}
	if kept := summaries["new"]; kept == nil || kept.Workloads != 42 {
		t.Fatalf("expected the existing summary to be kept, got %+v", kept)
	}
	if _, ok := summaries["broken"]; ok {
		t.Fatal("expected no summary for unparseable snapshot data")
	}
}
