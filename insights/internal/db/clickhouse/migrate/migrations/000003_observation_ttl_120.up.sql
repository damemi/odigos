-- Align sample-trace TTL with the 120-day findings lookback ceiling.
-- Existing clusters still have the original 7-day table TTL and a 30-day
-- settings seed. MODIFY TTL is metadata-only. The settings insert is a new
-- ReplacingMergeTree version so GetSystemSettings reads 120 after migrate.
-- NOTE: keep semicolons out of comments.

ALTER TABLE trace_observations
    MODIFY TTL toDateTime(observed_at) + INTERVAL 120 DAY;

INSERT INTO system_settings (
    id, updated_at,
    examples_per_transaction, example_sample_interval_seconds,
    observation_retention_days,
    max_resident_transactions, max_baseline_set_members,
    flush_interval_seconds
)
SELECT
    id,
    now64(3),
    examples_per_transaction,
    example_sample_interval_seconds,
    120,
    max_resident_transactions,
    max_baseline_set_members,
    flush_interval_seconds
FROM system_settings FINAL
WHERE id = 1;
