package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/gcp/pkg/types"
)

// Compute-platform kinds — managed infra Odigos is installed on (not instrumented). Kind strings match
// the spec compute-platform type names.
const (
	KindGKE = types.GKE
	KindGCE = types.GCE
)

// SupportedKinds returns all compute-platform kinds this connector publishes in ComputePlatformReports.
func SupportedKinds() []string {
	return []string{KindGKE, KindGCE}
}

// AssignableKinds returns compute-platform kinds that can be assigned via createSource (agent install
// targets). GCP installation is not supported yet, so nothing is assignable — GKE/GCE are discovered
// (coverage) but not assignable.
func AssignableKinds() []string {
	return nil
}

// GKECluster is a discovered GKE cluster inventory record.
type GKECluster struct {
	Location     string
	ResourceName string
	ClusterName  string
	Status       string
	Metadata     map[string]any
}

// GCEInstance is a discovered Compute Engine instance inventory record. Location holds the zone.
type GCEInstance struct {
	Location     string
	ResourceName string
	InstanceName string
	InstanceID   string
	Status       string
	Metadata     map[string]any
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

// UpsertGKECluster inserts or refreshes a GKE cluster inventory row, clearing any soft-delete marker.
func (s *Store) UpsertGKECluster(ctx context.Context, c GKECluster) error {
	inventoryLog.Debug("upsert gke cluster", "location", c.Location, "name", c.ClusterName)
	meta, err := metaJSON(c.Metadata)
	if err != nil {
		return err
	}
	_, err = s.Pool().Exec(ctx, `
		INSERT INTO connector_gcp.gke_clusters
			(connector_name, location, resource_name, cluster_name, status, metadata, first_seen_at, last_seen_at)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb, now(), now())
		ON CONFLICT (connector_name, resource_name) DO UPDATE SET
			location = EXCLUDED.location, cluster_name = EXCLUDED.cluster_name, status = EXCLUDED.status,
			metadata = EXCLUDED.metadata, last_seen_at = now(), deleted_at = NULL
	`, s.ConnectorName(), c.Location, c.ResourceName, c.ClusterName, c.Status, meta)
	return err
}

// UpsertGCEInstance inserts or refreshes a Compute Engine instance inventory row.
func (s *Store) UpsertGCEInstance(ctx context.Context, i GCEInstance) error {
	inventoryLog.Debug("upsert gce instance", "zone", i.Location, "name", i.InstanceName)
	meta, err := metaJSON(i.Metadata)
	if err != nil {
		return err
	}
	_, err = s.Pool().Exec(ctx, `
		INSERT INTO connector_gcp.gce_instances
			(connector_name, location, resource_name, instance_name, instance_id, status, metadata, first_seen_at, last_seen_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb, now(), now())
		ON CONFLICT (connector_name, resource_name) DO UPDATE SET
			location = EXCLUDED.location, instance_name = EXCLUDED.instance_name, instance_id = EXCLUDED.instance_id,
			status = EXCLUDED.status, metadata = EXCLUDED.metadata,
			last_seen_at = now(), deleted_at = NULL
	`, s.ConnectorName(), i.Location, i.ResourceName, i.InstanceName, i.InstanceID, i.Status, meta)
	return err
}

// PruneGKE / PruneGCE soft-delete rows of one kind not seen since the scan start (GCP scans every
// location in one call, so pruning is connector-wide per kind rather than per-region).
func (s *Store) PruneGKE(ctx context.Context, before time.Time) error {
	return s.pruneTable(ctx, "gke_clusters", before)
}

func (s *Store) PruneGCE(ctx context.Context, before time.Time) error {
	return s.pruneTable(ctx, "gce_instances", before)
}

func (s *Store) pruneTable(ctx context.Context, table string, before time.Time) error {
	inventoryLog.Debug("prune compute-platform inventory", "table", table)
	_, err := s.Pool().Exec(ctx,
		"UPDATE connector_gcp."+table+" SET deleted_at = now() WHERE connector_name = $1 AND last_seen_at < $2 AND deleted_at IS NULL",
		s.ConnectorName(), before)
	return err
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

// ListComputePlatforms returns the discovered compute platforms (GKE clusters + Compute Engine
// instances) as raw DiscoveredComputePlatform objects.
func (s *Store) ListComputePlatforms(ctx context.Context) ([]api.DiscoveredComputePlatform, error) {
	inventoryLog.Debug("list compute platforms", "connector", s.ConnectorName())
	queries := []struct {
		kind string
		sql  string
	}{
		{KindGKE, `SELECT cluster_name, location, resource_name, status, last_seen_at
			FROM connector_gcp.gke_clusters WHERE connector_name = $1 AND deleted_at IS NULL`},
		{KindGCE, `SELECT instance_name, location, resource_name, status, last_seen_at
			FROM connector_gcp.gce_instances WHERE connector_name = $1 AND deleted_at IS NULL`},
	}

	var out []api.DiscoveredComputePlatform
	for _, q := range queries {
		rows, err := s.Pool().Query(ctx, q.sql, s.ConnectorName())
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var name, location, externalID, status string
			var lastSeen time.Time
			if err := rows.Scan(&name, &location, &externalID, &status, &lastSeen); err != nil {
				rows.Close()
				return nil, err
			}
			meta := map[string]any{"status": status}
			out = append(out, api.DiscoveredComputePlatform{
				Name:       name,
				Kind:       q.kind,
				Region:     location,
				ExternalID: externalID,
				Status:     status,
				Metadata:   meta,
				LastSeenAt: lastSeen,
			})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	inventoryLog.Debug("list compute platforms complete", "connector", s.ConnectorName(), "count", len(out))
	return out, nil
}

// LookupDiscoveredComputePlatform finds a compute-platform inventory row matching a prospective
// assignment name/kind/region. region pins the exact regional row when non-empty.
func (s *Store) LookupDiscoveredComputePlatform(ctx context.Context, name, kind, region string) (externalID, foundRegion string, found bool, err error) {
	inventoryLog.Debug("lookup compute platform", "connector", s.ConnectorName(), "name", name, "kind", kind, "region", region)
	switch kind {
	case KindGKE:
		err = s.Pool().QueryRow(ctx, `
			SELECT resource_name, location FROM connector_gcp.gke_clusters
			WHERE connector_name = $1 AND cluster_name = $2 AND deleted_at IS NULL
			  AND ($3 = '' OR location = $3)
			ORDER BY location LIMIT 1
		`, s.ConnectorName(), name, region).Scan(&externalID, &foundRegion)
	case KindGCE:
		err = s.Pool().QueryRow(ctx, `
			SELECT resource_name, location FROM connector_gcp.gce_instances
			WHERE connector_name = $1 AND instance_name = $2 AND deleted_at IS NULL
			  AND ($3 = '' OR location = $3)
			ORDER BY location LIMIT 1
		`, s.ConnectorName(), name, region).Scan(&externalID, &foundRegion)
	default:
		return "", "", false, fmt.Errorf("unsupported compute-platform kind %q", kind)
	}
	if err != nil {
		if isNoRows(err) {
			return "", "", false, nil
		}
		return "", "", false, err
	}
	return externalID, foundRegion, true, nil
}
