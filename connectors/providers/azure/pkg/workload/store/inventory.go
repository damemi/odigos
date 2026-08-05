package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/azure/pkg/types"
)

// Workload kinds — directly-instrumentable resources. Kind strings match the spec type names so the
// coordinator resolves a workload's instrumentation availability from the spec.
const (
	KindContainerApps = types.ContainerApps
)

// SoftDeleteRetention is how long a soft-deleted inventory row is retained before the purge hard-deletes
// it. It gives operators/UI a window to see recently-removed resources while bounding table growth.
const SoftDeleteRetention = 7 * 24 * time.Hour

const (
	metaInstrumented         = "instrumented"
	metaRuntimeLanguage      = "runtimeLanguage"
	metaRuntimeVersion       = "runtimeVersion"
	metaInstrumentationError = "instrumentationError"
	metaInstrumentationPhase = "instrumentationPhase"
)

// SupportedKinds returns the workload kinds this connector publishes.
func SupportedKinds() []string {
	return []string{KindContainerApps}
}

// ContainerApp is a discovered Azure Container App inventory record.
type ContainerApp struct {
	// Location is the Azure region the app runs in.
	Location string
	// ResourceGroup is the resource group that owns the app.
	ResourceGroup string
	// ResourceID is the full ARM resource ID; the stable source identity (external id).
	ResourceID string
	// AppName is the short, human-facing app name.
	AppName string
	// ProvisioningState is the app's provisioning state when reported by ARM.
	ProvisioningState string
	// Instrumented is the live instrumentation state derived at discovery time.
	Instrumented bool
	// RuntimeLanguage is the resolved workload language; empty when unknown.
	RuntimeLanguage string
	// RuntimeVersion is the detected runtime version, when known.
	RuntimeVersion string
	// InstrumentationError is the last instrumentation/rollback error; empty when healthy.
	InstrumentationError string
	// InstrumentationPhase is the last in-flight instrumentation progress phase.
	InstrumentationPhase string
	Metadata             map[string]any
}

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

// UpsertContainerApp inserts or refreshes a Container App inventory row, clearing any soft-delete marker.
func (s *Store) UpsertContainerApp(ctx context.Context, app ContainerApp) error {
	inventoryLog.Debug("upsert container app", "location", app.Location, "app", app.AppName)
	if app.Metadata == nil {
		app.Metadata = map[string]any{}
	}
	app.Metadata["instrumented"] = app.Instrumented
	meta, err := metaJSON(app.Metadata)
	if err != nil {
		return err
	}
	_, err = s.Pool().Exec(ctx, `
		INSERT INTO connector_azure.container_apps
			(connector_name, location, resource_group, resource_id, app_name, provisioning_state, metadata, first_seen_at, last_seen_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb, now(), now())
		ON CONFLICT (connector_name, resource_id) DO UPDATE SET
			location = EXCLUDED.location, resource_group = EXCLUDED.resource_group, app_name = EXCLUDED.app_name,
			provisioning_state = EXCLUDED.provisioning_state,
			-- Merge (not replace) so instrumentation state written by the instrumentor survives a discovery refresh.
			metadata = container_apps.metadata || EXCLUDED.metadata,
			last_seen_at = now(), deleted_at = NULL
	`, s.ConnectorName(), app.Location, app.ResourceGroup, app.ResourceID, app.AppName, app.ProvisioningState, meta)
	return err
}

