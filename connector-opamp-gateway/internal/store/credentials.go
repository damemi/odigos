// Package store reads the managed-Collector credential table.
//
// Read-mostly by design. The gateway authenticates; it does not issue, rotate or revoke — those belong
// to Central's issuance API (MANAGED-COLLECTOR.md §13). The one write here is activated_at, which
// records that a credential was seen working, and only the gateway is in a position to know it.
//
// No query in this package selects a credential value, because no column holds one.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/odigos-io/odigos/connector-opamp-gateway/internal/gateway"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos/connectors/postgres"
)

// selectColumns is the Record's shape in table order. connector_name is deliberately absent: the
// gateway routes on connector_uid, and a name is not an identity.
const selectColumns = `
	collector_id, credential_id, connector_uid, provider, account_id, region, network_id,
	secret_ref, secret_version, token_sha256,
	state, issued_at, activated_at, superseded_by, expires_at, revoked_at, revoked_reason`

// Credentials reads connector_platform.collector_credentials.
type Credentials struct {
	pool *pgxpool.Pool
}

func NewCredentials(pool *pgxpool.Pool) *Credentials { return &Credentials{pool: pool} }

// ByTokenHash resolves the credential a Collector presented.
//
// The lookup key is the hash, on a UNIQUE index. It is deliberately not "fetch by collector id, then
// compare": a comparison in Go is a comparison somebody can make non-constant-time, and the id would
// have to come from the client, which is the one thing that must not choose the row.
func (c *Credentials) ByTokenHash(ctx context.Context, tokenHash string) (*collectorcredential.Record, error) {
	q := `SELECT` + selectColumns + ` FROM ` + postgres.TableCollectorCredentials + ` WHERE token_sha256 = $1`
	return scanOne(c.pool.QueryRow(ctx, q, tokenHash))
}

// ByID re-reads a credential the gateway already authenticated, for revalidating an open session.
func (c *Credentials) ByID(ctx context.Context, collectorID, credentialID string) (*collectorcredential.Record, error) {
	q := `SELECT` + selectColumns + ` FROM ` + postgres.TableCollectorCredentials +
		` WHERE collector_id = $1 AND credential_id = $2`
	return scanOne(c.pool.QueryRow(ctx, q, collectorID, credentialID))
}

// MarkActivated stamps the first successful connection under a credential.
//
// `WHERE activated_at IS NULL` makes it idempotent and, more importantly, truthful: the column means
// FIRST connection, and a rotation reads it to decide the replacement is carrying traffic and the
// outgoing credential can be revoked (§6.4). Overwriting it on every reconnect would make it a
// last-seen timestamp that happens to be named activated_at.
func (c *Credentials) MarkActivated(ctx context.Context, collectorID, credentialID string, at time.Time) error {
	q := `UPDATE ` + postgres.TableCollectorCredentials + `
		SET activated_at = $3
		WHERE collector_id = $1 AND credential_id = $2 AND activated_at IS NULL`
	_, err := c.pool.Exec(ctx, q, collectorID, credentialID, at)
	if err != nil {
		return fmt.Errorf("recording credential activation: %w", err)
	}
	return nil
}

// Ping reports whether the store is reachable, for the readiness probe. A gateway that cannot read
// credentials can only answer 503, so it should not be in the edge's rotation.
func (c *Credentials) Ping(ctx context.Context) error { return c.pool.Ping(ctx) }

func scanOne(row pgx.Row) (*collectorcredential.Record, error) {
	var (
		rec           collectorcredential.Record
		state         string
		secretVersion *string
		supersededBy  *string
		revokedReason *string
	)
	err := row.Scan(
		&rec.Binding.CollectorID,
		&rec.CredentialID,
		&rec.Binding.ConnectorUID,
		&rec.Binding.Provider,
		&rec.Binding.AccountID,
		&rec.Binding.Region,
		&rec.Binding.NetworkID,
		&rec.SecretRef,
		&secretVersion,
		&rec.TokenSHA256,
		&state,
		&rec.IssuedAt,
		&rec.ActivatedAt,
		&supersededBy,
		&rec.ExpiresAt,
		&rec.RevokedAt,
		&revokedReason,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		// Translated at the boundary so the handler can branch on it without importing pgx — and so an
		// unknown credential stays distinguishable from a store that is down.
		return nil, gateway.ErrCredentialNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", postgres.TableCollectorCredentials, err)
	}

	rec.State = collectorcredential.State(state)
	rec.SecretVersion = deref(secretVersion)
	rec.SupersededBy = deref(supersededBy)
	rec.RevokedReason = collectorcredential.Reason(deref(revokedReason))
	return &rec, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
