-- Actions are configuration owned by one regional managed Collector. They are
-- separate from workload destinations: a row changes the Collector's rendered
-- OpenTelemetry config and reaches it through its existing OpAMP session.

-- +goose Up
CREATE TABLE IF NOT EXISTS connector_aws.collector_actions (
  connector_name TEXT NOT NULL,
  region         TEXT NOT NULL,
  name           TEXT NOT NULL,
  action_type    TEXT NOT NULL,
  action_name    TEXT NOT NULL DEFAULT '',
  notes          TEXT NOT NULL DEFAULT '',
  disabled       BOOLEAN NOT NULL DEFAULT false,
  signals        JSONB NOT NULL DEFAULT '[]',
  pipelines      JSONB NOT NULL DEFAULT '[]',
  order_hint     INTEGER,
  config         JSONB NOT NULL DEFAULT '{}',
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (connector_name, region, name),
  CONSTRAINT collector_actions_region_not_blank CHECK (length(btrim(region)) > 0),
  CONSTRAINT collector_actions_name_component_safe CHECK (name ~ '^[A-Za-z0-9_-]+$')
);

UPDATE connector_platform.schema_versions
SET version = '12', applied_at = now()
WHERE schema_name = 'connector_aws';

-- +goose Down
DROP TABLE IF EXISTS connector_aws.collector_actions;

UPDATE connector_platform.schema_versions
SET version = '11', applied_at = now()
WHERE schema_name = 'connector_aws';
