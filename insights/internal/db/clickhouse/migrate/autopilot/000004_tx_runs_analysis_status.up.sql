-- AnalysisStatus records whether the model call for a run succeeded, apart
-- from ApplyStatus. Only succeeded runs at the current AnalysisRevision are
-- read back as the evidence the model has already seen, so failed or skipped
-- runs never hide call-graph edges from later analysis.
-- NOTE: keep semicolons out of comments - the migration runner splits on them.

ALTER TABLE tx_runs ADD COLUMN IF NOT EXISTS AnalysisStatus LowCardinality(String) CODEC(ZSTD(1));
ALTER TABLE tx_runs ADD COLUMN IF NOT EXISTS AnalysisRevision LowCardinality(String) CODEC(ZSTD(1));