// ListContainerApps returns the live Container Apps inventory for the instrumentor.
func (s *Store) ListContainerApps(ctx context.Context) ([]ContainerApp, error) {
	inventoryLog.Debug("list container apps", "connector", s.ConnectorName())
	rows, err := s.Pool().Query(ctx, `
		SELECT location, resource_group, resource_id, app_name, provisioning_state,
			COALESCE(metadata->>'instrumented','false')='true',
			COALESCE(metadata->>'runtimeLanguage',''),
			COALESCE(metadata->>'runtimeVersion',''),
			COALESCE(metadata->>'instrumentationError',''),
			COALESCE(metadata->>'instrumentationPhase','')
		FROM connector_azure.container_apps WHERE connector_name = $1 AND deleted_at IS NULL
	`, s.ConnectorName())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ContainerApp
	for rows.Next() {
		var app ContainerApp
		if err := rows.Scan(&app.Location, &app.ResourceGroup, &app.ResourceID, &app.AppName, &app.ProvisioningState,
			&app.Instrumented, &app.RuntimeLanguage, &app.RuntimeVersion, &app.InstrumentationError, &app.InstrumentationPhase); err != nil {
			return nil, err
		}
		out = append(out, app)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	inventoryLog.Debug("list container apps complete", "connector", s.ConnectorName(), "count", len(out))
	return out, nil
}

func (s *Store) mergeMetadata(ctx context.Context, resourceID string, patch map[string]any) error {
	b, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = s.Pool().Exec(ctx, `
		UPDATE connector_azure.container_apps SET metadata = metadata || $3::jsonb
		WHERE connector_name = $1 AND resource_id = $2
	`, s.ConnectorName(), resourceID, string(b))
	return err
}

func (s *Store) deleteMetadataKey(ctx context.Context, resourceID, key string) error {
	_, err := s.Pool().Exec(ctx, `
		UPDATE connector_azure.container_apps SET metadata = metadata - $3
		WHERE connector_name = $1 AND resource_id = $2
	`, s.ConnectorName(), resourceID, key)
	return err
}

func (s *Store) SetContainerAppInstrumented(ctx context.Context, resourceID string, instrumented bool) error {
	return s.mergeMetadata(ctx, resourceID, map[string]any{metaInstrumented: instrumented})
}

func (s *Store) SetContainerAppRuntime(ctx context.Context, resourceID, language, version string) error {
	return s.mergeMetadata(ctx, resourceID, map[string]any{metaRuntimeLanguage: language, metaRuntimeVersion: version})
}

func (s *Store) SetContainerAppInstrumentationError(ctx context.Context, resourceID, msg string) error {
	return s.mergeMetadata(ctx, resourceID, map[string]any{metaInstrumentationError: msg})
}

func (s *Store) ClearContainerAppInstrumentationError(ctx context.Context, resourceID string) error {
	return s.deleteMetadataKey(ctx, resourceID, metaInstrumentationError)
}

func (s *Store) SetContainerAppInstrumentationPhase(ctx context.Context, resourceID, phase string) error {
	return s.mergeMetadata(ctx, resourceID, map[string]any{metaInstrumentationPhase: phase})
}

func (s *Store) ClearContainerAppInstrumentationPhase(ctx context.Context, resourceID string) error {
	return s.deleteMetadataKey(ctx, resourceID, metaInstrumentationPhase)
}

// Prune soft-deletes Container App rows not seen since the scan start. Azure scans the whole
// subscription in one call, so this prunes across all locations for the connector.
func (s *Store) Prune(ctx context.Context, before time.Time) error {
	inventoryLog.Debug("prune container apps inventory", "connector", s.ConnectorName())
	_, err := s.Pool().Exec(ctx,
		"UPDATE connector_azure.container_apps SET deleted_at = now() WHERE connector_name = $1 AND last_seen_at < $2 AND deleted_at IS NULL",
		s.ConnectorName(), before)
	return err
}

// PurgeDeleted hard-deletes Container App rows soft-deleted before the cutoff, reclaiming space.
func (s *Store) PurgeDeleted(ctx context.Context, before time.Time) error {
	tag, err := s.Pool().Exec(ctx,
		"DELETE FROM connector_azure.container_apps WHERE connector_name = $1 AND deleted_at IS NOT NULL AND deleted_at < $2",
		s.ConnectorName(), before)
	if err != nil {
		inventoryLog.Error("purge soft-deleted container apps inventory failed", "error", err)
		return err
	}
	if n := tag.RowsAffected(); n > 0 {
		inventoryLog.Info("purged soft-deleted container apps inventory", "rows", n)
	}
	return nil
}

// UpdateDiscoveryState records the last full-scan time for a location/kind cursor.
func (s *Store) UpdateDiscoveryState(ctx context.Context, location, kind string, scanAt time.Time) error {
	_, err := s.Pool().Exec(ctx, `
		INSERT INTO connector_azure.discovery_state (connector_name, location, kind, cursor, last_full_scan_at)
		VALUES ($1,$2,$3,'{}'::jsonb,$4)
		ON CONFLICT (connector_name, location, kind) DO UPDATE SET last_full_scan_at = EXCLUDED.last_full_scan_at
	`, s.ConnectorName(), location, kind, scanAt)
	return err
}

// OwnerSubscription returns the Azure subscription this connector name currently owns, and whether one
// is recorded.
func (s *Store) OwnerSubscription(ctx context.Context) (string, bool, error) {
	var subscription string
	err := s.Pool().QueryRow(ctx,
		`SELECT subscription_id FROM connector_azure.connector_owner WHERE connector_name = $1`,
		s.ConnectorName()).Scan(&subscription)
	if isNoRows(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return subscription, true, nil
}

// SetOwnerSubscription records the Azure subscription this connector name currently owns.
func (s *Store) SetOwnerSubscription(ctx context.Context, subscriptionID string) error {
	_, err := s.Pool().Exec(ctx, `
		INSERT INTO connector_azure.connector_owner (connector_name, subscription_id, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (connector_name) DO UPDATE SET subscription_id = EXCLUDED.subscription_id, updated_at = now()
	`, s.ConnectorName(), subscriptionID)
	return err
}

// PurgeConnectorInventory hard-deletes all workload inventory + discovery cursors for this connector,
// used when the connector name is rebound to a different Azure subscription. Sources/destinations are
// purged separately via the base PurgeIntent.
func (s *Store) PurgeConnectorInventory(ctx context.Context) error {
	for _, table := range []string{"container_apps", "discovery_state"} {
		if _, err := s.Pool().Exec(ctx, "DELETE FROM connector_azure."+table+" WHERE connector_name = $1", s.ConnectorName()); err != nil {
			return fmt.Errorf("purge %s: %w", table, err)
		}
	}
	return nil
}

// ListWorkloads returns the discovered WORKLOAD inventory (Container Apps) as raw DiscoveredWorkload
// objects; the base coordinator enriches them with source intent.
func (s *Store) ListWorkloads(ctx context.Context) ([]api.DiscoveredWorkload, error) {
	inventoryLog.Debug("list workloads", "connector", s.ConnectorName())
	rows, err := s.Pool().Query(ctx, fmt.Sprintf(`
		SELECT app_name, location, resource_id, last_seen_at,
			COALESCE(metadata->>'instrumented','false')='true', '%s',
			COALESCE(metadata->>'runtimeLanguage',''),
			COALESCE(metadata->>'runtimeVersion',''),
			COALESCE(metadata->>'instrumentationError',''),
			COALESCE(metadata->>'instrumentationPhase','')
		FROM connector_azure.container_apps WHERE connector_name = $1 AND deleted_at IS NULL
	`, KindContainerApps), s.ConnectorName())
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
			Kind:                 KindContainerApps,
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
	case KindContainerApps:
		err = s.Pool().QueryRow(ctx, `
			SELECT resource_id, location FROM connector_azure.container_apps
			WHERE connector_name = $1 AND app_name = $2 AND deleted_at IS NULL
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
