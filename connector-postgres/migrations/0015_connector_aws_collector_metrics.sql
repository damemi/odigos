-- The Collector's last reported metrics snapshot.
--
-- In the PROVIDER schema, beside managed_collectors (migration 0012), for the same reason: these columns
-- describe a Collector, and a Collector row lives here.

-- +goose Up

-- The Collector's last reported counters, and when the COLLECTOR took them (not when we stored them).
--
-- One JSONB snapshot plus its timestamp rather than a column per counter: they are read and written as a
-- set, always by the same single writer (the OpAMP session), and the counter set follows the Collector's
-- own internal telemetry, which is not this schema's to freeze. Its own accessor and its own two columns
-- keep it out of the way of every other writer of this row (store/collectors.go, "Writers are split by
-- CONCERN").
--
-- NULL means this Collector has never reported. That is deliberately distinguishable from a snapshot of
-- zeroes, which means it reported and has handled nothing.
--
-- applied_config_hash is repeated with IF NOT EXISTS for pre-release databases that recorded version
-- 14 for the metrics migration before applied configuration took that number. Fresh databases receive
-- it from 0014; this bridge restores the durable OpAMP state when that migration was skipped.
ALTER TABLE connector_aws.managed_collectors
  ADD COLUMN IF NOT EXISTS applied_config_hash TEXT,
  ADD COLUMN IF NOT EXISTS metrics             JSONB,
  ADD COLUMN IF NOT EXISTS metrics_observed_at TIMESTAMPTZ;

-- Same compatibility backfill as 0014 for databases that skipped the new applied-config migration.
UPDATE connector_aws.managed_collectors
SET applied_config_hash = config_hash
WHERE applied_config_hash IS NULL
  AND first_ready_at IS NOT NULL
  AND config_hash IS NOT NULL
  AND status NOT IN ('Pending', 'Deploying', 'Error', 'AwaitingCustomer')
  AND message NOT LIKE 'the Collector rejected this configuration%';

UPDATE connector_platform.schema_versions
SET version = '7', applied_at = now()
WHERE schema_name = 'connector_aws';

-- +goose Down
ALTER TABLE connector_aws.managed_collectors
  DROP COLUMN IF EXISTS metrics,
  DROP COLUMN IF EXISTS metrics_observed_at;

UPDATE connector_platform.schema_versions
SET version = '6', applied_at = now()
WHERE schema_name = 'connector_aws';
