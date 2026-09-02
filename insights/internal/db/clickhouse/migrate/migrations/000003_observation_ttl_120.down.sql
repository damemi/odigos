-- Restore the pre-120-day table TTL. Settings are left at 120.
-- NOTE: keep semicolons out of comments.

ALTER TABLE trace_observations
    MODIFY TTL toDateTime(observed_at) + INTERVAL 7 DAY;
