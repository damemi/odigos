-- Initial schema (pre-release squash of former migrations 000001-000016).
-- The baseline model is deliberately "tall" so ClickHouse does the
-- cross-replica merge in its background merges instead of us locking and
-- rewriting rows:
--   - baseline_set_members  ReplacingMergeTree  -> idempotent set union
--   - baseline_counters     SummingMergeTree     -> additive counts (D8 histogram)
--   - baseline_meta         AggregatingMergeTree -> per-class scalars
-- Every CREATE is guarded with IF NOT EXISTS so a retried (dirty) migration
-- can be replayed safely.
-- NOTE: keep semicolons out of comments - the migration runner splits on them.

CREATE TABLE IF NOT EXISTS transactions (
    transaction_id Int64,
    service        String,
    namespace      String,
    operation      String,
    kind           String
) ENGINE = ReplacingMergeTree
ORDER BY transaction_id;

CREATE TABLE IF NOT EXISTS baseline_set_members (
    transaction_id Int64,
    class          LowCardinality(String),
    member         String
) ENGINE = ReplacingMergeTree
ORDER BY (transaction_id, class, member);

CREATE TABLE IF NOT EXISTS baseline_counters (
    transaction_id Int64,
    class          LowCardinality(String),
    bucket         String,
    count          Int64
) ENGINE = SummingMergeTree(count)
ORDER BY (transaction_id, class, bucket);

CREATE TABLE IF NOT EXISTS baseline_meta (
    transaction_id                   Int64,
    class                            LowCardinality(String),
    observation_count                SimpleAggregateFunction(sum, Int64),
    promoted                         SimpleAggregateFunction(max, UInt8),
    data_schema_version              SimpleAggregateFunction(max, Int32),
    learning_started_at              SimpleAggregateFunction(min, DateTime64(3)),
    last_changed_at                  SimpleAggregateFunction(max, DateTime64(3)),
    observation_count_at_last_change SimpleAggregateFunction(max, Int64)
) ENGINE = AggregatingMergeTree
ORDER BY (transaction_id, class);

CREATE TABLE IF NOT EXISTS trace_observations (
    transaction_id Int64,
    trace_id       String,
    observed_at    DateTime64(3),
    duration_ms    Int64,
    sample_reason  LowCardinality(String),
    raw_otlp       String
) ENGINE = ReplacingMergeTree
ORDER BY (transaction_id, sample_reason, trace_id)
TTL toDateTime(observed_at) + INTERVAL 120 DAY;

CREATE TABLE IF NOT EXISTS transaction_activity (
    transaction_id Int64,
    volume         SimpleAggregateFunction(sum, Int64),
    last_seen      SimpleAggregateFunction(max, DateTime64(3))
) ENGINE = AggregatingMergeTree
ORDER BY transaction_id;

