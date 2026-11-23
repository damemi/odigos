// Package pgtest gives Postgres integration tests a database of their own.
//
// The tests of internal/db/postgres and internal/autopilot/store share
// one database and keep their rows apart by unique names. A test whose check
// covers the whole database (a migration down and up, a count of open rows)
// cannot run there, because go test runs packages in parallel.
package pgtest

import (
	"context"
	"crypto/rand"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/odigos-io/odigos/insights/internal/db/postgres/migrate"
)

// defaultDSN points at the local dev container the store tests document
// (internal/db/postgres/postgres_test.go). Override with POSTGRES_TEST_DSN.
const defaultDSN = "postgres://insights:insightspass@localhost:15432/insights?sslmode=disable"

// DSN returns the test server's DSN and skips the test when nothing answers
// there, so go test ./... stays green on machines without Postgres.
func DSN(t testing.TB) string {
	t.Helper()
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		dsn = defaultDSN
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Skipf("postgres not reachable at %s (%v)", dsn, err)
	}
	_ = conn.Close(ctx)
	return dsn
}

// NewEmptyDatabase creates a database for this test alone and returns its DSN.
// The database is dropped when the test ends.
func NewEmptyDatabase(t testing.TB) string {
	t.Helper()
	dsn := DSN(t)
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" {
		t.Fatalf("POSTGRES_TEST_DSN must be a postgres:// URL")
	}
	name := "insights_test_" + strings.ToLower(rand.Text()[:16])
	ident := pgx.Identifier{name}.Sanitize()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+ident); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Errorf("drop test database %s: %v", name, err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		// FORCE ends connections a test left open, such as a pool it never closed.
		if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)"); err != nil {
			t.Errorf("drop test database %s: %v", name, err)
		}
	})

	u.Path = "/" + name
	return u.String()
}

// NewDatabase is NewEmptyDatabase with every migration applied.
func NewDatabase(t testing.TB) string {
	t.Helper()
	dsn := NewEmptyDatabase(t)
	if err := migrate.Apply(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return dsn
}

// Connect opens a connection to dsn that is closed when the test ends.
func Connect(t testing.TB, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}
