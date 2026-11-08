package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/mulesoft/pkg/types"
)

// SoftDeleteRetention is how long a soft-deleted inventory row is kept before the purge hard-deletes it.
const SoftDeleteRetention = 7 * 24 * time.Hour

// Application is a discovered Mule application.
type Application struct {
	// ExternalID is the stable source identity: the Application Manager deployment id, or
	// "<environment id>/<application id>" for a hybrid application.
	ExternalID string
	Kind       string
	// AppName is the application's name in Anypoint; the reported service name defaults to it.
	AppName         string
	EnvironmentID   string
	EnvironmentName string
	// TargetID/TargetName/TargetType/TargetRegion describe where it runs: a CloudHub 2.0 space or a Runtime
	// Fabric (TargetType is an anypoint.Target* class), or the hybrid server/group.
	TargetID     string
	TargetName   string
	TargetType   string
	TargetRegion string
	// RuntimeVersion is the Mule runtime version as reported ("4.11.0:1e-java17", or "4.11.0" for hybrid).
	RuntimeVersion string
	// Status is the platform's deployment/application status.
	Status string
	// Origin records which marker discovery found: OriginConnector (PropOdigosManaged), OriginExternal
	// (PropOdigosIaC), or "" when neither. Instrumented is derived from it.
	Origin string

	// Instrumentation state the instrumentor writes; read back by List* and never by discovery.
	Instrumented         bool
	InstrumentationError string
	InstrumentationPhase string

	// Observation is the manual-mode observer's last verdict; zero when none has run.
	Observation Observation

	// API is the API Manager instance the application is paired with through autodiscovery, or nil. Discovery
	// records it only from a successful API Manager read, so a failed read keeps the last answer.
	API *APIRef

	// GatewaySnapshot is the tracing configuration an Omni Gateway had before Odigos changed it, or nil when
	// Odigos has not changed it. Written by the instrumentor before its first write, cleared after a rollback.
	GatewaySnapshot *GatewaySnapshot

	// Metadata carries discovery-time extras (replicas, java version) merged into the metadata JSONB.
	Metadata map[string]any
}

// APIRef is the API Manager instance an application pairs with through autodiscovery.
type APIRef struct {
	ID      string `json:"id"`
	AssetID string `json:"assetId"`
	// Name is the Exchange asset's display name; Version the API version a consumer sees (productVersion).
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	Label   string `json:"label,omitempty"`
}

// GatewaySnapshot is a gateway's tracing configuration before Odigos touched it. Tracing is nil when the gateway
// had none, which a rollback restores by removing the configuration again.
type GatewaySnapshot struct {
	Tracing map[string]any `json:"tracing"`
}

// MetaAPI is the metadata key discovery records an application's API pairing under. A nil value clears it.
const MetaAPI = "api"

// Metadata keys. Instrumentation state lives in the metadata JSONB rather than columns so it can evolve without
// a migration, like the other providers' inventories.
const (
	metaInstrumented         = "instrumented"
	metaOrigin               = "origin"
	metaInstrumentationError = "instrumentationError"
	metaInstrumentationPhase = "instrumentationPhase"

	// Observation keys, written only by the manual-mode observer.
	metaConformance        = "conformance"
	metaConformanceMissing = "conformanceMissing"
	metaConformanceSince   = "conformanceSince"
	metaObservationError   = "observationError"
	metaSettling           = "settling"

	metaGatewaySnapshot = "gatewaySnapshot"
)

// Observation is the manual-mode observer's verdict on one application: does the Direct Telemetry Stream
// configuration the customer applied match what Odigos would publish for it.
type Observation struct {
	Conformance api.Conformance
	// Missing names the stale or absent parts in the base's provider-neutral component vocabulary. Non-empty
	// only for a Partial verdict.
	Missing []string
	// Since is when the current verdict was first observed; carried forward while it holds.
	Since time.Time
	// Settling is true while a deployment update is still rolling out, so a non-conformant read is not drift yet.
	Settling bool
	// Error records why the last observation could not be completed. It is not drift.
	Error string
}

