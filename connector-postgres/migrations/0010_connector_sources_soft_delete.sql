-- Soft-delete for sources, across all three provider schemas.
--
-- A hard delete erases the row entirely, so a deleted source and one that never existed look identical.
-- Orphan adoption (connectors/base/coordinator/adopt.go) needs to tell those apart: without this, it
-- resurrects a source moments after a human deletes it, whenever the workload's live instrumentation
-- has not yet caught up to that delete — an ODIGOS_MANAGED marker still mid-rollback, or an ODIGOS_IAC
-- marker the customer's own IaC still carries and always will.
--
-- Soft-deleting instead keeps the row under its existing primary key, so:
--   - adoption can see the tombstone (deleted_at IS NOT NULL) and skip that identity permanently;
--   - CreateSource's existing upsert (ON CONFLICT ... DO UPDATE) already runs on any create, so clearing
--     deleted_at there is all it takes for a human's explicit re-create to undo the tombstone — no new
--     "undelete" path needed;
--   - the tombstone doubles as a small audit trail of what was deliberately removed, and when.

-- +goose Up
ALTER TABLE connector_aws.sources ADD COLUMN deleted_at TIMESTAMPTZ;
ALTER TABLE connector_gcp.sources ADD COLUMN deleted_at TIMESTAMPTZ;
ALTER TABLE connector_azure.sources ADD COLUMN deleted_at TIMESTAMPTZ;

-- Every live read filters "connector_name = $1 AND deleted_at IS NULL" (ListSources, FindSource);
-- adoption's tombstone check filters the opposite. Index names are reused identically across the three
-- schemas — valid in Postgres, since index names are namespaced per schema, exactly like each of these
-- tables' own implicit primary-key index already is.
CREATE INDEX sources_live_idx ON connector_aws.sources (connector_name) WHERE deleted_at IS NULL;
CREATE INDEX sources_deleted_idx ON connector_aws.sources (connector_name) WHERE deleted_at IS NOT NULL;
CREATE INDEX sources_live_idx ON connector_gcp.sources (connector_name) WHERE deleted_at IS NULL;
CREATE INDEX sources_deleted_idx ON connector_gcp.sources (connector_name) WHERE deleted_at IS NOT NULL;
CREATE INDEX sources_live_idx ON connector_azure.sources (connector_name) WHERE deleted_at IS NULL;
CREATE INDEX sources_deleted_idx ON connector_azure.sources (connector_name) WHERE deleted_at IS NOT NULL;

UPDATE connector_platform.schema_versions SET version = '3', applied_at = now() WHERE schema_name = 'connector_aws';
UPDATE connector_platform.schema_versions SET version = '2', applied_at = now() WHERE schema_name = 'connector_gcp';
UPDATE connector_platform.schema_versions SET version = '2', applied_at = now() WHERE schema_name = 'connector_azure';

-- +goose Down
UPDATE connector_platform.schema_versions SET version = '2', applied_at = now() WHERE schema_name = 'connector_aws';
UPDATE connector_platform.schema_versions SET version = '1', applied_at = now() WHERE schema_name = 'connector_gcp';
UPDATE connector_platform.schema_versions SET version = '1', applied_at = now() WHERE schema_name = 'connector_azure';

DROP INDEX connector_azure.sources_deleted_idx;
DROP INDEX connector_azure.sources_live_idx;
DROP INDEX connector_gcp.sources_deleted_idx;
DROP INDEX connector_gcp.sources_live_idx;
DROP INDEX connector_aws.sources_deleted_idx;
DROP INDEX connector_aws.sources_live_idx;

ALTER TABLE connector_azure.sources DROP COLUMN deleted_at;
ALTER TABLE connector_gcp.sources DROP COLUMN deleted_at;
ALTER TABLE connector_aws.sources DROP COLUMN deleted_at;
