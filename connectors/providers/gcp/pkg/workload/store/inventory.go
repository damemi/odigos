package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/gcp/pkg/types"
)

// Workload kinds — directly-instrumentable resources. Kind strings match the spec type names so the
// coordinator resolves a workload's instrumentation availability from the spec.
const (
	KindCloudRun = types.CloudRun
)

// SoftDeleteRetention is how long a soft-deleted inventory row is retained before the purge hard-deletes
// it. It gives operators/UI a window to see recently-removed resources while bounding table growth.
const SoftDeleteRetention = 7 * 24 * time.Hour

// SupportedKinds returns the workload kinds this connector publishes.
func SupportedKinds() []string {
	return []string{KindCloudRun}
}

// CloudRunService is a discovered Cloud Run service inventory record.
type CloudRunService struct {
	// Location is the GCP region the service runs in.
	Location string
	// ResourceName is the full GCP resource name (projects/{p}/locations/{loc}/services/{svc}); the
	// stable source identity (external id).
	ResourceName string
	// ServiceID is the short, human-facing service name (the last resource-name segment).
	ServiceID string
	// UID is the service's server-assigned unique id.
	UID string
	// Ingress is the service's ingress setting (e.g. "INGRESS_TRAFFIC_ALL").
	Ingress string
	// Instrumented is the live instrumentation state derived at discovery time.
	Instrumented bool
	// RuntimeLanguage is the resolved workload language (e.g. "java", "python", "nodejs", "dotnet");
	// empty when unknown. Written by the instrumentor once it resolves the language, and used thereafter
	// to select the agent to inject.
	RuntimeLanguage string
	// RuntimeVersion is the detected runtime version, when known (currently informational).
	RuntimeVersion string
	// InstrumentationError is the last instrumentation/rollback error for this service; empty when healthy.
	InstrumentationError string
	// InstrumentationPhase is the last in-flight instrumentation progress phase; empty when converged.
	InstrumentationPhase string
	Metadata             map[string]any
}

// Metadata keys the instrumentor persists into the cloud_run_services.metadata JSONB (alongside the
// discovery-time keys). Kept as a dedicated JSONB rather than columns so instrumentation state evolves
// without a migration (mirrors the AWS Lambda inventory).
const (
	metaInstrumented         = "instrumented"
	metaRuntimeLanguage      = "runtimeLanguage"
	metaRuntimeVersion       = "runtimeVersion"
	metaInstrumentationError = "instrumentationError"
	metaInstrumentationPhase = "instrumentationPhase"
)

