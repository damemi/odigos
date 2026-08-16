package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	domainsrc "github.com/odigos-io/odigos-enterprise/connectors/base/domain/source"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
)

var sourcesLog = connlog.Subsystem("store-sources")

// SourceRecord is a persisted instrumentation-intent row.
type SourceRecord struct {
	Name               string
	Kind               string
	ServiceName        string
	Enabled            bool
	ExternalResourceID string
	Region             string
	Config             domainsrc.SourceConfig
	// UpdatedAt is when the intent last changed (created, edited, enabled or disabled). It is the base's
	// only clock over intent: reports use it to say how long a workload has been in its current state — how
	// long instrumentation has been awaited, or how long a disabled source has had live instrumentation on it
	// — which is the difference between "pending" and "stuck" for anyone reading a report.
	UpdatedAt time.Time
}

func (s *Store) ListSources(ctx context.Context) ([]SourceRecord, error) {
	sourcesLog.Debug("list sources", "connector", s.connectorName, "schema", s.schema)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT source_name, kind, COALESCE(service_name, ''), enabled, external_resource_id, COALESCE(region, ''), config, updated_at
		FROM %s.sources
		WHERE connector_name = $1
		ORDER BY source_name
	`, s.schema), s.connectorName)
	if err != nil {
		sourcesLog.Error("list sources failed", "connector", s.connectorName, "error", err)
		return nil, err
	}
	defer rows.Close()

	var out []SourceRecord
	for rows.Next() {
		rec, err := scanSourceRow(rows)
		if err != nil {
			sourcesLog.Error("scan source row failed", "error", err)
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		sourcesLog.Error("list sources failed", "connector", s.connectorName, "error", err)
		return nil, err
	}
	sourcesLog.Debug("list sources complete", "connector", s.connectorName, "count", len(out))
	return out, nil
}

// FindSource resolves a single source row by its source_name display handle, optionally narrowed by kind
// and region (empty = unconstrained). source_name is NOT the identity — the primary key is
// (connector_name, kind, external_resource_id) — so a bare name can match rows across regions/kinds.
// This returns an error when more than one row matches rather than silently picking one (the caller must
// then supply kind/region to disambiguate), and (nil, nil) when nothing matches. The returned record
// carries the true identity (Kind, ExternalResourceID) callers must key on to update or delete the exact
// row.
func (s *Store) FindSource(ctx context.Context, name, kind, region string) (*SourceRecord, error) {
	sourcesLog.Debug("find source", "connector", s.connectorName, "name", name, "kind", kind, "region", region)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT source_name, kind, COALESCE(service_name, ''), enabled, external_resource_id, COALESCE(region, ''), config, updated_at
		FROM %s.sources
		WHERE connector_name = $1 AND source_name = $2
		  AND ($3 = '' OR kind = $3)
		  AND ($4 = '' OR region = $4)
	`, s.schema), s.connectorName, name, kind, region)
	if err != nil {
		sourcesLog.Error("find source failed", "connector", s.connectorName, "name", name, "error", err)
		return nil, err
	}
	defer rows.Close()

	var recs []SourceRecord
	for rows.Next() {
		rec, err := scanSourceRow(rows)
		if err != nil {
			sourcesLog.Error("scan source row failed", "error", err)
			return nil, err
		}
		recs = append(recs, rec)
	}
	if err := rows.Err(); err != nil {
		sourcesLog.Error("find source failed", "connector", s.connectorName, "name", name, "error", err)
		return nil, err
	}
	switch len(recs) {
	case 0:
		return nil, nil
	case 1:
		return &recs[0], nil
	default:
		return nil, fmt.Errorf("ambiguous source %q matches %d rows; specify kind and region", name, len(recs))
	}
}

// scanSourceRow scans one sources row in the canonical column order shared by the source queries.
func scanSourceRow(row interface{ Scan(...any) error }) (SourceRecord, error) {
	var rec SourceRecord
	var configJSON []byte
	if err := row.Scan(&rec.Name, &rec.Kind, &rec.ServiceName, &rec.Enabled, &rec.ExternalResourceID, &rec.Region, &configJSON, &rec.UpdatedAt); err != nil {
		return SourceRecord{}, err
	}
	if len(configJSON) > 0 {
		if err := json.Unmarshal(configJSON, &rec.Config); err != nil {
			// Don't fail the whole list on one corrupt row, but never swallow it silently: a corrupt config
			// silently becoming empty would drop the source's overrides with no trace.
			sourcesLog.Warn("source config unmarshal failed; using empty config", "source", rec.Name, "kind", rec.Kind, "error", err)
		}
	}
	return rec, nil
}

// pgExecer is satisfied by both *pgxpool.Pool and pgx.Tx, so the source write statements run either
// standalone or inside a transaction (see MoveSource).
type pgExecer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// sqlUpsertSource / sqlDeleteSource are shared by the standalone methods and the transactional MoveSource
// so the identity keying (connector_name, kind, external_resource_id) stays in one place. Format with the
// provider schema.
const sqlUpsertSource = `
	INSERT INTO %s.sources
		(connector_name, source_name, kind, service_name, enabled, external_resource_id, region, config, created_at, updated_at)
	VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb, now(), now())
	ON CONFLICT (connector_name, kind, external_resource_id) DO UPDATE SET
		source_name = EXCLUDED.source_name,
		service_name = EXCLUDED.service_name,
		enabled = EXCLUDED.enabled,
		region = EXCLUDED.region,
		config = EXCLUDED.config,
		updated_at = now()`

