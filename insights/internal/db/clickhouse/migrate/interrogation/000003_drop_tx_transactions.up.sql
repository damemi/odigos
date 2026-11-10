-- Transaction inventory lives on insights.transactions (resource_attributes
-- map) so interrogation is not schematically bound to Kubernetes.
-- NOTE: keep semicolons out of comments - the migration runner splits on them.

DROP TABLE IF EXISTS tx_transactions;
