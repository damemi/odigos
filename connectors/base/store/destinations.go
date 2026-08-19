package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
)

var destinationsLog = connlog.Subsystem("store-destinations")

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// DestinationRecord is a persisted destination row.
type DestinationRecord struct {
	Name    string
	Type    string
	Config  map[string]any
	Signals []string
}

func (s *Store) ListDestinations(ctx context.Context) ([]DestinationRecord, error) {
	destinationsLog.Debug("list destinations", "connector", s.connectorName, "schema", s.schema)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT name, dest_type, config, signals
		FROM %s.destinations
		WHERE connector_name = $1
		ORDER BY name
	`, s.schema), s.connectorName)
	if err != nil {
		destinationsLog.Error("list destinations failed", "connector", s.connectorName, "error", err)
		return nil, err
	}
	defer rows.Close()

	var out []DestinationRecord
	for rows.Next() {
		var rec DestinationRecord
		var configJSON, signalsJSON []byte
		if err := rows.Scan(&rec.Name, &rec.Type, &configJSON, &signalsJSON); err != nil {
			destinationsLog.Error("scan destination row failed", "error", err)
			return nil, err
		}
		decodeDestJSON(rec.Name, "config", configJSON, &rec.Config)
		decodeDestJSON(rec.Name, "signals", signalsJSON, &rec.Signals)
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		destinationsLog.Error("list destinations failed", "connector", s.connectorName, "error", err)
		return nil, err
	}
	destinationsLog.Debug("list destinations complete", "connector", s.connectorName, "count", len(out))
	return out, nil
}

func (s *Store) GetDestination(ctx context.Context, name string) (*DestinationRecord, error) {
	destinationsLog.Debug("get destination", "connector", s.connectorName, "name", name)
	var rec DestinationRecord
	var configJSON, signalsJSON []byte
	err := s.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT name, dest_type, config, signals
		FROM %s.destinations
		WHERE connector_name = $1 AND name = $2
	`, s.schema), s.connectorName, name).Scan(&rec.Name, &rec.Type, &configJSON, &signalsJSON)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		destinationsLog.Error("get destination failed", "connector", s.connectorName, "name", name, "error", err)
		return nil, err
	}
	decodeDestJSON(rec.Name, "config", configJSON, &rec.Config)
	decodeDestJSON(rec.Name, "signals", signalsJSON, &rec.Signals)
	return &rec, nil
}

// decodeDestJSON unmarshals a destination JSONB column, warning (not failing) on corruption. A corrupt
// destination config silently becoming empty would make ResolveOTLPTarget find no endpoint and instrumented
// workloads export nowhere with no trace, so the failure must at least be logged.
func decodeDestJSON(name, field string, data []byte, into any) {
	if len(data) == 0 {
		return
	}
	if err := json.Unmarshal(data, into); err != nil {
		destinationsLog.Warn("destination json unmarshal failed; using empty value", "destination", name, "field", field, "error", err)
	}
}

func (s *Store) UpsertDestination(ctx context.Context, dest domaindest.Destination) error {
	destinationsLog.Info("upsert destination",
		"connector", s.connectorName,
		"name", dest.Name,
		"type", dest.Type,
	)
	configJSON, err := json.Marshal(dest.Config)
	if err != nil {
		return err
	}
	signalsJSON, err := json.Marshal(dest.Signals)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s.destinations
			(connector_name, name, dest_type, config, signals, created_at, updated_at)
		VALUES ($1,$2,$3,$4::jsonb,$5::jsonb, now(), now())
		ON CONFLICT (connector_name, name) DO UPDATE SET
			dest_type = EXCLUDED.dest_type,
			config = EXCLUDED.config,
			signals = EXCLUDED.signals,
			updated_at = now()
	`, s.schema), s.connectorName, dest.Name, dest.Type, string(configJSON), string(signalsJSON))
	if err != nil {
		destinationsLog.Error("upsert destination failed", "connector", s.connectorName, "name", dest.Name, "error", err)
	}
	return err
}

func (s *Store) DeleteDestination(ctx context.Context, name string) error {
	destinationsLog.Info("delete destination", "connector", s.connectorName, "name", name)
	_, err := s.pool.Exec(ctx,
		fmt.Sprintf(`DELETE FROM %s.destinations WHERE connector_name = $1 AND name = $2`, s.schema),
		s.connectorName, name)
	if err != nil {
		destinationsLog.Error("delete destination failed", "connector", s.connectorName, "name", name, "error", err)
	}
	return err
}

// OTLPExportTarget resolves the connector's OTLP export target (endpoint, TLS-insecure, and the signals
// the destination accepts) from the configured destinations. Connector-wide and provider-agnostic:
// workload instrumentation across providers uses it to export only the signals the destination implements.
// Returns found=false when no usable OTLP destination exists.
func (s *Store) OTLPExportTarget(ctx context.Context) (domaindest.OTLPTarget, bool, error) {
	recs, err := s.ListDestinations(ctx)
	if err != nil {
		return domaindest.OTLPTarget{}, false, err
	}
	dests := make([]domaindest.Destination, 0, len(recs))
	for _, rec := range recs {
		dests = append(dests, DestinationRecordToDomain(rec))
	}
	target, found := domaindest.ResolveOTLPTarget(dests)
	return target, found, nil
}

// OTLPExportHeaders returns the per-request headers the connector's OTLP destination requires — its
// credential, in practice — or nil when it needs none.
//
// Separate from OTLPExportTarget rather than folded into it because OTLPTarget mirrors the destination
// definitions generated upstream in odigos, and this connector must not fork that shape. The headers come
// off the raw Destination.Config the generated model already carries through, converted in code by
// domaindest.ResolveOTLPHeaders, which selects the same destination OTLPExportTarget resolves.
func (s *Store) OTLPExportHeaders(ctx context.Context) (map[string]string, error) {
	recs, err := s.ListDestinations(ctx)
	if err != nil {
		return nil, err
	}
	dests := make([]domaindest.Destination, 0, len(recs))
	for _, rec := range recs {
		dests = append(dests, DestinationRecordToDomain(rec))
	}
	return domaindest.ResolveOTLPHeaders(dests), nil
}

func DestinationRecordToDomain(rec DestinationRecord) domaindest.Destination {
	cfg := rec.Config
	if cfg == nil {
		cfg = map[string]any{}
	}
	return domaindest.Destination{
		Name:    rec.Name,
		Type:    rec.Type,
		Config:  cfg,
		Signals: rec.Signals,
	}
}
