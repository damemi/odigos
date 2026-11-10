-- collector_credentials: the bearer credentials managed Collectors present to connector-opamp-gateway.
--
-- Why connector_platform and not connector_<provider>: the gateway's lookup key is the token hash, and
-- it has to resolve a credential BEFORE it knows which provider issued it — the provider is a column of
-- the row it is trying to find. A provider-schema table would force a UNION across every provider on
-- every handshake and would need editing each time a provider is added. The binding already carries
-- `provider`, so one platform-wide table is both the smaller schema and the only one the gateway can
-- actually query. See MANAGED-COLLECTOR.md §6.3 and collector/managed/OPAMP_ROUTING.md.
--
-- Its own table rather than columns on managed_collectors because a rotation needs two credentials live
-- at once (§6.4) and a Collector has several over its life.
--
-- No column holds a credential VALUE. token_sha256 is the only representation kept anywhere.

-- +goose Up
CREATE TABLE connector_platform.collector_credentials (
  collector_id     TEXT        NOT NULL,   -- the Collector identity this authenticates
  credential_id    TEXT        NOT NULL,   -- unique per ISSUANCE; what revocation names
  connector_name   TEXT        NOT NULL,
  connector_uid    TEXT        NOT NULL,   -- the CR UID at issue time; a recreated CR does not inherit
  provider         TEXT        NOT NULL,
  account_id       TEXT        NOT NULL,
  region           TEXT        NOT NULL,
  network_id       TEXT        NOT NULL,

  secret_ref       TEXT        NOT NULL,   -- where the VALUE lives; stable across rotations
  secret_version   TEXT,                   -- the version this credential was written as
  token_sha256     TEXT        NOT NULL,

  state            TEXT        NOT NULL,
  issued_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  activated_at     TIMESTAMPTZ,            -- first successful OpAMP connection under it
  superseded_by    TEXT,                   -- credential_id of the replacement
  expires_at       TIMESTAMPTZ,            -- NULL in v1; see §6.4
  revoked_at       TIMESTAMPTZ,
  revoked_reason   TEXT,

  PRIMARY KEY (collector_id, credential_id),

  CONSTRAINT collector_credentials_state_check
    CHECK (state IN ('pending', 'active', 'superseded', 'revoked')),

  CONSTRAINT collector_credentials_revoked_reason_check
    CHECK (revoked_reason IS NULL OR revoked_reason IN
      ('teardown', 'rotation', 'compromise', 'connector-recreated', 'capability-removed')),

  -- SHA-256 of the empty string. A failed mint reaches the store as a well-formed 64-character digest
  -- that reads as a successfully issued credential, and every failed mint produces the same one — so
  -- they would all collide onto one row. collectorcredential.Hash refuses to produce it; this refuses
  -- to store it, because the two places that can go wrong are the minting code and a hand-written row.
  CONSTRAINT collector_credentials_token_not_empty_digest
    CHECK (token_sha256 <> 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855'),

  CONSTRAINT collector_credentials_token_shape
    CHECK (token_sha256 ~ '^[0-9a-f]{64}$')
);

-- The gateway's only lookup, and it must resolve to at most one row: two rows for one hash would make
-- which Collector a credential authenticates depend on scan order.
CREATE UNIQUE INDEX collector_credentials_token_sha256_key
  ON connector_platform.collector_credentials (token_sha256);

-- Teardown and connector recreation revoke every credential belonging to a connector (§6.4), so that
-- has to be one indexed sweep rather than a scan of the whole fleet.
CREATE INDEX collector_credentials_connector_uid_idx
  ON connector_platform.collector_credentials (connector_uid);

UPDATE connector_platform.schema_versions
SET version = '3', applied_at = now()
WHERE schema_name = 'connector_platform';

-- +goose Down
UPDATE connector_platform.schema_versions
SET version = '2', applied_at = now()
WHERE schema_name = 'connector_platform';

DROP INDEX connector_platform.collector_credentials_connector_uid_idx;
DROP INDEX connector_platform.collector_credentials_token_sha256_key;
DROP TABLE connector_platform.collector_credentials;
