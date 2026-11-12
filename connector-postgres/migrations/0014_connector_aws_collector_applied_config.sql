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

-- A database migrated by a release that shipped the FIRST form of migration 0012 (v1.38.0 onwards) has
-- managed_collectors without idle_since and first_ready_at, keyed (connector_name, region, network_id),
-- carrying the older ingress_tier vocabulary and missing the regional marker index. 0012 was later
-- rewritten in place, and goose records it as applied and never re-runs it, so that rewrite reaches a
-- fresh database only. Reconcile the released shape here, before the statements below read
-- first_ready_at: without this the whole migration Job fails on every upgraded installation, the Helm
-- post-upgrade hook fails with it, and the schema stays behind ExpectedSchemaVersions — which every
-- connector runtime verifies at startup, so all of them refuse to start.
--
-- Each statement is a no-op on a database that received 0012 in its current form. These columns and
-- constraints belong to 0012, so Down leaves them in place, exactly like the destinations bridge above.
ALTER TABLE connector_aws.managed_collectors
  ADD COLUMN IF NOT EXISTS idle_since     TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS first_ready_at TIMESTAMPTZ;

-- The host network is placement metadata, not row identity. UpsertManagedCollector names
-- ON CONFLICT (connector_name, region), and Postgres infers an arbiter only from a unique index over
-- exactly the named columns — so against the three-column key every upsert fails to plan (SQLSTATE
-- 42P10) and no Collector can ever be recorded. That is also why re-keying cannot lose a row: the
-- upsert is the only statement that inserts one, so the table is empty wherever the old key survived.
ALTER TABLE connector_aws.managed_collectors
  DROP CONSTRAINT IF EXISTS managed_collectors_pkey;
ALTER TABLE connector_aws.managed_collectors
  ADD CONSTRAINT managed_collectors_pkey PRIMARY KEY (connector_name, region);

UPDATE connector_aws.managed_collectors
SET ingress_tier = 'network-identities'
WHERE ingress_tier = 'security-groups';

ALTER TABLE connector_aws.managed_collectors
  DROP CONSTRAINT IF EXISTS managed_collectors_ingress_tier_check;
ALTER TABLE connector_aws.managed_collectors
  ADD CONSTRAINT managed_collectors_ingress_tier_check
    CHECK (ingress_tier IN ('network-identities', 'configured-cidrs', 'network-cidr', 'none'));

CREATE UNIQUE INDEX IF NOT EXISTS destinations_managed_collector_region_uidx
  ON connector_aws.destinations (connector_name, region)
  WHERE lower(dest_type) = 'odigos-managed-collector';

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
