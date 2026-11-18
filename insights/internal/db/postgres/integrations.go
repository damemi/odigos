package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// ---------- integrations ----------

const integrationCols = `integration_id, name, type, enabled, config, filters, created_at, updated_at`

// ListIntegrations returns every configured backend sorted by name.
func (r *Repo) ListIntegrations(ctx context.Context) ([]repo.Integration, error) {
	return queryRows(ctx, r.pool, "list integrations",
		`SELECT `+integrationCols+` FROM integrations ORDER BY name ASC, integration_id ASC`,
		scanIntegration)
}

// GetIntegration returns one backend by id; ok=false when unknown.
func (r *Repo) GetIntegration(ctx context.Context, id string) (repo.Integration, bool, error) {
	return queryRow(ctx, r.pool, "get integration",
		`SELECT `+integrationCols+` FROM integrations WHERE integration_id = $1`,
		scanIntegration, id)
}

func scanIntegration(s rowScanner) (repo.Integration, error) {
	var (
		in      repo.Integration
		config  string
		filters string
	)
	if err := s.Scan(&in.ID, &in.Name, &in.Type, &in.Enabled, &config, &filters, &in.CreatedAt, &in.UpdatedAt); err != nil {
		return repo.Integration{}, err
	}
	if config == "" {
		config = "{}"
	}
	in.Config = json.RawMessage(config)
	if filters != "" {
		if err := json.Unmarshal([]byte(filters), &in.Filters); err != nil {
			return repo.Integration{}, fmt.Errorf("decode integration %s filters: %w", in.ID, err)
		}
	}
	return in, nil
}

// UpsertIntegration inserts or replaces one backend, keyed by id. UpdatedAt is stamped here; CreatedAt is whatever the caller carried.
func (r *Repo) UpsertIntegration(ctx context.Context, in repo.Integration) error {
	filters, err := json.Marshal(in.Filters)
	if err != nil {
		return fmt.Errorf("encode integration filters: %w", err)
	}
	config := string(in.Config)
	if config == "" {
		config = "{}"
	}
	now := time.Now()
	createdAt := in.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	_, err = r.pool.Exec(ctx,
		`INSERT INTO integrations (`+integrationCols+`)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT (integration_id) DO UPDATE SET
			name = EXCLUDED.name, type = EXCLUDED.type, enabled = EXCLUDED.enabled, config = EXCLUDED.config,
			filters = EXCLUDED.filters, created_at = EXCLUDED.created_at, updated_at = EXCLUDED.updated_at`,
		in.ID, in.Name, in.Type, in.Enabled, config, string(filters), createdAt, now)
	if err != nil {
		return fmt.Errorf("upsert integration: %w", err)
	}
	return nil
}

