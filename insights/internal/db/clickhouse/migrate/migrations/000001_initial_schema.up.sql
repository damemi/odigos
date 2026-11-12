-- Initial schema (pre-release squash of former migrations 000001-000009).
-- The baseline model is deliberately "tall" so ClickHouse does the
-- cross-replica merge in its background merges instead of us locking and
-- rewriting rows:
--   - baseline_set_members  ReplacingMergeTree  -> idempotent set union
--   - baseline_counters     SummingMergeTree     -> additive counts (D7 histogram)
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
    saturated                        SimpleAggregateFunction(max, UInt8),
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
    duration_us    Int64,
    sample_reason  LowCardinality(String),
    raw_otlp       String
) ENGINE = ReplacingMergeTree
ORDER BY (transaction_id, sample_reason, trace_id)
-- Relation samples only feed the recommendation miner, so they age out after
-- 14 days regardless of the operator-set retention for examples and evidence.
TTL toDateTime(observed_at) + toIntervalDay(if(sample_reason = 'relation', 14, 120));

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
    -- Delivery to integrations: on by default. notify_muted sends this
    -- policy's anomalies nowhere, excluded_integration_ids skips only those.
    notify_muted             UInt8 DEFAULT 0,
    excluded_integration_ids Array(String) DEFAULT [],
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
    -- Per-rule stamp of the config a rule's allowlist was built under, e.g.
    -- allowed_transactions -> fingerprint of the enabled identity dimensions.
    rule_fingerprints Map(String, String),
    -- Per-rule structured payloads keyed by rule_key, JSON-encoded. Used by the
    -- attribute_correlation rule to store its cross-span correlation specs.
    rule_correlations Map(String, String),
    -- Delivery to integrations, same semantics as alert_policies.
    notify_muted             UInt8 DEFAULT 0,
    excluded_integration_ids Array(String) DEFAULT [],
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
    accepted_at   SimpleAggregateFunction(max, DateTime64(3)),
    dismissed_at  SimpleAggregateFunction(max, DateTime64(3)),
    reopened_at   SimpleAggregateFunction(max, DateTime64(3)),
    last_trace_id SimpleAggregateFunction(anyLast, String)
) ENGINE = AggregatingMergeTree()
ORDER BY (scope_type, scope_key, rule_key, offending);

