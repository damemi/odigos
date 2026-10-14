package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
)

var destinationsLog = connlog.Subsystem("store-destinations")

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// DestinationRecord is a persisted destination row.
type DestinationRecord struct {
	Name      string
	Type      string
	Region    string
	Config    map[string]any
	Signals   []string
	Disabled  bool
	UpdatedAt time.Time
}

func (s *Store) ListDestinations(ctx context.Context) ([]DestinationRecord, error) {
	destinationsLog.Debug("list destinations", "connector", s.connectorName, "schema", s.schema)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT name, dest_type, COALESCE(region, ''), config, signals, disabled, updated_at
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
		if err := rows.Scan(&rec.Name, &rec.Type, &rec.Region, &configJSON, &signalsJSON, &rec.Disabled, &rec.UpdatedAt); err != nil {
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
		SELECT name, dest_type, COALESCE(region, ''), config, signals, disabled, updated_at
		FROM %s.destinations
		WHERE connector_name = $1 AND name = $2
	`, s.schema), s.connectorName, name).Scan(&rec.Name, &rec.Type, &rec.Region, &configJSON, &signalsJSON, &rec.Disabled, &rec.UpdatedAt)
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
		"region", dest.Region,
		"disabled", dest.Disabled,
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
			(connector_name, name, dest_type, region, config, signals, disabled, created_at, updated_at)
		VALUES ($1,$2,$3,NULLIF($4,''),$5::jsonb,$6::jsonb,$7, now(), now())
		ON CONFLICT (connector_name, name) DO UPDATE SET
			dest_type = EXCLUDED.dest_type,
			region = EXCLUDED.region,
			config = EXCLUDED.config,
			signals = EXCLUDED.signals,
			disabled = EXCLUDED.disabled,
			updated_at = now()
	`, s.schema), s.connectorName, dest.Name, dest.Type, dest.Region, string(configJSON), string(signalsJSON), dest.Disabled)
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
//
// Equivalent to OTLPExportTargetForNetwork with no network, and implemented that way rather than
// separately: a caller that does not know its workload's network must get the same answer the
// network-aware path gives for a workload that has none.
func (s *Store) OTLPExportTarget(ctx context.Context) (domaindest.OTLPTarget, bool, error) {
	routing, err := s.OTLPRouting(ctx, "")
	return routing.Target, routing.Found, err
}

// OTLPExportTargetForNetwork resolves where a workload on `network` exports: the Collector serving that
// network when one is configured, otherwise the destination directly. Pass an empty network for a
// workload that has none.
func (s *Store) OTLPExportTargetForNetwork(ctx context.Context, network string) (domaindest.OTLPTarget, bool, error) {
	routing, err := s.OTLPRouting(ctx, network)
	return routing.Target, routing.Found, err
}

// OTLPRouting is the single resolution every plan, instrumentor and observer reads through.
//
// One function rather than one per consumer, because the endpoint Odigos APPLIES and the endpoint Odigos
// COMPARES AGAINST have to be the same value: a report that resolved differently from the instrumentor
// would call every correctly-routed workload drifted.
//
// It also carries the headers, so the endpoint and the credential are decided together — a workload
// pointed at a Collector while still holding the destination's credential would hand that credential to
// every instrumented workload in the account.
func (s *Store) OTLPRouting(ctx context.Context, network string) (domaindest.Routing, error) {
	dests, err := s.destinationsForResolution(ctx)
	if err != nil {
		return domaindest.Routing{}, err
	}
	// A caller without region context cannot safely select a regional Collector. It also must not read
	// managed_collectors: providers that do not implement this feature have no such provider table.
	return domaindest.ResolveRouting(dests, nil, network), nil
}

// OTLPRoutingForRegion resolves the route for a regional workload. The managed-Collector destination
// marker is applicable only to its own region; global OTLP terminals remain available everywhere. Every
// workload requires explicit provider reachability evidence, including ingress admission in the host
// network. A different workload network additionally requires its provider's validated cross-network path
// (for AWS, direct VPC peering and reciprocal routes).
func (s *Store) OTLPRoutingForRegion(ctx context.Context, region, network string, reachability domaindest.Reachability) (domaindest.Routing, error) {
	dests, err := s.destinationsForResolution(ctx)
	if err != nil {
		return domaindest.Routing{}, err
	}
	collectors, err := s.CollectorTargets(ctx)
	if err != nil {
		return domaindest.Routing{}, err
	}
	return domaindest.ResolveRoutingForRegion(dests, collectors, region, network, reachability), nil
}

// CollectorTargets returns one routing entry per regional managed Collector. Network is the host VPC,
// not the Collector identity and not an assertion that other VPCs can reach it.
//
// It returns configuration only, never health. Selection must not change on whether a Collector is
// currently answering, and domaindest.CollectorTarget has nowhere to put it.
func (s *Store) CollectorTargets(ctx context.Context) ([]domaindest.CollectorTarget, error) {
	records, err := s.ListManagedCollectors(ctx)
	if err != nil {
		return nil, err
	}
	return collectorTargets(records), nil
}

func collectorTargets(records []CollectorRecord) []domaindest.CollectorTarget {
	targets := make([]domaindest.CollectorTarget, 0, len(records))
	for _, rec := range records {
		targets = append(targets, domaindest.CollectorTarget{
			Region:       rec.Key.Region,
			Network:      rec.NetworkID,
			Endpoint:     rec.EndpointGRPC,
			HTTPEndpoint: rec.EndpointHTTP,
			Insecure:     rec.TLSMode == "off",
			// FirstReadyAt is a one-way latch. A transient status regression must not
			// make already-cut-over workloads fall back to the direct destination.
			EverReady: rec.FirstReadyAt != nil,
		})
	}
	return targets
}

func (s *Store) destinationsForResolution(ctx context.Context) ([]domaindest.Destination, error) {
	recs, err := s.ListDestinations(ctx)
	if err != nil {
		return nil, err
	}
	dests := make([]domaindest.Destination, 0, len(recs))
	for _, rec := range recs {
		dests = append(dests, DestinationRecordToDomain(rec))
	}
	return dests, nil
}

// OTLPExportHeaders returns the per-request headers the connector's OTLP destination requires — its
// credential, in practice — or nil when it needs none.
//
// Separate from OTLPExportTarget rather than folded into it because OTLPTarget mirrors the destination
// definitions generated upstream in odigos, and this connector must not fork that shape. The headers come
// off the raw Destination.Config the generated model already carries through, converted in code by
// domaindest.ResolveOTLPHeaders, which selects the same destination OTLPExportTarget resolves.
func (s *Store) OTLPExportHeaders(ctx context.Context) (map[string]string, error) {
	return s.OTLPExportHeadersForNetwork(ctx, "")
}

// OTLPExportHeadersForNetwork returns the headers a workload on `network` must send: the destination's
// credential when it exports directly, and none when it routes through a Collector, which holds that
// credential itself.
//
// Resolved through the same call as the endpoint so the two can never disagree.
func (s *Store) OTLPExportHeadersForNetwork(ctx context.Context, network string) (map[string]string, error) {
	routing, err := s.OTLPRouting(ctx, network)
	return routing.Headers, err
}

func DestinationRecordToDomain(rec DestinationRecord) domaindest.Destination {
	cfg := rec.Config
	if cfg == nil {
		cfg = map[string]any{}
	}
	return domaindest.Destination{
		Name:     rec.Name,
		Type:     rec.Type,
		Region:   rec.Region,
		Config:   cfg,
		Signals:  rec.Signals,
		Disabled: rec.Disabled,
	}
}
