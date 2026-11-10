-- Source-identity resource attributes on the shared transaction inventory.
-- Agents key by the attributes they need (k8s workload, host, instance)
-- instead of dedicated Kubernetes columns. Not part of transaction_id.
-- NOTE: keep semicolons out of comments - the migration runner splits on them.

ALTER TABLE transactions
    ADD COLUMN IF NOT EXISTS resource_attributes Map(String, String);
