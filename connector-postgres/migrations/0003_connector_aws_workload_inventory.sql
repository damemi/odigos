-- connector_aws: workload discovery inventory (directly instrumentable resources).
--
--   lambda_functions — Lambda functions discovered for per-function instrumentation
--   ecs_tasks        — Fargate-cluster tasks discovered for per-task sidecar instrumentation
--
-- EC2-backed tasks on pure ecs-cluster environments are covered by the cluster agent (service domain)
-- and are not stored here.

-- +goose Up
CREATE TABLE connector_aws.lambda_functions (
  connector_name TEXT        NOT NULL,
  region         TEXT        NOT NULL,
  function_arn   TEXT        NOT NULL,
  function_name  TEXT        NOT NULL,
  runtime        TEXT,
  architecture   TEXT,
  role_arn       TEXT,
  tracing_mode   TEXT,
  layers         JSONB       NOT NULL DEFAULT '[]',
  env_hash       TEXT,
  config_hash    TEXT,
  metadata       JSONB       NOT NULL DEFAULT '{}',
  first_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at     TIMESTAMPTZ,
  PRIMARY KEY (connector_name, function_arn)
);

-- Every read filters "connector_name = $1 AND deleted_at IS NULL". A partial index over the live rows
-- keeps those scans off the soft-deleted (deleted_at IS NOT NULL) rows, which accumulate until purged.
CREATE INDEX lambda_functions_live_idx ON connector_aws.lambda_functions (connector_name) WHERE deleted_at IS NULL;
-- Retention purge (PurgeDeletedBefore) deletes by deleted_at; index it so the purge is a range scan.
CREATE INDEX lambda_functions_deleted_at_idx ON connector_aws.lambda_functions (deleted_at) WHERE deleted_at IS NOT NULL;

CREATE TABLE connector_aws.ecs_tasks (
  connector_name  TEXT        NOT NULL,
  region          TEXT        NOT NULL,
  cluster_arn     TEXT        NOT NULL,
  task_arn        TEXT        NOT NULL,
  service_name    TEXT,
  task_definition TEXT,
  launch_type     TEXT,
  last_status     TEXT,
  desired_status  TEXT,
  metadata        JSONB       NOT NULL DEFAULT '{}',
  first_seen_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at      TIMESTAMPTZ,
  PRIMARY KEY (connector_name, task_arn)
);

-- ECS tasks churn on every deploy/scale, so soft-deleted rows accrue fast. Same live/purge index pair as
-- lambda_functions (see above).
CREATE INDEX ecs_tasks_live_idx ON connector_aws.ecs_tasks (connector_name) WHERE deleted_at IS NULL;
CREATE INDEX ecs_tasks_deleted_at_idx ON connector_aws.ecs_tasks (deleted_at) WHERE deleted_at IS NOT NULL;

-- +goose Down
-- Reverse creation order; dropping each table also drops its partial indexes.
DROP TABLE connector_aws.ecs_tasks;
DROP TABLE connector_aws.lambda_functions;
