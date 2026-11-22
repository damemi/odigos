package apimap

import (
	"testing"
	"time"

	"github.com/odigos-io/odigos/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

func TestFromStorageHealth(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	in := repo.StorageHealth{
		CheckedAt: now,
		Status:    repo.StorageStatusHealthy,
		Connection: repo.StorageConnection{
			Reachable: true, PingLatencyMs: 3, Version: "17.6", Database: "odigos_insights", UptimeSeconds: 9,
		},
		Disk: repo.StorageDisk{DatabaseBytes: 7, VolumeBytes: 100, UsedPercent: 7, Status: "ok"},
		Connections: repo.StorageConnections{
			Active: 1, Idle: 2, Total: 3, Max: 100, PoolAcquired: 1, PoolTotal: 4, PoolMax: 8, PoolWaitCount: 5,
		},
		Database: repo.StorageDatabaseStats{CacheHitPercent: 99.5, Commits: 10, Deadlocks: 1, StatsSince: now},
		Queries: repo.StorageQueries{
			Active: 1, LongestSeconds: 0.2,
			Running: []repo.StorageQuery{{ElapsedSeconds: 0.2, User: "u", State: "active", WaitEvent: "Lock:tuple", Query: "SELECT 1"}},
			TopStatements: []repo.StorageStatement{{
				Query: "SELECT heavy", Calls: 3, TotalSeconds: 1.5, MeanMs: 500, Rows: 9,
			}},
			StatementsAvailable: true,
		},
		Tables:      []repo.StorageTable{{Name: "t", Rows: 2, DeadRows: 1, BytesOnDisk: 3, LastVacuumAt: now}},
		Maintenance: repo.StorageMaintenance{DeadRows: 1, WaitingLocks: 2, OldestTransactionSeconds: 4},
		Retention: repo.StorageRetention{
			ObservationRetentionDays: 120, RelationRetentionDays: 14, AttemptRetentionDays: 30,
			EverRan: true, LastRunAt: now, LastRunOK: true, ObservationsDeleted: 6, AttemptsDeleted: 2,
		},
		Writeback: repo.StorageWriteback{
			FlushIntervalSeconds: 15, EverFlushed: true, LastFlushOK: true, LastFlushAt: now,
		},
	}
	out := FromStorageHealth(in)
	if out.Status != apitypes.StorageHealthStatusHealthy {
		t.Fatalf("status = %q", out.Status)
	}
	if out.Connection.UptimeSeconds != 9 || out.Connection.Version != "17.6" {
		t.Fatalf("connection = %+v", out.Connection)
	}
	if out.Disk.DatabaseBytes != 7 || out.Disk.VolumeBytes != 100 {
		t.Fatalf("disk = %+v", out.Disk)
	}
	if out.Connections.PoolMax != 8 || out.Connections.PoolWaitCount != 5 || out.Connections.Max != 100 {
		t.Fatalf("connections = %+v", out.Connections)
	}
	if out.Database.CacheHitPercent != 99.5 || out.Database.Deadlocks != 1 || out.Database.StatsSince != now {
		t.Fatalf("database = %+v", out.Database)
	}
	if len(out.Queries.Running) != 1 || out.Queries.Running[0].WaitEvent != "Lock:tuple" {
		t.Fatalf("running = %+v", out.Queries.Running)
	}
	if !out.Queries.StatementsAvailable || len(out.Queries.TopStatements) != 1 || out.Queries.TopStatements[0].MeanMs != 500 {
		t.Fatalf("top statements = %+v", out.Queries.TopStatements)
	}
	if len(out.Tables) != 1 || out.Tables[0].DeadRows != 1 || out.Tables[0].LastVacuumAt != now {
		t.Fatalf("tables = %+v", out.Tables)
	}
	if out.Maintenance.WaitingLocks != 2 {
		t.Fatalf("maintenance = %+v", out.Maintenance)
	}
	if !out.Retention.EverRan || out.Retention.LastRunAt != now || out.Retention.ObservationsDeleted != 6 {
		t.Fatalf("retention = %+v", out.Retention)
	}
	if !out.Writeback.LastFlushOk || out.Writeback.LastFlushAt != now {
		t.Fatalf("writeback = %+v", out.Writeback)
	}
}

func TestFromStorageHealthOmitsUnsetRetentionRun(t *testing.T) {
	out := FromStorageHealth(repo.StorageHealth{Retention: repo.StorageRetention{LastRunAt: time.Unix(5, 0)}})
	if !out.Retention.LastRunAt.IsZero() {
		t.Fatalf("last_run_at = %v, want zero when the job never ran", out.Retention.LastRunAt)
	}
}
