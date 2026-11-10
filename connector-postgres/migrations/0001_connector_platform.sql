-- connector_platform: shared platform schema for all cloud connectors.
--
--   schema_versions     — version markers each runtime verifies at startup (never migrates itself)
--   runtime_heartbeat   — live registration + health cache (replaces Redis live-status)
--   installation_audit  — install / upgrade audit trail

-- +goose Up
CREATE SCHEMA connector_platform;

CREATE TABLE connector_platform.schema_versions (
  schema_name TEXT        PRIMARY KEY,
  version     TEXT        NOT NULL,
  applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE connector_platform.runtime_heartbeat (
  connector_name  TEXT        PRIMARY KEY,
  runtime_id      TEXT        NOT NULL,
  pod_name        TEXT,
  status          TEXT        NOT NULL,
  version         TEXT,
  supported_kinds JSONB       NOT NULL DEFAULT '[]',
  health          JSONB       NOT NULL DEFAULT '{}',
  last_seen_at    TIMESTAMPTZ NOT NULL,
  updated_at      TIMESTAMPTZ NOT NULL
);

CREATE TABLE connector_platform.installation_audit (
  id             BIGSERIAL   PRIMARY KEY,
  connector_name TEXT        NOT NULL,
  event          TEXT        NOT NULL,
  actor          TEXT,
  detail         JSONB       NOT NULL DEFAULT '{}',
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Operational connector configuration (tunables): a per-connector JSON object of ONLY the
-- explicitly-overridden keys, merged over code defaults at read time. This is the DB-backed config the
-- agents-api config/updateConfig operations expose; the connector reads it live (no restart). Bootstrap
-- config (credentials, account, provider, DSN, capabilities) stays on the CR/command line, not here.
CREATE TABLE connector_platform.connector_config (
  connector_name TEXT        PRIMARY KEY,
  config         JSONB       NOT NULL DEFAULT '{}',
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO connector_platform.schema_versions (schema_name, version)
VALUES ('connector_platform', '1');

-- +goose Down
-- Tables dropped in reverse creation order; dropping schema_versions also removes the version marker.
DROP TABLE connector_platform.connector_config;
DROP TABLE connector_platform.installation_audit;
DROP TABLE connector_platform.runtime_heartbeat;
DROP TABLE connector_platform.schema_versions;
DROP SCHEMA connector_platform;
