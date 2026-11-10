-- +goose Up
ALTER TABLE connector_platform.collector_resource_state
  DROP CONSTRAINT collector_resource_state_check;
ALTER TABLE connector_platform.collector_resource_state
  ADD CONSTRAINT collector_resource_state_check
  CHECK (state IN ('not-created', 'provisioning', 'created', 'tearing-down', 'moving', 'orphaned', 'destroyed'));
ALTER TABLE connector_platform.collector_credentials
  DROP CONSTRAINT collector_credentials_revoked_reason_check;
ALTER TABLE connector_platform.collector_credentials
  ADD CONSTRAINT collector_credentials_revoked_reason_check
  CHECK (revoked_reason IS NULL OR revoked_reason IN
    ('teardown', 'rotation', 'compromise', 'connector-recreated', 'capability-removed', 'network-changed'));
UPDATE connector_platform.schema_versions
SET version = '5', applied_at = now()
WHERE schema_name = 'connector_platform';

-- +goose Down
UPDATE connector_platform.collector_credentials
SET revoked_reason = 'teardown'
WHERE revoked_reason = 'network-changed';
ALTER TABLE connector_platform.collector_credentials
  DROP CONSTRAINT collector_credentials_revoked_reason_check;
ALTER TABLE connector_platform.collector_credentials
  ADD CONSTRAINT collector_credentials_revoked_reason_check
  CHECK (revoked_reason IS NULL OR revoked_reason IN
    ('teardown', 'rotation', 'compromise', 'connector-recreated', 'capability-removed'));
UPDATE connector_platform.collector_resource_state
SET state = 'orphaned'
WHERE state = 'tearing-down';
UPDATE connector_platform.collector_resource_state
SET state = 'orphaned'
WHERE state = 'moving';
ALTER TABLE connector_platform.collector_resource_state
  DROP CONSTRAINT collector_resource_state_check;
ALTER TABLE connector_platform.collector_resource_state
  ADD CONSTRAINT collector_resource_state_check
  CHECK (state IN ('not-created', 'provisioning', 'created', 'orphaned', 'destroyed'));
UPDATE connector_platform.schema_versions
SET version = '4', applied_at = now()
WHERE schema_name = 'connector_platform';
