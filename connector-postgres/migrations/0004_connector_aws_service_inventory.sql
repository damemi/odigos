-- connector_aws: service discovery inventory (managed infra Odigos is installed on).
--
--   ecs_clusters — ECS clusters classified as aws.ecs-cluster (EC2 agent install) or aws.fargate-cluster
--                  (coverage grouping; per-task instrumentation lives in ecs_tasks)
--
-- registered_ec2_instances and metadata.hasFargate drive classification at read time.

-- +goose Up
CREATE TABLE connector_aws.ecs_clusters (
  connector_name           TEXT        NOT NULL,
  region                   TEXT        NOT NULL,
  cluster_arn              TEXT        NOT NULL,
  cluster_name             TEXT        NOT NULL,
  status                   TEXT,
  registered_ec2_instances INTEGER     NOT NULL DEFAULT 0,
  metadata                 JSONB       NOT NULL DEFAULT '{}',
  first_seen_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at               TIMESTAMPTZ,
  PRIMARY KEY (connector_name, cluster_arn)
);

-- +goose Down
DROP TABLE connector_aws.ecs_clusters;
