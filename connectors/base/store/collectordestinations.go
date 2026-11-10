package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	domaindest "github.com/odigos-io/odigos/connectors/base/domain/destination"
	connlog "github.com/odigos-io/odigos/connectors/base/log"
)

var collectorDestinationsLog = connlog.Subsystem("store-collector-destinations")

// Managed-Collector destinations, keyed (connector_name, region, name).
//
// Separate from ListDestinations and its table: those are the workload destinations, and a Collector's
// exporter set is configured independently of where the workloads shipping to it were told to send.
//
// A whole-row upsert rather than the per-concern writers managed_collectors uses: one of these rows is
// written only by the customer's own mutation, so a whole-row write cannot clobber a concurrent one.

// ListCollectorDestinations returns every destination of one region's Collector, ordered by name.
func (s *Store) ListCollectorDestinations(ctx context.Context, region string) ([]domaindest.Destination, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT region, name, dest_type, config, signals
		FROM %s.collector_destinations
		WHERE connector_name = $1 AND region = $2
		ORDER BY name
	`, s.schema), s.connectorName, region)
	if err != nil {
		collectorDestinationsLog.Error("list collector destinations failed",
			"connector", s.connectorName, "region", region, "error", err)
		return nil, fmt.Errorf("listing the destinations of the Collector in region %s: %w", region, err)
	}
	defer rows.Close()

	var out []domaindest.Destination
	for rows.Next() {
		d, err := scanCollectorDestination(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing the destinations of the Collector in region %s: %w", region, err)
	}
	return out, nil
}

// GetCollectorDestination returns one destination, or nil when that region has none by that name.
//
// nil rather than an error for a miss, matching GetDestination: "does this exist" is a question the
// caller asks on the way to create and update, and an error there would make the normal path noisy.
func (s *Store) GetCollectorDestination(ctx context.Context, region, name string) (*domaindest.Destination, error) {
	row := s.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT region, name, dest_type, config, signals
		FROM %s.collector_destinations
		WHERE connector_name = $1 AND region = $2 AND name = $3
	`, s.schema), s.connectorName, region, name)

	d, err := scanCollectorDestination(row)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		collectorDestinationsLog.Error("get collector destination failed",
			"connector", s.connectorName, "region", region, "name", name, "error", err)
		return nil, fmt.Errorf("reading destination %q of the Collector in region %s: %w", name, region, err)
	}
	return &d, nil
}

// UpsertCollectorDestination writes one destination. Its Region is the Collector it belongs to.
func (s *Store) UpsertCollectorDestination(ctx context.Context, d domaindest.Destination) error {
	// Never the config: it carries the destination's credentials.
	collectorDestinationsLog.Info("upsert collector destination",
		"connector", s.connectorName, "region", d.Region, "name", d.Name, "type", d.Type)

	signals, err := json.Marshal(nonNilSlice(d.Signals))
	if err != nil {
		return fmt.Errorf("encoding the signals of destination %q: %w", d.Name, err)
	}
	config := d.Config
	if config == nil {
		// A nil map encodes as JSON null, which the NOT NULL jsonb column rejects.
		config = map[string]any{}
	}
	configJSON, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("encoding the config of destination %q: %w", d.Name, err)
	}

	_, err = s.pool.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s.collector_destinations
			(connector_name, region, name, dest_type, config, signals)
		VALUES ($1,$2,$3,$4,$5::jsonb,$6::jsonb)
		ON CONFLICT (connector_name, region, name) DO UPDATE SET
			dest_type = EXCLUDED.dest_type,
			config = EXCLUDED.config,
			signals = EXCLUDED.signals,
			updated_at = now()
	`, s.schema), s.connectorName, d.Region, d.Name, d.Type, string(configJSON), string(signals))
	if err != nil {
		collectorDestinationsLog.Error("upsert collector destination failed",
			"connector", s.connectorName, "region", d.Region, "name", d.Name, "error", err)
		return fmt.Errorf("writing destination %q of the Collector in region %s: %w", d.Name, d.Region, err)
	}
	return nil
}

// DeleteCollectorDestination removes one destination, reporting whether there was one to remove.
func (s *Store) DeleteCollectorDestination(ctx context.Context, region, name string) (bool, error) {
	tag, err := s.pool.Exec(ctx, fmt.Sprintf(`
		DELETE FROM %s.collector_destinations
		WHERE connector_name = $1 AND region = $2 AND name = $3
	`, s.schema), s.connectorName, region, name)
	if err != nil {
		collectorDestinationsLog.Error("delete collector destination failed",
			"connector", s.connectorName, "region", region, "name", name, "error", err)
		return false, fmt.Errorf("deleting destination %q of the Collector in region %s: %w", name, region, err)
	}
	deleted := tag.RowsAffected() > 0
	collectorDestinationsLog.Info("deleted collector destination",
		"connector", s.connectorName, "region", region, "name", name, "existed", deleted)
	return deleted, nil
}

// DeleteCollectorDestinationsForRegion removes every destination of a region's Collector, for teardown.
//
// Called when a Collector is destroyed: its destinations have no meaning without it, and leaving the rows
// behind would silently re-apply them — credentials included — to a Collector created in that region
// later.
func (s *Store) DeleteCollectorDestinationsForRegion(ctx context.Context, region string) error {
	_, err := s.pool.Exec(ctx, fmt.Sprintf(`
		DELETE FROM %s.collector_destinations WHERE connector_name = $1 AND region = $2
	`, s.schema), s.connectorName, region)
	if err != nil {
		return fmt.Errorf("deleting the destinations of the Collector in region %s: %w", region, err)
	}
	return nil
}

func scanCollectorDestination(row pgx.Row) (domaindest.Destination, error) {
	var (
		d                     domaindest.Destination
		configRaw, signalsRaw []byte
	)
	if err := row.Scan(&d.Region, &d.Name, &d.Type, &configRaw, &signalsRaw); err != nil {
		return domaindest.Destination{}, err
	}
	if err := json.Unmarshal(configRaw, &d.Config); err != nil {
		return domaindest.Destination{}, fmt.Errorf("decoding the config of destination %q: %w", d.Name, err)
	}
	if err := json.Unmarshal(signalsRaw, &d.Signals); err != nil {
		return domaindest.Destination{}, fmt.Errorf("decoding the signals of destination %q: %w", d.Name, err)
	}
	return d, nil
}
