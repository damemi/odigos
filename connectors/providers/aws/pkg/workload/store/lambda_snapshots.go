package store

import (
	"context"
	"fmt"
)

// SaveLambdaSnapshot persists private rollback data before AWS references it. Content-addressed,
// immutable rows retain the last accepted revision if a later AWS update conflicts or fails.
func (s *Store) SaveLambdaSnapshot(ctx context.Context, arn, id string, payload []byte) error {
	_, err := s.Pool().Exec(ctx, `INSERT INTO connector_aws.lambda_instrumentation_snapshots
 (connector_name, function_arn, snapshot_id, payload) VALUES ($1,$2,$3,$4)
 ON CONFLICT (connector_name,function_arn,snapshot_id) DO NOTHING`, s.ConnectorName(), arn, id, payload)
	if err != nil {
		return fmt.Errorf("persist Lambda rollback snapshot: %w", err)
	}
	return nil
}

func (s *Store) LoadLambdaSnapshot(ctx context.Context, arn, id string) ([]byte, error) {
	var payload []byte
	err := s.Pool().QueryRow(ctx, `SELECT payload FROM connector_aws.lambda_instrumentation_snapshots
 WHERE connector_name=$1 AND function_arn=$2 AND snapshot_id=$3`, s.ConnectorName(), arn, id).Scan(&payload)
	if err != nil {
		return nil, fmt.Errorf("load Lambda rollback snapshot %s: %w", id, err)
	}
	return payload, nil
}
