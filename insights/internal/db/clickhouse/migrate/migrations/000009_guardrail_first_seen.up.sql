-- Track earliest occurrence for guardrail violations the same way anomaly_issues
-- tracks first_seen, so the unified findings feed can show when a violation
-- started (not only when it was last seen).
-- NOTE: keep semicolons out of comments.

ALTER TABLE guardrail_violations
    ADD COLUMN IF NOT EXISTS first_seen SimpleAggregateFunction(min, DateTime64(3));