// DeleteIntegration removes a backend plus its ledger, cursor and audit rows
// in one transaction.
func (r *Repo) DeleteIntegration(ctx context.Context, id string) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		for _, table := range []string{"integrations", "notification_dispatch", "notification_cursors", "notification_attempts"} {
			if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE integration_id = $1`, id); err != nil {
				return fmt.Errorf("delete integration %s from %s: %w", id, table, err)
			}
		}
		return nil
	})
}

// ---------- delivery ledger ----------

const dispatchCols = `integration_id, finding_key, last_action, state, attempts, next_attempt_at, last_error,
	sent_at, occurrences_at_send, severity_at_send, status_at_send, external_id, updated_at`

// ListNotificationDispatch returns ledger rows matching filter, newest first.
func (r *Repo) ListNotificationDispatch(ctx context.Context, f repo.DispatchListFilter) ([]repo.NotificationDispatch, error) {
	var (
		where []string
		a     args
	)
	if f.IntegrationID != "" {
		where = append(where, "integration_id = "+a.add(f.IntegrationID))
	}
	if len(f.FindingKeys) > 0 {
		where = append(where, "finding_key = ANY("+a.add(f.FindingKeys)+")")
	}
	if f.State != "" {
		where = append(where, "state = "+a.add(string(f.State)))
	}
	if !f.DueBefore.IsZero() {
		where = append(where, "next_attempt_at <= "+a.add(f.DueBefore))
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 500
	}
	q := `SELECT ` + dispatchCols + ` FROM notification_dispatch`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	q += ` ORDER BY updated_at DESC LIMIT ` + a.add(limit)
	return queryRows(ctx, r.pool, "list notification dispatch", q, scanDispatch, a...)
}

func scanDispatch(s rowScanner) (repo.NotificationDispatch, error) {
	var (
		d                  repo.NotificationDispatch
		action, state      string
		attempts           int32
		severity, statusAt string
	)
	if err := s.Scan(&d.IntegrationID, &d.FindingKey, &action, &state, &attempts, &d.NextAttemptAt, &d.LastError,
		&d.SentAt, &d.OccurrencesAtSend, &severity, &statusAt, &d.ExternalID, &d.UpdatedAt); err != nil {
		return repo.NotificationDispatch{}, err
	}
	d.LastAction = repo.DispatchAction(action)
	d.State = repo.DispatchState(state)
	d.Attempts = int(attempts)
	d.SeverityAtSend = severity
	d.StatusAtSend = statusAt
	d.SentAt = normalizeEpoch(d.SentAt)
	d.NextAttemptAt = normalizeEpoch(d.NextAttemptAt)
	return d, nil
}

// UpsertNotificationDispatch writes ledger rows, one per (integration,
// finding), replacing the previous state.
func (r *Repo) UpsertNotificationDispatch(ctx context.Context, rows []repo.NotificationDispatch) error {
	if len(rows) == 0 {
		return nil
	}
	sorted := append([]repo.NotificationDispatch(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].IntegrationID != sorted[j].IntegrationID {
			return sorted[i].IntegrationID < sorted[j].IntegrationID
		}
		return sorted[i].FindingKey < sorted[j].FindingKey
	})
	now := time.Now()
	batch := make([][]any, 0, len(sorted))
	for _, d := range sorted {
		batch = append(batch, []any{
			d.IntegrationID, d.FindingKey, string(d.LastAction), string(d.State), int32(d.Attempts),
			orEpoch(d.NextAttemptAt), truncateError(d.LastError),
			orEpoch(d.SentAt), d.OccurrencesAtSend, d.SeverityAtSend, d.StatusAtSend, d.ExternalID, now,
		})
	}
	err := execBatch(ctx, r.pool,
		`INSERT INTO notification_dispatch (`+dispatchCols+`)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		 ON CONFLICT (integration_id, finding_key) DO UPDATE SET
			last_action = EXCLUDED.last_action, state = EXCLUDED.state, attempts = EXCLUDED.attempts,
			next_attempt_at = EXCLUDED.next_attempt_at, last_error = EXCLUDED.last_error, sent_at = EXCLUDED.sent_at,
			occurrences_at_send = EXCLUDED.occurrences_at_send, severity_at_send = EXCLUDED.severity_at_send,
			status_at_send = EXCLUDED.status_at_send, external_id = EXCLUDED.external_id, updated_at = EXCLUDED.updated_at`,
		batch)
	if err != nil {
		return fmt.Errorf("upsert notification dispatch: %w", err)
	}
	return nil
}

// ResetNotificationDispatch re-arms failed/dead deliveries: read-modify-write
// of the selected rows back to pending with attempts cleared, so the next tick
// resends. Operator action, cold path.
func (r *Repo) ResetNotificationDispatch(ctx context.Context, integrationID string, findingKeys []string) error {
	var rows []repo.NotificationDispatch
	if len(findingKeys) > 0 {
		var err error
		rows, err = r.ListNotificationDispatch(ctx, repo.DispatchListFilter{IntegrationID: integrationID, FindingKeys: findingKeys, Limit: len(findingKeys)})
		if err != nil {
			return err
		}
	} else {
		for _, state := range []repo.DispatchState{repo.DispatchFailed, repo.DispatchDead} {
			part, err := r.ListNotificationDispatch(ctx, repo.DispatchListFilter{IntegrationID: integrationID, State: state, Limit: 10000})
			if err != nil {
				return err
			}
			rows = append(rows, part...)
		}
	}
	if len(rows) == 0 {
		return nil
	}
	now := time.Now()
	for i := range rows {
		rows[i].State = repo.DispatchPending
		rows[i].Attempts = 0
		rows[i].NextAttemptAt = now
		rows[i].LastError = ""
	}
	return r.UpsertNotificationDispatch(ctx, rows)
}

// ---------- attempts audit ----------

// RecordNotificationAttempts appends audit rows.
func (r *Repo) RecordNotificationAttempts(ctx context.Context, attempts []repo.NotificationAttempt) error {
	if len(attempts) == 0 {
		return nil
	}
	now := time.Now()
	batch := make([][]any, 0, len(attempts))
	for _, a := range attempts {
		at := a.AttemptedAt
		if at.IsZero() {
			at = now
		}
		count := a.EventCount
		if count <= 0 {
			count = 1
		}
		batch = append(batch, []any{
			a.IntegrationID, a.FindingKey, string(a.Action), int32(a.Attempt), string(a.Outcome),
			int32(a.HTTPStatus), truncateError(a.Error), int32(a.Duration.Milliseconds()), int32(count), at,
		})
	}
	err := execBatch(ctx, r.pool,
		`INSERT INTO notification_attempts (integration_id, finding_key, action, attempt, outcome, http_status, error, duration_ms, event_count, attempted_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		batch)
	if err != nil {
		return fmt.Errorf("record notification attempts: %w", err)
	}
	return nil
}

