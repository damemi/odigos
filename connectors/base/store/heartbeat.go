package store

import (
	"context"
	"encoding/json"
	"time"

	connlog "github.com/odigos-io/odigos/connectors/base/log"
)

var heartbeatLog = connlog.Subsystem("store-heartbeat")

// Register upserts the runtime's heartbeat/registration row with the given status and supported kinds.
//
// A fresh process start is a clean slate: it resets health (clearing any stale shutdownComplete a
// previous instance left behind) and clears the drain request. Without this, a connector recreated with
// the same name would inherit health.shutdownComplete=true / a stale drain request and connector-runtime
// could skip delete cleanup immediately. The controller re-requests a drain on the next delete reconcile
// if one is still warranted, so clearing here is self-healing.
func (s *Store) Register(ctx context.Context, runtimeID, podName, version, status string, kinds []string) error {
	heartbeatLog.Info("register runtime",
		"connector", s.connectorName,
		"runtime_id", runtimeID,
		"pod", podName,
		"status", status,
	)
	// Claim the row for this pod incarnation and remember the id so subsequent heartbeat/shutdown writes
	// fence on it (a stale previous pod, still holding the old id, can no longer clobber this row).
	s.runtimeID = runtimeID
	kindsJSON, err := json.Marshal(kinds)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO connector_platform.runtime_heartbeat
			(connector_name, runtime_id, pod_name, status, version, supported_kinds, health, last_seen_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, '{}'::jsonb, now(), now())
		ON CONFLICT (connector_name) DO UPDATE SET
			runtime_id = EXCLUDED.runtime_id,
			pod_name = EXCLUDED.pod_name,
			status = EXCLUDED.status,
			version = EXCLUDED.version,
			supported_kinds = EXCLUDED.supported_kinds,
			health = '{}'::jsonb,
			drain_requested_at = NULL,
			drain_reason = NULL,
			last_seen_at = now(),
			updated_at = now()
	`, s.connectorName, runtimeID, podName, status, version, string(kindsJSON))
	if err != nil {
		heartbeatLog.Error("register runtime failed", "connector", s.connectorName, "error", err)
	}
	return err
}

// Heartbeat updates the runtime's status, health blob and last-seen timestamp.
func (s *Store) Heartbeat(ctx context.Context, status string, health map[string]any) error {
	heartbeatLog.Debug("write heartbeat", "connector", s.connectorName, "status", status)
	healthJSON, err := json.Marshal(health)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE connector_platform.runtime_heartbeat
		SET status = $2, health = $3::jsonb, last_seen_at = now(), updated_at = now()
		WHERE connector_name = $1 AND runtime_id = $4
	`, s.connectorName, status, string(healthJSON), s.runtimeID)
	if err != nil {
		heartbeatLog.Error("write heartbeat failed", "connector", s.connectorName, "error", err)
	}
	return err
}

// LastHeartbeat returns the last_seen_at for this connector and whether a heartbeat row exists. A real DB
// error is returned (not swallowed as "not registered"), so callers can tell a transient DB failure apart
// from a genuinely absent row.
func (s *Store) LastHeartbeat(ctx context.Context) (time.Time, bool, error) {
	var ts time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT last_seen_at FROM connector_platform.runtime_heartbeat WHERE connector_name = $1`,
		s.connectorName).Scan(&ts)
	if isNoRows(err) {
		return time.Time{}, false, nil
	}
	if err != nil {
		heartbeatLog.Error("read heartbeat failed", "connector", s.connectorName, "error", err)
		return time.Time{}, false, err
	}
	heartbeatLog.Debug("read heartbeat", "connector", s.connectorName, "last_seen_at", ts)
	return ts, true, nil
}
