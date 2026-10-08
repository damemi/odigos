-- A provider-independent record of whether a Collector declaration has reached cloud
-- infrastructure. The provider row may not exist yet when create returns, and it is
-- removed after teardown, so neither that row nor its refs can represent both ends
-- of the lifecycle. Keep the final state for operational diagnosis and reset it on
-- a later create of the same region.

-- +goose Up
CREATE TABLE connector_platform.collector_resource_state (
  connector_name TEXT NOT NULL,
  provider_schema TEXT NOT NULL,
  region TEXT NOT NULL,
  state TEXT NOT NULL,
  detail TEXT,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (connector_name, provider_schema, region),
  CONSTRAINT collector_resource_state_check
    CHECK (state IN ('not-created', 'provisioning', 'created', 'orphaned', 'destroyed'))
);

-- Existing rows with recorded infrastructure need cloud teardown. An empty
-- row with no deployment parameters is the legacy UI failure: no cloud work
-- could have started, so its declaration may be removed locally. Every other
-- row without refs is ambiguous and remains orphaned.
INSERT INTO connector_platform.collector_resource_state
  (connector_name, provider_schema, region, state, detail)
SELECT connector_name, 'connector_aws', region,
  CASE WHEN workload_ref IS NOT NULL OR revision_ref IS NOT NULL OR
            runtime_ref IS NOT NULL OR frontend_ref IS NOT NULL OR
            ingress_ref IS NOT NULL OR backend_refs <> '[]'::jsonb OR
            placement_refs <> '[]'::jsonb
       THEN 'created'
       WHEN mode = 'automatic' AND origin = 'odigos' AND
            collector_id IS NULL AND credential_ref IS NULL AND
            active_credential_id IS NULL AND license_secret_ref IS NULL AND
            COALESCE(cfg.config #>> ARRAY['collector', 'regions', region, 'deploymentRoleRef'], '') = '' AND
            COALESCE(cfg.config #>> ARRAY['collector', 'regions', region, 'networkId'], '') = '' AND
            COALESCE(cfg.config #>> ARRAY['collector', 'regions', region, 'hostNetworkId'], '') = '' AND
            COALESCE(cfg.config #> ARRAY['collector', 'regions', region, 'provider'], '{}'::jsonb) = '{}'::jsonb AND
            COALESCE(cfg.config #>> '{collector,defaults,deploymentRoleRef}', '') = '' AND
            COALESCE(cfg.config #>> '{collector,defaults,networkId}', '') = '' AND
            COALESCE(cfg.config #>> '{collector,defaults,hostNetworkId}', '') = '' AND
            COALESCE(cfg.config #> '{collector,defaults,provider}', '{}'::jsonb) = '{}'::jsonb
       THEN 'not-created'
       ELSE 'orphaned' END,
  'Migrated from a Collector record; provisioning history is unavailable'
FROM connector_aws.managed_collectors AS mc
LEFT JOIN connector_platform.connector_config AS cfg USING (connector_name);

UPDATE connector_platform.schema_versions
SET version = '4', applied_at = now()
WHERE schema_name = 'connector_platform';

-- +goose Down
UPDATE connector_platform.schema_versions
SET version = '3', applied_at = now()
WHERE schema_name = 'connector_platform';

DROP TABLE connector_platform.collector_resource_state;
