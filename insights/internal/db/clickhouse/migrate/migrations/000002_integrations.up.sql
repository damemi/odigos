-- Findings integrations: third-party backends that receive findings, the
-- per-scope routing to them, and the notifier's durable delivery state.

-- One row per configured backend. type names a catalog descriptor in the
-- notify package. config is JSON keyed by that descriptor's fields (secret
-- fields are stored as-is and redacted on read). filters is JSON with the
-- common delivery filters (min_severity, kinds, notify_on_resolve).
CREATE TABLE IF NOT EXISTS integrations (
    integration_id String,
    name           String,
    type           LowCardinality(String),
    enabled        UInt8 DEFAULT 1,
    config         String DEFAULT '{}',
    filters        String DEFAULT '{}',
    created_at     DateTime64(3),
    updated_at     DateTime64(3)
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY integration_id;

-- Per-scope routing: which integrations a fired finding is delivered to. The
-- most specific scope wins, like every other scoped field. An empty list on an
-- override falls back to the global alert policy's list (the default route).
ALTER TABLE alert_policies ADD COLUMN IF NOT EXISTS integration_ids Array(String) DEFAULT [];
ALTER TABLE service_guardrails ADD COLUMN IF NOT EXISTS integration_ids Array(String) DEFAULT [];

-- Delivery ledger: one row per (integration, finding) with the last action
-- sent, the retry schedule, and the external id a backend returned (ticketing).
-- This is what makes "new vs repeat" and "resolve once" decidable without any
-- engine state, and what the retry loop drives from.
CREATE TABLE IF NOT EXISTS notification_dispatch (
    integration_id      String,
    finding_key         String,
    last_action         LowCardinality(String),
    state               LowCardinality(String),
    attempts            UInt16 DEFAULT 0,
    next_attempt_at     DateTime64(3),
    last_error          String DEFAULT '',
    sent_at             DateTime64(3),
    occurrences_at_send UInt64 DEFAULT 0,
    severity_at_send    LowCardinality(String) DEFAULT '',
    status_at_send      LowCardinality(String) DEFAULT '',
    external_id         String DEFAULT '',
    updated_at          DateTime64(3)
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (integration_id, finding_key);

-- Notifier progress per integration: the newest finding activity timestamp it
-- has processed. Written only by the elected leader, so there is no
-- last-writer-wins between replicas. leader is informational.
CREATE TABLE IF NOT EXISTS notification_cursors (
    integration_id String,
    cursor         DateTime64(3),
    last_run_at    DateTime64(3),
    leader         String DEFAULT '',
    updated_at     DateTime64(3)
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY integration_id;

-- Audit trail: one append per delivery attempt (success or failure, including
-- the test endpoint). finding_key is empty for a batch-level failure. Bounded
-- by TTL so it cannot grow with traffic.
CREATE TABLE IF NOT EXISTS notification_attempts (
    integration_id String,
    finding_key    String DEFAULT '',
    action         LowCardinality(String),
    attempt        UInt16 DEFAULT 1,
    outcome        LowCardinality(String),
    http_status    Int32 DEFAULT 0,
    error          String DEFAULT '',
    duration_ms    UInt32 DEFAULT 0,
    event_count    UInt32 DEFAULT 1,
    attempted_at   DateTime64(3)
) ENGINE = MergeTree
ORDER BY (integration_id, attempted_at)
TTL toDateTime(attempted_at) + toIntervalDay(30);