-- Guardrail recommendations: rules Insights suggests instead of waiting for an
-- operator to author them. Scoped like guardrails (scope_type/scope_key).
-- transaction_id is the resolved id for transaction scope (0 otherwise) and
-- joins the transactions table. namespace/service are stored for service scope
-- (empty otherwise, the transaction join supplies them). Mining stats are
-- rewritten on every run, state is the operator's decision and is carried over
-- by the writer. The payload is kind-specific JSON: spec (a CorrelationSpec)
-- for attribute_correlation, rules (the allowlists to enforce) plus
-- applied_rules (which of them an apply turned on) for service_guardrail.
CREATE TABLE IF NOT EXISTS guardrail_recommendations (
    scope_type        LowCardinality(String) DEFAULT 'transaction',
    scope_key         String,
    transaction_id    Int64,
    namespace         String DEFAULT '',
    service           String DEFAULT '',
    recommendation_id String,
    kind              LowCardinality(String),
    state             LowCardinality(String) DEFAULT 'open',
    stale             UInt8 DEFAULT 0,
    rank              Int32 DEFAULT 0,
    spec              String,
    transform         LowCardinality(String),
    transport         UInt8 DEFAULT 0,
    rules             String DEFAULT '[]',
    applied_rules     String DEFAULT '[]',
    observed          Int32,
    held              Int32,
    distinct_values   Int32,
    sample_count      Int32,
    examples          String DEFAULT '[]',
    mined_at          DateTime64(3),
    created_at        DateTime64(3),
    updated_at        DateTime64(3)
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (scope_type, scope_key, recommendation_id);

-- Shadow-evaluation counters: how a still-open recommendation fares against
-- live traffic. Summed across replicas and flush cycles like the other counters.
-- rule_key splits a service_guardrail card's counts per proposed rule. It is
-- empty for correlation rows, which count as a whole.
CREATE TABLE IF NOT EXISTS recommendation_live_counts (
    scope_type        LowCardinality(String) DEFAULT 'transaction',
    scope_key         String,
    transaction_id    Int64,
    recommendation_id String,
    rule_key          LowCardinality(String) DEFAULT '',
    held              SimpleAggregateFunction(sum, UInt64),
    broken            SimpleAggregateFunction(sum, UInt64),
    first_at          SimpleAggregateFunction(min, DateTime64(3)),
    last_at           SimpleAggregateFunction(max, DateTime64(3))
) ENGINE = AggregatingMergeTree()
ORDER BY (scope_type, scope_key, recommendation_id, rule_key);

-- One row per transaction: the newest mining run, which also acts as the soft
-- claim replicas use to avoid mining the same transaction at once (mined_at is
-- zero while a run is in progress).
CREATE TABLE IF NOT EXISTS recommendation_mining_runs (
    transaction_id Int64,
    claimed_at     DateTime64(3),
    mined_at       DateTime64(3),
    sample_count   Int32,
    candidates     Int32,
    updated_at     DateTime64(3)
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY transaction_id;

-- Service-graph edges: the client -> server topology the vendored servicegraph
-- connector emits in the Odigos gateway, shipped to insights over OTLP and
-- stored here so the UI can draw a per-anomaly blast-radius subgraph.
-- ReplacingMergeTree(observed_at) collapses repeated inserts for the same key to
-- the newest observed_at, so re-sending the same edge is idempotent.
-- server_is_virtual marks an uninstrumented dependency (DB, queue, external API)
-- inferred as a virtual node. client_is_virtual marks the connector's synthetic
-- "user" node so blast-radius BFS treats it as a leaf.
CREATE TABLE IF NOT EXISTS service_graph_edges (
    client_namespace  LowCardinality(String),
    client_service    String,
    server_namespace  LowCardinality(String),
    server_service    String,
    connection_type   LowCardinality(String),
    server_is_virtual UInt8,
    client_is_virtual UInt8 DEFAULT 0,
    collector_pod     LowCardinality(String),
    request_count     UInt64,
    failed_count      UInt64,
    observed_at       DateTime64(3)
) ENGINE = ReplacingMergeTree(observed_at)
ORDER BY (server_namespace, server_service, client_namespace, client_service, connection_type, collector_pod);

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
    auto_transaction_guardrail UInt8 DEFAULT 1,

    -- Configurable low-cardinality transaction identity dimensions, JSON array of
    -- {key, enabled}. Nil/missing is filled by DefaultTransactionIdentityDimensions.
    transaction_identity_dimensions String
        DEFAULT '[{"key":"http.response.status_code","enabled":true},{"key":"rpc.grpc.status_code","enabled":true}]',

    -- Bumpable epoch so multi-replica engines clear in-memory transaction state
    -- after an identity-dimension reset wipe. Not an operator-facing setting.
    transaction_inventory_epoch UInt32 DEFAULT 0,

    -- Relation samples kept per learning transaction for the recommendation
    -- miner, and the bar a mined correlation must clear to be recommended.
    relation_samples_per_transaction UInt32 DEFAULT 50,
    relation_sample_interval_seconds UInt32 DEFAULT 5,
    recommendation_min_distinct UInt32 DEFAULT 5,
    recommendation_min_hold_percent UInt32 DEFAULT 98,

    -- Per-service cap on distinct transactions. Unseen operations past it are
    -- not admitted to the inventory, so span names that carry request ids
    -- cannot grow the transactions and baseline tables without bound.
    max_transactions_per_service UInt32 DEFAULT 200
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY id;

-- Findings integrations: third-party backends that receive findings. type
-- names a catalog descriptor in the notify package. config is JSON keyed by
-- that descriptor's fields (secret fields are stored as-is and redacted on
-- read). filters is JSON with the delivery filters (min_severity, kinds,
-- notify_on_resolve).
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

-- Delivery ledger: one row per (integration, finding) with the last action
-- sent, the retry schedule, and the external id a backend returned. This is
-- what makes "new vs repeat" and "resolve once" decidable without engine
-- state, and what the retry loop drives from.
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
-- has processed. Written only by the elected leader. leader is informational.
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

-- Global default alert policy: fire when risk score >= 20, no weight/list overrides.
INSERT INTO alert_policies
    (scope_type, scope_key, policy_id, name, fire_at_score, signal_weights, enricher_lists, enabled, updated_at)
VALUES
    ('global', '', 1, 'global default', 20, map(), map(), 1, now64(3));

-- Per-class GLOBAL learning defaults, one per deviation class.
-- Promote once the baseline has been structurally stable for 2 weeks
-- (20160 minutes). Time only, so a busy transaction does not promote before
-- its weekly and bi-weekly patterns have had a chance to show up.
INSERT INTO learning_policies
    (class, scope_type, scope_key, match_mode, conditions, min_matches, updated_at)
VALUES
    ('D1_call_edges',   'global', '', 'all', '[{"type":"stable_duration","stableMinutes":20160}]', 1, now64(3)),
    ('D2_egress',       'global', '', 'all', '[{"type":"stable_duration","stableMinutes":20160}]', 1, now64(3)),
    ('D3_latency',      'global', '', 'all', '[{"type":"stable_duration","stableMinutes":20160}]', 1, now64(3)),
    ('D4_args_returns', 'global', '', 'all', '[{"type":"stable_duration","stableMinutes":20160}]', 1, now64(3)),
    ('D5_libraries',    'global', '', 'all', '[{"type":"stable_duration","stableMinutes":20160}]', 1, now64(3)),
    ('D6_db_access',    'global', '', 'all', '[{"type":"stable_duration","stableMinutes":20160}]', 1, now64(3)),
    ('D7_payload_size', 'global', '', 'all', '[{"type":"stable_duration","stableMinutes":20160}]', 1, now64(3));

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
    50000, 1000,
    15,
    1
);
