-- Initial schema. Unset timestamps are stored as the Unix epoch (not NULL) so
-- GREATEST/LEAST merges and the Go side treat "never" uniformly.

-- The Odigos source a transaction's entry span came from.
CREATE TABLE IF NOT EXISTS sources (
    source_id bigint PRIMARY KEY,
    -- The platform the source belongs to, e.g. "k8s", "vm"
    platform  text NOT NULL
);

CREATE TABLE IF NOT EXISTS k8s_source_identity (
    source_id      bigint PRIMARY KEY REFERENCES sources ON DELETE CASCADE,
    namespace      text NOT NULL,
    workload_kind  text NOT NULL,
    workload_name  text NOT NULL,
    container_name text NOT NULL
);
-- One identity per container.
CREATE UNIQUE INDEX IF NOT EXISTS k8s_source_identity_workload_idx
    ON k8s_source_identity (namespace, workload_kind, workload_name, container_name);

CREATE TABLE IF NOT EXISTS transactions (
    transaction_id  bigint PRIMARY KEY,
    source_id       bigint NOT NULL REFERENCES sources,
    service         text NOT NULL,
    -- The entry span's name, e.g. 'POST /checkout', 'myapp.UserService/Login'
    -- or 'kafka.consume orders', followed by the identity dimensions it
    -- carried, e.g. 'POST /checkout [http.response.status_code=200]'. The
    -- transaction_id is hashed from it.
    operation       text NOT NULL,
    -- The entry span's name alone, e.g. 'POST /checkout'.
    operation_name  text NOT NULL,
    -- The configured identity dimensions the entry span carried, in settings
    -- order, e.g. [{"http.response.status_code": "200"}].
    attributes      jsonb NOT NULL DEFAULT '[]',
    -- HTTP, GRPC, CONSUMER, PRODUCER, CRON or INTERNAL.
    entry_kind      text NOT NULL
);
CREATE INDEX IF NOT EXISTS transactions_service_idx ON transactions (service);
-- (source_id, service) so a namespace's services are read from the index alone.
CREATE INDEX IF NOT EXISTS transactions_source_idx ON transactions (source_id, service);

-- Set classes (D1/D2/D5/D6): one row per learned member. Members can be long
-- JSON, so uniqueness is on a hash to stay under the btree entry size limit.
CREATE TABLE IF NOT EXISTS baseline_set_members (
    transaction_id bigint NOT NULL,
    class          text NOT NULL,
    member         text NOT NULL,
    member_hash    bytea GENERATED ALWAYS AS (decode(md5(member), 'hex')) STORED,
    PRIMARY KEY (transaction_id, class, member_hash)
);
CREATE INDEX IF NOT EXISTS baseline_set_members_class_idx ON baseline_set_members (class);

-- Additive classes (D7 histogram): one row per bucket, counts summed on write.
CREATE TABLE IF NOT EXISTS baseline_counters (
    transaction_id bigint NOT NULL,
    class          text NOT NULL,
    bucket         text NOT NULL,
    count          bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (transaction_id, class, bucket)
);

-- Per-class scalars. A class exists iff it has a meta row.
CREATE TABLE IF NOT EXISTS baseline_meta (
    transaction_id                   bigint NOT NULL,
    class                            text NOT NULL,
    observation_count                bigint NOT NULL DEFAULT 0,
    promoted                         boolean NOT NULL DEFAULT false,
    saturated                        boolean NOT NULL DEFAULT false,
    data_schema_version              integer NOT NULL DEFAULT 1,
    learning_started_at              timestamptz NOT NULL DEFAULT now(),
    last_changed_at                  timestamptz NOT NULL DEFAULT 'epoch',
    observation_count_at_last_change bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (transaction_id, class)
);

-- Stored traces kept as examples and evidence. Expired by the retention job:
-- relation samples after 14 days, the rest after observation_retention_days.
CREATE TABLE IF NOT EXISTS trace_observations (
    transaction_id bigint NOT NULL,
    sample_reason  text NOT NULL,
    trace_id       text NOT NULL,
    observed_at    timestamptz NOT NULL,
    duration_us    bigint NOT NULL DEFAULT 0,
    raw_otlp       bytea NOT NULL,
    PRIMARY KEY (transaction_id, sample_reason, trace_id)
);
CREATE INDEX IF NOT EXISTS trace_observations_reason_observed_idx ON trace_observations (sample_reason, observed_at DESC);

