-- +goose Up
CREATE TABLE connector_aws.collector_cutover_announcements (
  connector_name     TEXT        NOT NULL,
  region             TEXT        NOT NULL,
  marker_updated_at  TIMESTAMPTZ NOT NULL,
  first_ready_at     TIMESTAMPTZ NOT NULL,
  announced_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (connector_name, region)
);

UPDATE connector_platform.schema_versions
SET version = '9', applied_at = now()
WHERE schema_name = 'connector_aws';

-- +goose Down
DROP TABLE connector_aws.collector_cutover_announcements;

UPDATE connector_platform.schema_versions
SET version = '8', applied_at = now()
WHERE schema_name = 'connector_aws';
