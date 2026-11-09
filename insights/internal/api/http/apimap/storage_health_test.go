package apimap

import (
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

func TestFromStorageHealth(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	in := repo.StorageHealth{
		CheckedAt: now,
		Status:    repo.StorageStatusHealthy,
		Connection: repo.StorageConnection{
			Reachable: true, PingLatencyMs: 3, Version: "24.8", Database: "odigos_insights",
		},
		Disk: repo.StorageDisk{
			InsightsBytes: 7, TotalBytes: 100, UsedBytes: 40, AvailableBytes: 60, UsedPercent: 40, Status: "ok",
		},
		Memory: repo.StorageMemory{ProcessRSSBytes: 10, CGroupUsedBytes: 11, CGroupTotalBytes: 20, UptimeSeconds: 9},
		Queries: repo.StorageQueries{
			Active: 1, LongestSeconds: 0.2, PeakMemoryBytes: 5,
			Items: []repo.StorageQuery{{ElapsedSeconds: 0.2, User: "u", MemoryBytes: 5, Query: "SELECT 1"}},
			RecentHeaviest: []repo.StorageQuery{{
				ElapsedSeconds: 1.5, User: "u", MemoryBytes: 9, Query: "SELECT heavy", FinishedAt: now,
			}},
			RecentLatest: []repo.StorageQuery{{
				ElapsedSeconds: 0.01, User: "u", MemoryBytes: 1, Query: "SELECT latest", FinishedAt: now,
			}},
		},
		Tables: []repo.StorageTable{{Name: "t", Rows: 2, BytesOnDisk: 3, ActiveParts: 1}},
		WritePressure: repo.StorageWritePressure{UnfinishedMutations: 0, ActiveParts: 1},
		Writeback: repo.StorageWriteback{
			FlushIntervalSeconds: 15, EverFlushed: true, LastFlushOK: true, LastFlushAt: now,
		},
	}
	out := FromStorageHealth(in)
	if out.Status != apitypes.StorageHealthStatusHealthy {
		t.Fatalf("status = %q", out.Status)
	}
	if out.Memory.ProcessRssBytes != 10 || out.Memory.CgroupTotalBytes != 20 {
		t.Fatalf("memory = %+v", out.Memory)
	}
	if !out.Writeback.LastFlushOk || out.Writeback.LastFlushAt != now {
		t.Fatalf("writeback = %+v", out.Writeback)
	}
	if len(out.Tables) != 1 || out.Tables[0].Name != "t" {
		t.Fatalf("tables = %+v", out.Tables)
	}
	if out.Disk.InsightsBytes != 7 {
		t.Fatalf("insights_bytes = %d", out.Disk.InsightsBytes)
	}
	if len(out.Queries.RecentHeaviest) != 1 || out.Queries.RecentHeaviest[0].Query != "SELECT heavy" {
		t.Fatalf("recent_heaviest = %+v", out.Queries.RecentHeaviest)
	}
	if len(out.Queries.RecentLatest) != 1 || out.Queries.RecentLatest[0].Query != "SELECT latest" {
		t.Fatalf("recent_latest = %+v", out.Queries.RecentLatest)
	}
	if out.Queries.RecentHeaviest[0].FinishedAt != now {
		t.Fatalf("finished_at = %v", out.Queries.RecentHeaviest[0].FinishedAt)
	}
}
