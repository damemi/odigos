package migrate_test

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"

	migratelib "github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5"

	"github.com/odigos-io/odigos/insights/internal/db/postgres/migrate"
	"github.com/odigos-io/odigos/insights/internal/db/postgres/pgtest"
)

const latestVersion = 2

func migrator(t *testing.T, dsn string) *migratelib.Migrate {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.NewMigrator(db)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	// Closing the migrator closes db.
	t.Cleanup(func() { _, _ = m.Close() })
	return m
}

func wantVersion(t *testing.T, m *migratelib.Migrate, want uint) {
	t.Helper()
	v, dirty, err := m.Version()
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if v != want || dirty {
		t.Fatalf("schema at version %d (dirty %t), want %d, not dirty", v, dirty, want)
	}
}

func queryStrings(t *testing.T, conn *pgx.Conn, query string, args ...any) []string {
	t.Helper()
	rows, err := conn.Query(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	return out
}

// catalog lists the columns, indexes and constraints of the schemas the
// migrations write to, one line each.
func catalog(t *testing.T, conn *pgx.Conn) []string {
	t.Helper()
	var out []string
	out = append(out, queryStrings(t, conn, `
SELECT concat_ws(' ', 'column', table_schema, table_name, ordinal_position, column_name,
                 data_type, datetime_precision, is_nullable, column_default)
FROM information_schema.columns
WHERE table_schema IN ('public', 'adk')
ORDER BY table_schema, table_name, ordinal_position`)...)
	out = append(out, queryStrings(t, conn, `
SELECT concat_ws(' ', 'index', schemaname, indexdef)
FROM pg_indexes
WHERE schemaname IN ('public', 'adk')
ORDER BY schemaname, indexname`)...)
	out = append(out, queryStrings(t, conn, `
SELECT concat_ws(' ', 'constraint', n.nspname, c.conrelid::regclass, c.conname, pg_get_constraintdef(c.oid))
FROM pg_constraint c JOIN pg_namespace n ON n.oid = c.connamespace
WHERE n.nspname IN ('public', 'adk')
ORDER BY n.nspname, c.conrelid::regclass::text, c.conname`)...)
	return out
}

func diffLines(t *testing.T, before, after []string) {
	t.Helper()
	for _, l := range before {
		if !slices.Contains(after, l) {
			t.Errorf("gone: %s", l)
		}
	}
	for _, l := range after {
		if !slices.Contains(before, l) {
			t.Errorf("new: %s", l)
		}
	}
	if !t.Failed() && !slices.Equal(before, after) {
		t.Errorf("same lines in another order")
	}
}

func TestMigrateDownAndUp(t *testing.T) {
	dsn := pgtest.NewEmptyDatabase(t)
	conn := pgtest.Connect(t, dsn)
	if err := migrate.Apply(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	m := migrator(t, dsn)
	wantVersion(t, m, latestVersion)
	latest := catalog(t, conn)

	if err := m.Migrate(1); err != nil {
		t.Fatalf("down to 1: %v", err)
	}
	wantVersion(t, m, 1)
	if got := queryStrings(t, conn, `
SELECT name FROM unnest(ARRAY['interrogation_requested_probes', 'adk.sessions']) AS name
WHERE to_regclass(name) IS NOT NULL`); len(got) > 0 {
		t.Errorf("at version 1 these still exist: %v", got)
	}

	if err := m.Up(); err != nil {
		t.Fatalf("up again: %v", err)
	}
	wantVersion(t, m, latestVersion)
	diffLines(t, latest, catalog(t, conn))

	if err := m.Down(); err != nil {
		t.Fatalf("down to 0: %v", err)
	}
	if _, _, err := m.Version(); !errors.Is(err, migratelib.ErrNilVersion) {
		t.Errorf("version after down: %v, want none", err)
	}
	if got := queryStrings(t, conn, `SELECT nspname::text FROM pg_namespace WHERE nspname = 'adk'`); len(got) > 0 {
		t.Error("schema adk left after migrating down to 0")
	}
}

func TestMigration000002_Indexes(t *testing.T) {
	conn := pgtest.Connect(t, pgtest.NewDatabase(t))

	type index struct {
		unique, partial, valid bool
	}
	want := map[string]index{
		"interrogation_open_workload_idx": {unique: true, partial: true, valid: true},
		"interrogation_created_idx":       {valid: true},
		"events_session_timestamp_idx":    {valid: true},
	}
	rows, err := conn.Query(context.Background(), `
SELECT c.relname, i.indisunique, i.indpred IS NOT NULL, i.indisvalid
FROM pg_indexes x
JOIN pg_class c ON c.relname = x.indexname
JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = x.schemaname
JOIN pg_index i ON i.indexrelid = c.oid
WHERE x.schemaname = 'adk' AND x.indexname = ANY($1)`, []string{
		"interrogation_open_workload_idx", "interrogation_created_idx", "events_session_timestamp_idx",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]index{}
	var name string
	var idx index
	if _, err := pgx.ForEachRow(rows, []any{&name, &idx.unique, &idx.partial, &idx.valid}, func() error {
		got[name] = idx
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for n, w := range want {
		g, ok := got[n]
		if !ok {
			t.Errorf("index %s missing", n)
			continue
		}
		if g != w {
			t.Errorf("index %s: %+v, want %+v", n, g, w)
		}
	}
}