CREATE TABLE IF NOT EXISTS transaction_activity (
    transaction_id bigint PRIMARY KEY,
    volume         bigint NOT NULL DEFAULT 0,
    last_seen      timestamptz NOT NULL DEFAULT 'epoch'
);

CREATE TABLE IF NOT EXISTS alert_policies (
    scope_type     text NOT NULL DEFAULT 'global',
    scope_key      text NOT NULL DEFAULT '',
    policy_id      bigint NOT NULL,
    name           text NOT NULL DEFAULT '',
    fire_at_score  integer NOT NULL DEFAULT 20,
    signal_weights jsonb NOT NULL DEFAULT '{}',
    enricher_lists jsonb NOT NULL DEFAULT '{}',
    enabled        boolean NOT NULL DEFAULT true,
    -- Delivery to integrations: on by default. notify_muted sends this
    -- policy's anomalies nowhere, excluded_integration_ids skips only those.
    notify_muted             boolean NOT NULL DEFAULT false,
    excluded_integration_ids text[] NOT NULL DEFAULT '{}',
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope_type, scope_key)
);

CREATE TABLE IF NOT EXISTS learning_policies (
    class       text NOT NULL,
    scope_type  text NOT NULL DEFAULT 'global',
    scope_key   text NOT NULL DEFAULT '',
    match_mode  text NOT NULL,
    conditions  text NOT NULL DEFAULT '',
    min_matches bigint NOT NULL DEFAULT 0,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (class, scope_type, scope_key)
);

CREATE TABLE IF NOT EXISTS anomaly_issues (
    transaction_id    bigint NOT NULL,
    signature         text NOT NULL,
    triggered_classes text NOT NULL DEFAULT '',
    offending         text NOT NULL DEFAULT '',
    evidence          text NOT NULL DEFAULT '{}',
    policy_id         bigint NOT NULL DEFAULT 0,
    occurrences       bigint NOT NULL DEFAULT 0,
    max_score         integer NOT NULL DEFAULT 0,
    first_seen        timestamptz NOT NULL,
    last_seen         timestamptz NOT NULL DEFAULT 'epoch',
    accepted_at       timestamptz NOT NULL DEFAULT 'epoch',
    dismissed_at      timestamptz NOT NULL DEFAULT 'epoch',
    reopened_at       timestamptz NOT NULL DEFAULT 'epoch',
    last_trace_id     text NOT NULL DEFAULT '',
    PRIMARY KEY (transaction_id, signature)
);
CREATE INDEX IF NOT EXISTS anomaly_issues_last_seen_idx ON anomaly_issues (last_seen DESC);

CREATE TABLE IF NOT EXISTS service_guardrails (
    scope_type text NOT NULL DEFAULT 'service',
    scope_key  text NOT NULL DEFAULT '',
    -- rule_key -> allowlist
    rule_lists jsonb NOT NULL DEFAULT '{}',
    -- rule_key -> enforce / suggest / off
    rule_modes jsonb NOT NULL DEFAULT '{}',
    -- Per-rule provenance, e.g. allowed_transactions -> auto_transaction_guardrail.
    rule_origins jsonb NOT NULL DEFAULT '{}',
    -- Per-rule stamp of the config a rule's allowlist was built under, e.g.
    -- allowed_transactions -> fingerprint of the enabled identity dimensions.
    rule_fingerprints jsonb NOT NULL DEFAULT '{}',
    -- rule_key -> JSON-encoded []CorrelationSpec for attribute_correlation.
    rule_correlations jsonb NOT NULL DEFAULT '{}',
    notify_muted             boolean NOT NULL DEFAULT false,
    excluded_integration_ids text[] NOT NULL DEFAULT '{}',
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope_type, scope_key)
);

CREATE TABLE IF NOT EXISTS guardrail_violations (
    scope_type    text NOT NULL DEFAULT 'service',
    scope_key     text NOT NULL,
    rule_key      text NOT NULL,
    offending     text NOT NULL,
    occurrences   bigint NOT NULL DEFAULT 0,
    max_score     integer NOT NULL DEFAULT 0,
    first_seen    timestamptz NOT NULL,
    last_seen     timestamptz NOT NULL DEFAULT 'epoch',
    accepted_at   timestamptz NOT NULL DEFAULT 'epoch',
    dismissed_at  timestamptz NOT NULL DEFAULT 'epoch',
    reopened_at   timestamptz NOT NULL DEFAULT 'epoch',
    last_trace_id text NOT NULL DEFAULT '',
    PRIMARY KEY (scope_type, scope_key, rule_key, offending)
);
CREATE INDEX IF NOT EXISTS guardrail_violations_last_seen_idx ON guardrail_violations (last_seen DESC);
CREATE INDEX IF NOT EXISTS guardrail_violations_rule_idx ON guardrail_violations (rule_key);

