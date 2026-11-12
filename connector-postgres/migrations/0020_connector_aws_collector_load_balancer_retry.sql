-- +goose Up
-- A regional account restriction can outlive a connector pod. Keep the next attempt in
-- Postgres so a restart does not resume a minute-by-minute CreateLoadBalancer loop.
CREATE TABLE connector_aws.collector_load_balancer_retries (
  connector_name TEXT NOT NULL,
  region TEXT NOT NULL,
  attempts INTEGER NOT NULL CHECK (attempts > 0),
  retry_after TIMESTAMPTZ NOT NULL,
  request_id TEXT,
  PRIMARY KEY (connector_name, region),
  FOREIGN KEY (connector_name, region)
    REFERENCES connector_aws.managed_collectors (connector_name, region) ON DELETE CASCADE
);

UPDATE connector_platform.schema_versions
SET version = '10', applied_at = now()
WHERE schema_name = 'connector_aws';

-- +goose Down
DROP TABLE connector_aws.collector_load_balancer_retries;

UPDATE connector_platform.schema_versions
SET version = '9', applied_at = now()
WHERE schema_name = 'connector_aws';
