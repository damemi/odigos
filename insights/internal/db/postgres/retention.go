package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

const (
	retentionJob      = "retention"
	retentionInterval = time.Hour
	// notificationAttemptRetentionDays bounds the delivery audit trail so it
	// cannot grow with traffic.
	notificationAttemptRetentionDays = 30
	// retentionChunk bounds one DELETE so a large backlog never holds row
	// locks or bloats WAL in a single statement.
	retentionChunk = 5000
)

// RunRetention deletes expired samples and delivery attempts every hour until
// ctx is done. It runs as a leader job, so only one replica prunes at a time.
func (r *Repo) RunRetention(ctx context.Context, log *slog.Logger) {
	ticker := time.NewTicker(retentionInterval)
	defer ticker.Stop()
	for {
		if err := r.PruneExpired(ctx, time.Now()); err != nil && ctx.Err() == nil {
			log.Warn("retention run failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// PruneExpired applies the configured retention once and records the outcome
// in maintenance_runs.
func (r *Repo) PruneExpired(ctx context.Context, now time.Time) error {
	deleted := map[string]int64{}
	err := func() error {
		settings, err := r.GetSystemSettings(ctx)
		if err != nil {
			return err
		}
		n, err := r.pruneObservations(ctx, settings.ObservationRetentionDays, now)
		if err != nil {
			return err
		}
		deleted["trace_observations"] = n
		n, err = r.deleteInChunks(ctx, "notification_attempts", "attempted_at < $1",
			now.AddDate(0, 0, -notificationAttemptRetentionDays))
		if err != nil {
			return fmt.Errorf("prune notification attempts: %w", err)
		}
		deleted["notification_attempts"] = n
		return nil
	}()
	if recErr := r.recordMaintenanceRun(ctx, retentionJob, now, err, deleted); recErr != nil && err == nil {
		err = recErr
	}
	return err
}

// pruneObservations deletes relation samples older than their fixed retention
// and every other sample older than days.
func (r *Repo) pruneObservations(ctx context.Context, days int, now time.Time) (int64, error) {
	if days <= 0 {
		days = repo.DefaultObservationRetentionDays
	}
	n, err := r.deleteInChunks(ctx, "trace_observations",
		"(sample_reason = $1 AND observed_at < $2) OR (sample_reason <> $1 AND observed_at < $3)",
		repo.RelationSampleReason,
		now.AddDate(0, 0, -repo.DefaultRelationSampleRetentionDays),
		now.AddDate(0, 0, -days))
	if err != nil {
		return n, fmt.Errorf("prune trace observations: %w", err)
	}
	return n, nil
}

// deleteInChunks deletes rows of table matching where, retentionChunk rows per
// statement, and returns how many it removed.
func (r *Repo) deleteInChunks(ctx context.Context, table, where string, whereArgs ...any) (int64, error) {
	var total int64
	for {
		tag, err := r.pool.Exec(ctx,
			fmt.Sprintf(`DELETE FROM %[1]s WHERE ctid IN (SELECT ctid FROM %[1]s WHERE %[2]s LIMIT %[3]d)`, table, where, retentionChunk),
			whereArgs...)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < retentionChunk {
			return total, nil
		}
	}
}

func (r *Repo) recordMaintenanceRun(ctx context.Context, job string, at time.Time, runErr error, deleted map[string]int64) error {
	errText := ""
	if runErr != nil {
		errText = truncateError(runErr.Error())
	}
	counts, err := json.Marshal(deleted)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx,
		`INSERT INTO maintenance_runs (job, ran_at, ok, error, deleted_rows)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (job) DO UPDATE SET
			ran_at = EXCLUDED.ran_at, ok = EXCLUDED.ok, error = EXCLUDED.error, deleted_rows = EXCLUDED.deleted_rows`,
		job, at, runErr == nil, errText, string(counts))
	if err != nil {
		return fmt.Errorf("record maintenance run: %w", err)
	}
	return nil
}

// readRetention reports the retention policy and the last run's outcome.
func (r *Repo) readRetention(ctx context.Context) repo.StorageRetention {
	out := repo.StorageRetention{
		RelationRetentionDays: repo.DefaultRelationSampleRetentionDays,
		AttemptRetentionDays:  notificationAttemptRetentionDays,
	}
	if s, err := r.GetSystemSettings(ctx); err == nil {
		out.ObservationRetentionDays = s.ObservationRetentionDays
	}
	var (
		deleted map[string]int64
		errText string
	)
	err := r.pool.QueryRow(ctx,
		`SELECT ran_at, ok, error, deleted_rows FROM maintenance_runs WHERE job = $1`, retentionJob).
		Scan(&out.LastRunAt, &out.LastRunOK, &errText, &deleted)
	if err != nil {
		return out
	}
	out.EverRan = true
	out.LastError = errText
	out.ObservationsDeleted = deleted["trace_observations"]
	out.AttemptsDeleted = deleted["notification_attempts"]
	return out
}
