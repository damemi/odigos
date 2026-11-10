package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	cpg "github.com/odigos-io/odigos-enterprise/connectors/postgres"
)

// DrainState reports whether the controller has requested a graceful instrumentation drain.
type DrainState struct {
	Requested bool
	Reason    string
	At        time.Time
}

// DrainRequested reports whether a drain was requested for this connector (set by the controller or
// RequestCleanup). The runtime checks this on shutdown to decide whether to roll back instrumentation.
func (s *Store) DrainRequested(ctx context.Context) (DrainState, error) {
	var at *time.Time
	var reason *string
	err := s.pool.QueryRow(ctx, `
		SELECT drain_requested_at, drain_reason
		FROM connector_platform.runtime_heartbeat
		WHERE connector_name = $1
	`, s.connectorName).Scan(&at, &reason)
	if isNoRows(err) {
		return DrainState{}, nil
	}
	if err != nil {
		return DrainState{}, err
	}
	if at == nil {
		return DrainState{}, nil
	}
	st := DrainState{Requested: true, At: *at}
	if reason != nil {
		st.Reason = *reason
	}
	return st, nil
}

// ErrOtherDrainPending is returned by RequestDrain when a drain with a different reason is already pending.
var ErrOtherDrainPending = errors.New("another drain is already in progress")

// RequestDrain records a drain request with this reason. Used by RequestCleanup; connector-runtime writes its
// own CR-delete request.
//
// A pending drain with a different reason is never relabeled, and ErrOtherDrainPending is returned instead.
// The reason decides what the drain does (see the cpg.DrainReason constants), so a manual cleanup asked for
// while the connector is being deleted would turn the delete drain into a cleanup that never completes the
// delete. The check and the write are one statement, so a request racing the controller's cannot slip in.
func (s *Store) RequestDrain(ctx context.Context, reason string) error {
	heartbeatLog.Info("request drain", "connector", s.connectorName, "reason", reason)
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO connector_platform.runtime_heartbeat
			(connector_name, runtime_id, status, supported_kinds, health, last_seen_at, updated_at,
			 drain_requested_at, drain_reason)
		VALUES ($1, '', $2, '[]'::jsonb, '{}'::jsonb, now(), now(), now(), $3)
		ON CONFLICT (connector_name) DO UPDATE SET
			drain_requested_at = now(),
			drain_reason = EXCLUDED.drain_reason,
			updated_at = now()
		WHERE runtime_heartbeat.drain_requested_at IS NULL
		   OR runtime_heartbeat.drain_reason IS NOT DISTINCT FROM EXCLUDED.drain_reason
	`, s.connectorName, cpg.HeartbeatStatusConnected, reason)
	if err != nil {
		heartbeatLog.Error("request drain failed", "connector", s.connectorName, "error", err)
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrOtherDrainPending
	}
	return nil
}

// ClearDrain clears a drain request WITHOUT the pod-termination semantics of MarkShutdownComplete: it
// leaves status and the health blob untouched and does not set shutdownComplete. It is used after a
// successful manual RequestCleanup, where the pod keeps running: the rollback is done and every source
// is already disabled in the store, so the request must not persist — otherwise a later, unrelated pod
// restart (an upgrade/reschedule, which must NOT drain) would observe the stale flag and silently roll
// back instrumentation the user re-enabled in the meantime.
//
// Only a request with this reason is cleared. The controller may replace a pending manual cleanup with a
// CR-delete request at any moment, and clearing that one would leave the deletion waiting on a drain nobody
// runs.
func (s *Store) ClearDrain(ctx context.Context, reason string) error {
	heartbeatLog.Info("clear drain request", "connector", s.connectorName, "reason", reason)
	_, err := s.pool.Exec(ctx, `
		UPDATE connector_platform.runtime_heartbeat
		SET drain_requested_at = NULL,
		    drain_reason = NULL,
		    updated_at = now()
		WHERE connector_name = $1 AND runtime_id = $2 AND drain_reason = $3
	`, s.connectorName, s.runtimeID, reason)
	if err != nil {
		heartbeatLog.Error("clear drain request failed", "connector", s.connectorName, "error", err)
	}
	return err
}

// MarkShutdownComplete records that instrumentation rollback finished and clears the drain request.
func (s *Store) MarkShutdownComplete(ctx context.Context, health map[string]any) error {
	if health == nil {
		health = map[string]any{}
	}
	health["shutdownComplete"] = true
	healthJSON, err := json.Marshal(health)
	if err != nil {
		return err
	}
	heartbeatLog.Info("mark shutdown complete", "connector", s.connectorName)
	_, err = s.pool.Exec(ctx, `
		UPDATE connector_platform.runtime_heartbeat
		SET status = $2,
		    health = $3::jsonb,
		    drain_requested_at = NULL,
		    drain_reason = NULL,
		    last_seen_at = now(),
		    updated_at = now()
		WHERE connector_name = $1 AND runtime_id = $4
	`, s.connectorName, cpg.HeartbeatStatusDeleting, string(healthJSON), s.runtimeID)
	if err != nil {
		heartbeatLog.Error("mark shutdown complete failed", "connector", s.connectorName, "error", err)
	}
	return err
}

// ShutdownComplete reports whether the runtime finished its drain shutdown reconcile.
func (s *Store) ShutdownComplete(ctx context.Context) (bool, error) {
	var healthBytes []byte
	err := s.pool.QueryRow(ctx, `
		SELECT health FROM connector_platform.runtime_heartbeat WHERE connector_name = $1
	`, s.connectorName).Scan(&healthBytes)
	if isNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var health struct {
		ShutdownComplete bool `json:"shutdownComplete"`
	}
	if len(healthBytes) > 0 {
		_ = json.Unmarshal(healthBytes, &health)
	}
	return health.ShutdownComplete, nil
}
