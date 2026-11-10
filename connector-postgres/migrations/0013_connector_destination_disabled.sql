-- Destination.disabled: pause export without deleting the destination row.
-- Mirrors K8s Destination.spec.disabled / agents-api Destination.disabled (v0.24.0+).
-- Disabled destinations stay in the table (config retained for re-enable) but are
-- skipped by ResolveOTLPTarget / ResolveOTLPHeaders.

-- +goose Up
ALTER TABLE connector_aws.destinations ADD COLUMN disabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE connector_gcp.destinations ADD COLUMN disabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE connector_azure.destinations ADD COLUMN disabled BOOLEAN NOT NULL DEFAULT false;

UPDATE connector_platform.schema_versions SET version = '5', applied_at = now() WHERE schema_name = 'connector_aws';
UPDATE connector_platform.schema_versions SET version = '3', applied_at = now() WHERE schema_name = 'connector_gcp';
UPDATE connector_platform.schema_versions SET version = '3', applied_at = now() WHERE schema_name = 'connector_azure';

-- +goose Down
UPDATE connector_platform.schema_versions SET version = '4', applied_at = now() WHERE schema_name = 'connector_aws';
UPDATE connector_platform.schema_versions SET version = '2', applied_at = now() WHERE schema_name = 'connector_gcp';
UPDATE connector_platform.schema_versions SET version = '2', applied_at = now() WHERE schema_name = 'connector_azure';

ALTER TABLE connector_azure.destinations DROP COLUMN disabled;
ALTER TABLE connector_gcp.destinations DROP COLUMN disabled;
ALTER TABLE connector_aws.destinations DROP COLUMN disabled;
