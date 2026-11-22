package postgres

import (
	"context"
	"strings"
	"time"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

const (
	storageRunningLimit     = 20
	storageStatementsLimit  = 10
	storageQueryMaxChars    = 240
	diskWarnUsedPercent     = 80
	diskCritUsedPercent     = 90
	connWarnUsedPercent     = 90
	longQueryWarnSeconds    = 5
	longTransactionWarnSecs = 300
)

// GetStorageHealth collects a Postgres health snapshot for the Storage page.
// Each section is best effort: a view the role cannot read leaves its section
// empty instead of failing the whole snapshot.
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

	if err := r.pool.QueryRow(ctx,
		`SELECT current_setting('server_version'), current_database(),
			extract(epoch FROM now() - pg_postmaster_start_time())::float8`).
		Scan(&h.Connection.Version, &h.Connection.Database, &h.Connection.UptimeSeconds); err != nil {
		h.Connection.Error = err.Error()
	}

	h.Tables = r.readTables(ctx)
	h.Disk = r.readDisk(ctx)
	h.Connections = r.readConnections(ctx)
	h.Database = r.readDatabaseStats(ctx)
	h.Queries = r.readQueries(ctx)
	h.Maintenance = r.readMaintenance(ctx, h.Tables)
	h.Retention = r.readRetention(ctx)
	h.Status = deriveStorageStatus(h)
	return h, nil
}

func (r *Repo) readDisk(ctx context.Context) repo.StorageDisk {
	d := repo.StorageDisk{Status: "ok", VolumeBytes: r.volumeBytes}
	var size int64
	_ = r.pool.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&size)
	d.DatabaseBytes = uint64(size)
	if d.VolumeBytes > 0 {
		d.UsedPercent = 100 * float64(d.DatabaseBytes) / float64(d.VolumeBytes)
	}
	switch {
	case d.UsedPercent >= diskCritUsedPercent:
		d.Status = "critical"
	case d.UsedPercent >= diskWarnUsedPercent:
		d.Status = "warning"
	}
	return d
}

func (r *Repo) readConnections(ctx context.Context) repo.StorageConnections {
	var c repo.StorageConnections
	_ = r.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE state = 'active'),
			count(*) FILTER (WHERE state = 'idle'),
			count(*) FILTER (WHERE state LIKE 'idle in transaction%'),
			count(*),
			current_setting('max_connections')::int
		FROM pg_stat_activity
		WHERE datname = current_database() AND backend_type = 'client backend'`).
		Scan(&c.Active, &c.Idle, &c.IdleInTransaction, &c.Total, &c.Max)

	stat := r.pool.Stat()
	c.PoolAcquired = int(stat.AcquiredConns())
	c.PoolIdle = int(stat.IdleConns())
	c.PoolTotal = int(stat.TotalConns())
	c.PoolMax = int(stat.MaxConns())
	c.PoolWaitCount = stat.EmptyAcquireCount()
	c.PoolWaitSeconds = stat.AcquireDuration().Seconds()
	return c
}

func (r *Repo) readDatabaseStats(ctx context.Context) repo.StorageDatabaseStats {
	var (
		s          repo.StorageDatabaseStats
		statsSince *time.Time
	)
	_ = r.pool.QueryRow(ctx, `
		SELECT
			CASE WHEN blks_hit + blks_read > 0 THEN 100.0 * blks_hit / (blks_hit + blks_read) ELSE 100 END,
			xact_commit, xact_rollback, deadlocks, temp_bytes, stats_reset
		FROM pg_stat_database
		WHERE datname = current_database()`).
		Scan(&s.CacheHitPercent, &s.Commits, &s.Rollbacks, &s.Deadlocks, &s.TempBytes, &statsSince)
	if statsSince != nil {
		s.StatsSince = *statsSince
	}
	return s
}

func (r *Repo) readQueries(ctx context.Context) repo.StorageQueries {
	out := repo.StorageQueries{}
	running, err := queryRows(ctx, r.pool, "running queries", `
		SELECT extract(epoch FROM now() - query_start)::float8, COALESCE(usename, ''), COALESCE(state, ''),
			COALESCE(wait_event_type || ':' || wait_event, ''), query
		FROM pg_stat_activity
		WHERE datname = current_database()
		  AND state = 'active'
		  AND pid <> pg_backend_pid()
		  AND backend_type = 'client backend'
		ORDER BY query_start ASC
		LIMIT $1`,
		func(s rowScanner) (repo.StorageQuery, error) {
			var q repo.StorageQuery
			err := s.Scan(&q.ElapsedSeconds, &q.User, &q.State, &q.WaitEvent, &q.Query)
			q.Query = truncateQuery(q.Query, storageQueryMaxChars)
			return q, err
		}, storageRunningLimit)
	if err == nil {
		out.Running = running
	}
	out.Active = len(out.Running)
	for _, q := range out.Running {
		if q.ElapsedSeconds > out.LongestSeconds {
			out.LongestSeconds = q.ElapsedSeconds
		}
	}

	var installed bool
	if err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_stat_statements')`).Scan(&installed); err != nil || !installed {
		return out
	}
	statements, err := queryRows(ctx, r.pool, "top statements", `
		SELECT s.query, s.calls, s.total_exec_time / 1000.0, s.mean_exec_time, s.rows
		FROM pg_stat_statements AS s
		JOIN pg_database AS d ON d.oid = s.dbid
		WHERE d.datname = current_database()
		ORDER BY s.total_exec_time DESC
		LIMIT $1`,
		func(s rowScanner) (repo.StorageStatement, error) {
			var st repo.StorageStatement
			err := s.Scan(&st.Query, &st.Calls, &st.TotalSeconds, &st.MeanMs, &st.Rows)
			st.Query = truncateQuery(st.Query, storageQueryMaxChars)
			return st, err
		}, storageStatementsLimit)
	if err == nil {
		out.TopStatements = statements
		out.StatementsAvailable = true
	}
	return out
}

