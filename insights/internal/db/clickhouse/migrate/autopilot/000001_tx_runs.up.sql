-- Autopilot schema. Serving replicas and the Redis trie flusher do not
-- run DDL - the Helm migrate Job applies this set, same as odigos_insights.
-- NOTE: keep semicolons out of comments - the migration runner splits on them.

CREATE TABLE IF NOT EXISTS tx_runs (
	Timestamp DateTime64(9) CODEC(Delta, ZSTD(1)),
	Namespace LowCardinality(String) CODEC(ZSTD(1)),
	WorkloadKind LowCardinality(String) CODEC(ZSTD(1)),
	WorkloadName LowCardinality(String) CODEC(ZSTD(1)),
	ContainerName LowCardinality(String) CODEC(ZSTD(1)),
	TransactionId String CODEC(ZSTD(1)),
	TrieHash String CODEC(ZSTD(1)),
	EvidenceSnapshot String CODEC(ZSTD(1)),
	Model LowCardinality(String) CODEC(ZSTD(1)),
	PromptHash String CODEC(ZSTD(1)),
	RawResponse String CODEC(ZSTD(1)),
	Parsed String CODEC(ZSTD(1)),
	ApplyStatus LowCardinality(String) CODEC(ZSTD(1)),
	Error String CODEC(ZSTD(1)),
	INDEX idx_transaction_id TransactionId TYPE bloom_filter(0.01) GRANULARITY 1
) ENGINE = MergeTree()
ORDER BY (Namespace, WorkloadKind, WorkloadName, ContainerName, TransactionId, Timestamp)
SETTINGS index_granularity=8192;

ALTER TABLE tx_runs ADD COLUMN IF NOT EXISTS EvidenceSnapshot String CODEC(ZSTD(1));

-- One row per distinct transaction identity for a workload container.
-- Written by the insights autopilot correlator.
CREATE TABLE IF NOT EXISTS tx_transactions (
	Timestamp DateTime64(9) CODEC(Delta, ZSTD(1)),
	Namespace LowCardinality(String) CODEC(ZSTD(1)),
	WorkloadKind LowCardinality(String) CODEC(ZSTD(1)),
	WorkloadName LowCardinality(String) CODEC(ZSTD(1)),
	ContainerName LowCardinality(String) CODEC(ZSTD(1)),
	TransactionId String CODEC(ZSTD(1)),
	RootSpanName String CODEC(ZSTD(1)),
	RootSpanKind LowCardinality(String) CODEC(ZSTD(1)),
	RootAttributes Map(String, String) CODEC(ZSTD(1)),
	SampleTrace String CODEC(ZSTD(1)),
	INDEX idx_transaction_id TransactionId TYPE bloom_filter(0.01) GRANULARITY 1
) ENGINE = MergeTree()
ORDER BY (Namespace, WorkloadKind, WorkloadName, ContainerName, TransactionId)
SETTINGS index_granularity=8192;

-- One row per call-path node flushed from Redis. ReplacingMergeTree collapses
-- repeat inserts of the same node. NodeId already covers parent, frame type,
-- and function name. SampleType is not part of the node identity.
CREATE TABLE IF NOT EXISTS tx_call_trie_structure (
	Namespace LowCardinality(String) CODEC(ZSTD(1)),
	WorkloadKind LowCardinality(String) CODEC(ZSTD(1)),
	WorkloadName LowCardinality(String) CODEC(ZSTD(1)),
	ContainerName LowCardinality(String) CODEC(ZSTD(1)),
	TransactionId String CODEC(ZSTD(1)),
	SampleType Enum8('events' = 1, 'samples' = 2),
	NodeId String CODEC(ZSTD(1)),
	Parent String CODEC(ZSTD(1)),
	FunctionName String CODEC(ZSTD(1)),
	FrameType Enum8('jvm' = 1, 'hotspot' = 2),
	INDEX idx_transaction_id TransactionId TYPE bloom_filter(0.01) GRANULARITY 1,
	INDEX idx_node_id NodeId TYPE bloom_filter(0.01) GRANULARITY 1
) ENGINE = ReplacingMergeTree
ORDER BY (Namespace, WorkloadKind, WorkloadName, ContainerName, TransactionId, SampleType, NodeId)
SETTINGS index_granularity=8192;
