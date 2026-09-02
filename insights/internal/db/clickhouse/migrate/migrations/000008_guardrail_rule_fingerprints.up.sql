-- Per-rule stamp of the config a rule's allowlist was built under, e.g.
-- allowed_transactions -> fingerprint of the enabled identity dimensions.
-- The engine skips enforcing a rule whose stamp differs from the scheme it is
-- currently using, so a replica holding a stale allowlist cannot raise
-- violations after an identity-dimension change, without waiting for a reload.
-- NOTE: keep semicolons out of comments.

ALTER TABLE service_guardrails
    ADD COLUMN IF NOT EXISTS rule_fingerprints Map(String, String);