-- Guardrail recommendations: rules Insights suggests instead of waiting for an
-- operator to author them. transaction_id is the resolved id for transaction
-- scope (0 otherwise). namespace/service are stored for service scope only.
-- Mining stats are rewritten on every run, state is the operator's decision
-- and is carried over by the writer.
CREATE TABLE IF NOT EXISTS guardrail_recommendations (
    scope_type        text NOT NULL DEFAULT 'transaction',
    scope_key         text NOT NULL,
    recommendation_id text NOT NULL,
    transaction_id    bigint NOT NULL DEFAULT 0,
    namespace         text NOT NULL DEFAULT '',
    service           text NOT NULL DEFAULT '',
    kind              text NOT NULL,
    state             text NOT NULL DEFAULT 'open',
    stale             boolean NOT NULL DEFAULT false,
    rank              integer NOT NULL DEFAULT 0,
    spec              text NOT NULL DEFAULT '',
    transform         text NOT NULL DEFAULT '',
    transport         boolean NOT NULL DEFAULT false,
    rules             text NOT NULL DEFAULT '[]',
    applied_rules     text NOT NULL DEFAULT '[]',
    observed          integer NOT NULL DEFAULT 0,
    held              integer NOT NULL DEFAULT 0,
    distinct_values   integer NOT NULL DEFAULT 0,
    sample_count      integer NOT NULL DEFAULT 0,
    examples          text NOT NULL DEFAULT '[]',
    mined_at          timestamptz NOT NULL,
    created_at        timestamptz NOT NULL,
    updated_at        timestamptz NOT NULL,
    PRIMARY KEY (scope_type, scope_key, recommendation_id)
);
CREATE INDEX IF NOT EXISTS guardrail_recommendations_id_idx ON guardrail_recommendations (recommendation_id);
CREATE INDEX IF NOT EXISTS guardrail_recommendations_tx_idx ON guardrail_recommendations (transaction_id);

-- Shadow-evaluation counters of a still-open recommendation against live
-- traffic. rule_key splits a service_guardrail card's counts per proposed
-- rule and is empty for correlation rows.
CREATE TABLE IF NOT EXISTS recommendation_live_counts (
    scope_type        text NOT NULL DEFAULT 'transaction',
    scope_key         text NOT NULL,
    recommendation_id text NOT NULL,
    rule_key          text NOT NULL DEFAULT '',
    transaction_id    bigint NOT NULL DEFAULT 0,
    held              bigint NOT NULL DEFAULT 0,
    broken            bigint NOT NULL DEFAULT 0,
    first_at          timestamptz NOT NULL,
    last_at           timestamptz NOT NULL,
    PRIMARY KEY (scope_type, scope_key, recommendation_id, rule_key)
);
CREATE INDEX IF NOT EXISTS recommendation_live_counts_id_idx ON recommendation_live_counts (recommendation_id);
CREATE INDEX IF NOT EXISTS recommendation_live_counts_tx_idx ON recommendation_live_counts (transaction_id);

-- One row per transaction: the newest mining run, which also acts as the soft
-- claim replicas use to avoid mining the same transaction at once (mined_at is
-- the epoch while a run is in progress).
CREATE TABLE IF NOT EXISTS recommendation_mining_runs (
    transaction_id bigint PRIMARY KEY,
    claimed_at     timestamptz NOT NULL,
    mined_at       timestamptz NOT NULL DEFAULT 'epoch',
    sample_count   integer NOT NULL DEFAULT 0,
    candidates     integer NOT NULL DEFAULT 0,
    updated_at     timestamptz NOT NULL
);

