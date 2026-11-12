package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/types"
)

// Compute-platform kinds — managed infra Odigos is installed on (not instrumented). Kind strings match the spec
// compute-platform type names.
const (
	KindECSCluster        = types.ECSCluster
	KindECSFargateCluster = types.FargateCluster
)

// SupportedKinds returns all compute-platform kinds this connector publishes in ComputePlatformReports.
func SupportedKinds() []string {
	return []string{KindECSFargateCluster, KindECSCluster}
}

// AssignableKinds returns compute-platform kinds that can be assigned via createSource (agent install targets).
// fargate-cluster is coverage-only — no agent is installed on it.
func AssignableKinds() []string {
	return []string{KindECSCluster}
}

// ECSCluster is a discovered ECS cluster inventory record. The detector classifies it at discovery time:
// RegisteredEC2Instances records EC2 capacity, and HasFargate records whether the cluster runs any
// Fargate task. A cluster that runs Fargate is a fargate-cluster (handled fully as fargate); an EC2-only
// cluster (capacity, no Fargate) is an ecs-cluster (agent installed). Both flags are persisted in
// metadata so ListComputePlatforms classifies without touching the workload domain's task inventory.
type ECSCluster struct {
	Region                 string
	ClusterARN             string
	ClusterName            string
	Status                 string
	RegisteredEC2Instances int32
	HasFargate             bool
	Metadata               map[string]any
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

// UpsertECSCluster inserts or refreshes an ECS cluster inventory row, recording EC2 capacity + Fargate
// presence (the latter in metadata.hasFargate, used for classification).
func (s *Store) UpsertECSCluster(ctx context.Context, c ECSCluster) error {
	inventoryLog.Debug("upsert ecs cluster", "region", c.Region, "name", c.ClusterName, "ec2Instances", c.RegisteredEC2Instances, "hasFargate", c.HasFargate)
	if c.Metadata == nil {
		c.Metadata = map[string]any{}
	}
	c.Metadata["hasFargate"] = c.HasFargate
	meta, err := metaJSON(c.Metadata)
	if err != nil {
		return err
	}
	_, err = s.Pool().Exec(ctx, `
		INSERT INTO connector_aws.ecs_clusters
			(connector_name, region, cluster_arn, cluster_name, status, registered_ec2_instances, metadata, first_seen_at, last_seen_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb, now(), now())
		ON CONFLICT (connector_name, cluster_arn) DO UPDATE SET
			region = EXCLUDED.region, cluster_name = EXCLUDED.cluster_name, status = EXCLUDED.status,
			registered_ec2_instances = EXCLUDED.registered_ec2_instances,
			metadata = EXCLUDED.metadata, last_seen_at = now(), deleted_at = NULL
	`, s.ConnectorName(), c.Region, c.ClusterARN, c.ClusterName, c.Status, c.RegisteredEC2Instances, meta)
	return err
}

// Prune soft-deletes compute-platform inventory rows (ecs_clusters) for a region not seen since the scan-start.
func (s *Store) Prune(ctx context.Context, region string, before time.Time) error {
	inventoryLog.Debug("prune compute-platform inventory", "region", region)
	_, err := s.Pool().Exec(ctx,
		"UPDATE connector_aws.ecs_clusters SET deleted_at = now() WHERE connector_name = $1 AND region = $2 AND last_seen_at < $3 AND deleted_at IS NULL",
		s.ConnectorName(), region, before)
	return err
}

// UpdateDiscoveryState records the last full-scan time for a region/kind cursor.
func (s *Store) UpdateDiscoveryState(ctx context.Context, region, kind string, scanAt time.Time) error {
	_, err := s.Pool().Exec(ctx, `
		INSERT INTO connector_aws.discovery_state (connector_name, region, kind, cursor, last_full_scan_at)
		VALUES ($1,$2,$3,'{}'::jsonb,$4)
		ON CONFLICT (connector_name, region, kind) DO UPDATE SET last_full_scan_at = EXCLUDED.last_full_scan_at
	`, s.ConnectorName(), region, kind, scanAt)
	return err
}

// ListComputePlatforms returns the discovered compute platforms (managed install targets) classified from the cluster
// inventory: a cluster that runs Fargate tasks is a fargate-cluster; an EC2-capacity cluster with no
// Fargate tasks is an ecs-cluster. Classification reads only the compute-platform store's own columns/metadata.
func (s *Store) ListComputePlatforms(ctx context.Context) ([]api.DiscoveredComputePlatform, error) {
	inventoryLog.Debug("list compute platforms", "connector", s.ConnectorName())
	queries := []struct {
		kind string
		sql  string
	}{
		// fargate-cluster: runs Fargate tasks (hybrid clusters included).
		{KindECSFargateCluster, `SELECT cluster_name, region, cluster_arn, registered_ec2_instances, last_seen_at
			FROM connector_aws.ecs_clusters
			WHERE connector_name = $1 AND deleted_at IS NULL AND metadata->>'hasFargate' = 'true'`},
		// ecs-cluster: EC2 capacity and NO Fargate tasks (pure EC2).
		{KindECSCluster, `SELECT cluster_name, region, cluster_arn, registered_ec2_instances, last_seen_at
			FROM connector_aws.ecs_clusters
			WHERE connector_name = $1 AND deleted_at IS NULL AND registered_ec2_instances > 0
			  AND COALESCE(metadata->>'hasFargate','false') <> 'true'`},
	}

	var out []api.DiscoveredComputePlatform
	for _, q := range queries {
		rows, err := s.Pool().Query(ctx, q.sql, s.ConnectorName())
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var name, region, externalID string
			var ec2Count int32
			var lastSeen time.Time
			if err := rows.Scan(&name, &region, &externalID, &ec2Count, &lastSeen); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, api.DiscoveredComputePlatform{
				Name:       name,
				Kind:       q.kind,
				Region:     region,
				ExternalID: externalID,
				Metadata:   map[string]any{"ec2InstanceCount": ec2Count},
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

// LookupDiscoveredComputePlatform finds a compute-platform inventory row matching a prospective assignment name/kind/region.
// region pins the exact regional row when non-empty; empty matches by name+kind alone (deterministic).
func (s *Store) LookupDiscoveredComputePlatform(ctx context.Context, name, kind, region string) (externalID, foundRegion string, found bool, err error) {
	inventoryLog.Debug("lookup compute platform", "connector", s.ConnectorName(), "name", name, "kind", kind, "region", region)
	switch kind {
	case KindECSCluster:
		err = s.Pool().QueryRow(ctx, `
			SELECT cluster_arn, region FROM connector_aws.ecs_clusters
			WHERE connector_name = $1 AND cluster_name = $2 AND deleted_at IS NULL
			  AND registered_ec2_instances > 0
			  AND COALESCE(metadata->>'hasFargate','false') <> 'true'
			  AND ($3 = '' OR region = $3)
			ORDER BY region LIMIT 1
		`, s.ConnectorName(), name, region).Scan(&externalID, &foundRegion)
	case KindECSFargateCluster:
		err = s.Pool().QueryRow(ctx, `
			SELECT cluster_arn, region FROM connector_aws.ecs_clusters
			WHERE connector_name = $1 AND cluster_name = $2 AND deleted_at IS NULL
			  AND metadata->>'hasFargate' = 'true'
			  AND ($3 = '' OR region = $3)
			ORDER BY region LIMIT 1
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
