DROP TABLE IF EXISTS notification_attempts;
DROP TABLE IF EXISTS notification_cursors;
DROP TABLE IF EXISTS notification_dispatch;
ALTER TABLE service_guardrails DROP COLUMN IF EXISTS integration_ids;
ALTER TABLE alert_policies DROP COLUMN IF EXISTS integration_ids;
DROP TABLE IF EXISTS integrations;