const sqlDeleteSource = `DELETE FROM %s.sources WHERE connector_name = $1 AND kind = $2 AND external_resource_id = $3`

func (s *Store) upsertSourceExec(ctx context.Context, db pgExecer, src domainsrc.Source) error {
	configJSON, err := json.Marshal(src.Config)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, fmt.Sprintf(sqlUpsertSource, s.schema),
		s.connectorName, src.Name, src.Kind, src.ServiceName, src.Enabled, src.ExternalResourceID, src.Region, string(configJSON))
	return err
}

func (s *Store) UpsertSource(ctx context.Context, src domainsrc.Source) error {
	sourcesLog.Info("upsert source",
		"connector", s.connectorName,
		"name", src.Name,
		"kind", src.Kind,
		"enabled", src.Enabled,
	)
	// Identity is (connector_name, kind, external_resource_id) — the stable cloud resource id. source_name,
	// service_name and region are non-key attributes updated in place on conflict.
	if err := s.upsertSourceExec(ctx, s.pool, src); err != nil {
		sourcesLog.Error("upsert source failed", "connector", s.connectorName, "name", src.Name, "error", err)
		return err
	}
	return nil
}

// DeleteSourceByKey removes a source addressed by its full identity (kind + external_resource_id), the
// primary key. This is the only delete path: callers resolve the exact row (via FindSource) and pass its
// true identity from the resolved record, so the right row is removed regardless of the display handle or
// a service-name override.
func (s *Store) DeleteSourceByKey(ctx context.Context, kind, externalResourceID string) error {
	sourcesLog.Info("delete source by key", "connector", s.connectorName, "kind", kind, "external_id", externalResourceID)
	_, err := s.pool.Exec(ctx, fmt.Sprintf(sqlDeleteSource, s.schema), s.connectorName, kind, externalResourceID)
	if err != nil {
		sourcesLog.Error("delete source by key failed", "connector", s.connectorName, "kind", kind, "external_id", externalResourceID, "error", err)
	}
	return err
}

// MoveSource atomically repoints a source to a new identity: it deletes the old-identity row and upserts
// the new one in a single transaction, so a crash between the two can never lose the intent (leaving a
// workload instrumented with no source row, which the next reconcile would silently roll back). Used only
// when a source's identity (kind or external_resource_id) actually changes.
func (s *Store) MoveSource(ctx context.Context, oldKind, oldExternalID string, src domainsrc.Source) error {
	sourcesLog.Info("move source", "connector", s.connectorName, "name", src.Name,
		"old_kind", oldKind, "old_external_id", oldExternalID, "new_kind", src.Kind, "new_external_id", src.ExternalResourceID)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) // no-op after a successful Commit
	if _, err := tx.Exec(ctx, fmt.Sprintf(sqlDeleteSource, s.schema), s.connectorName, oldKind, oldExternalID); err != nil {
		sourcesLog.Error("move source delete-old failed", "connector", s.connectorName, "name", src.Name, "error", err)
		return err
	}
	if err := s.upsertSourceExec(ctx, tx, src); err != nil {
		sourcesLog.Error("move source upsert-new failed", "connector", s.connectorName, "name", src.Name, "error", err)
		return err
	}
	return tx.Commit(ctx)
}

func SourceRecordToDomain(rec SourceRecord) domainsrc.Source {
	return domainsrc.Source{
		Name:               rec.Name,
		Kind:               rec.Kind,
		ServiceName:        rec.ServiceName,
		Enabled:            rec.Enabled,
		ExternalResourceID: rec.ExternalResourceID,
		Region:             rec.Region,
		Config:             rec.Config,
	}
}

// ReportedServiceName returns the OTel service.name instrumentation should report for this source row.
func (rec SourceRecord) ReportedServiceName() string {
	return SourceRecordToDomain(rec).ReportedServiceName()
}

// PurgeIntent hard-deletes ALL sources and destinations for this connector. Used when a reused connector
// name is found to point at a different provider account than before: the persisted intent belongs to the
// old account and must not carry over (a stale enabled source could auto-instrument a look-alike workload
// in the new account). Distinct from DisableAllSources, which only disables (for drain) and keeps the rows.
func (s *Store) PurgeIntent(ctx context.Context) error {
	sourcesLog.Warn("purging all sources and destinations for connector", "connector", s.connectorName)
	if _, err := s.pool.Exec(ctx, fmt.Sprintf(`DELETE FROM %s.sources WHERE connector_name = $1`, s.schema), s.connectorName); err != nil {
		return fmt.Errorf("purge sources: %w", err)
	}
	if _, err := s.pool.Exec(ctx, fmt.Sprintf(`DELETE FROM %s.destinations WHERE connector_name = $1`, s.schema), s.connectorName); err != nil {
		return fmt.Errorf("purge destinations: %w", err)
	}
	return nil
}

// DisableAllSources sets enabled=false on every source intent row for this connector. Used during
// shutdown drain so the final reconcile pass rolls back all instrumentation without provider-specific
// shutdown logic.
func (s *Store) DisableAllSources(ctx context.Context) error {
	sourcesLog.Info("disable all sources for shutdown", "connector", s.connectorName)
	_, err := s.pool.Exec(ctx, fmt.Sprintf(`
		UPDATE %s.sources SET enabled = false, updated_at = now()
		WHERE connector_name = $1 AND enabled = true
	`, s.schema), s.connectorName)
	if err != nil {
		sourcesLog.Error("disable all sources failed", "connector", s.connectorName, "error", err)
	}
	return err
}
