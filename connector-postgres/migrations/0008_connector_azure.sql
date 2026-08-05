-- connector_azure: the Azure provider schema. One migration finalizes the schema (mirrors the AWS schema
-- split across 0002-0005, collapsed here since Azure is introduced whole):
--
--   sources / destinations              — instrumentation intent + OTLP export targets (base framework tables)
--   container_apps                      — Container Apps workload discovery inventory (directly instrumentable)
--   aks_clusters / virtual_machines     — compute-platform discovery inventory (managed infra Odigos installs on)
--   discovery_state                     — per-location discovery cursors shared by all detectors
--   connector_owner                     — the Azure subscription a connector name currently owns (rebind purge guard)
--
-- Azure ARM list-by-subscription APIs span every resource group, so the location column records where a
-- resource lives (region) rather than driving per-region prunes.

-- +goose Up
CREATE SCHEMA connector_azure;

-- Sources are keyed by the STABLE cloud resource identity (kind + external_resource_id, e.g. a Container
-- App resource ID). service_name (the OTel service.name, user-overridable) and region are mutable
-- attributes, NOT identity. Destinations are keyed by name.
CREATE TABLE connector_azure.sources (
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
  PRIMARY KEY (connector_name, kind, external_resource_id)
);

CREATE TABLE connector_azure.destinations (
  connector_name TEXT        NOT NULL,
  name           TEXT        NOT NULL,
  dest_type      TEXT        NOT NULL,
  region         TEXT,
  config         JSONB       NOT NULL DEFAULT '{}',
  signals        JSONB       NOT NULL DEFAULT '[]',
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (connector_name, name)
);

-- Container Apps discovered for per-app instrumentation. resource_id is the full ARM resource ID
-- (/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.App/containerApps/{name}) and is the
-- stable source identity.
CREATE TABLE connector_azure.container_apps (
  connector_name TEXT        NOT NULL,
  location       TEXT        NOT NULL,
  resource_group TEXT        NOT NULL DEFAULT '',
  resource_id    TEXT        NOT NULL,
  app_name       TEXT        NOT NULL,
  provisioning_state TEXT,
  metadata       JSONB       NOT NULL DEFAULT '{}',
  first_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at     TIMESTAMPTZ,
  PRIMARY KEY (connector_name, resource_id)
);

CREATE INDEX container_apps_live_idx ON connector_azure.container_apps (connector_name) WHERE deleted_at IS NULL;
CREATE INDEX container_apps_deleted_at_idx ON connector_azure.container_apps (deleted_at) WHERE deleted_at IS NOT NULL;

-- AKS clusters discovered as managed environments (odiglet install target once installation lands).
CREATE TABLE connector_azure.aks_clusters (
  connector_name TEXT        NOT NULL,
  location       TEXT        NOT NULL,
  resource_group TEXT        NOT NULL DEFAULT '',
  resource_id    TEXT        NOT NULL,
  cluster_name   TEXT        NOT NULL,
  status         TEXT,
  metadata       JSONB       NOT NULL DEFAULT '{}',
  first_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at     TIMESTAMPTZ,
  PRIMARY KEY (connector_name, resource_id)
);

CREATE INDEX aks_clusters_live_idx ON connector_azure.aks_clusters (connector_name) WHERE deleted_at IS NULL;
CREATE INDEX aks_clusters_deleted_at_idx ON connector_azure.aks_clusters (deleted_at) WHERE deleted_at IS NOT NULL;

-- Azure Virtual Machines discovered as managed environments (VM agent install target once installation
-- lands). location holds the Azure region.
CREATE TABLE connector_azure.virtual_machines (
  connector_name TEXT        NOT NULL,
  location       TEXT        NOT NULL,
  resource_group TEXT        NOT NULL DEFAULT '',
  resource_id    TEXT        NOT NULL,
  vm_name        TEXT        NOT NULL,
  vm_id          TEXT,
  status         TEXT,
  metadata       JSONB       NOT NULL DEFAULT '{}',
  first_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at     TIMESTAMPTZ,
  PRIMARY KEY (connector_name, resource_id)
);

CREATE INDEX virtual_machines_live_idx ON connector_azure.virtual_machines (connector_name) WHERE deleted_at IS NULL;
CREATE INDEX virtual_machines_deleted_at_idx ON connector_azure.virtual_machines (deleted_at) WHERE deleted_at IS NOT NULL;

-- Per-location discovery cursors, one row per (connector_name, location, kind). Azure detectors scan the
-- whole subscription in a single call, so location may be a wildcard ("-") for a global scan cursor.
CREATE TABLE connector_azure.discovery_state (
  connector_name           TEXT        NOT NULL,
  location                 TEXT        NOT NULL,
  kind                     TEXT        NOT NULL,
  cursor                   JSONB       NOT NULL DEFAULT '{}',
  last_full_scan_at        TIMESTAMPTZ,
  last_incremental_scan_at TIMESTAMPTZ,
  PRIMARY KEY (connector_name, location, kind)
);

-- Records which Azure subscription a connector name currently owns. connector_name is reused across CR
-- delete/recreate, so if a recreated connector points at a DIFFERENT subscription the runtime detects the
-- change at startup and purges the prior subscription's sources/inventory (cross-subscription intent leakage).
CREATE TABLE connector_azure.connector_owner (
  connector_name  TEXT        PRIMARY KEY,
  subscription_id TEXT        NOT NULL,
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Finalize the connector_azure schema contract (runtimes verify this version at startup).
INSERT INTO connector_platform.schema_versions (schema_name, version)
VALUES ('connector_azure', '1');

-- +goose Down
-- Reverse creation order; the connector_azure version marker is removed first, schema dropped last.
DELETE FROM connector_platform.schema_versions WHERE schema_name = 'connector_azure';
DROP TABLE connector_azure.connector_owner;
DROP TABLE connector_azure.discovery_state;
DROP TABLE connector_azure.virtual_machines;
DROP TABLE connector_azure.aks_clusters;
DROP TABLE connector_azure.container_apps;
DROP TABLE connector_azure.destinations;
DROP TABLE connector_azure.sources;
DROP SCHEMA connector_azure;
