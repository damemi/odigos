-- managed_collectors: one row per region in which Odigos runs (or observes) a managed Collector.
--
-- In the PROVIDER schema, reached through base accessors, exactly like sources and destinations:
-- identical structure across providers, only the schema name differs. Its sibling
-- connector_platform.collector_credentials (migration 0011) is platform-wide instead, because the OpAMP
-- gateway resolves a credential by token hash before it knows which provider issued it.
--
-- No column here holds a credential VALUE or a verifier. credential_ref says where the value lives and
-- active_credential_id says which issuance is in force; the hash lives in collector_credentials and

-- +goose Up
CREATE TABLE connector_aws.managed_collectors (
  connector_name        TEXT        NOT NULL,
  region                TEXT        NOT NULL,
  network_id            TEXT        NOT NULL,   -- host VPC id / VNet id / VPC network name (metadata)
  kind                  TEXT        NOT NULL,   -- aws.fargate-collector

  -- Authority is two INDEPENDENT bits, never one enum: mode says who converges the Collector
  -- from now on, origin says who created its infrastructure. Only (automatic, odigos) may be deleted,
  -- and a mode change never rewrites origin — which is what makes the automatic -> manual -> automatic
  -- round trip safe.
  mode                  TEXT        NOT NULL,   -- automatic | manual
  origin                TEXT        NOT NULL,   -- odigos | customer
  adopted_at            TIMESTAMPTZ,            -- set when Odigos took over infrastructure it did not create

  -- When this recorded Collector was first observed without enabled regional lifecycle intent, and the
  -- clock the retention policy runs on. NULL means the region is still configured, or that no
  -- pass has seen it idle yet.
  --
  -- Persisted rather than held in the connector's memory because the graces are measured in hours and a
  -- connector restart is not: an in-memory clock would mean a fleet upgraded weekly never reaches a
  -- 7-day delete grace, which is a policy that silently never applies. Cleared the moment regional
  -- lifecycle intent comes back, so the wait starts again rather than resuming.
  idle_since            TIMESTAMPTZ,

  -- Identity. Refs only.
  collector_id          TEXT,
  credential_ref        TEXT,                   -- secret ARN / Key Vault id / Secret Manager name
  active_credential_id  TEXT,                   -- -> collector_credentials.credential_id; never a value

  -- Infrastructure. Populated by the deployer in automatic mode; discovered in manual mode.
  --
  -- These refs are the SECOND half of the deletion gate: an object may be deleted only when its
  -- tag says Odigos created it AND its ref is recorded here. Losing this table therefore loses delete
  -- authority until a tag scan re-records it — the correct direction to fail in, because leaked
  -- infrastructure is visible and recoverable and a deleted customer resource is not.
  workload_ref          TEXT,                   -- ECS service ARN
  revision_ref          TEXT,                   -- task definition ARN
  runtime_ref           TEXT,                   -- ECS cluster ARN
  frontend_ref          TEXT,                   -- load balancer ARN
  backend_refs          JSONB       NOT NULL DEFAULT '[]', -- target group ARNs
  ingress_ref           TEXT,                   -- security group id
  placement_refs        JSONB       NOT NULL DEFAULT '[]', -- subnet ids

  -- Published endpoint (what workloads are pointed at).
  endpoint_grpc         TEXT,
  endpoint_http         TEXT,
  endpoint_source       TEXT,                   -- lb | override | shared

  -- Transport and ingress posture. Stored rather than derived at read time so a reader can
  -- see WHO can write to this Collector and whether the hop is encrypted, neither of which is visible
  -- from the endpoint alone.
  tls_mode              TEXT        NOT NULL DEFAULT 'off',
  tls_termination       TEXT,
  ingress_tier          TEXT        NOT NULL DEFAULT 'none',

  -- Image and licence. The digest is recorded rather than the tag, because a tag is not an
  -- identity and a drift check against one can only ever report that the tag still matches.
  image_digest          TEXT,
  image_tag             TEXT,
  image_arch            TEXT,                   -- amd64 | arm64
  license_secret_ref    TEXT,
  license_expires_at    TIMESTAMPTZ,

  -- Desired configuration. config_hash is what decides whether an OpAMP push is needed, so steady state
  -- is zero pushes.
  config_hash           TEXT,
  config_pushed_at      TIMESTAMPTZ,

  -- Scaling.
  scaling_mode          TEXT,                   -- manual | auto
  desired_count         INTEGER,
  running_count         INTEGER,
  min_count             INTEGER,
  max_count             INTEGER,
  cpu                   TEXT,
  memory                TEXT,

  -- OpAMP liveness. Reported, never an input to endpoint selection.
  opamp_instance_uid    TEXT,
  opamp_connected_at    TIMESTAMPTZ,
  opamp_last_seen_at    TIMESTAMPTZ,
  agent_health          TEXT,

  status                TEXT        NOT NULL DEFAULT 'Pending',
  message               TEXT,
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),

  -- One regional Collector is hosted in one network and may serve workloads from other networks through
  -- provider reachability. The host network remains metadata, not row identity.
  PRIMARY KEY (connector_name, region),

  CONSTRAINT managed_collectors_mode_check
    CHECK (mode IN ('automatic', 'manual')),
  CONSTRAINT managed_collectors_origin_check
    CHECK (origin IN ('odigos', 'customer')),
  CONSTRAINT managed_collectors_status_check
    CHECK (status IN ('Pending', 'Deploying', 'Ready', 'Degraded', 'Error', 'AwaitingCustomer')),
  CONSTRAINT managed_collectors_tls_mode_check
    CHECK (tls_mode IN ('off', 'platform', 'loadBalancer', 'collector')),
  CONSTRAINT managed_collectors_ingress_tier_check
    CHECK (ingress_tier IN ('network-identities', 'configured-cidrs', 'network-cidr', 'none')),
  CONSTRAINT managed_collectors_endpoint_source_check
    CHECK (endpoint_source IS NULL OR endpoint_source IN ('lb', 'override', 'shared')),
  CONSTRAINT managed_collectors_image_arch_check
    CHECK (image_arch IS NULL OR image_arch IN ('amd64', 'arm64')),
  CONSTRAINT managed_collectors_scaling_mode_check
    CHECK (scaling_mode IS NULL OR scaling_mode IN ('manual', 'auto')),

  -- A digest is an identity and a tag is not. Recording a tag where a digest belongs would make every
  -- drift check report that the tag still matches, whatever bytes are behind it.
  CONSTRAINT managed_collectors_image_digest_shape
    CHECK (image_digest IS NULL OR image_digest ~ '^sha256:[0-9a-f]{64}$'),

  -- An active credential is meaningless without the Collector identity it authenticates.
  CONSTRAINT managed_collectors_credential_needs_identity
    CHECK (active_credential_id IS NULL OR collector_id IS NOT NULL)
);