CREATE TABLE IF NOT EXISTS alert_policies (
    scope_type     LowCardinality(String) DEFAULT 'global',
    scope_key      String DEFAULT '',
    policy_id      Int64,
    name           String,
    fire_at_score  Int32 DEFAULT 20,
    signal_weights Map(String, Int32),
    enricher_lists Map(String, Array(String)),
    enabled        UInt8,
    updated_at     DateTime64(3)
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (scope_type, scope_key);

CREATE TABLE IF NOT EXISTS learning_policies (
    class       String,
    scope_type  LowCardinality(String) DEFAULT 'global',
    scope_key   String DEFAULT '',
    match_mode  String,
    conditions  String DEFAULT '',
    min_matches Int64 DEFAULT 0,
    updated_at  DateTime64(3)
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (class, scope_type, scope_key);

CREATE TABLE IF NOT EXISTS anomaly_issues (
    transaction_id    Int64,
    signature         String,
    triggered_classes SimpleAggregateFunction(anyLast, String),
    offending         SimpleAggregateFunction(anyLast, String),
    evidence          SimpleAggregateFunction(anyLast, String),
    policy_id         SimpleAggregateFunction(anyLast, Int64),
    occurrences       SimpleAggregateFunction(sum, UInt64),
    max_score         SimpleAggregateFunction(max, Int32),
    first_seen        SimpleAggregateFunction(min, DateTime64(3)),
    last_seen         SimpleAggregateFunction(max, DateTime64(3)),
    accepted_at       SimpleAggregateFunction(max, DateTime64(3)),
    dismissed_at      SimpleAggregateFunction(max, DateTime64(3)),
    reopened_at       SimpleAggregateFunction(max, DateTime64(3)),
    last_trace_id     SimpleAggregateFunction(anyLast, String)
) ENGINE = AggregatingMergeTree()
ORDER BY (transaction_id, signature);

CREATE TABLE IF NOT EXISTS service_guardrails (
    scope_type LowCardinality(String) DEFAULT 'service',
    scope_key  String DEFAULT '',
    rule_lists Map(String, Array(String)),
    rule_modes Map(String, String),
    -- Per-rule provenance, e.g. allowed_transactions -> auto_transaction_guardrail.
    rule_origins Map(String, String),
    updated_at DateTime64(3)
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (scope_type, scope_key);

CREATE TABLE IF NOT EXISTS guardrail_violations (
    scope_type    LowCardinality(String) DEFAULT 'service',
    scope_key     String,
    rule_key      LowCardinality(String),
    offending     String,
    occurrences   SimpleAggregateFunction(sum, UInt64),
    max_score     SimpleAggregateFunction(max, Int32),
    first_seen    SimpleAggregateFunction(min, DateTime64(3)),
    last_seen     SimpleAggregateFunction(max, DateTime64(3)),
    allowed_at    SimpleAggregateFunction(max, DateTime64(3)), -- renamed to accepted_at in 000005
    dismissed_at  SimpleAggregateFunction(max, DateTime64(3)),
    reopened_at   SimpleAggregateFunction(max, DateTime64(3)),
    last_trace_id SimpleAggregateFunction(anyLast, String)
) ENGINE = AggregatingMergeTree()
ORDER BY (scope_type, scope_key, rule_key, offending);

CREATE TABLE IF NOT EXISTS system_settings (
    id UInt8,
    updated_at DateTime64(3),

    examples_per_transaction UInt32,
    example_sample_interval_seconds UInt32,

    observation_retention_days UInt32,

    max_resident_transactions UInt32,
    max_baseline_set_members UInt32,

    flush_interval_seconds UInt32,

    -- When 1, auto-enable allowed_transactions guardrail once all of a service's
    -- transactions are promoted. Per-service disable uses rule mode "off".
    auto_transaction_guardrail UInt8 DEFAULT 1
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY id;

-- Global default alert policy: fire when risk score >= 20, no weight/list overrides.
INSERT INTO alert_policies
    (scope_type, scope_key, policy_id, name, fire_at_score, signal_weights, enricher_lists, enabled, updated_at)
VALUES
    ('global', '', 1, 'global default', 20, map(), map(), 1, now64(3));

-- Per-class GLOBAL learning defaults for implemented scorers only
-- (D6_attr_relations has no scorer yet and is omitted).
-- Promote once the baseline is unchanged for 300 observations OR 1 week
-- (10080 minutes), whichever comes first.
INSERT INTO learning_policies
    (class, scope_type, scope_key, match_mode, conditions, min_matches, updated_at)
VALUES
    ('D1_call_edges',   'global', '', 'any', '[{"type":"stable_observations","stableObservations":300},{"type":"stable_duration","stableMinutes":10080}]', 1, now64(3)),
    ('D2_egress',       'global', '', 'any', '[{"type":"stable_observations","stableObservations":300},{"type":"stable_duration","stableMinutes":10080}]', 1, now64(3)),
    ('D3_latency',      'global', '', 'any', '[{"type":"stable_observations","stableObservations":300},{"type":"stable_duration","stableMinutes":10080}]', 1, now64(3)),
    ('D4_args_returns', 'global', '', 'any', '[{"type":"stable_observations","stableObservations":300},{"type":"stable_duration","stableMinutes":10080}]', 1, now64(3)),
    ('D5_libraries',    'global', '', 'any', '[{"type":"stable_observations","stableObservations":300},{"type":"stable_duration","stableMinutes":10080}]', 1, now64(3)),
    ('D7_db_access',    'global', '', 'any', '[{"type":"stable_observations","stableObservations":300},{"type":"stable_duration","stableMinutes":10080}]', 1, now64(3)),
    ('D8_payload_size', 'global', '', 'any', '[{"type":"stable_observations","stableObservations":300},{"type":"stable_duration","stableMinutes":10080}]', 1, now64(3));

INSERT INTO system_settings (
    id, updated_at,
    examples_per_transaction, example_sample_interval_seconds,
    observation_retention_days,
    max_resident_transactions, max_baseline_set_members,
    flush_interval_seconds,
    auto_transaction_guardrail
) VALUES (
    1, now64(3),
    10, 30,
    120,
    50000, 5000,
    15,
    1
);
