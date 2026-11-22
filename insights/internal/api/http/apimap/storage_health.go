package apimap

import (
	"github.com/odigos-io/odigos/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

func FromStorageHealth(h repo.StorageHealth) apitypes.StorageHealth {
	tables := make([]apitypes.StorageTable, 0, len(h.Tables))
	for _, t := range h.Tables {
		tables = append(tables, apitypes.StorageTable{
			Name:         t.Name,
			Rows:         t.Rows,
			DeadRows:     t.DeadRows,
			BytesOnDisk:  int64(t.BytesOnDisk),
			LastVacuumAt: t.LastVacuumAt,
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
	retention := apitypes.StorageRetention{
		ObservationRetentionDays: h.Retention.ObservationRetentionDays,
		RelationRetentionDays:    h.Retention.RelationRetentionDays,
		AttemptRetentionDays:     h.Retention.AttemptRetentionDays,
		EverRan:                  h.Retention.EverRan,
		LastRunOk:                h.Retention.LastRunOK,
		LastError:                h.Retention.LastError,
		ObservationsDeleted:      h.Retention.ObservationsDeleted,
		AttemptsDeleted:          h.Retention.AttemptsDeleted,
	}
	if h.Retention.EverRan {
		retention.LastRunAt = h.Retention.LastRunAt
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
			UptimeSeconds: h.Connection.UptimeSeconds,
		},
		Disk: apitypes.StorageDisk{
			DatabaseBytes: int64(h.Disk.DatabaseBytes),
			VolumeBytes:   int64(h.Disk.VolumeBytes),
			UsedPercent:   h.Disk.UsedPercent,
			Status:        apitypes.StorageDiskStatus(h.Disk.Status),
		},
		Connections: apitypes.StorageConnections{
			Active:            h.Connections.Active,
			Idle:              h.Connections.Idle,
			IdleInTransaction: h.Connections.IdleInTransaction,
			Total:             h.Connections.Total,
			Max:               h.Connections.Max,
			PoolAcquired:      h.Connections.PoolAcquired,
			PoolIdle:          h.Connections.PoolIdle,
			PoolTotal:         h.Connections.PoolTotal,
			PoolMax:           h.Connections.PoolMax,
			PoolWaitCount:     h.Connections.PoolWaitCount,
			PoolWaitSeconds:   h.Connections.PoolWaitSeconds,
		},
		Database: apitypes.StorageDatabaseStats{
			CacheHitPercent: h.Database.CacheHitPercent,
			Commits:         h.Database.Commits,
			Rollbacks:       h.Database.Rollbacks,
			Deadlocks:       h.Database.Deadlocks,
			TempBytes:       h.Database.TempBytes,
			StatsSince:      h.Database.StatsSince,
		},
		Queries: apitypes.StorageQueries{
			Active:              h.Queries.Active,
			LongestSeconds:      h.Queries.LongestSeconds,
			Running:             mapStorageQueries(h.Queries.Running),
			TopStatements:       mapStorageStatements(h.Queries.TopStatements),
			StatementsAvailable: h.Queries.StatementsAvailable,
		},
		Tables: tables,
		Maintenance: apitypes.StorageMaintenance{
			DeadRows:                 h.Maintenance.DeadRows,
			VacuumsRunning:           h.Maintenance.VacuumsRunning,
			WaitingLocks:             h.Maintenance.WaitingLocks,
			OldestTransactionSeconds: h.Maintenance.OldestTransactionSeconds,
		},
		Retention: retention,
		Writeback: wb,
	}
}

func mapStorageQueries(in []repo.StorageQuery) []apitypes.StorageQuery {
	out := make([]apitypes.StorageQuery, 0, len(in))
	for _, q := range in {
		out = append(out, apitypes.StorageQuery{
			ElapsedSeconds: q.ElapsedSeconds,
			User:           q.User,
			State:          q.State,
			WaitEvent:      q.WaitEvent,
			Query:          q.Query,
		})
	}
	return out
}

func mapStorageStatements(in []repo.StorageStatement) []apitypes.StorageStatement {
	out := make([]apitypes.StorageStatement, 0, len(in))
	for _, s := range in {
		out = append(out, apitypes.StorageStatement{
			Query:        s.Query,
			Calls:        s.Calls,
			TotalSeconds: s.TotalSeconds,
			MeanMs:       s.MeanMs,
			Rows:         s.Rows,
		})
	}
	return out
}
