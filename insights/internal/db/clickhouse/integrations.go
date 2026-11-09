package clickhouse

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// ---------- integrations ----------

const integrationCols = `integration_id, name, type, enabled, config, filters, created_at, updated_at`

// ListIntegrations returns every configured backend sorted by name.
func (r *Repo) ListIntegrations(ctx context.Context) ([]repo.Integration, error) {
	return queryRows(ctx, r.conn, "list integrations",
		`SELECT `+integrationCols+` FROM integrations FINAL ORDER BY name ASC, integration_id ASC`,
		scanIntegration)
}

// GetIntegration returns one backend by id; ok=false when unknown.
func (r *Repo) GetIntegration(ctx context.Context, id string) (repo.Integration, bool, error) {
	return queryRow(ctx, r.conn, "get integration",
		`SELECT `+integrationCols+` FROM integrations FINAL WHERE integration_id = ? LIMIT 1`,
		scanIntegration, id)
}

func scanIntegration(s rowScanner) (repo.Integration, error) {
	var (
		in      repo.Integration
		enabled uint8
		config  string
		filters string
	)
	if err := s.Scan(&in.ID, &in.Name, &in.Type, &enabled, &config, &filters, &in.CreatedAt, &in.UpdatedAt); err != nil {
		return repo.Integration{}, err
	}
	in.Enabled = enabled == 1
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

// UpsertIntegration inserts or replaces one backend (ReplacingMergeTree keyed
// by id). UpdatedAt is stamped here; CreatedAt is whatever the caller carried.
func (r *Repo) UpsertIntegration(ctx context.Context, in repo.Integration) error {
	filters, err := json.Marshal(in.Filters)
	if err != nil {
		return fmt.Errorf("encode integration filters: %w", err)
	}
	config := string(in.Config)
	if config == "" {
		config = "{}"
	}
	enabled := uint8(0)
	if in.Enabled {
		enabled = 1
	}
	now := time.Now()
	createdAt := in.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	err = r.insert(ctx,
		`INSERT INTO integrations (`+integrationCols+`)`,
		func(batch driver.Batch) error {
			return batch.Append(in.ID, in.Name, in.Type, enabled, config, string(filters), createdAt, now)
		})
	if err != nil {
		return fmt.Errorf("upsert integration: %w", err)
	}
	return nil
}

// DeleteIntegration removes a backend plus its ledger, cursor and audit rows
// via ALTER ... DELETE mutations (cold path).
func (r *Repo) DeleteIntegration(ctx context.Context, id string) error {
	for _, table := range []string{"integrations", "notification_dispatch", "notification_cursors", "notification_attempts"} {
		if err := r.conn.Exec(ctx, `ALTER TABLE `+table+` DELETE WHERE integration_id = ?`, id); err != nil {
			return fmt.Errorf("delete integration %s from %s: %w", id, table, err)
		}
	}
	return nil
}

// ---------- delivery ledger ----------

const dispatchCols = `integration_id, finding_key, last_action, state, attempts, next_attempt_at, last_error,
	sent_at, occurrences_at_send, severity_at_send, status_at_send, external_id, updated_at`

// ListNotificationDispatch returns ledger rows matching filter, newest first.
func (r *Repo) ListNotificationDispatch(ctx context.Context, f repo.DispatchListFilter) ([]repo.NotificationDispatch, error) {
	var (
		where []string
		args  []any
	)
	if f.IntegrationID != "" {
		where = append(where, "integration_id = ?")
		args = append(args, f.IntegrationID)
	}
	if len(f.FindingKeys) > 0 {
		where = append(where, "finding_key IN (?)")
		args = append(args, f.FindingKeys)
	}
	if f.State != "" {
		where = append(where, "state = ?")
		args = append(args, string(f.State))
	}
	if !f.DueBefore.IsZero() {
		where = append(where, "next_attempt_at <= ?")
		args = append(args, f.DueBefore)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 500
	}
	args = append(args, uint64(limit))
	q := `SELECT ` + dispatchCols + ` FROM notification_dispatch FINAL`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	q += ` ORDER BY updated_at DESC LIMIT ?`
	return queryRows(ctx, r.conn, "list notification dispatch", q, scanDispatch, args...)
}

func scanDispatch(s rowScanner) (repo.NotificationDispatch, error) {
	var (
		d                  repo.NotificationDispatch
		action, state      string
		attempts           uint16
		occurrencesAtSend  uint64
		severity, statusAt string
	)
	if err := s.Scan(&d.IntegrationID, &d.FindingKey, &action, &state, &attempts, &d.NextAttemptAt, &d.LastError,
		&d.SentAt, &occurrencesAtSend, &severity, &statusAt, &d.ExternalID, &d.UpdatedAt); err != nil {
		return repo.NotificationDispatch{}, err
	}
	d.LastAction = repo.DispatchAction(action)
	d.State = repo.DispatchState(state)
	d.Attempts = int(attempts)
	d.OccurrencesAtSend = int64(occurrencesAtSend)
	d.SeverityAtSend = severity
	d.StatusAtSend = statusAt
	if d.SentAt.Equal(decisionEpoch) {
		d.SentAt = time.Time{}
	}
	if d.NextAttemptAt.Equal(decisionEpoch) {
		d.NextAttemptAt = time.Time{}
	}
	return d, nil
}

// UpsertNotificationDispatch writes ledger rows; the ReplacingMergeTree keeps
// the newest updated_at per (integration, finding).
func (r *Repo) UpsertNotificationDispatch(ctx context.Context, rows []repo.NotificationDispatch) error {
	if len(rows) == 0 {
		return nil
	}
	now := time.Now()
	err := r.insert(ctx,
		`INSERT INTO notification_dispatch (`+dispatchCols+`)`,
		func(batch driver.Batch) error {
			for _, d := range rows {
				if err := batch.Append(
					d.IntegrationID, d.FindingKey, string(d.LastAction), string(d.State), uint16(d.Attempts),
					orEpoch(d.NextAttemptAt), truncateError(d.LastError),
					orEpoch(d.SentAt), uint64(d.OccurrencesAtSend), d.SeverityAtSend, d.StatusAtSend, d.ExternalID, now,
				); err != nil {
					return err
				}
			}
			return nil
		})
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
	err := r.insert(ctx,
		`INSERT INTO notification_attempts (integration_id, finding_key, action, attempt, outcome, http_status, error, duration_ms, event_count, attempted_at)`,
		func(batch driver.Batch) error {
			for _, a := range attempts {
				at := a.AttemptedAt
				if at.IsZero() {
					at = time.Now()
				}
				count := a.EventCount
				if count <= 0 {
					count = 1
				}
				if err := batch.Append(
					a.IntegrationID, a.FindingKey, string(a.Action), uint16(a.Attempt), string(a.Outcome),
					int32(a.HTTPStatus), truncateError(a.Error), uint32(a.Duration.Milliseconds()), uint32(count), at,
				); err != nil {
					return err
				}
			}
			return nil
		})
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
	if since.IsZero() {
		since = decisionEpoch
	}
	return queryRows(ctx, r.conn, "list notification attempts",
		`SELECT integration_id, finding_key, action, attempt, outcome, http_status, error, duration_ms, event_count, attempted_at
		 FROM notification_attempts
		 WHERE integration_id = ? AND attempted_at >= ?
		 ORDER BY attempted_at DESC
		 LIMIT ?`,
		scanAttempt, integrationID, since, uint64(limit))
}

func scanAttempt(s rowScanner) (repo.NotificationAttempt, error) {
	var (
		a               repo.NotificationAttempt
		action, outcome string
		attempt         uint16
		httpStatus      int32
		durationMS      uint32
		eventCount      uint32
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
		failsSinceSuccess uint64
	}
	since := time.Now().Add(-healthWindow)
	// fails_since_ok counts the trailing run of failures: the history is sorted
	// newest first and cut at the first success (or taken whole when none).
	attempts, err := queryRows(ctx, r.conn, "integration health attempts",
		`SELECT integration_id, last_ok, last_fail, last_err, fails_since_ok
		 FROM (
		     SELECT integration_id,
		            maxIf(attempted_at, outcome = 'ok')                       AS last_ok,
		            maxIf(attempted_at, outcome != 'ok')                      AS last_fail,
		            argMaxIf(error, attempted_at, outcome != 'ok')            AS last_err,
		            arrayReverseSort(x -> x.1, groupArray((attempted_at, outcome))) AS hist,
		            arrayFirstIndex(x -> x.2 = 'ok', hist)                    AS first_ok,
		            toUInt64(if(first_ok = 0, toInt64(length(hist)), toInt64(first_ok) - 1)) AS fails_since_ok
		     FROM notification_attempts
		     WHERE attempted_at >= ?
		     GROUP BY integration_id
		 )`,
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
		// maxIf with no match yields the epoch: treat it as unset.
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
		n         uint64
	}
	states, err := queryRows(ctx, r.conn, "integration health ledger",
		`SELECT integration_id, state, count() FROM notification_dispatch FINAL GROUP BY integration_id, state`,
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
			h.Pending = int64(s.n)
		case repo.DispatchFailed:
			h.Failed = int64(s.n)
		case repo.DispatchDead:
			h.Dead = int64(s.n)
		}
		out[s.id] = h
	}
	return out, nil
}

