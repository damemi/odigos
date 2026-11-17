-- Catch-up for clusters that already applied 000001 when it only created
-- tx_runs. Fresh installs created these in 000001. IF NOT EXISTS keeps this
-- a no-op either way. Serving replicas do not create these tables.
-- NOTE: keep semicolons out of comments - the migration runner splits on them.

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
