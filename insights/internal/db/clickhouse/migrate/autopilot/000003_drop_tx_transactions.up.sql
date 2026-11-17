-- Transaction inventory lives on insights.transactions (source_identity
-- map) so autopilot is not schematically bound to Kubernetes.
-- NOTE: keep semicolons out of comments - the migration runner splits on them.

DROP TABLE IF EXISTS tx_transactions;