// SetObservation records the observer's verdict. It touches only the observation keys: instrumentation state
// belongs to the instrumentor and discovery, and last_seen_at to discovery's prune.
func (s *Store) SetObservation(ctx context.Context, externalID string, obs Observation) error {
	missing := obs.Missing
	if missing == nil {
		missing = []string{}
	}
	patch := map[string]any{
		metaConformance:        string(obs.Conformance),
		metaConformanceMissing: missing,
		metaObservationError:   obs.Error,
		metaSettling:           obs.Settling,
		metaConformanceSince:   nil,
	}
	if !obs.Since.IsZero() {
		patch[metaConformanceSince] = obs.Since.UTC().Format(time.RFC3339)
	}
	return s.mergeMetadata(ctx, externalID, patch)
}

// UpsertApplication inserts or refreshes an inventory row and clears any soft-delete marker. Metadata is merged,
// so the instrumentor's keys (phase, error) survive a discovery refresh while discovery's overlay fresh values.
func (s *Store) UpsertApplication(ctx context.Context, app Application) error {
	meta := map[string]any{}
	for k, v := range app.Metadata {
		meta[k] = v
	}
	meta[metaOrigin] = app.Origin
	meta[metaInstrumented] = app.Origin != ""
	b, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	_, err = s.Pool().Exec(ctx, `
		INSERT INTO connector_mulesoft.applications
			(connector_name, external_id, kind, app_name, environment_id, environment_name, target_id, target_name,
			 target_type, target_region, runtime_version, status, metadata, first_seen_at, last_seen_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::jsonb, now(), now())
		ON CONFLICT (connector_name, external_id) DO UPDATE SET
			kind = EXCLUDED.kind, app_name = EXCLUDED.app_name, environment_id = EXCLUDED.environment_id,
			environment_name = EXCLUDED.environment_name, target_id = EXCLUDED.target_id,
			target_name = EXCLUDED.target_name, target_type = EXCLUDED.target_type,
			target_region = EXCLUDED.target_region, runtime_version = EXCLUDED.runtime_version,
			status = EXCLUDED.status,
			metadata = applications.metadata || EXCLUDED.metadata,
			last_seen_at = now(), deleted_at = NULL
	`, s.ConnectorName(), app.ExternalID, app.Kind, app.AppName, app.EnvironmentID, app.EnvironmentName,
		app.TargetID, app.TargetName, app.TargetType, app.TargetRegion, app.RuntimeVersion, app.Status, string(b))
	return err
}

const selectApplication = `
	SELECT external_id, kind, app_name, environment_id, environment_name, target_id, target_name, target_type,
		target_region, runtime_version, status, last_seen_at,
		COALESCE(metadata->>'origin',''),
		COALESCE(metadata->>'instrumented','false')='true',
		COALESCE(metadata->>'instrumentationError',''),
		COALESCE(metadata->>'instrumentationPhase',''),
		COALESCE(metadata->>'conformance',''),
		COALESCE(metadata->'conformanceMissing','[]'::jsonb)::text,
		COALESCE(metadata->>'conformanceSince',''),
		COALESCE(metadata->>'observationError',''),
		COALESCE(metadata->>'settling','false')='true',
		COALESCE(metadata->'api','null'::jsonb)::text,
		COALESCE(metadata->'gatewaySnapshot','null'::jsonb)::text
	FROM connector_mulesoft.applications`

func scanApplication(row interface{ Scan(...any) error }) (Application, time.Time, error) {
	var a Application
	var lastSeen time.Time
	var conformance, missing, since, apiJSON, snapshotJSON string
	err := row.Scan(&a.ExternalID, &a.Kind, &a.AppName, &a.EnvironmentID, &a.EnvironmentName, &a.TargetID,
		&a.TargetName, &a.TargetType, &a.TargetRegion, &a.RuntimeVersion, &a.Status, &lastSeen,
		&a.Origin, &a.Instrumented, &a.InstrumentationError, &a.InstrumentationPhase,
		&conformance, &missing, &since, &a.Observation.Error, &a.Observation.Settling,
		&apiJSON, &snapshotJSON)
	if err != nil {
		return a, lastSeen, err
	}
	_ = json.Unmarshal([]byte(apiJSON), &a.API)
	if a.API != nil && a.API.ID == "" {
		a.API = nil
	}
	_ = json.Unmarshal([]byte(snapshotJSON), &a.GatewaySnapshot)
	a.Observation.Conformance = api.Conformance(conformance)
	_ = json.Unmarshal([]byte(missing), &a.Observation.Missing)
	if t, perr := time.Parse(time.RFC3339, since); perr == nil {
		a.Observation.Since = t
	}
	return a, lastSeen, nil
}

