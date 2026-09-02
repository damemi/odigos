-- Unify the affirmative resolution term across finding kinds: a guardrail
-- violation that an operator declares legitimate is now "accepted" (the same
-- word anomalies use when their behaviour is folded into the baseline), not
-- "allowed". The lifecycle status is derived at read time from the max-merged
-- decision timestamps, so only the column name changes here -- rename the
-- affirmative-decision timestamp allowed_at to accepted_at to match
-- anomaly_issues.accepted_at. Existing rows keep their timestamps.
-- NOTE: keep semicolons out of comments.

ALTER TABLE guardrail_violations
    RENAME COLUMN allowed_at TO accepted_at;