// ListNotificationAttempts returns one integration's audit rows newest first.
func (r *Repo) ListNotificationAttempts(ctx context.Context, integrationID string, since time.Time, limit int) ([]repo.NotificationAttempt, error) {
	if limit <= 0 {
		limit = 200
	}
	return queryRows(ctx, r.pool, "list notification attempts",
		`SELECT integration_id, finding_key, action, attempt, outcome, http_status, error, duration_ms, event_count, attempted_at
		 FROM notification_attempts
		 WHERE integration_id = $1 AND attempted_at >= $2
		 ORDER BY attempted_at DESC
		 LIMIT $3`,
		scanAttempt, integrationID, orEpoch(since), limit)
}

func scanAttempt(s rowScanner) (repo.NotificationAttempt, error) {
	var (
		a               repo.NotificationAttempt
		action, outcome string
		attempt         int32
		httpStatus      int32
		durationMS      int32
		eventCount      int32
	)
	if err := s.Scan(&a.IntegrationID, &a.FindingKey, &action, &attempt, &outcome, &httpStatus, &a.Error, &durationMS, &eventCount, &a.AttemptedAt); err != nil {
		return repo.NotificationAttempt{}, err
	}
	a.Action = repo.DispatchAction(action)
	a.Attempt = int(attempt)
	a.Outcome = repo.AttemptOutcome(outcome)
	a.HTTPStatus = int(httpStatus)
	a.Duration = time.Duration(durationMS) * time.Millisecond
	a.EventCount = int(eventCount)
	return a, nil
}

// ---------- health ----------

// healthWindow bounds how far back the audit is read for health; older
// failures are history, not current health.
const healthWindow = 24 * time.Hour