// ListApplications returns the live inventory of the given kinds (all kinds when none are given).
func (s *Store) ListApplications(ctx context.Context, kinds ...string) ([]Application, error) {
	if len(kinds) == 0 {
		kinds = SupportedKinds()
	}
	rows, err := s.Pool().Query(ctx, selectApplication+`
		WHERE connector_name = $1 AND deleted_at IS NULL AND kind = ANY($2)
		ORDER BY environment_name, app_name`, s.ConnectorName(), kinds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Application
	for rows.Next() {
		a, _, err := scanApplication(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// KnownTarget is the classification discovery last recorded for a deployment.
type KnownTarget struct {
	Kind         string
	TargetName   string
	TargetType   string
	TargetRegion string
}

// KnownTargets returns the recorded kind and target of every inventory row, keyed by external id, including
// soft-deleted rows within retention: a deployment that briefly disappeared still keeps its classification.
func (s *Store) KnownTargets(ctx context.Context) (map[string]KnownTarget, error) {
	rows, err := s.Pool().Query(ctx, `
		SELECT external_id, kind, target_name, target_type, target_region
		FROM connector_mulesoft.applications WHERE connector_name = $1
	`, s.ConnectorName())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]KnownTarget{}
	for rows.Next() {
		var id string
		var k KnownTarget
		if err := rows.Scan(&id, &k.Kind, &k.TargetName, &k.TargetType, &k.TargetRegion); err != nil {
			return nil, err
		}
		out[id] = k
	}
	return out, rows.Err()
}

// ApplicationByID returns one live inventory row by its external id.
func (s *Store) ApplicationByID(ctx context.Context, externalID string) (Application, bool, error) {
	a, _, err := scanApplication(s.Pool().QueryRow(ctx, selectApplication+`
		WHERE connector_name = $1 AND external_id = $2 AND deleted_at IS NULL`, s.ConnectorName(), externalID))
	if isNoRows(err) {
		return Application{}, false, nil
	}
	if err != nil {
		return Application{}, false, err
	}
	return a, true, nil
}

func (s *Store) mergeMetadata(ctx context.Context, externalID string, patch map[string]any) error {
	b, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = s.Pool().Exec(ctx, `
		UPDATE connector_mulesoft.applications SET metadata = metadata || $3::jsonb
		WHERE connector_name = $1 AND external_id = $2
	`, s.ConnectorName(), externalID, string(b))
	return err
}

func (s *Store) deleteMetadataKey(ctx context.Context, externalID, key string) error {
	_, err := s.Pool().Exec(ctx, `
		UPDATE connector_mulesoft.applications SET metadata = metadata - $3
		WHERE connector_name = $1 AND external_id = $2
	`, s.ConnectorName(), externalID, key)
	return err
}

// SetInstrumented records whether instrumentation is applied, and by whom (origin "" when not applied).
func (s *Store) SetInstrumented(ctx context.Context, externalID, origin string) error {
	return s.mergeMetadata(ctx, externalID, map[string]any{metaInstrumented: origin != "", metaOrigin: origin})
}

// SetGatewaySnapshot records (or, with nil, clears) a gateway's pre-Odigos tracing configuration.
func (s *Store) SetGatewaySnapshot(ctx context.Context, externalID string, snap *GatewaySnapshot) error {
	return s.mergeMetadata(ctx, externalID, map[string]any{metaGatewaySnapshot: snap})
}

// SetInstrumentationError records the last instrumentation or rollback error for an application.
func (s *Store) SetInstrumentationError(ctx context.Context, externalID, msg string) error {
	return s.mergeMetadata(ctx, externalID, map[string]any{metaInstrumentationError: msg})
}

// ClearInstrumentationError clears the recorded instrumentation error.
func (s *Store) ClearInstrumentationError(ctx context.Context, externalID string) error {
	return s.deleteMetadataKey(ctx, externalID, metaInstrumentationError)
}

// SetInstrumentationPhase records the in-flight instrumentation phase.
func (s *Store) SetInstrumentationPhase(ctx context.Context, externalID, phase string) error {
	return s.mergeMetadata(ctx, externalID, map[string]any{metaInstrumentationPhase: phase})
}

// ClearInstrumentationPhase clears the in-flight phase once an application has converged.
func (s *Store) ClearInstrumentationPhase(ctx context.Context, externalID string) error {
	return s.deleteMetadataKey(ctx, externalID, metaInstrumentationPhase)
}

// PruneScope is what one discovery scan can vouch for. A row missing from the scan is only gone when the scan
// read its environment in full; anywhere else it may merely have been unreadable this cycle, and pruning it
// would read as an undeployed application, which the reconciler rolls back.
type PruneScope struct {
	// Complete lists the environments whose applications were all read.
	Complete []string
	// Listed is every environment a COMPLETE environment list returned, or nil when the list was truncated.
	// Rows in an environment outside it belong to an environment that no longer exists or is no longer visible.
	Listed []string
}

// Empty reports whether the scope allows pruning anything.
func (p PruneScope) Empty() bool { return len(p.Complete) == 0 && p.Listed == nil }

// Prune soft-deletes rows of one kind, within the scope, not seen since the scan start. Scoped by kind because
// each kind has its own detector, and one kind's scan says nothing about another's rows.
func (s *Store) Prune(ctx context.Context, kind string, before time.Time, scope PruneScope) error {
	if scope.Empty() {
		return nil
	}
	complete := scope.Complete
	if complete == nil {
		complete = []string{}
	}
	listed := scope.Listed
	if listed == nil {
		listed = []string{}
	}
	_, err := s.Pool().Exec(ctx, `
		UPDATE connector_mulesoft.applications SET deleted_at = now()
		WHERE connector_name = $1 AND kind = $2 AND last_seen_at < $3 AND deleted_at IS NULL
		  AND (environment_id = ANY($4::text[]) OR ($5 AND NOT environment_id = ANY($6::text[])))
	`, s.ConnectorName(), kind, before, complete, scope.Listed != nil, listed)
	return err
}

// PurgeDeleted hard-deletes rows soft-deleted before the cutoff.
func (s *Store) PurgeDeleted(ctx context.Context, before time.Time) error {
	tag, err := s.Pool().Exec(ctx, `
		DELETE FROM connector_mulesoft.applications
		WHERE connector_name = $1 AND deleted_at IS NOT NULL AND deleted_at < $2
	`, s.ConnectorName(), before)
	if err != nil {
		return err
	}
	if n := tag.RowsAffected(); n > 0 {
		inventoryLog.Info("purged soft-deleted mule applications", "rows", n)
	}
	return nil
}

// UpdateDiscoveryState records the last full-scan time for an environment/kind cursor.
func (s *Store) UpdateDiscoveryState(ctx context.Context, environmentID, kind string, scanAt time.Time) error {
	_, err := s.Pool().Exec(ctx, `
		INSERT INTO connector_mulesoft.discovery_state (connector_name, location, kind, cursor, last_full_scan_at)
		VALUES ($1,$2,$3,'{}'::jsonb,$4)
		ON CONFLICT (connector_name, location, kind) DO UPDATE SET last_full_scan_at = EXCLUDED.last_full_scan_at
	`, s.ConnectorName(), environmentID, kind, scanAt)
	return err
}

// OwnerOrganization returns the Anypoint organization this connector name currently owns, if recorded.
func (s *Store) OwnerOrganization(ctx context.Context) (string, bool, error) {
	var org string
	err := s.Pool().QueryRow(ctx,
		`SELECT organization_id FROM connector_mulesoft.connector_owner WHERE connector_name = $1`,
		s.ConnectorName()).Scan(&org)
	if isNoRows(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return org, true, nil
}

// SetOwnerOrganization records the Anypoint organization this connector name currently owns.
func (s *Store) SetOwnerOrganization(ctx context.Context, orgID string) error {
	_, err := s.Pool().Exec(ctx, `
		INSERT INTO connector_mulesoft.connector_owner (connector_name, organization_id, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (connector_name) DO UPDATE SET organization_id = EXCLUDED.organization_id, updated_at = now()
	`, s.ConnectorName(), orgID)
	return err
}

// PurgeConnectorInventory hard-deletes all inventory and discovery cursors for this connector, used when the
// connector name is rebound to a different organization.
func (s *Store) PurgeConnectorInventory(ctx context.Context) error {
	for _, table := range []string{"applications", "discovery_state"} {
		if _, err := s.Pool().Exec(ctx, "DELETE FROM connector_mulesoft."+table+" WHERE connector_name = $1", s.ConnectorName()); err != nil {
			return fmt.Errorf("purge %s: %w", table, err)
		}
	}
	return nil
}

// ToDiscoveredWorkload maps an inventory row to the report the base enriches with source intent.
//
// Region carries the ENVIRONMENT name: an app name is unique only within an environment, and the same app in
// Sandbox and Production is the normal case, so the report key needs the environment to tell them apart.
// Details stay nil until agents-api carries a Mule details type; the target and its cloud region are in the
// inventory for when it does.
func ToDiscoveredWorkload(a Application, lastSeen time.Time) api.DiscoveredWorkload {
	w := api.DiscoveredWorkload{
		ServiceName:           a.AppName,
		Kind:                  a.Kind,
		Region:                a.EnvironmentName,
		ExternalID:            a.ExternalID,
		RuntimeLanguage:       types.Language,
		RuntimeVersion:        MuleVersion(a.RuntimeVersion),
		Instrumented:          a.Instrumented,
		InstrumentationOrigin: a.Origin,
		InstrumentationError:  a.InstrumentationError,
		InstrumentationPhase:  a.InstrumentationPhase,
		Conformance:           a.Observation.Conformance,
		MissingComponents:     a.Observation.Missing,
		ConformanceSince:      a.Observation.Since,
		Settling:              a.Observation.Settling,
		ObservationError:      a.Observation.Error,
		LastSeenAt:            lastSeen,
	}
	if a.Kind != KindHybrid {
		w.InstrumentationType = a.Kind
	}
	if a.Kind == KindOmniGateway {
		// A gateway is not a Mule application: it reports its own version and no language.
		w.RuntimeLanguage = ""
		w.RuntimeVersion = a.RuntimeVersion
	}
	return w
}

// ListWorkloads returns the discovered inventory as DiscoveredWorkloads.
func (s *Store) ListWorkloads(ctx context.Context) ([]api.DiscoveredWorkload, error) {
	rows, err := s.Pool().Query(ctx, selectApplication+`
		WHERE connector_name = $1 AND deleted_at IS NULL
		ORDER BY environment_name, app_name`, s.ConnectorName())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []api.DiscoveredWorkload
	for rows.Next() {
		a, lastSeen, err := scanApplication(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ToDiscoveredWorkload(a, lastSeen))
	}
	return out, rows.Err()
}

// LookupDiscoveredWorkload finds the row a prospective source names. region is the environment name. Empty
// matches by name and kind alone, but only when that is one application: the same application deployed to
// Sandbox and Production is the normal case on Anypoint, and silently picking one would instrument — and
// redeploy — an environment nobody named.
func (s *Store) LookupDiscoveredWorkload(ctx context.Context, serviceName, kind, region string) (externalID, foundRegion string, found bool, err error) {
	switch kind {
	case KindCloudHub2, KindPrivateSpace, KindRTF, KindHybrid, KindOmniGateway:
	default:
		return "", "", false, fmt.Errorf("unsupported workload kind %q", kind)
	}
	rows, err := s.Pool().Query(ctx, `
		SELECT external_id, environment_name FROM connector_mulesoft.applications
		WHERE connector_name = $1 AND app_name = $2 AND kind = $3 AND deleted_at IS NULL
		  AND ($4 = '' OR environment_name = $4)
		ORDER BY environment_name LIMIT 3
	`, s.ConnectorName(), serviceName, kind, region)
	if err != nil {
		return "", "", false, err
	}
	defer rows.Close()
	var ids, envs []string
	for rows.Next() {
		var id, env string
		if err := rows.Scan(&id, &env); err != nil {
			return "", "", false, err
		}
		ids, envs = append(ids, id), append(envs, env)
	}
	if err := rows.Err(); err != nil {
		return "", "", false, err
	}
	switch {
	case len(ids) == 0:
		return "", "", false, nil
	case len(ids) > 1:
		return "", "", false, fmt.Errorf("%q is deployed in more than one environment (%s); set the source's region to the environment name", serviceName, strings.Join(envs, ", "))
	}
	return ids[0], envs[0], true, nil
}
