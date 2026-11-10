-- Probes the model recommended for a workload, written by the leader after a
-- successful analysis and before that run is acknowledged. This is the desired
-- probe set that InstrumentationRules are reconciled to. The writer skips
-- probes already stored, so there is one row per probe. A crash can still
-- leave a repeat, so readers group by (Language, Target) and keep the
-- earliest FirstSeen, which also gives a stable order for probe caps.
-- NOTE: keep semicolons out of comments - the migration runner splits on them.

CREATE TABLE IF NOT EXISTS tx_recommendations (
	FirstSeen DateTime64(9) CODEC(Delta, ZSTD(1)),
	Namespace LowCardinality(String) CODEC(ZSTD(1)),
	WorkloadKind LowCardinality(String) CODEC(ZSTD(1)),
	WorkloadName LowCardinality(String) CODEC(ZSTD(1)),
	ContainerName LowCardinality(String) CODEC(ZSTD(1)),
	TransactionId String CODEC(ZSTD(1)),
	Language LowCardinality(String) CODEC(ZSTD(1)),
	Target String CODEC(ZSTD(1)),
	Category LowCardinality(String) CODEC(ZSTD(1)),
	Function String CODEC(ZSTD(1)),
	Why String CODEC(ZSTD(1)),
	ClassName String CODEC(ZSTD(1)),
	MethodName String CODEC(ZSTD(1)),
	PackageName String CODEC(ZSTD(1)),
	FunctionName String CODEC(ZSTD(1)),
	ReceiverName String CODEC(ZSTD(1)),
	ReceiverMethodName String CODEC(ZSTD(1)),
	Signature String CODEC(ZSTD(1)),
	AnalysisRevision LowCardinality(String) CODEC(ZSTD(1))
) ENGINE = MergeTree()
ORDER BY (Namespace, WorkloadKind, WorkloadName, Language, Target, FirstSeen)
SETTINGS index_granularity=8192;
