package clickhouse

import (
	"context"
	"strings"
	"time"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

const (
	storageRunningLimit   = 20
	storageRecentLimit    = 10
	storageQueryMaxChars  = 240
	storageRecentMinutes  = 5
	diskWarnUsedPercent   = 80
	diskCritUsedPercent   = 90
	longQueryWarnSeconds  = 5
	cgroupWarnUsedPercent = 90
)

type storageQueryRow struct {
	elapsed    float64
	user       string
	mem        uint64
	query      string
	finishedAt time.Time
}

// GetStorageHealth collects a ClickHouse health snapshot for the debug Storage page.
func (r *Repo) GetStorageHealth(ctx context.Context) (repo.StorageHealth, error) {
	h := repo.StorageHealth{
		CheckedAt: time.Now().UTC(),
		Status:    repo.StorageStatusHealthy,
	}

	start := time.Now()
	if err := r.Ping(ctx); err != nil {
		h.Status = repo.StorageStatusUnreachable
		h.Connection = repo.StorageConnection{Reachable: false, Error: err.Error()}
		return h, nil
	}
	h.Connection.Reachable = true
	h.Connection.PingLatencyMs = time.Since(start).Milliseconds()

	if err := r.conn.QueryRow(ctx, `SELECT version(), currentDatabase()`).
		Scan(&h.Connection.Version, &h.Connection.Database); err != nil {
		h.Connection.Error = err.Error()
	}

	h.Tables = r.readTables(ctx)
	h.Disk = r.readDisk(ctx, h.Tables)
	h.Memory = r.readMemory(ctx)
	h.Queries = r.readQueries(ctx)
	h.WritePressure = r.readWritePressure(ctx, h.Tables)
	h.Status = deriveStorageStatus(h)
	return h, nil
}

func (r *Repo) readDisk(ctx context.Context, tables []repo.StorageTable) repo.StorageDisk {
	var total, used, avail float64
	_ = r.conn.QueryRow(ctx, `
		SELECT
			maxIf(value, metric = 'DiskTotal_default'),
			maxIf(value, metric = 'DiskUsed_default'),
			maxIf(value, metric = 'DiskAvailable_default')
		FROM system.asynchronous_metrics
		WHERE metric IN ('DiskTotal_default', 'DiskUsed_default', 'DiskAvailable_default')`).
		Scan(&total, &used, &avail)

	var insights uint64
	for _, t := range tables {
		insights += t.BytesOnDisk
	}

	d := repo.StorageDisk{
		InsightsBytes:  insights,
		TotalBytes:     uint64(total),
		UsedBytes:      uint64(used),
		AvailableBytes: uint64(avail),
		Status:         "ok",
	}
	if d.TotalBytes > 0 {
		d.UsedPercent = 100 * float64(d.UsedBytes) / float64(d.TotalBytes)
	}
	switch {
	case d.UsedPercent >= diskCritUsedPercent:
		d.Status = "critical"
	case d.UsedPercent >= diskWarnUsedPercent:
		d.Status = "warning"
	}
	return d
}

func (r *Repo) readMemory(ctx context.Context) repo.StorageMemory {
	var rss, cUsed, cTotal, uptime float64
	_ = r.conn.QueryRow(ctx, `
		SELECT
			maxIf(value, metric = 'MemoryResident'),
			maxIf(value, metric = 'CGroupMemoryUsed'),
			maxIf(value, metric = 'CGroupMemoryTotal'),
			maxIf(value, metric = 'Uptime')
		FROM system.asynchronous_metrics
		WHERE metric IN ('MemoryResident', 'CGroupMemoryUsed', 'CGroupMemoryTotal', 'Uptime')`).
		Scan(&rss, &cUsed, &cTotal, &uptime)
	return repo.StorageMemory{
		ProcessRSSBytes:  uint64(rss),
		CGroupUsedBytes:  uint64(cUsed),
		CGroupTotalBytes: uint64(cTotal),
		UptimeSeconds:    uptime,
	}
}

func (r *Repo) readQueries(ctx context.Context) repo.StorageQueries {
	out := repo.StorageQueries{
		Items:          toStorageQueries(r.readRunningQueries(ctx)),
		RecentHeaviest: toStorageQueries(r.readRecentQueries(ctx, "query_duration_ms DESC, memory_usage DESC")),
		RecentLatest:   toStorageQueries(r.readRecentQueries(ctx, "event_time DESC")),
	}
	out.Active = len(out.Items)
	for _, x := range out.Items {
		if x.ElapsedSeconds > out.LongestSeconds {
			out.LongestSeconds = x.ElapsedSeconds
		}
		if x.MemoryBytes > out.PeakMemoryBytes {
			out.PeakMemoryBytes = x.MemoryBytes
		}
	}
	return out
}

func (r *Repo) readRunningQueries(ctx context.Context) []storageQueryRow {
	rows, err := queryRows(ctx, r.conn, "running queries", `
		SELECT elapsed, user, memory_usage, query
		FROM system.processes
		WHERE query NOT LIKE '%system.processes%'
		  AND query NOT LIKE '%system.query_log%'
		  AND query NOT LIKE '%system.asynchronous_metrics%'
		  AND query NOT LIKE '%system.parts%'
		  AND query NOT LIKE '%system.mutations%'
		ORDER BY elapsed DESC
		LIMIT ?`,
		scanRunningQueryRow, uint64(storageRunningLimit))
	if err != nil {
		return nil
	}
	return rows
}

// orderBy is a trusted internal ORDER BY clause (not user input).
func (r *Repo) readRecentQueries(ctx context.Context, orderBy string) []storageQueryRow {
	rows, err := queryRows(ctx, r.conn, "recent queries", `
		SELECT
			query_duration_ms / 1000.0,
			user,
			memory_usage,
			query,
			event_time
		FROM system.query_log
		WHERE type = 'QueryFinish'
		  AND event_time >= now() - toIntervalMinute(?)
		  AND query NOT LIKE '%system.query_log%'
		  AND query NOT LIKE '%system.processes%'
		  AND query NOT LIKE '%system.asynchronous_metrics%'
		  AND query NOT LIKE '%system.parts%'
		  AND query NOT LIKE '%system.mutations%'
		ORDER BY `+orderBy+`
		LIMIT ?`,
		scanRecentQueryRow, uint64(storageRecentMinutes), uint64(storageRecentLimit))
	if err != nil {
		return nil
	}
	return rows
}

func scanRunningQueryRow(s rowScanner) (storageQueryRow, error) {
	var x storageQueryRow
	err := s.Scan(&x.elapsed, &x.user, &x.mem, &x.query)
	return x, err
}

func scanRecentQueryRow(s rowScanner) (storageQueryRow, error) {
	var x storageQueryRow
	err := s.Scan(&x.elapsed, &x.user, &x.mem, &x.query, &x.finishedAt)
	return x, err
}

func toStorageQueries(rows []storageQueryRow) []repo.StorageQuery {
	out := make([]repo.StorageQuery, 0, len(rows))
	for _, x := range rows {
		out = append(out, repo.StorageQuery{
			ElapsedSeconds: x.elapsed,
			User:           x.user,
			MemoryBytes:    x.mem,
			Query:          truncateQuery(x.query, storageQueryMaxChars),
			FinishedAt:     x.finishedAt.UTC(),
		})
	}
	return out
}

func (r *Repo) readTables(ctx context.Context) []repo.StorageTable {
	tables, err := queryRows(ctx, r.conn, "table sizes", `
		SELECT table, sum(rows), sum(bytes_on_disk), count()
		FROM system.parts
		WHERE active AND database = currentDatabase()
		GROUP BY table
		ORDER BY sum(bytes_on_disk) DESC`,
		func(s rowScanner) (repo.StorageTable, error) {
			var t repo.StorageTable
			err := s.Scan(&t.Name, &t.Rows, &t.BytesOnDisk, &t.ActiveParts)
			return t, err
		})
	if err != nil {
		return nil
	}
	return tables
}

func (r *Repo) readWritePressure(ctx context.Context, tables []repo.StorageTable) repo.StorageWritePressure {
	var mutations uint64
	_ = r.conn.QueryRow(ctx, `
		SELECT count()
		FROM system.mutations
		WHERE NOT is_done AND database = currentDatabase()`).Scan(&mutations)
	var parts uint64
	for _, t := range tables {
		parts += t.ActiveParts
	}
	return repo.StorageWritePressure{UnfinishedMutations: mutations, ActiveParts: parts}
}

func deriveStorageStatus(h repo.StorageHealth) string {
	if !h.Connection.Reachable {
		return repo.StorageStatusUnreachable
	}
	status := repo.StorageStatusHealthy
	bump := func(s string) {
		rank := map[string]int{
			repo.StorageStatusHealthy:  0,
			repo.StorageStatusDegraded: 1,
			repo.StorageStatusCritical: 2,
		}
		if rank[s] > rank[status] {
			status = s
		}
	}
	switch h.Disk.Status {
	case "critical":
		bump(repo.StorageStatusCritical)
	case "warning":
		bump(repo.StorageStatusDegraded)
	}
	if h.Memory.CGroupTotalBytes > 0 {
		pct := 100 * float64(h.Memory.CGroupUsedBytes) / float64(h.Memory.CGroupTotalBytes)
		if pct >= cgroupWarnUsedPercent {
			bump(repo.StorageStatusDegraded)
		}
	}
	if h.WritePressure.UnfinishedMutations > 0 {
		bump(repo.StorageStatusDegraded)
	}
	if h.Queries.LongestSeconds >= longQueryWarnSeconds {
		bump(repo.StorageStatusDegraded)
	}
	return status
}

func truncateQuery(q string, max int) string {
	q = strings.Join(strings.Fields(q), " ")
	if max <= 0 || len(q) <= max {
		return q
	}
	return q[:max-1] + "…"
}
