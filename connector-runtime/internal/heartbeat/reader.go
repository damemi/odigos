// Package heartbeat provides read/write access to connector_platform.runtime_heartbeat for the
// connector-runtime controller (status mirroring and shutdown drain coordination).
package heartbeat

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	cpg "github.com/odigos-io/odigos-enterprise/connectors/postgres"
)

type Snapshot struct {
	Found            bool
	Status           string
	Version          string
	LastSeenAt       time.Time
	DiscoveryReady   bool
	Authenticated    bool
	DrainRequested   bool
	DrainRequestedAt time.Time
	DrainReason      string
	ShutdownComplete bool
	// AwaitingManualCleanup is true while automatic-mode rollback finished but manual-mode (IaC)
	// workloads are still observed as instrumented. The connector withholds shutdownComplete until
	// discovery reports them uninstrumented (or the controller's cleanup timeout force-removes the
	// finalizer).
	AwaitingManualCleanup bool
	// ManualInstrumentationRetained is how many manual-mode workloads are still carrying Odigos
	// instrumentation the connector will not remove. Non-zero while AwaitingManualCleanup, and may
	// also appear on a timed-out delete so the Event can name what was left behind.
	ManualInstrumentationRetained int
	// ManualInstrumentationWorkloads samples the retained workloads (kind/serviceName), capped by the
	// connector. The count above is authoritative; this is somewhere to start looking.
	ManualInstrumentationWorkloads []string
}

type Reader struct {
	pool *pgxpool.Pool
}

// NewReader connects to Postgres for heartbeat reads/writes. An empty DSN yields a disabled reader
// whose methods are no-ops (heartbeat mirroring and drain coordination off).
func NewReader(ctx context.Context, dsn string) (*Reader, error) {
	if dsn == "" {
		return &Reader{}, nil
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	return &Reader{pool: pool}, nil
}

func (r *Reader) Close() {
	if r.pool != nil {
		r.pool.Close()
	}
}

// Enabled reports whether heartbeat access is configured.
func (r *Reader) Enabled() bool { return r != nil && r.pool != nil }

// Get returns the heartbeat snapshot for a connector. A missing row yields Found=false with no error;
// a connectivity error is returned so the caller can mark conditions Unknown.
func (r *Reader) Get(ctx context.Context, connectorName string) (Snapshot, error) {
	if !r.Enabled() {
		return Snapshot{}, nil
	}
	var status, version string
	var healthBytes []byte
	var lastSeen time.Time
	var drainAt *time.Time
	var drainReason *string
	err := r.pool.QueryRow(ctx, `
		SELECT status, version, health, last_seen_at, drain_requested_at, drain_reason
		FROM connector_platform.runtime_heartbeat
		WHERE connector_name = $1
	`, connectorName).Scan(&status, &version, &healthBytes, &lastSeen, &drainAt, &drainReason)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Snapshot{}, nil
		}
		return Snapshot{}, err
	}

	s := Snapshot{Found: true, Status: status, Version: version, LastSeenAt: lastSeen}
	if drainAt != nil {
		s.DrainRequested = true
		s.DrainRequestedAt = *drainAt
	}
	if drainReason != nil {
		s.DrainReason = *drainReason
	}
	var health struct {
		DiscoveryReady                 bool     `json:"discoveryReady"`
		Authenticated                  bool     `json:"authenticated"`
		ShutdownComplete               bool     `json:"shutdownComplete"`
		AwaitingManualCleanup          bool     `json:"awaitingManualCleanup"`
		ManualInstrumentationRetained  int      `json:"manualInstrumentationRetained"`
		ManualInstrumentationWorkloads []string `json:"manualInstrumentationWorkloads"`
	}
	if len(healthBytes) > 0 {
		_ = json.Unmarshal(healthBytes, &health)
	}
	s.DiscoveryReady = health.DiscoveryReady
	s.Authenticated = health.Authenticated
	s.ShutdownComplete = health.ShutdownComplete
	s.AwaitingManualCleanup = health.AwaitingManualCleanup
	s.ManualInstrumentationRetained = health.ManualInstrumentationRetained
	s.ManualInstrumentationWorkloads = health.ManualInstrumentationWorkloads
	return s, nil
}

// RequestDrain marks the connector for instrumentation rollback on the next pod shutdown.
func (r *Reader) RequestDrain(ctx context.Context, connectorName, reason string) error {
	if !r.Enabled() {
		return nil
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO connector_platform.runtime_heartbeat
			(connector_name, runtime_id, status, supported_kinds, health, last_seen_at, updated_at,
			 drain_requested_at, drain_reason)
		VALUES ($1, '', $2, '[]'::jsonb, '{}'::jsonb, now(), now(), now(), $3)
		ON CONFLICT (connector_name) DO UPDATE SET
			drain_requested_at = now(),
			drain_reason = EXCLUDED.drain_reason,
			updated_at = now()
	`, connectorName, cpg.HeartbeatStatusConnected, reason)
	return err
}
