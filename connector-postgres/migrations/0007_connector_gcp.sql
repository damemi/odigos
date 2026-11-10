-- connector_gcp: the GCP provider schema. One migration finalizes the schema (mirrors the AWS schema
-- split across 0002-0005, collapsed here since GCP is introduced whole):
--
--   sources / destinations       — instrumentation intent + OTLP export targets (base framework tables)
--   cloud_run_services           — Cloud Run workload discovery inventory (directly instrumentable)
--   gke_clusters / gce_instances — compute-platform discovery inventory (managed infra Odigos installs on)
--   discovery_state              — per-location discovery cursors shared by all detectors
--   connector_owner              — the GCP project a connector name currently owns (rebind purge guard)
--
-- Unlike AWS (per-region scans), GCP detectors scan every location/zone in one call via a location
-- wildcard, so the location column records where a resource lives rather than driving per-region prunes.

-- +goose Up
CREATE SCHEMA connector_gcp;

-- Sources are keyed by the STABLE cloud resource identity (kind + external_resource_id, e.g. a Cloud
-- Run service resource name). service_name (the OTel service.name, user-overridable) and region are
-- mutable attributes, NOT identity. Destinations are keyed by name.
CREATE TABLE connector_gcp.sources (
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

CREATE TABLE connector_gcp.destinations (
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

-- Cloud Run services discovered for per-service instrumentation. resource_name is the full GCP resource
-- name (projects/{p}/locations/{loc}/services/{svc}) and is the stable source identity.
CREATE TABLE connector_gcp.cloud_run_services (
  connector_name TEXT        NOT NULL,
  location       TEXT        NOT NULL,
  resource_name  TEXT        NOT NULL,
  service_id     TEXT        NOT NULL,
  uid            TEXT,
  ingress        TEXT,
  metadata       JSONB       NOT NULL DEFAULT '{}',
  first_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at     TIMESTAMPTZ,
  PRIMARY KEY (connector_name, resource_name)
);

CREATE INDEX cloud_run_services_live_idx ON connector_gcp.cloud_run_services (connector_name) WHERE deleted_at IS NULL;
CREATE INDEX cloud_run_services_deleted_at_idx ON connector_gcp.cloud_run_services (deleted_at) WHERE deleted_at IS NOT NULL;

-- GKE clusters discovered as managed environments (odiglet install target once installation lands).
CREATE TABLE connector_gcp.gke_clusters (
  connector_name TEXT        NOT NULL,
  location       TEXT        NOT NULL,
  resource_name  TEXT        NOT NULL,
  cluster_name   TEXT        NOT NULL,
  status         TEXT,
  metadata       JSONB       NOT NULL DEFAULT '{}',
  first_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at     TIMESTAMPTZ,
  PRIMARY KEY (connector_name, resource_name)
);

CREATE INDEX gke_clusters_live_idx ON connector_gcp.gke_clusters (connector_name) WHERE deleted_at IS NULL;
CREATE INDEX gke_clusters_deleted_at_idx ON connector_gcp.gke_clusters (deleted_at) WHERE deleted_at IS NOT NULL;

-- Compute Engine instances discovered as managed environments (VM agent install target once
-- installation lands). location holds the instance zone.
CREATE TABLE connector_gcp.gce_instances (
  connector_name TEXT        NOT NULL,
  location       TEXT        NOT NULL,
  resource_name  TEXT        NOT NULL,
  instance_name  TEXT        NOT NULL,
  instance_id    TEXT,
  status         TEXT,
  metadata       JSONB       NOT NULL DEFAULT '{}',
  first_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at     TIMESTAMPTZ,
  PRIMARY KEY (connector_name, resource_name)
);

CREATE INDEX gce_instances_live_idx ON connector_gcp.gce_instances (connector_name) WHERE deleted_at IS NULL;
CREATE INDEX gce_instances_deleted_at_idx ON connector_gcp.gce_instances (deleted_at) WHERE deleted_at IS NOT NULL;

-- Per-location discovery cursors, one row per (connector_name, location, kind). GCP detectors scan all
-- locations in a single call, so location may be a wildcard ("-") for a global scan cursor.
CREATE TABLE connector_gcp.discovery_state (
  connector_name           TEXT        NOT NULL,
  location                 TEXT        NOT NULL,
  kind                     TEXT        NOT NULL,
  cursor                   JSONB       NOT NULL DEFAULT '{}',
  last_full_scan_at        TIMESTAMPTZ,
  last_incremental_scan_at TIMESTAMPTZ,
  PRIMARY KEY (connector_name, location, kind)
);

-- Records which GCP project a connector name currently owns. connector_name is reused across CR
-- delete/recreate, so if a recreated connector points at a DIFFERENT project the runtime detects the
-- change at startup and purges the prior project's sources/inventory (cross-project intent leakage).
CREATE TABLE connector_gcp.connector_owner (
  connector_name TEXT        PRIMARY KEY,
  project_id     TEXT        NOT NULL,
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Finalize the connector_gcp schema contract (runtimes verify this version at startup).
INSERT INTO connector_platform.schema_versions (schema_name, version)
VALUES ('connector_gcp', '1');

-- +goose Down
-- Reverse creation order; the connector_gcp version marker is removed first, schema dropped last.
DELETE FROM connector_platform.schema_versions WHERE schema_name = 'connector_gcp';
DROP TABLE connector_gcp.connector_owner;
DROP TABLE connector_gcp.discovery_state;
DROP TABLE connector_gcp.gce_instances;
DROP TABLE connector_gcp.gke_clusters;
DROP TABLE connector_gcp.cloud_run_services;
DROP TABLE connector_gcp.destinations;
DROP TABLE connector_gcp.sources;
DROP SCHEMA connector_gcp;
