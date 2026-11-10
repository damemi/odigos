-- connector_aws: instrumentation intent (sources) and OTLP export targets (destinations).
--
-- Sources are keyed by the STABLE cloud resource identity (kind + external_resource_id, e.g. a Lambda
-- function ARN). service_name (the OTel service.name, user-overridable) and region are mutable
-- attributes, NOT identity: keying on service_name let two distinct workloads that report the same
-- service name in a kind+region collide and silently overwrite each other's intent, and made a
-- service-name override "move" the row. source_name remains a non-key display handle. Destinations are
-- keyed by name; region is stored for fidelity only — the connector selects a single global OTLP target.

-- +goose Up
CREATE SCHEMA connector_aws;

CREATE TABLE connector_aws.sources (
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

CREATE TABLE connector_aws.destinations (
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

-- +goose Down
-- Reverse creation order; the connector_aws schema is dropped last, once 0003–0005 have been reverted.
DROP TABLE connector_aws.destinations;
DROP TABLE connector_aws.sources;
DROP SCHEMA connector_aws;
