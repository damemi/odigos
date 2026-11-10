-- connector_mulesoft: the MuleSoft provider schema, introduced whole. The base tables carry, from the start, the
-- columns later migrations added to the other providers (sources.deleted_at from 0010, destinations.disabled from
-- 0013), so this schema needs no catch-up ALTERs.
--
--   sources / destinations  — instrumentation intent + OTLP export targets (base framework tables)
--   applications            — Mule application inventory: CloudHub 2.0, Runtime Fabric and hybrid, one table with a
--                             kind column because one Application Manager scan yields both CloudHub 2.0 and RTF rows
--   discovery_state         — per-environment discovery cursors
--   connector_owner         — the Anypoint organization a connector name currently owns (rebind purge guard)

-- +goose Up
CREATE SCHEMA connector_mulesoft;

-- Sources are keyed by the stable resource identity (kind + external_resource_id: an Application Manager
-- deployment id, or "<environment id>/<application id>" for hybrid). service_name and region are mutable
-- attributes. For this provider region holds the Anypoint ENVIRONMENT name, which is what tells one app name in
-- Sandbox apart from the same name in Production.
CREATE TABLE connector_mulesoft.sources (
  connector_name       TEXT        NOT NULL,
  source_name          TEXT        NOT NULL,
  kind                 TEXT        NOT NULL,
  service_name         TEXT        NOT NULL DEFAULT '',
  enabled              BOOLEAN     NOT NULL DEFAULT true,
  external_resource_id TEXT        NOT NULL,
  region               TEXT        NOT NULL DEFAULT '',
  config               JSONB       NOT NULL DEFAULT '{}',
  created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at           TIMESTAMPTZ,
  PRIMARY KEY (connector_name, kind, external_resource_id)
);

CREATE INDEX sources_live_idx ON connector_mulesoft.sources (connector_name) WHERE deleted_at IS NULL;
CREATE INDEX sources_deleted_idx ON connector_mulesoft.sources (connector_name) WHERE deleted_at IS NOT NULL;

CREATE TABLE connector_mulesoft.destinations (
  connector_name TEXT        NOT NULL,
  name           TEXT        NOT NULL,
  dest_type      TEXT        NOT NULL,
  region         TEXT,
  config         JSONB       NOT NULL DEFAULT '{}',
  signals        JSONB       NOT NULL DEFAULT '[]',
  disabled       BOOLEAN     NOT NULL DEFAULT false,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (connector_name, name)
);

-- Mule applications. external_id is the source identity. environment_name is the report "region" (see sources);
-- target_region is the cloud region of the CloudHub 2.0 space or fabric, kept for future same-region routing.
CREATE TABLE connector_mulesoft.applications (
  connector_name   TEXT        NOT NULL,
  external_id      TEXT        NOT NULL,
  kind             TEXT        NOT NULL,
  app_name         TEXT        NOT NULL,
  environment_id   TEXT        NOT NULL,
  environment_name TEXT        NOT NULL,
  target_id        TEXT        NOT NULL DEFAULT '',
  target_name      TEXT        NOT NULL DEFAULT '',
  target_type      TEXT        NOT NULL DEFAULT '',
  target_region    TEXT        NOT NULL DEFAULT '',
  runtime_version  TEXT        NOT NULL DEFAULT '',
  status           TEXT        NOT NULL DEFAULT '',
  metadata         JSONB       NOT NULL DEFAULT '{}',
  first_seen_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at       TIMESTAMPTZ,
  PRIMARY KEY (connector_name, external_id)
);

CREATE INDEX applications_live_idx ON connector_mulesoft.applications (connector_name, kind) WHERE deleted_at IS NULL;
CREATE INDEX applications_deleted_at_idx ON connector_mulesoft.applications (deleted_at) WHERE deleted_at IS NOT NULL;

-- Discovery cursors, one row per (connector_name, location, kind); location is an environment id.
CREATE TABLE connector_mulesoft.discovery_state (
  connector_name           TEXT        NOT NULL,
  location                 TEXT        NOT NULL,
  kind                     TEXT        NOT NULL,
  cursor                   JSONB       NOT NULL DEFAULT '{}',
  last_full_scan_at        TIMESTAMPTZ,
  last_incremental_scan_at TIMESTAMPTZ,
  PRIMARY KEY (connector_name, location, kind)
);

-- Records which Anypoint organization a connector name currently owns, so a connector recreated for a different
-- organization purges the previous one's sources and inventory at startup.
CREATE TABLE connector_mulesoft.connector_owner (
  connector_name  TEXT        PRIMARY KEY,
  organization_id TEXT        NOT NULL,
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO connector_platform.schema_versions (schema_name, version)
VALUES ('connector_mulesoft', '1');

-- +goose Down
DELETE FROM connector_platform.schema_versions WHERE schema_name = 'connector_mulesoft';
DROP TABLE connector_mulesoft.connector_owner;
DROP TABLE connector_mulesoft.discovery_state;
DROP TABLE connector_mulesoft.applications;
DROP TABLE connector_mulesoft.destinations;
DROP TABLE connector_mulesoft.sources;
DROP SCHEMA connector_mulesoft;
