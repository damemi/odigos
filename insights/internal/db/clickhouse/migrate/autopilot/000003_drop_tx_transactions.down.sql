-- Restore the pre-consolidation autopilot inventory table.
-- Lossy: the table comes back empty. The up migration dropped its rows and
-- nothing repopulates them, so rolling back leaves the old loop with no
-- transactions until traffic is re-recorded by a pre-consolidation build.
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
