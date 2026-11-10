-- +goose Up
CREATE TABLE connector_aws.agentcore_runtimes (
  connector_name TEXT NOT NULL,
  runtime_arn TEXT NOT NULL,
  runtime_name TEXT NOT NULL,
  region TEXT NOT NULL,
  inventory JSONB NOT NULL,
  metadata JSONB NOT NULL DEFAULT '{}',
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at TIMESTAMPTZ,
  PRIMARY KEY (connector_name, runtime_arn)
);
CREATE INDEX agentcore_runtimes_lookup ON connector_aws.agentcore_runtimes (connector_name, runtime_name, region) WHERE deleted_at IS NULL;
CREATE TABLE connector_aws.agentcore_instrumentation_snapshots (
  connector_name TEXT NOT NULL,
  runtime_arn TEXT NOT NULL,
  snapshot_id TEXT NOT NULL,
  payload JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (connector_name, runtime_arn, snapshot_id)
);
UPDATE connector_platform.schema_versions SET version = '13', applied_at = now() WHERE schema_name = 'connector_aws';

-- +goose Down
DROP TABLE connector_aws.agentcore_instrumentation_snapshots;
DROP TABLE connector_aws.agentcore_runtimes;
UPDATE connector_platform.schema_versions SET version = '12', applied_at = now() WHERE schema_name = 'connector_aws';