// ListIntegrationHealth derives every integration's health in two aggregate
// queries: attempt outcomes over the last day, and ledger rows by state.
func (r *Repo) ListIntegrationHealth(ctx context.Context) (map[string]repo.IntegrationHealth, error) {
	out := map[string]repo.IntegrationHealth{}
	type attemptAgg struct {
		id                string
		lastOK, lastFail  time.Time
		lastErr           string
		failsSinceSuccess int64
	}
	since := time.Now().Add(-healthWindow)
	// The trailing run of failures is every attempt newer than the last success
	// (all of them when none succeeded).
	attempts, err := queryRows(ctx, r.pool, "integration health attempts",
		`WITH recent AS (
			SELECT integration_id, attempted_at, outcome, error
			FROM notification_attempts
			WHERE attempted_at >= $1
		 ), last_ok AS (
			SELECT integration_id, max(attempted_at) FILTER (WHERE outcome = 'ok') AS at
			FROM recent GROUP BY integration_id
		 )
		 SELECT r.integration_id,
			COALESCE(max(r.attempted_at) FILTER (WHERE r.outcome = 'ok'), 'epoch'),
			COALESCE(max(r.attempted_at) FILTER (WHERE r.outcome <> 'ok'), 'epoch'),
			COALESCE((array_agg(r.error ORDER BY r.attempted_at DESC) FILTER (WHERE r.outcome <> 'ok'))[1], ''),
			count(*) FILTER (WHERE r.attempted_at > COALESCE(l.at, '-infinity'))
		 FROM recent AS r
		 JOIN last_ok AS l ON l.integration_id = r.integration_id
		 GROUP BY r.integration_id`,
		func(s rowScanner) (attemptAgg, error) {
			var a attemptAgg
			err := s.Scan(&a.id, &a.lastOK, &a.lastFail, &a.lastErr, &a.failsSinceSuccess)
			return a, err
		}, since)
	if err != nil {
		return nil, err
	}
	for _, a := range attempts {
		h := out[a.id]
		if a.lastOK.Unix() > 0 {
			h.LastSuccessAt = a.lastOK
		}
		if a.lastFail.Unix() > 0 {
			h.LastFailureAt = a.lastFail
			h.LastError = a.lastErr
		}
		h.ConsecutiveFailures = int(a.failsSinceSuccess)
		out[a.id] = h
	}

	type stateAgg struct {
		id, state string
		n         int64
	}
	states, err := queryRows(ctx, r.pool, "integration health ledger",
		`SELECT integration_id, state, count(*) FROM notification_dispatch GROUP BY integration_id, state`,
		func(s rowScanner) (stateAgg, error) {
			var a stateAgg
			err := s.Scan(&a.id, &a.state, &a.n)
			return a, err
		})
	if err != nil {
		return nil, err
	}
	for _, s := range states {
		h := out[s.id]
		switch repo.DispatchState(s.state) {
		case repo.DispatchPending:
			h.Pending = s.n
		case repo.DispatchFailed:
			h.Failed = s.n
		case repo.DispatchDead:
			h.Dead = s.n
		}
		out[s.id] = h
	}
	return out, nil
}

// ---------- cursors ----------

// GetNotificationCursor returns the notifier's progress for one integration.
func (r *Repo) GetNotificationCursor(ctx context.Context, integrationID string) (repo.NotificationCursor, bool, error) {
	return queryRow(ctx, r.pool, "get notification cursor",
		`SELECT integration_id, cursor, last_run_at, leader FROM notification_cursors WHERE integration_id = $1`,
		func(s rowScanner) (repo.NotificationCursor, error) {
			var c repo.NotificationCursor
			err := s.Scan(&c.IntegrationID, &c.Cursor, &c.LastRunAt, &c.Leader)
			return c, err
		}, integrationID)
}

// SetNotificationCursor records the notifier's progress.
func (r *Repo) SetNotificationCursor(ctx context.Context, c repo.NotificationCursor) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO notification_cursors (integration_id, cursor, last_run_at, leader, updated_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (integration_id) DO UPDATE SET
			cursor = EXCLUDED.cursor, last_run_at = EXCLUDED.last_run_at, leader = EXCLUDED.leader, updated_at = EXCLUDED.updated_at`,
		c.IntegrationID, orEpoch(c.Cursor), orEpoch(c.LastRunAt), c.Leader, time.Now())
	if err != nil {
		return fmt.Errorf("set notification cursor: %w", err)
	}
	return nil
}

// maxStoredError bounds error strings so a backend returning an HTML page
// does not bloat the audit table.
const maxStoredError = 1024

func truncateError(s string) string {
	if len(s) <= maxStoredError {
		return s
	}
	return s[:maxStoredError]
}