func (r *Repo) readTables(ctx context.Context) []repo.StorageTable {
	tables, err := queryRows(ctx, r.pool, "table sizes", `
		SELECT relname, n_live_tup, n_dead_tup, pg_total_relation_size(relid),
			COALESCE(GREATEST(last_vacuum, last_autovacuum), 'epoch')
		FROM pg_stat_user_tables
		WHERE schemaname = current_schema()
		ORDER BY pg_total_relation_size(relid) DESC`,
		func(s rowScanner) (repo.StorageTable, error) {
			var (
				t     repo.StorageTable
				bytes int64
			)
			if err := s.Scan(&t.Name, &t.Rows, &t.DeadRows, &bytes, &t.LastVacuumAt); err != nil {
				return repo.StorageTable{}, err
			}
			t.BytesOnDisk = uint64(bytes)
			t.LastVacuumAt = normalizeEpoch(t.LastVacuumAt)
			return t, nil
		})
	if err != nil {
		return nil
	}
	return tables
}

func (r *Repo) readMaintenance(ctx context.Context, tables []repo.StorageTable) repo.StorageMaintenance {
	var m repo.StorageMaintenance
	for _, t := range tables {
		m.DeadRows += t.DeadRows
	}
	_ = r.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM pg_stat_progress_vacuum WHERE datname = current_database()),
			(SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'),
			(SELECT COALESCE(max(extract(epoch FROM now() - xact_start)), 0)::float8
			 FROM pg_stat_activity
			 WHERE datname = current_database() AND xact_start IS NOT NULL AND pid <> pg_backend_pid())`).
		Scan(&m.VacuumsRunning, &m.WaitingLocks, &m.OldestTransactionSeconds)
	return m
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
	if h.Connections.Max > 0 && 100*h.Connections.Total >= connWarnUsedPercent*h.Connections.Max {
		bump(repo.StorageStatusDegraded)
	}
	if h.Queries.LongestSeconds >= longQueryWarnSeconds {
		bump(repo.StorageStatusDegraded)
	}
	if h.Maintenance.OldestTransactionSeconds >= longTransactionWarnSecs {
		bump(repo.StorageStatusDegraded)
	}
	if h.Retention.EverRan && !h.Retention.LastRunOK {
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
