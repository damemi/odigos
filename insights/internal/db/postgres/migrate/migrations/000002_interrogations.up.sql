-- On-demand interrogations. A run is stored as ADK sessions (schema adk
-- below); the probes runs ask for are rows of interrogation_requested_probes.

-- Probes an on-demand interrogation asked for: one row per probe and run.
-- The run and the API write the desired state (state, expires_at,
-- end_reason, kept_by, kept_at); only the autopilot reconciler on the leader
-- writes the observed columns (in_rule, being_placed, being_placed_at,
-- place_gen, applied_at, removed_at, block_reason). The loop's own
-- recommendations stay in autopilot_recommendations. The probe columns mirror
-- that table so both kinds of row build the same rule entry.
CREATE TABLE IF NOT EXISTS interrogation_requested_probes (
    change_id            text PRIMARY KEY,
    interrogation_id     text NOT NULL,
    round                integer NOT NULL DEFAULT 0,
    first_seen           timestamptz NOT NULL DEFAULT 'epoch',
    namespace            text NOT NULL,
    workload_kind        text NOT NULL,
    workload_name        text NOT NULL,
    container_name       text NOT NULL DEFAULT '',
    transaction_id       bigint NOT NULL,
    language             text NOT NULL,
    target               text NOT NULL,
    category             text NOT NULL DEFAULT '',
    function             text NOT NULL DEFAULT '',
    why                  text NOT NULL DEFAULT '',
    class_name           text NOT NULL DEFAULT '',
    method_name          text NOT NULL DEFAULT '',
    package_name         text NOT NULL DEFAULT '',
    function_name        text NOT NULL DEFAULT '',
    receiver_name        text NOT NULL DEFAULT '',
    receiver_method_name text NOT NULL DEFAULT '',
    signature            text NOT NULL DEFAULT '',
    analysis_revision    text NOT NULL DEFAULT '',
    -- 'approved' (live until expires_at), 'kept' (no expiry, also after its
    -- run is gone, until someone removes it) or 'reverted'.
    state                text NOT NULL DEFAULT 'approved',
    expires_at           timestamptz NOT NULL DEFAULT 'epoch',
    kept_by              text NOT NULL DEFAULT '',
    kept_at              timestamptz NOT NULL DEFAULT 'epoch',
    end_reason           text NOT NULL DEFAULT '',
    in_rule              boolean NOT NULL DEFAULT false,
    -- Set before a rule write that adds the probe and cleared when its outcome
    -- is recorded, so a lost outcome still leaves the row to the reconciler.
    being_placed         boolean NOT NULL DEFAULT false,
    -- When being_placed was set. Until it is a few minutes old only a write
    -- that placed the probe clears it: another leader's write may still land.
    being_placed_at      timestamptz NOT NULL DEFAULT 'epoch',
    -- Bumped with every being_placed flag. An outcome is recorded only on rows
    -- whose place_gen is what the reconciler read or flagged, so a leader
    -- that lost the lease cannot overwrite a newer write's outcome.
    place_gen            bigint NOT NULL DEFAULT 0,
    applied_at           timestamptz NOT NULL DEFAULT 'epoch',
    removed_at           timestamptz NOT NULL DEFAULT 'epoch',
    block_reason         text NOT NULL DEFAULT '',
    updated_at           timestamptz NOT NULL DEFAULT 'epoch'
);
CREATE INDEX IF NOT EXISTS interrogation_requested_probes_interrogation_idx
    ON interrogation_requested_probes (interrogation_id);
-- Rows the reconciler may still have to look at.
CREATE INDEX IF NOT EXISTS interrogation_requested_probes_workload_idx
    ON interrogation_requested_probes (namespace, workload_kind, workload_name)
    WHERE in_rule OR being_placed OR state IN ('approved', 'kept');

-- ADK session store (google.golang.org/adk/v2 v2.8.0, session/database).
-- An on-demand interrogation is stored as ADK sessions: there is no table of
-- ours for a run. Table and column names are the library's and the DDL is
-- what its AutoMigrate creates. They live in their own schema because the
-- names are generic and the library owns their shape. When ADK is upgraded,
-- TestADKSchemaMatches fails if a release needs a column this file does not
-- have, and the TestADKContract tests fail if it writes a session differently
-- from what the indexes and statements of ours rely on.
CREATE SCHEMA IF NOT EXISTS adk;

CREATE TABLE IF NOT EXISTS adk.sessions (
    app_name    text,
    user_id     text,
    id          text,
    state       jsonb,
    create_time timestamptz(6),
    update_time timestamptz(6),
    PRIMARY KEY (app_name, user_id, id)
);

CREATE TABLE IF NOT EXISTS adk.events (
    id                         text,
    app_name                   text,
    user_id                    text,
    session_id                 text,
    invocation_id              text,
    author                     text,
    actions                    bytea,
    long_running_tool_ids_json jsonb,
    routes_json                jsonb,
    output_json                jsonb,
    node_info_json             jsonb,
    requested_input_json       jsonb,
    branch                     text,
    isolation_scope            text,
    "timestamp"                timestamptz(6),
    content                    jsonb,
    grounding_metadata         jsonb,
    custom_metadata            jsonb,
    usage_metadata             jsonb,
    citation_metadata          jsonb,
    input_transcription        jsonb,
    output_transcription       jsonb,
    partial                    boolean,
    turn_complete              boolean,
    error_code                 text,
    error_message              text,
    interrupted                boolean,
    PRIMARY KEY (id, app_name, user_id, session_id),
    CONSTRAINT fk_sessions_events FOREIGN KEY (app_name, user_id, session_id)
        REFERENCES adk.sessions (app_name, user_id, id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS adk.app_states (
    app_name    text,
    state       jsonb,
    update_time timestamptz(6),
    PRIMARY KEY (app_name)
);

CREATE TABLE IF NOT EXISTS adk.user_states (
    app_name    text,
    user_id     text,
    state       jsonb,
    update_time timestamptz(6),
    PRIMARY KEY (app_name, user_id)
);

-- One open interrogation per workload. state.workload is "namespace/Kind/name";
-- state.open is true from Create until the event that ends the run.
CREATE UNIQUE INDEX IF NOT EXISTS interrogation_open_workload_idx
    ON adk.sessions ((state->>'workload'))
    WHERE app_name = 'interrogation' AND state->>'open' = 'true';

-- The list of interrogations, newest first.
CREATE INDEX IF NOT EXISTS interrogation_created_idx
    ON adk.sessions (app_name, create_time DESC, id DESC);

-- ADK reads a session's events by session and has no index for it. The API's
-- event reads and the delete cascade use it too.
CREATE INDEX IF NOT EXISTS events_session_timestamp_idx
    ON adk.events (app_name, user_id, session_id, "timestamp");
