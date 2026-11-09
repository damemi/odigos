package apimap

import (
	"github.com/odigos-io/odigos/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

func FromStorageHealth(h repo.StorageHealth) apitypes.StorageHealth {
	tables := make([]apitypes.StorageTable, 0, len(h.Tables))
	for _, t := range h.Tables {
		tables = append(tables, apitypes.StorageTable{
			Name:        t.Name,
			Rows:        int64(t.Rows),
			BytesOnDisk: int64(t.BytesOnDisk),
			ActiveParts: int64(t.ActiveParts),
		})
	}
	wb := apitypes.StorageWriteback{
		FlushIntervalSeconds: h.Writeback.FlushIntervalSeconds,
		EverFlushed:          h.Writeback.EverFlushed,
		LastFlushOk:          h.Writeback.LastFlushOK,
		LastFlushError:       h.Writeback.LastFlushError,
	}
	if h.Writeback.EverFlushed && !h.Writeback.LastFlushAt.IsZero() {
		wb.LastFlushAt = h.Writeback.LastFlushAt
	}
	return apitypes.StorageHealth{
		CheckedAt: h.CheckedAt,
		Status:    apitypes.StorageHealthStatus(h.Status),
		Connection: apitypes.StorageConnection{
			Reachable:     h.Connection.Reachable,
			PingLatencyMs: h.Connection.PingLatencyMs,
			Version:       h.Connection.Version,
			Database:      h.Connection.Database,
			Error:         h.Connection.Error,
		},
		Disk: apitypes.StorageDisk{
			InsightsBytes:  int64(h.Disk.InsightsBytes),
			TotalBytes:     int64(h.Disk.TotalBytes),
			UsedBytes:      int64(h.Disk.UsedBytes),
			AvailableBytes: int64(h.Disk.AvailableBytes),
			UsedPercent:    h.Disk.UsedPercent,
			Status:         apitypes.StorageDiskStatus(h.Disk.Status),
		},
		Memory: apitypes.StorageMemory{
			ProcessRssBytes:  int64(h.Memory.ProcessRSSBytes),
			CgroupUsedBytes:  int64(h.Memory.CGroupUsedBytes),
			CgroupTotalBytes: int64(h.Memory.CGroupTotalBytes),
			UptimeSeconds:    h.Memory.UptimeSeconds,
		},
		Queries: apitypes.StorageQueries{
			Active:          h.Queries.Active,
			LongestSeconds:  h.Queries.LongestSeconds,
			PeakMemoryBytes: int64(h.Queries.PeakMemoryBytes),
			Items:           mapStorageQueries(h.Queries.Items),
			RecentHeaviest:  mapStorageQueries(h.Queries.RecentHeaviest),
			RecentLatest:    mapStorageQueries(h.Queries.RecentLatest),
		},
		Tables: tables,
		WritePressure: apitypes.StorageWritePressure{
			UnfinishedMutations: int64(h.WritePressure.UnfinishedMutations),
			ActiveParts:         int64(h.WritePressure.ActiveParts),
		},
		Writeback: wb,
	}
}

func mapStorageQueries(in []repo.StorageQuery) []apitypes.StorageQuery {
	out := make([]apitypes.StorageQuery, 0, len(in))
	for _, q := range in {
		item := apitypes.StorageQuery{
			ElapsedSeconds: q.ElapsedSeconds,
			User:           q.User,
			MemoryBytes:    int64(q.MemoryBytes),
			Query:          q.Query,
		}
		if !q.FinishedAt.IsZero() {
			item.FinishedAt = q.FinishedAt
		}
		out = append(out, item)
	}
	return out
}
