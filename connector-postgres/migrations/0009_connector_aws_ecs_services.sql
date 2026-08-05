-- connector_aws: ECS service inventory for Fargate service instrumentation.
--
--   ecs_services — Fargate ECS services discovered per region (via ecs:ListServices/DescribeServices).
--
-- Fargate task ARNs recycle on every deploy/scale, so per-service instrumentation state (instrumented,
-- detected runtime, error, phase — kept in metadata, mirroring lambda_functions) needs a home stable
-- across deploys: the (cluster, service) identity. The PRIMARY KEY matches the external-id convention
-- already used by aws.fargate-task sources — cluster_arn || '/' || service_name — NOT the service ARN
-- (old-format service ARNs lack the cluster segment, so they are not a safe join key).
--
-- EC2-backed services/tasks are NOT stored here: EC2-launch-type ECS is covered by the cluster agent
-- (compute-platform domain), Fargate by the per-task sidecar this table backs.

-- +goose Up
CREATE TABLE connector_aws.ecs_services (
  connector_name        TEXT        NOT NULL,
  region                TEXT        NOT NULL,
  cluster_arn           TEXT        NOT NULL,
  service_name          TEXT        NOT NULL,
  service_arn           TEXT,
  task_definition       TEXT,        -- taskDefinition ARN the service currently points at (DescribeServices)
  launch_type           TEXT,        -- FARGATE | '' (capacity-provider services report no launch type)
  deployment_controller TEXT,        -- ECS | CODE_DEPLOY | EXTERNAL (only ECS supports UpdateService task-definition rolls)
  status                TEXT,
  desired_count         INTEGER     NOT NULL DEFAULT 0,
  running_count         INTEGER     NOT NULL DEFAULT 0,
  metadata              JSONB       NOT NULL DEFAULT '{}',
  first_seen_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at            TIMESTAMPTZ,
  PRIMARY KEY (connector_name, cluster_arn, service_name)
);

-- Same live/purge partial index pair as the other inventory tables (see 0003).
CREATE INDEX ecs_services_live_idx ON connector_aws.ecs_services (connector_name) WHERE deleted_at IS NULL;
CREATE INDEX ecs_services_deleted_at_idx ON connector_aws.ecs_services (deleted_at) WHERE deleted_at IS NOT NULL;

UPDATE connector_platform.schema_versions
SET version = '2', applied_at = now()
WHERE schema_name = 'connector_aws';

-- +goose Down
UPDATE connector_platform.schema_versions
SET version = '1', applied_at = now()
WHERE schema_name = 'connector_aws';

DROP TABLE connector_aws.ecs_services;
