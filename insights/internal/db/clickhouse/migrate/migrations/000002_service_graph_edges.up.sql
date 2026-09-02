-- Service-graph edges: the client -> server topology the vendored servicegraph
-- connector emits in the Odigos gateway, shipped to insights over OTLP and
-- stored here so the UI can draw a per-anomaly blast-radius subgraph (the
-- neighborhood of services and uninstrumented dependencies around an anomalous
-- service).
--
-- The connector's counters are cumulative per gateway pod, and trace load
-- balancing spreads an edge's spans across pods, so we key by (edge, pod) and
-- keep the latest datapoint per key. ReplacingMergeTree(observed_at) collapses
-- repeated inserts for the same key to the newest observed_at, so re-sending the
-- same edge is idempotent and storage tracks a recent snapshot rather than one
-- row per export. Reads sum request/failed across pods for a logical edge.
-- Exact lifetime totals are not the goal -- edge existence, direction,
-- virtual-ness, and rough magnitude/error presence are what the blast-radius
-- view needs.
--
-- server_is_virtual marks an uninstrumented dependency (DB, queue, external API)
-- that the connector inferred as a virtual node, so the UI can render it apart
-- from real services.
-- NOTE: keep semicolons out of comments -- the migration runner splits on them.
CREATE TABLE IF NOT EXISTS service_graph_edges (
    client_namespace  LowCardinality(String),
    client_service    String,
    server_namespace  LowCardinality(String),
    server_service    String,
    connection_type   LowCardinality(String),
    server_is_virtual UInt8,
    collector_pod     LowCardinality(String),
    request_count     UInt64,
    failed_count      UInt64,
    observed_at       DateTime64(3)
) ENGINE = ReplacingMergeTree(observed_at)
ORDER BY (server_namespace, server_service, client_namespace, client_service, connection_type, collector_pod);
