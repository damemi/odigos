-- Managed Collector state is provider-owned: the base store selects the provider
-- schema at runtime, while each provider migration instantiates this contract.
-- Keep this AWS instance in lockstep with connectors/base/store/collectors.go.

-- +goose Up
CREATE TABLE connector_aws.managed_collectors (
  connector_name        TEXT        NOT NULL,
  region                TEXT        NOT NULL,
  network_id            TEXT        NOT NULL,
  kind                  TEXT        NOT NULL,
  mode                  TEXT        NOT NULL,
  origin                TEXT        NOT NULL,
  adopted_at            TIMESTAMPTZ,

  collector_id          TEXT,
  credential_ref        TEXT,
  active_credential_id  TEXT,

  workload_ref          TEXT,
  revision_ref          TEXT,
  runtime_ref           TEXT,
  frontend_ref          TEXT,
  backend_refs          JSONB       NOT NULL DEFAULT '[]',
  ingress_ref           TEXT,
  placement_refs        JSONB       NOT NULL DEFAULT '[]',

  endpoint_grpc         TEXT,
  endpoint_http         TEXT,
  endpoint_source       TEXT,
  tls_mode              TEXT        NOT NULL DEFAULT 'off',
  tls_termination       TEXT,
  ingress_tier          TEXT        NOT NULL DEFAULT 'none',

  image_digest          TEXT,
  image_tag             TEXT,
  image_arch            TEXT,
  license_secret_ref    TEXT,
  license_expires_at    TIMESTAMPTZ,
  config_hash           TEXT,
  config_pushed_at      TIMESTAMPTZ,

  scaling_mode          TEXT,
  desired_count         INTEGER,
  running_count         INTEGER,
  min_count             INTEGER,
  max_count             INTEGER,
  cpu                   TEXT,
  memory                TEXT,

  opamp_instance_uid    TEXT,
  opamp_connected_at    TIMESTAMPTZ,
  opamp_last_seen_at    TIMESTAMPTZ,
  agent_health          TEXT,
  status                TEXT        NOT NULL DEFAULT 'Pending',
  message               TEXT,
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),

  PRIMARY KEY (connector_name, region, network_id),
  CONSTRAINT managed_collectors_mode_check
    CHECK (mode IN ('automatic', 'manual')),
  CONSTRAINT managed_collectors_origin_check
    CHECK (origin IN ('odigos', 'customer')),
  CONSTRAINT managed_collectors_status_check
    CHECK (status IN ('Pending', 'Deploying', 'Ready', 'Degraded', 'Error', 'AwaitingCustomer')),
  CONSTRAINT managed_collectors_tls_mode_check
    CHECK (tls_mode IN ('off', 'platform', 'loadBalancer', 'collector')),
  CONSTRAINT managed_collectors_ingress_tier_check
    CHECK (ingress_tier IN ('security-groups', 'configured-cidrs', 'network-cidr', 'none')),
  CONSTRAINT managed_collectors_endpoint_source_check
    CHECK (endpoint_source IS NULL OR endpoint_source IN ('lb', 'override', 'shared')),
  CONSTRAINT managed_collectors_image_arch_check
    CHECK (image_arch IS NULL OR image_arch IN ('amd64', 'arm64')),
  CONSTRAINT managed_collectors_scaling_mode_check
    CHECK (scaling_mode IS NULL OR scaling_mode IN ('manual', 'auto')),
  CONSTRAINT managed_collectors_image_digest_shape
    CHECK (image_digest IS NULL OR image_digest ~ '^sha256:[0-9a-f]{64}$'),
  CONSTRAINT managed_collectors_credential_needs_identity
    CHECK (active_credential_id IS NULL OR collector_id IS NOT NULL)
);

CREATE INDEX managed_collectors_collector_id_idx
  ON connector_aws.managed_collectors (collector_id)
  WHERE collector_id IS NOT NULL;

UPDATE connector_platform.schema_versions
SET version = '4', applied_at = now()
WHERE schema_name = 'connector_aws';

-- +goose Down
UPDATE connector_platform.schema_versions
SET version = '3', applied_at = now()
WHERE schema_name = 'connector_aws';

DROP INDEX connector_aws.managed_collectors_collector_id_idx;
DROP TABLE connector_aws.managed_collectors;
