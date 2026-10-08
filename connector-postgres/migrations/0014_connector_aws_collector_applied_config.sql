-- The last Collector configuration positively acknowledged by the Supervisor.
--
-- config_hash records the last document Odigos sent. That is intentionally not enough to restore an
-- OpAMP session after a connector restart: the process may have stopped between sending a document and
-- receiving its acknowledgement. Keeping the applied hash separately lets a reconnected, already-ready
-- Collector resume steady state without either assuming an unacknowledged document worked or repeatedly
-- sending an identical document that the Supervisor will not acknowledge twice.

-- +goose Up
-- Pre-release databases may have recorded version 13 for a Collector migration before the released
-- destination-disabled migration took that number. Restore the columns that goose would skip there.
-- The released migration owns these columns, so this bridge leaves them in place on Down.
ALTER TABLE connector_aws.destinations
  ADD COLUMN IF NOT EXISTS disabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE connector_gcp.destinations
  ADD COLUMN IF NOT EXISTS disabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE connector_azure.destinations
  ADD COLUMN IF NOT EXISTS disabled BOOLEAN NOT NULL DEFAULT false;

UPDATE connector_platform.schema_versions
SET version = '3', applied_at = now()
WHERE schema_name IN ('connector_gcp', 'connector_azure');

ALTER TABLE connector_aws.managed_collectors
  ADD COLUMN IF NOT EXISTS applied_config_hash TEXT;

-- Existing ready Collectors predate this column. Their last sent hash is also their last known applied
-- hash unless the durable verdict says that exact document is still applying or was rejected. Preserve
-- infrastructure Degraded verdicts: those describe ECS/ELB state and do not revoke an earlier OpAMP
-- acknowledgement.
UPDATE connector_aws.managed_collectors
SET applied_config_hash = config_hash
WHERE first_ready_at IS NOT NULL
  AND config_hash IS NOT NULL
  AND status NOT IN ('Pending', 'Deploying', 'Error', 'AwaitingCustomer')
  AND message NOT LIKE 'the Collector rejected this configuration%';

UPDATE connector_platform.schema_versions
SET version = '6', applied_at = now()
WHERE schema_name = 'connector_aws';

-- +goose Down
ALTER TABLE connector_aws.managed_collectors
  DROP COLUMN IF EXISTS applied_config_hash;

UPDATE connector_platform.schema_versions
SET version = '5', applied_at = now()
WHERE schema_name = 'connector_aws';