func metaJSON(m map[string]any) (string, error) {
	if m == nil {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// UpsertCloudRunService inserts or refreshes a Cloud Run inventory row, clearing any soft-delete marker.
func (s *Store) UpsertCloudRunService(ctx context.Context, svc CloudRunService) error {
	inventoryLog.Debug("upsert cloud run service", "location", svc.Location, "service", svc.ServiceID)
	if svc.Metadata == nil {
		svc.Metadata = map[string]any{}
	}
	svc.Metadata["instrumented"] = svc.Instrumented
	meta, err := metaJSON(svc.Metadata)
	if err != nil {
		return err
	}
	_, err = s.Pool().Exec(ctx, `
		INSERT INTO connector_gcp.cloud_run_services
			(connector_name, location, resource_name, service_id, uid, ingress, metadata, first_seen_at, last_seen_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb, now(), now())
		ON CONFLICT (connector_name, resource_name) DO UPDATE SET
			location = EXCLUDED.location, service_id = EXCLUDED.service_id, uid = EXCLUDED.uid,
			ingress = EXCLUDED.ingress,
			-- Merge (not replace) so instrumentation state written by the instrumentor
			-- (runtimeLanguage/instrumentationError/instrumentationPhase) survives a discovery refresh; the
			-- discovery-owned keys (uri/latestRevision/instrumented) still overlay with their fresh values.
			metadata = cloud_run_services.metadata || EXCLUDED.metadata,
			last_seen_at = now(), deleted_at = NULL
	`, s.ConnectorName(), svc.Location, svc.ResourceName, svc.ServiceID, svc.UID, svc.Ingress, meta)
	return err
}

// ListCloudRunServices returns the full live Cloud Run inventory for the instrumentor, with the
// instrumentation state parsed out of the metadata JSONB.
func (s *Store) ListCloudRunServices(ctx context.Context) ([]CloudRunService, error) {
	inventoryLog.Debug("list cloud run services", "connector", s.ConnectorName())
	rows, err := s.Pool().Query(ctx, `
		SELECT location, resource_name, service_id, uid, ingress,
			COALESCE(metadata->>'instrumented','false')='true',
			COALESCE(metadata->>'runtimeLanguage',''),
			COALESCE(metadata->>'runtimeVersion',''),
			COALESCE(metadata->>'instrumentationError',''),
			COALESCE(metadata->>'instrumentationPhase','')
		FROM connector_gcp.cloud_run_services WHERE connector_name = $1 AND deleted_at IS NULL
	`, s.ConnectorName())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CloudRunService
	for rows.Next() {
		var svc CloudRunService
		if err := rows.Scan(&svc.Location, &svc.ResourceName, &svc.ServiceID, &svc.UID, &svc.Ingress,
			&svc.Instrumented, &svc.RuntimeLanguage, &svc.RuntimeVersion, &svc.InstrumentationError, &svc.InstrumentationPhase); err != nil {
			return nil, err
		}
		out = append(out, svc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	inventoryLog.Debug("list cloud run services complete", "connector", s.ConnectorName(), "count", len(out))
	return out, nil
}

// mergeMetadata overlays patch onto a service row's metadata JSONB (right wins), leaving other keys
// intact. Used by the instrumentor to record instrumentation state without a read-modify-write.
func (s *Store) mergeMetadata(ctx context.Context, resourceName string, patch map[string]any) error {
	b, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = s.Pool().Exec(ctx, `
		UPDATE connector_gcp.cloud_run_services SET metadata = metadata || $3::jsonb
		WHERE connector_name = $1 AND resource_name = $2
	`, s.ConnectorName(), resourceName, string(b))
	return err
}

// deleteMetadataKey removes a single key from a service row's metadata JSONB (idempotent).
func (s *Store) deleteMetadataKey(ctx context.Context, resourceName, key string) error {
	_, err := s.Pool().Exec(ctx, `
		UPDATE connector_gcp.cloud_run_services SET metadata = metadata - $3
		WHERE connector_name = $1 AND resource_name = $2
	`, s.ConnectorName(), resourceName, key)
	return err
}

// SetCloudRunInstrumented records whether Odigos instrumentation is currently applied to a service.
func (s *Store) SetCloudRunInstrumented(ctx context.Context, resourceName string, instrumented bool) error {
	return s.mergeMetadata(ctx, resourceName, map[string]any{metaInstrumented: instrumented})
}

// SetCloudRunRuntime records the detected/overridden runtime language (and version, when known).
func (s *Store) SetCloudRunRuntime(ctx context.Context, resourceName, language, version string) error {
	return s.mergeMetadata(ctx, resourceName, map[string]any{metaRuntimeLanguage: language, metaRuntimeVersion: version})
}

// SetCloudRunInstrumentationError records the last instrumentation/rollback error for a service.
func (s *Store) SetCloudRunInstrumentationError(ctx context.Context, resourceName, msg string) error {
	return s.mergeMetadata(ctx, resourceName, map[string]any{metaInstrumentationError: msg})
}

// ClearCloudRunInstrumentationError clears the recorded instrumentation error for a service.
func (s *Store) ClearCloudRunInstrumentationError(ctx context.Context, resourceName string) error {
	return s.deleteMetadataKey(ctx, resourceName, metaInstrumentationError)
}

// SetCloudRunInstrumentationPhase records the in-flight instrumentation progress phase for a service.
func (s *Store) SetCloudRunInstrumentationPhase(ctx context.Context, resourceName, phase string) error {
	return s.mergeMetadata(ctx, resourceName, map[string]any{metaInstrumentationPhase: phase})
}

// ClearCloudRunInstrumentationPhase clears the in-flight phase once a service has converged.
func (s *Store) ClearCloudRunInstrumentationPhase(ctx context.Context, resourceName string) error {
	return s.deleteMetadataKey(ctx, resourceName, metaInstrumentationPhase)
}

// Prune soft-deletes Cloud Run rows not seen since the scan start. GCP scans every location in one call,
// so this prunes across all locations for the connector (no per-region scoping like AWS).
func (s *Store) Prune(ctx context.Context, before time.Time) error {
	inventoryLog.Debug("prune cloud run inventory", "connector", s.ConnectorName())
	_, err := s.Pool().Exec(ctx,
		"UPDATE connector_gcp.cloud_run_services SET deleted_at = now() WHERE connector_name = $1 AND last_seen_at < $2 AND deleted_at IS NULL",
		s.ConnectorName(), before)
	return err
}

// PurgeDeleted hard-deletes Cloud Run rows soft-deleted before the cutoff, reclaiming space.
func (s *Store) PurgeDeleted(ctx context.Context, before time.Time) error {
	tag, err := s.Pool().Exec(ctx,
		"DELETE FROM connector_gcp.cloud_run_services WHERE connector_name = $1 AND deleted_at IS NOT NULL AND deleted_at < $2",
		s.ConnectorName(), before)
	if err != nil {
		inventoryLog.Error("purge soft-deleted cloud run inventory failed", "error", err)
		return err
	}
	if n := tag.RowsAffected(); n > 0 {
		inventoryLog.Info("purged soft-deleted cloud run inventory", "rows", n)
	}
	return nil
}

// UpdateDiscoveryState records the last full-scan time for a location/kind cursor.
func (s *Store) UpdateDiscoveryState(ctx context.Context, location, kind string, scanAt time.Time) error {
	_, err := s.Pool().Exec(ctx, `
		INSERT INTO connector_gcp.discovery_state (connector_name, location, kind, cursor, last_full_scan_at)
		VALUES ($1,$2,$3,'{}'::jsonb,$4)
		ON CONFLICT (connector_name, location, kind) DO UPDATE SET last_full_scan_at = EXCLUDED.last_full_scan_at
	`, s.ConnectorName(), location, kind, scanAt)
	return err
}

// OwnerProject returns the GCP project this connector name currently owns, and whether one is recorded.
func (s *Store) OwnerProject(ctx context.Context) (string, bool, error) {
	var project string
	err := s.Pool().QueryRow(ctx,
		`SELECT project_id FROM connector_gcp.connector_owner WHERE connector_name = $1`,
		s.ConnectorName()).Scan(&project)
	if isNoRows(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return project, true, nil
}

// SetOwnerProject records the GCP project this connector name currently owns.
func (s *Store) SetOwnerProject(ctx context.Context, projectID string) error {
	_, err := s.Pool().Exec(ctx, `
		INSERT INTO connector_gcp.connector_owner (connector_name, project_id, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (connector_name) DO UPDATE SET project_id = EXCLUDED.project_id, updated_at = now()
	`, s.ConnectorName(), projectID)
	return err
}

// PurgeConnectorInventory hard-deletes all workload inventory + discovery cursors for this connector,
// used when the connector name is rebound to a different GCP project. Sources/destinations are purged
// separately via the base PurgeIntent.
func (s *Store) PurgeConnectorInventory(ctx context.Context) error {
	for _, table := range []string{"cloud_run_services", "discovery_state"} {
		if _, err := s.Pool().Exec(ctx, "DELETE FROM connector_gcp."+table+" WHERE connector_name = $1", s.ConnectorName()); err != nil {
			return fmt.Errorf("purge %s: %w", table, err)
		}
	}
	return nil
}

// ListWorkloads returns the discovered WORKLOAD inventory (Cloud Run services) as raw DiscoveredWorkload
// objects; the base coordinator enriches them with source intent.
func (s *Store) ListWorkloads(ctx context.Context) ([]api.DiscoveredWorkload, error) {
	inventoryLog.Debug("list workloads", "connector", s.ConnectorName())
	rows, err := s.Pool().Query(ctx, fmt.Sprintf(`
		SELECT service_id, location, resource_name, last_seen_at,
			COALESCE(metadata->>'instrumented','false')='true', '%s',
			COALESCE(metadata->>'runtimeLanguage',''),
			COALESCE(metadata->>'runtimeVersion',''),
			COALESCE(metadata->>'instrumentationError',''),
			COALESCE(metadata->>'instrumentationPhase','')
		FROM connector_gcp.cloud_run_services WHERE connector_name = $1 AND deleted_at IS NULL
	`, KindCloudRun), s.ConnectorName())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []api.DiscoveredWorkload
	for rows.Next() {
		var name, location, externalID, instrType, runtimeLang, runtimeVer, instrErr, instrPhase string
		var lastSeen time.Time
		var instrumented bool
		if err := rows.Scan(&name, &location, &externalID, &lastSeen, &instrumented, &instrType,
			&runtimeLang, &runtimeVer, &instrErr, &instrPhase); err != nil {
			return nil, err
		}
		out = append(out, api.DiscoveredWorkload{
			ServiceName:          name,
			Kind:                 KindCloudRun,
			Region:               location,
			ExternalID:           externalID,
			RuntimeLanguage:      runtimeLang,
			RuntimeVersion:       runtimeVer,
			Instrumented:         instrumented,
			InstrumentationError: instrErr,
			InstrumentationPhase: instrPhase,
			InstrumentationType:  instrType,
			LastSeenAt:           lastSeen,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	inventoryLog.Debug("list workloads complete", "connector", s.ConnectorName(), "count", len(out))
	return out, nil
}

// LookupDiscoveredWorkload finds a workload inventory row matching a prospective source name/kind/region.
// region pins the exact regional row when non-empty; empty matches by name+kind alone (deterministic).
func (s *Store) LookupDiscoveredWorkload(ctx context.Context, serviceName, kind, region string) (externalID, foundRegion string, found bool, err error) {
	inventoryLog.Debug("lookup workload", "connector", s.ConnectorName(), "service", serviceName, "kind", kind, "region", region)
	switch kind {
	case KindCloudRun:
		err = s.Pool().QueryRow(ctx, `
			SELECT resource_name, location FROM connector_gcp.cloud_run_services
			WHERE connector_name = $1 AND service_id = $2 AND deleted_at IS NULL
			  AND ($3 = '' OR location = $3)
			ORDER BY location LIMIT 1
		`, s.ConnectorName(), serviceName, region).Scan(&externalID, &foundRegion)
	default:
		return "", "", false, fmt.Errorf("unsupported workload kind %q", kind)
	}
	if err != nil {
		if isNoRows(err) {
			return "", "", false, nil
		}
		return "", "", false, err
	}
	return externalID, foundRegion, true, nil
}