-- Service-graph edges from the gateway's servicegraph connector: the latest
-- cumulative counts per (edge, collector pod). Reads sum across pods.
-- server_is_virtual marks an uninstrumented dependency (DB, queue, external
-- API). client_is_virtual marks the connector's synthetic "user" node.
CREATE TABLE IF NOT EXISTS service_graph_edges (
    server_namespace  text NOT NULL,
    server_service    text NOT NULL,
    client_namespace  text NOT NULL,
    client_service    text NOT NULL,
    connection_type   text NOT NULL,
    collector_pod     text NOT NULL,
    server_is_virtual boolean NOT NULL DEFAULT false,
    client_is_virtual boolean NOT NULL DEFAULT false,
    request_count     bigint NOT NULL DEFAULT 0,
    failed_count      bigint NOT NULL DEFAULT 0,
    observed_at       timestamptz NOT NULL,
    PRIMARY KEY (server_namespace, server_service, client_namespace, client_service, connection_type, collector_pod)
);
CREATE INDEX IF NOT EXISTS service_graph_edges_client_idx ON service_graph_edges (client_namespace, client_service);

CREATE TABLE IF NOT EXISTS system_settings (
    id smallint PRIMARY KEY,
    updated_at timestamptz NOT NULL DEFAULT now(),

    examples_per_transaction integer NOT NULL,
    example_sample_interval_seconds integer NOT NULL,

    observation_retention_days integer NOT NULL,

    max_resident_transactions integer NOT NULL,
    max_baseline_set_members integer NOT NULL,

    flush_interval_seconds integer NOT NULL,

    -- When true, auto-enable the allowed_transactions guardrail once all of a
    -- service's transactions are promoted. Per-service disable uses rule mode "off".
    auto_transaction_guardrail boolean NOT NULL DEFAULT true,

    -- Configurable low-cardinality transaction identity dimensions, JSON array of
    -- {key, enabled}. Empty is filled by DefaultTransactionIdentityDimensions.
    transaction_identity_dimensions text NOT NULL
        DEFAULT '[{"key":"http.response.status_code","enabled":true},{"key":"rpc.grpc.status_code","enabled":true}]',

    -- Bumpable epoch so multi-replica engines clear in-memory transaction state
    -- after an identity-dimension reset wipe. Not an operator-facing setting.
    transaction_inventory_epoch integer NOT NULL DEFAULT 0,

    -- Relation samples kept per learning transaction for the recommendation
    -- miner, and the bar a mined correlation must clear to be recommended.
    relation_samples_per_transaction integer NOT NULL DEFAULT 50,
    relation_sample_interval_seconds integer NOT NULL DEFAULT 5,
    recommendation_min_distinct integer NOT NULL DEFAULT 5,
    recommendation_min_hold_percent integer NOT NULL DEFAULT 98,

    -- Per-service cap on distinct transactions. Unseen operations past it are
    -- not admitted to the inventory, so span names that carry request ids
    -- cannot grow the transactions and baseline tables without bound.
    max_transactions_per_service integer NOT NULL DEFAULT 200
);

-- Findings integrations: third-party backends that receive findings. type
-- names a catalog descriptor in the notify package. config is JSON keyed by
-- that descriptor's fields (secret fields are stored as-is and redacted on
-- read). filters is JSON with the delivery filters.
CREATE TABLE IF NOT EXISTS integrations (
    integration_id text PRIMARY KEY,
    name           text NOT NULL,
    type           text NOT NULL,
    enabled        boolean NOT NULL DEFAULT true,
    config         text NOT NULL DEFAULT '{}',
    filters        text NOT NULL DEFAULT '{}',
    created_at     timestamptz NOT NULL,
    updated_at     timestamptz NOT NULL
);

-- Delivery ledger: one row per (integration, finding) with the last action
-- sent, the retry schedule, and the external id a backend returned.
CREATE TABLE IF NOT EXISTS notification_dispatch (
    integration_id      text NOT NULL,
    finding_key         text NOT NULL,
    last_action         text NOT NULL,
    state               text NOT NULL,
    attempts            integer NOT NULL DEFAULT 0,
    next_attempt_at     timestamptz NOT NULL DEFAULT 'epoch',
    last_error          text NOT NULL DEFAULT '',
    sent_at             timestamptz NOT NULL DEFAULT 'epoch',
    occurrences_at_send bigint NOT NULL DEFAULT 0,
    severity_at_send    text NOT NULL DEFAULT '',
    status_at_send      text NOT NULL DEFAULT '',
    external_id         text NOT NULL DEFAULT '',
    updated_at          timestamptz NOT NULL,
    PRIMARY KEY (integration_id, finding_key)
);
CREATE INDEX IF NOT EXISTS notification_dispatch_state_idx ON notification_dispatch (state, next_attempt_at);

