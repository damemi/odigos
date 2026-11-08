// Package store is the provider-agnostic connector persistence layer over PostgreSQL (pgx). It owns the
// shared connector_platform heartbeat/version tables and the per-provider sources/destinations tables
// (identical structure across providers, only the schema name differs — passed to Open). A provider
// embeds *Store in its own store and adds inventory tables. The runtime never alters schema; it
// verifies the schema version at startup and fails fast on mismatch.
package store

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
)

var storeLog = connlog.Subsystem("store")

// schemaNameRe matches a safe unquoted PostgreSQL identifier. The provider schema is interpolated into
// queries via fmt.Sprintf (it is not a bind parameter), so it is validated once here — defense-in-depth
// against SQL injection should the schema ever become derived from a CRD field or other external input.
var schemaNameRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// Store holds the connection pool, the connector identity used as a row discriminator, and the
// provider schema that sources/destinations live in.
type Store struct {
	pool          *pgxpool.Pool
	connectorName string
	schema        string
	// runtimeID identifies THIS pod incarnation; set by Register (which claims the heartbeat row). Heartbeat
	// and shutdown writes are fenced on it so a stale/partitioned previous pod can't clobber the live pod's
	// row or prematurely record shutdownComplete.
	runtimeID string
}

// Open connects to Postgres, verifies connectivity, and binds the connector identity + provider schema.
func Open(ctx context.Context, dsn, connectorName, schema string) (*Store, error) {
	storeLog.Info("opening postgres connection", "connector", connectorName, "schema", schema)
	if !schemaNameRe.MatchString(schema) {
		return nil, fmt.Errorf("invalid provider schema name %q: must match %s", schema, schemaNameRe.String())
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		storeLog.Error("postgres connect failed", "connector", connectorName, "error", err)
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		storeLog.Error("postgres ping failed", "connector", connectorName, "error", err)
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	storeLog.Info("postgres connection ready", "connector", connectorName, "schema", schema)
	return &Store{pool: pool, connectorName: connectorName, schema: schema}, nil
}

func (s *Store) Close() {
	storeLog.Info("closing postgres connection", "connector", s.connectorName)
	s.pool.Close()
}

// Pool exposes the connection pool so a provider's embedding store can run inventory queries on it.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Now returns the database's current time. A discovery cycle takes its prune cutoff from it: inventory rows
// record last_seen_at with Postgres's now(), so a cutoff read from the connector's own clock compares two
// clocks, and with Postgres even slightly behind the pod a row upserted just after the cutoff looks older
// than it and is soft-deleted as gone.
func (s *Store) Now(ctx context.Context) (time.Time, error) {
	var t time.Time
	err := s.pool.QueryRow(ctx, `SELECT now()`).Scan(&t)
	return t, err
}

// ConnectorName returns the row-discriminator identity.
func (s *Store) ConnectorName() string { return s.connectorName }

// Schema returns the provider schema name.
func (s *Store) Schema() string { return s.schema }

// VerifySchemaVersion checks that the live connector_platform.schema_versions rows match the versions
// this runtime was built against. Returns an error (which the caller treats as fatal) on any mismatch
// or missing schema row.
func (s *Store) VerifySchemaVersion(ctx context.Context, expected map[string]string) error {
	storeLog.Debug("verifying schema versions", "schemas", len(expected))
	for schema, want := range expected {
		var got string
		err := s.pool.QueryRow(ctx,
			`SELECT version FROM connector_platform.schema_versions WHERE schema_name = $1`, schema).Scan(&got)
		if err != nil {
			storeLog.Error("read schema version failed", "schema", schema, "error", err)
			return fmt.Errorf("read schema version for %q: %w", schema, err)
		}
		if got != want {
			storeLog.Error("schema version mismatch", "schema", schema, "live", got, "expected", want)
			return fmt.Errorf("schema %q version mismatch: live=%s expected=%s (run migrations / align connector image)", schema, got, want)
		}
		storeLog.Debug("schema version ok", "schema", schema, "version", got)
	}
	return nil
}
