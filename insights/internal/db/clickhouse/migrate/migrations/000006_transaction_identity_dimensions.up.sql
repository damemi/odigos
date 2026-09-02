-- Add configurable low-cardinality transaction identity dimensions.
-- Stored as a JSON array of {key, enabled}. Nil/missing on older rows is
-- filled by DefaultTransactionIdentityDimensions in WithDefaults.
-- NOTE: keep semicolons out of comments.

ALTER TABLE system_settings
    ADD COLUMN IF NOT EXISTS transaction_identity_dimensions String
    DEFAULT '[{"key":"http.response.status_code","enabled":true},{"key":"rpc.grpc.status_code","enabled":true}]';
