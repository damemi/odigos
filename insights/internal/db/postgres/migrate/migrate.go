// Package migrate applies the Postgres schema. It is deliberately separate
// from the request-serving repository (package postgres): it runs as a
// one-shot step from the Helm migration Job, owns the embedded SQL migrations,
// and pulls in the golang-migrate dependency the serving path never needs.
package migrate

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"

	migratelib "github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// migrationFiles holds the versioned SQL migrations (golang-migrate layout:
// <version>_<name>.{up,down}.sql). They are embedded into the binary so the
// same image can both serve traffic and, via `insights migrate`, apply the
// schema - no separate migration image or mounted files needed.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

// Apply runs all pending schema migrations at dsn, then returns. It opens its
// own short-lived connection (independent of the serving Repo) so it can run
// as a standalone step.
//
// This is meant to run once per rollout from the Helm migration Job. The
// driver holds a Postgres advisory lock while migrating and runs each file in
// one implicit transaction, so concurrent runs serialize and a failed file
// leaves no partial schema behind.
func Apply(dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open postgres: %w", err)
	}
	defer db.Close()

	m, err := newMigrator(db)
	if err != nil {
		return err
	}
	return up(m)
}

func newMigrator(db *sql.DB) (*migratelib.Migrate, error) {
	driver, err := migratepgx.WithInstance(db, &migratepgx.Config{})
	if err != nil {
		return nil, fmt.Errorf("postgres migrate driver: %w", err)
	}

	src, err := iofs.New(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("load embedded migrations: %w", err)
	}

	m, err := migratelib.NewWithInstance("iofs", src, "pgx5", driver)
	if err != nil {
		return nil, fmt.Errorf("init migrator: %w", err)
	}
	return m, nil
}

// up applies pending migrations, self-healing a dirty database once.
//
// golang-migrate flips schema_migrations.dirty=true before running a migration
// and clears it only on success, then refuses to run anything while dirty. A
// failed file rolls back as a whole on Postgres, so forcing the version back to
// the last good one and re-applying is safe and recovers on the next deploy
// without manual intervention.
func up(m *migratelib.Migrate) error {
	err := m.Up()
	if err == nil || errors.Is(err, migratelib.ErrNoChange) {
		return nil
	}

	var dirty migratelib.ErrDirty
	if !errors.As(err, &dirty) {
		return fmt.Errorf("apply migrations: %w", err)
	}

	if ferr := m.Force(dirty.Version - 1); ferr != nil {
		return fmt.Errorf("apply migrations: recover dirty v%d: %w", dirty.Version, ferr)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migratelib.ErrNoChange) {
		return fmt.Errorf("apply migrations: after recovering dirty v%d: %w", dirty.Version, err)
	}
	return nil
}
