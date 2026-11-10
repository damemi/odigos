-- +goose Up
-- Private rollback state, independent of discovery inventory and public source metadata.
-- Immutable snapshots survive retries and restarts; a Lambda marker references one exact revision.
CREATE TABLE connector_aws.lambda_instrumentation_snapshots (
  connector_name TEXT NOT NULL,
  function_arn TEXT NOT NULL,
  snapshot_id TEXT NOT NULL,
  payload JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (connector_name, function_arn, snapshot_id)
);
UPDATE connector_platform.schema_versions SET version = '11', applied_at = now()
WHERE schema_name = 'connector_aws';

-- +goose Down
DROP TABLE connector_aws.lambda_instrumentation_snapshots;
UPDATE connector_platform.schema_versions SET version = '10', applied_at = now()
WHERE schema_name = 'connector_aws';