-- Finding the row a credential belongs to: the recreation sweep clears active_credential_id by
-- collector_id, and reporting joins the two tables the same way.
--
-- Deliberately NOT a foreign key to connector_platform.collector_credentials: that table is platform-wide
-- and this one is per provider schema, so an FK would make dropping a provider schema depend on the
-- platform one. Credential rows are never deleted anyway — revocation is a state, and revoked is
-- terminal — so the referent cannot vanish underneath this column.
CREATE INDEX managed_collectors_collector_id_idx
  ON connector_aws.managed_collectors (collector_id)
  WHERE collector_id IS NOT NULL;

-- Validation provides the useful error before a write. This partial unique index is the concurrency
-- backstop so two simultaneous creates cannot install two regional routing markers.
CREATE UNIQUE INDEX destinations_managed_collector_region_uidx
  ON connector_aws.destinations (connector_name, region)
  WHERE lower(dest_type) = 'odigos-managed-collector';

UPDATE connector_platform.schema_versions
SET version = '4', applied_at = now()
WHERE schema_name = 'connector_aws';

-- +goose Down
UPDATE connector_platform.schema_versions
SET version = '3', applied_at = now()
WHERE schema_name = 'connector_aws';

DROP INDEX connector_aws.managed_collectors_collector_id_idx;
DROP INDEX connector_aws.destinations_managed_collector_region_uidx;
DROP TABLE connector_aws.managed_collectors;
