package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/agentcore"
)

func (s *Store) UpsertAgentCore(ctx context.Context, r agentcore.Runtime) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.Pool().Exec(ctx, `INSERT INTO connector_aws.agentcore_runtimes
	(connector_name,runtime_arn,runtime_name,region,inventory,last_seen_at)
	VALUES ($1,$2,$3,$4,$5,clock_timestamp()) ON CONFLICT (connector_name,runtime_arn) DO UPDATE
	SET runtime_name=EXCLUDED.runtime_name, region=EXCLUDED.region, inventory=EXCLUDED.inventory,
	last_seen_at=EXCLUDED.last_seen_at,deleted_at=NULL,
	metadata=agentcore_runtimes.metadata || CASE WHEN EXCLUDED.inventory->>'origin'='' AND EXCLUDED.inventory->>'status'='READY'
	THEN '{"instrumentation_phase":"","settling":false}'::jsonb ELSE '{}'::jsonb END`, s.ConnectorName(), r.ARN, r.Name, r.Region, raw)
	return err
}
func (s *Store) ListAgentCore(ctx context.Context) ([]agentcore.Runtime, error) {
	rows, err := s.Pool().Query(ctx, `SELECT inventory,last_seen_at FROM connector_aws.agentcore_runtimes WHERE connector_name=$1 AND deleted_at IS NULL ORDER BY region,runtime_arn`, s.ConnectorName())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []agentcore.Runtime
	for rows.Next() {
		var raw []byte
		var r agentcore.Runtime
		if err := rows.Scan(&raw, &r.LastSeen); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) PruneAgentCore(ctx context.Context, region string, before time.Time) error {
	_, err := s.Pool().Exec(ctx, `UPDATE connector_aws.agentcore_runtimes SET deleted_at=now() WHERE connector_name=$1 AND region=$2 AND last_seen_at<$3 AND deleted_at IS NULL`, s.ConnectorName(), region, before)
	return err
}
func (s *Store) SetAgentCoreState(ctx context.Context, arn string, values map[string]any) error {
	data, err := json.Marshal(values)
	if err != nil {
		return err
	}
	_, err = s.Pool().Exec(ctx, `UPDATE connector_aws.agentcore_runtimes SET metadata=metadata || $3::jsonb WHERE connector_name=$1 AND runtime_arn=$2`, s.ConnectorName(), arn, data)
	return err
}
func (s *Store) AgentCoreState(ctx context.Context, arn string) (map[string]any, error) {
	var data []byte
	err := s.Pool().QueryRow(ctx, `SELECT metadata FROM connector_aws.agentcore_runtimes WHERE connector_name=$1 AND runtime_arn=$2`, s.ConnectorName(), arn).Scan(&data)
	if err != nil {
		return nil, err
	}
	var state map[string]any
	err = json.Unmarshal(data, &state)
	return state, err
}
func (s *Store) SaveAgentCoreSnapshot(ctx context.Context, arn, id string, data []byte) error {
	_, err := s.Pool().Exec(ctx, `INSERT INTO connector_aws.agentcore_instrumentation_snapshots (connector_name,runtime_arn,snapshot_id,payload) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, s.ConnectorName(), arn, id, data)
	return err
}
func (s *Store) LoadAgentCoreSnapshot(ctx context.Context, arn, id string) ([]byte, error) {
	var raw []byte
	err := s.Pool().QueryRow(ctx, `SELECT payload FROM connector_aws.agentcore_instrumentation_snapshots WHERE connector_name=$1 AND runtime_arn=$2 AND snapshot_id=$3`, s.ConnectorName(), arn, id).Scan(&raw)
	return raw, err
}
