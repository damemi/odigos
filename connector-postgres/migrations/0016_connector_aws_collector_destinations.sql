-- The destinations a managed Collector exports to, owned by that Collector.
--
-- Not connector_aws.destinations. Those are the WORKLOAD destinations: where a workload's telemetry goes,
-- which may be a regional Collector through the route marker. These say where that Collector sends what
-- it receives, and the two sets are configured independently.
--
-- Scope is (connector_name, region, name), and the region in that key is the point. A connector has one
-- independent Collector per region, so the same name in two regions is two destinations configuring two
-- different pipelines — collapsing them to a name alone would make editing one silently reconfigure the
-- other.
--
-- In the PROVIDER schema, beside managed_collectors (migration 0012), for the same reason the metrics
-- columns are: these rows describe a Collector, and a Collector row lives here.

-- +goose Up

-- A pre-release database may already have recorded version 15 for this table, skipping the released
-- version 13 destination-disabled migration and the new version 15 metrics migration. Restore the
-- skipped columns before advancing the schema marker. Fresh databases already have them.
ALTER TABLE connector_aws.destinations
  ADD COLUMN IF NOT EXISTS disabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE connector_gcp.destinations
  ADD COLUMN IF NOT EXISTS disabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE connector_azure.destinations
  ADD COLUMN IF NOT EXISTS disabled BOOLEAN NOT NULL DEFAULT false;

UPDATE connector_platform.schema_versions
SET version = '3', applied_at = now()
WHERE schema_name IN ('connector_gcp', 'connector_azure');

CREATE TABLE IF NOT EXISTS connector_aws.collector_destinations (
  connector_name  TEXT        NOT NULL,
  region          TEXT        NOT NULL,   -- -> managed_collectors.region; the Collector that exports
  name            TEXT        NOT NULL,   -- unique within the region; names the rendered exporters

  -- An Odigos destination catalog type key (odigos/destinations). Not constrained to a list here: the
  -- catalog grows in the core product, and the connector validates against the catalog it was built
  -- with.
  dest_type       TEXT        NOT NULL,

  -- Field values keyed by the catalog field name, secret fields included — the same shape, and the same
  -- storage, as the workload destinations' config.
  config          JSONB       NOT NULL DEFAULT '{}',
  signals         JSONB       NOT NULL DEFAULT '[]',

  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

  PRIMARY KEY (connector_name, region, name),

  CONSTRAINT collector_destinations_region_not_blank
    CHECK (length(btrim(region)) > 0),
  -- The name becomes part of a Collector component id (e.g. otlp/generic-<name>), whose grammar is
  -- narrower than a free-form string. Refused here too, so a row written around the connector cannot
  -- render a document the Collector rejects.
  CONSTRAINT collector_destinations_name_component_safe
    CHECK (name ~ '^[A-Za-z0-9_-]+$')
);

UPDATE connector_platform.schema_versions
SET version = '8', applied_at = now()
WHERE schema_name = 'connector_aws';

-- +goose Down
DROP TABLE IF EXISTS connector_aws.collector_destinations;

UPDATE connector_platform.schema_versions
SET version = '7', applied_at = now()
WHERE schema_name = 'connector_aws';
