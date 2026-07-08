-- connector_aws: per-region discovery cursors shared by workload and service detectors.
--
-- Each (connector_name, region, kind) row tracks the last full / incremental scan timestamps for one
-- discovery type (e.g. aws.lambda, aws.fargate-task, aws.ecs-cluster).

-- +goose Up
CREATE TABLE connector_aws.discovery_state (
  connector_name           TEXT        NOT NULL,
  region                   TEXT        NOT NULL,
  kind                     TEXT        NOT NULL,
  cursor                   JSONB       NOT NULL DEFAULT '{}',
  last_full_scan_at        TIMESTAMPTZ,
  last_incremental_scan_at TIMESTAMPTZ,
  PRIMARY KEY (connector_name, region, kind)
);

-- Records which AWS account a connector name currently owns. connector_name is reused across CR
-- delete/recreate, so if a recreated connector points at a DIFFERENT account the runtime detects the
-- change at startup and purges the prior account's sources/inventory — otherwise stale enabled sources
-- could auto-instrument look-alike workloads in the new account (cross-account intent leakage).
CREATE TABLE connector_aws.connector_owner (
  connector_name TEXT        PRIMARY KEY,
  account_id     TEXT        NOT NULL,
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Finalize the connector_aws schema contract (runtimes verify this version at startup).
INSERT INTO connector_platform.schema_versions (schema_name, version)
VALUES ('connector_aws', '1');

-- +goose Down
-- Reverse creation order; the connector_aws version marker is removed first.
DELETE FROM connector_platform.schema_versions WHERE schema_name = 'connector_aws';
DROP TABLE connector_aws.connector_owner;
DROP TABLE connector_aws.discovery_state;
