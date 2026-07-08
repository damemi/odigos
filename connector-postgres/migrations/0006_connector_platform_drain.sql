-- Drain/shutdown coordination between connector-runtime (controller) and connector pods.
-- The controller sets drain_requested_at before terminating a pod; the runtime rolls back
-- instrumentation on shutdown only when that flag is set (upgrades/reschedules leave it null).

-- +goose Up
ALTER TABLE connector_platform.runtime_heartbeat
  ADD COLUMN drain_requested_at TIMESTAMPTZ,
  ADD COLUMN drain_reason          TEXT;

UPDATE connector_platform.schema_versions
SET version = '2', applied_at = now()
WHERE schema_name = 'connector_platform';

-- +goose Down
-- Roll the connector_platform version back to '1', then drop the columns in reverse add order.
UPDATE connector_platform.schema_versions
SET version = '1', applied_at = now()
WHERE schema_name = 'connector_platform';

ALTER TABLE connector_platform.runtime_heartbeat
  DROP COLUMN drain_reason,
  DROP COLUMN drain_requested_at;