-- Notifier progress per integration. Written only by the elected leader.
CREATE TABLE IF NOT EXISTS notification_cursors (
    integration_id text PRIMARY KEY,
    cursor         timestamptz NOT NULL DEFAULT 'epoch',
    last_run_at    timestamptz NOT NULL DEFAULT 'epoch',
    leader         text NOT NULL DEFAULT '',
    updated_at     timestamptz NOT NULL
);

-- Audit trail: one row per delivery attempt. finding_key is empty for a
-- batch-level failure. Expired after 30 days by the retention job.
CREATE TABLE IF NOT EXISTS notification_attempts (
    integration_id text NOT NULL,
    finding_key    text NOT NULL DEFAULT '',
    action         text NOT NULL,
    attempt        integer NOT NULL DEFAULT 1,
    outcome        text NOT NULL,
    http_status    integer NOT NULL DEFAULT 0,
    error          text NOT NULL DEFAULT '',
    duration_ms    integer NOT NULL DEFAULT 0,
    event_count    integer NOT NULL DEFAULT 1,
    attempted_at   timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS notification_attempts_integration_idx ON notification_attempts (integration_id, attempted_at DESC);
CREATE INDEX IF NOT EXISTS notification_attempts_attempted_idx ON notification_attempts (attempted_at);

-- Last outcome of each periodic maintenance job (e.g. retention), so every
-- replica can report it on the Storage page, not only the leader that ran it.
CREATE TABLE IF NOT EXISTS maintenance_runs (
    job          text PRIMARY KEY,
    ran_at       timestamptz NOT NULL,
    ok           boolean NOT NULL,
    error        text NOT NULL DEFAULT '',
    deleted_rows jsonb NOT NULL DEFAULT '{}'
);

-- Global default alert policy: fire when risk score >= 20, no weight/list overrides.
INSERT INTO alert_policies (scope_type, scope_key, policy_id, name, fire_at_score, enabled)
VALUES ('global', '', 1, 'global default', 20, true)
ON CONFLICT DO NOTHING;

-- Per-class GLOBAL learning defaults, one per deviation class.
-- Promote once the baseline has been structurally stable for 2 weeks
-- (20160 minutes). Time only, so a busy transaction does not promote before
-- its weekly and bi-weekly patterns have had a chance to show up.
INSERT INTO learning_policies (class, scope_type, scope_key, match_mode, conditions, min_matches)
VALUES
    ('D1_call_edges',   'global', '', 'all', '[{"type":"stable_duration","stableMinutes":20160}]', 1),
    ('D2_egress',       'global', '', 'all', '[{"type":"stable_duration","stableMinutes":20160}]', 1),
    ('D3_latency',      'global', '', 'all', '[{"type":"stable_duration","stableMinutes":20160}]', 1),
    ('D4_args_returns', 'global', '', 'all', '[{"type":"stable_duration","stableMinutes":20160}]', 1),
    ('D5_libraries',    'global', '', 'all', '[{"type":"stable_duration","stableMinutes":20160}]', 1),
    ('D6_db_access',    'global', '', 'all', '[{"type":"stable_duration","stableMinutes":20160}]', 1),
    ('D7_payload_size', 'global', '', 'all', '[{"type":"stable_duration","stableMinutes":20160}]', 1)
ON CONFLICT DO NOTHING;

INSERT INTO system_settings (
    id,
    examples_per_transaction, example_sample_interval_seconds,
    observation_retention_days,
    max_resident_transactions, max_baseline_set_members,
    flush_interval_seconds,
    auto_transaction_guardrail
) VALUES (
    1,
    10, 30,
    120,
    50000, 1000,
    15,
    true
)
ON CONFLICT DO NOTHING;

-- One row per call-path node of an autopilot call tree, synced from Redis.
-- source_id and transaction_id are the sources and transactions ids. No
-- foreign keys: a call tree is synced within seconds, usually before the
-- baseline flush writes its source and transaction. Every sync re-sends the
-- whole call tree, and a node already stored is skipped. node_id already
-- covers the parent, frame type, and function name. A root node's parent is
-- 'root'.
CREATE TABLE IF NOT EXISTS autopilot_call_tree_nodes (
    source_id      bigint NOT NULL,
    transaction_id bigint NOT NULL,
    -- 'events' or 'samples'.
    sample_type    text NOT NULL,
    node_id        text NOT NULL,
    parent         text NOT NULL,
    function_name  text NOT NULL,
    -- 'jvm' or 'hotspot'.
    frame_type     text NOT NULL,
    PRIMARY KEY (source_id, transaction_id, sample_type, node_id)
);
