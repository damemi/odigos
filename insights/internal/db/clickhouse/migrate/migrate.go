// Package migrate applies the ClickHouse schema. It is deliberately separate
// from the request-serving repository (package clickhouse): it runs as a
// one-shot step from the Helm migration Job, owns the embedded SQL migrations,
// and pulls in the golang-migrate dependency the serving path never needs.
package migrate

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	migratelib "github.com/golang-migrate/migrate/v4"
	migrateclickhouse "github.com/golang-migrate/migrate/v4/database/clickhouse"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// migrationFiles holds the versioned SQL migrations (golang-migrate layout:
// <version>_<name>.{up,down}.sql). They are embedded into the binary so the
// same image can both serve traffic and, via `insights migrate`, apply the
// schema - no separate migration image or mounted files needed.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

// autopilotFiles is the schema for the autopilot database. It is a
// separate migration history because that database is not odigos_insights.
// Serving replicas and the Redis trie flusher do not run DDL.
//
//go:embed autopilot/*.sql
var autopilotFiles embed.FS

// Apply runs pending schema migrations, then returns. insightsDSN is the
// odigos_insights database and is required. autopilotDSN is the
// autopilot database; an empty value skips that schema, which is how
// installs with autopilot disabled behave.
//
// The two databases keep separate migration directories. Both start at version
// 000001, and golang-migrate records only the version number in
// schema_migrations inside the database the DSN names, so one directory cannot
// target both.
//
// This is meant to run exactly ONCE per rollout - from the Helm migration Job -
// not from every app replica, so replicas can scale without racing on DDL.
// golang-migrate skips versions it has already applied, so re-running is a no-op.
func Apply(insightsDSN, autopilotDSN string) error {
	if err := applyEmbedded(insightsDSN, migrationFiles, "migrations"); err != nil {
		return err
	}
	if autopilotDSN == "" {
		return nil
	}
	if err := ensureDatabase(autopilotDSN); err != nil {
		return fmt.Errorf("autopilot: %w", err)
	}
	if err := applyEmbedded(autopilotDSN, autopilotFiles, "autopilot"); err != nil {
		return fmt.Errorf("autopilot: %w", err)
	}
	return nil
}

// ensureDatabase creates the DSN's database from the default database, because
// the ClickHouse driver cannot connect to a database that does not exist yet.
func ensureDatabase(dsn string) error {
	opts, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		return fmt.Errorf("parse clickhouse dsn: %w", err)
	}
	name := opts.Auth.Database
	if name == "" || name == "default" {
		return fmt.Errorf("autopilot dsn must name its database")
	}
	opts.Auth.Database = "default"
	db := clickhouse.OpenDB(opts)
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping clickhouse: %w", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", quoteIdent(name))); err != nil {
		return fmt.Errorf("create database %s: %w", name, err)
	}
	return nil
}

func quoteIdent(name string) string {
	return "`" + name + "`"
}

func applyEmbedded(dsn string, files embed.FS, dir string) error {
	opts, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		return fmt.Errorf("parse clickhouse dsn: %w", err)
	}

	db := clickhouse.OpenDB(opts)
	defer db.Close()

	// MultiStatementEnabled lets a single migration file hold several
	// statements (the initial schema creates eight tables); golang-migrate
	// splits them on ';'.
	driver, err := migrateclickhouse.WithInstance(db, &migrateclickhouse.Config{
		DatabaseName:          opts.Auth.Database,
		MultiStatementEnabled: true,
	})
	if err != nil {
		return fmt.Errorf("clickhouse migrate driver: %w", err)
	}

	// NOTE: the ClickHouse driver splits files on ';' with a scanner that is
	// blind to comments, so a ';' inside a comment is mistaken for a statement
	// terminator and yields an empty query (ClickHouse error 62). Keep comments
	// semicolon-free; TestMigrationsHaveNoSemicolonInComments guards this.
	src, err := iofs.New(files, dir)
	if err != nil {
		return fmt.Errorf("load embedded migrations: %w", err)
	}

	m, err := migratelib.NewWithInstance("iofs", src, "clickhouse", driver)
	if err != nil {
		return fmt.Errorf("init migrator: %w", err)
	}

	if err := up(m); err != nil {
		return err
	}
	return nil
}

// up applies pending migrations, self-healing a dirty database once.
//
// golang-migrate flips schema_migrations.dirty=true before running a migration
// and clears it only on success, then REFUSES to run anything while dirty
// (ErrDirty). A single failed migration therefore wedges every future rollout -
// even after the SQL is fixed - until a human manually forces the version. That
// manual step is the "everytime the migrations fail I'm stuck" pain.
//
// Instead, on a dirty database we force the version back to the last good one
// and re-apply, so a corrected (or transient) migration recovers on the next
// deploy with no manual intervention. This is safe because our migrations are
// written to be re-runnable (IF NOT EXISTS / idempotent DDL); a migration that
// half-applies non-idempotent DDL would still need a manual reset, which the
// TestMigrations* guards and review are there to prevent.
func up(m *migratelib.Migrate) error {
	err := m.Up()
	if err == nil || errors.Is(err, migratelib.ErrNoChange) {
		return nil
	}

	var dirty migratelib.ErrDirty
	if !errors.As(err, &dirty) {
		return fmt.Errorf("apply migrations: %w", err)
	}

	// Clear the dirty marker by forcing to the version below the failed one, so
	// Up re-applies the failed migration rather than skipping it.
	if ferr := m.Force(dirty.Version - 1); ferr != nil {
		return fmt.Errorf("apply migrations: recover dirty v%d: %w", dirty.Version, ferr)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migratelib.ErrNoChange) {
		return fmt.Errorf("apply migrations: after recovering dirty v%d: %w", dirty.Version, err)
	}
	return nil
}