// ---------- cursors ----------

// GetNotificationCursor returns the notifier's progress for one integration.
func (r *Repo) GetNotificationCursor(ctx context.Context, integrationID string) (repo.NotificationCursor, bool, error) {
	return queryRow(ctx, r.conn, "get notification cursor",
		`SELECT integration_id, cursor, last_run_at, leader FROM notification_cursors FINAL WHERE integration_id = ? LIMIT 1`,
		func(s rowScanner) (repo.NotificationCursor, error) {
			var c repo.NotificationCursor
			err := s.Scan(&c.IntegrationID, &c.Cursor, &c.LastRunAt, &c.Leader)
			return c, err
		}, integrationID)
}

// SetNotificationCursor records the notifier's progress.
func (r *Repo) SetNotificationCursor(ctx context.Context, c repo.NotificationCursor) error {
	err := r.insert(ctx,
		`INSERT INTO notification_cursors (integration_id, cursor, last_run_at, leader, updated_at)`,
		func(batch driver.Batch) error {
			return batch.Append(c.IntegrationID, orEpoch(c.Cursor), orEpoch(c.LastRunAt), c.Leader, time.Now())
		})
	if err != nil {
		return fmt.Errorf("set notification cursor: %w", err)
	}
	return nil
}

// orEpoch maps a zero time to the epoch sentinel the DateTime64 columns store
// for "unset" (the driver cannot store Go's zero time).
func orEpoch(t time.Time) time.Time {
	if t.IsZero() {
		return decisionEpoch
	}
	return t
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
