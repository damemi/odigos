-- Autopilot tables. The leader's autopilot loop writes them, and the UI reads
-- the call tries. transaction_id is the insights transactions.transaction_id.

-- One row per call-path node flushed from Redis. Every flush re-sends the
-- whole trie, and a node already stored is skipped. node_id already covers the
-- parent, frame type, and function name. A root node's parent is 'root'.
CREATE TABLE IF NOT EXISTS autopilot_call_trie_nodes (
    namespace      text NOT NULL,
    workload_kind  text NOT NULL,
    workload_name  text NOT NULL,
    container_name text NOT NULL,
    transaction_id bigint NOT NULL,
    -- 'events' or 'samples'.
    sample_type    text NOT NULL,
    node_id        text NOT NULL,
    parent         text NOT NULL,
    function_name  text NOT NULL,
    -- 'jvm' or 'hotspot'.
    frame_type     text NOT NULL,
    PRIMARY KEY (namespace, workload_kind, workload_name, container_name, transaction_id, sample_type, node_id)
);

-- One row per analysis attempt for a transaction. Only succeeded runs at the
-- current analysis_revision are read back as the evidence the model has
-- already seen, so failed runs never hide call-graph edges from later analysis.
CREATE TABLE IF NOT EXISTS autopilot_runs (
    run_id            bigserial PRIMARY KEY,
    created_at        timestamptz NOT NULL,
    namespace         text NOT NULL,
    workload_kind     text NOT NULL,
    workload_name     text NOT NULL,
    container_name    text NOT NULL,
    transaction_id    bigint NOT NULL,
    -- Structural hash of evidence_snapshot.
    trie_hash         text NOT NULL,
    -- The cumulative call graph the model has seen once this run succeeds.
    evidence_snapshot text NOT NULL,
    model             text NOT NULL,
    -- Hash of the request payload and the prompt revision.
    prompt_hash       text NOT NULL,
    raw_response      text NOT NULL,
    parsed            text NOT NULL,
    error             text NOT NULL,
    -- 'succeeded', 'failed_transient' or 'failed_permanent'.
    analysis_status   text NOT NULL,
    -- The prompt, request schema, and model the run used.
    analysis_revision text NOT NULL
);
CREATE INDEX IF NOT EXISTS autopilot_runs_transaction_idx
    ON autopilot_runs (namespace, workload_kind, workload_name, container_name, transaction_id, created_at DESC);

-- Probes the model recommended for a workload: the desired probe set that
-- InstrumentationRules are reconciled to. One row per (language, target), the
-- first recommendation of the probe, which also gives probe caps a stable order.
CREATE TABLE IF NOT EXISTS autopilot_recommendations (
    namespace            text NOT NULL,
    workload_kind        text NOT NULL,
    workload_name        text NOT NULL,
    language             text NOT NULL,
    target               text NOT NULL,
    first_seen           timestamptz NOT NULL,
    container_name       text NOT NULL,
    transaction_id       bigint NOT NULL,
    category             text NOT NULL,
    function             text NOT NULL,
    why                  text NOT NULL,
    class_name           text NOT NULL,
    method_name          text NOT NULL,
    package_name         text NOT NULL,
    function_name        text NOT NULL,
    receiver_name        text NOT NULL,
    receiver_method_name text NOT NULL,
    signature            text NOT NULL,
    analysis_revision    text NOT NULL,
    PRIMARY KEY (namespace, workload_kind, workload_name, language, target)
);
