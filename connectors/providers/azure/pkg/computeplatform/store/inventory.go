package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/azure/pkg/types"
)

// Compute-platform kinds — managed infra Odigos is installed on (not instrumented). Kind strings match
// the spec compute-platform type names.
const (
	KindAKS = types.AKS
	KindVM  = types.VM
)

// SupportedKinds returns all compute-platform kinds this connector publishes in ComputePlatformReports.
func SupportedKinds() []string {
	return []string{KindAKS, KindVM}
}

// AssignableKinds returns compute-platform kinds that can be assigned via createSource (agent install
// targets). Azure installation is not supported yet, so nothing is assignable — AKS/VM are discovered
// (coverage) but not assignable.
func AssignableKinds() []string {
	return nil
}

// AKSCluster is a discovered AKS cluster inventory record.
type AKSCluster struct {
	Location      string
	ResourceGroup string
	ResourceID    string
	ClusterName   string
	Status        string
	Metadata      map[string]any
}

// VirtualMachine is a discovered Azure Virtual Machine inventory record. Location holds the region.
type VirtualMachine struct {
	Location      string
	ResourceGroup string
	ResourceID    string
	VMName        string
	VMID          string
	Status        string
	Metadata      map[string]any
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

// UpsertAKSCluster inserts or refreshes an AKS cluster inventory row, clearing any soft-delete marker.
func (s *Store) UpsertAKSCluster(ctx context.Context, c AKSCluster) error {
	inventoryLog.Debug("upsert aks cluster", "location", c.Location, "name", c.ClusterName)
	meta, err := metaJSON(c.Metadata)
	if err != nil {
		return err
	}
	_, err = s.Pool().Exec(ctx, `
		INSERT INTO connector_azure.aks_clusters
			(connector_name, location, resource_group, resource_id, cluster_name, status, metadata, first_seen_at, last_seen_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb, now(), now())
		ON CONFLICT (connector_name, resource_id) DO UPDATE SET
			location = EXCLUDED.location, resource_group = EXCLUDED.resource_group, cluster_name = EXCLUDED.cluster_name,
			status = EXCLUDED.status, metadata = EXCLUDED.metadata, last_seen_at = now(), deleted_at = NULL
	`, s.ConnectorName(), c.Location, c.ResourceGroup, c.ResourceID, c.ClusterName, c.Status, meta)
	return err
}

// UpsertVirtualMachine inserts or refreshes a Virtual Machine inventory row.
func (s *Store) UpsertVirtualMachine(ctx context.Context, vm VirtualMachine) error {
	inventoryLog.Debug("upsert virtual machine", "location", vm.Location, "name", vm.VMName)
	meta, err := metaJSON(vm.Metadata)
	if err != nil {
		return err
	}
	_, err = s.Pool().Exec(ctx, `
		INSERT INTO connector_azure.virtual_machines
			(connector_name, location, resource_group, resource_id, vm_name, vm_id, status, metadata, first_seen_at, last_seen_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb, now(), now())
		ON CONFLICT (connector_name, resource_id) DO UPDATE SET
			location = EXCLUDED.location, resource_group = EXCLUDED.resource_group, vm_name = EXCLUDED.vm_name,
			vm_id = EXCLUDED.vm_id, status = EXCLUDED.status, metadata = EXCLUDED.metadata,
			last_seen_at = now(), deleted_at = NULL
	`, s.ConnectorName(), vm.Location, vm.ResourceGroup, vm.ResourceID, vm.VMName, vm.VMID, vm.Status, meta)
	return err
}

// PruneAKS / PruneVM soft-delete rows of one kind not seen since the scan start.
func (s *Store) PruneAKS(ctx context.Context, before time.Time) error {
	return s.pruneTable(ctx, "aks_clusters", before)
}

func (s *Store) PruneVM(ctx context.Context, before time.Time) error {
	return s.pruneTable(ctx, "virtual_machines", before)
}

func (s *Store) pruneTable(ctx context.Context, table string, before time.Time) error {
	inventoryLog.Debug("prune compute-platform inventory", "table", table)
	_, err := s.Pool().Exec(ctx,
		"UPDATE connector_azure."+table+" SET deleted_at = now() WHERE connector_name = $1 AND last_seen_at < $2 AND deleted_at IS NULL",
		s.ConnectorName(), before)
	return err
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

// ListComputePlatforms returns the discovered compute platforms (AKS clusters + Virtual Machines) as
// raw DiscoveredComputePlatform objects.
func (s *Store) ListComputePlatforms(ctx context.Context) ([]api.DiscoveredComputePlatform, error) {
	inventoryLog.Debug("list compute platforms", "connector", s.ConnectorName())
	queries := []struct {
		kind string
		sql  string
	}{
		{KindAKS, `SELECT cluster_name, location, resource_id, status, last_seen_at
			FROM connector_azure.aks_clusters WHERE connector_name = $1 AND deleted_at IS NULL`},
		{KindVM, `SELECT vm_name, location, resource_id, status, last_seen_at
			FROM connector_azure.virtual_machines WHERE connector_name = $1 AND deleted_at IS NULL`},
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
	case KindAKS:
		err = s.Pool().QueryRow(ctx, `
			SELECT resource_id, location FROM connector_azure.aks_clusters
			WHERE connector_name = $1 AND cluster_name = $2 AND deleted_at IS NULL
			  AND ($3 = '' OR location = $3)
			ORDER BY location LIMIT 1
		`, s.ConnectorName(), name, region).Scan(&externalID, &foundRegion)
	case KindVM:
		err = s.Pool().QueryRow(ctx, `
			SELECT resource_id, location FROM connector_azure.virtual_machines
			WHERE connector_name = $1 AND vm_name = $2 AND deleted_at IS NULL
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
