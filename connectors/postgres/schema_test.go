package postgres

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// migrationsDir is the goose migration set that owns the schema_versions markers. It lives in a sibling
// top-level directory rather than in this module, so it cannot be go:embed-ed — the path is resolved
// relative to this package instead.
const migrationsDir = "../../connector-postgres/migrations"

// Markers the migrations use to establish or bump a schema's version. Both forms appear in this repo:
// the schema-creating migration INSERTs its initial version, and a later migration that changes that
// schema UPDATEs it. Capture groups differ in order between the two, hence the separate expressions.
var (
	insertVersionRe = regexp.MustCompile(`(?s)INSERT\s+INTO\s+connector_platform\.schema_versions[^;]*?VALUES\s*\(\s*'([^']+)'\s*,\s*'([^']+)'\s*\)`)
	updateVersionRe = regexp.MustCompile(`(?s)UPDATE\s+connector_platform\.schema_versions\s+SET\s+version\s*=\s*'([^']+)'[^;]*?WHERE\s+schema_name\s*=\s*'([^']+)'`)
)

// TestExpectedSchemaVersionsMatchMigrations is the guard against the failure mode that has no other
// detector: ExpectedSchemaVersions and the migrations drift apart. Every connector runtime verifies this
// whole map with exact equality at startup, so a migration that bumps a schema without bumping the
// constant (or vice versa) makes every connector pod refuse to start against a correctly-migrated
// database — and nothing in CI catches it, because the migrations workflow only runs SQL and the Go
// tests never read the SQL.
func TestExpectedSchemaVersionsMatchMigrations(t *testing.T) {
	got := versionsAfterAllUpMigrations(t)

	for schema, want := range ExpectedSchemaVersions {
		switch v, ok := got[schema]; {
		case !ok:
			t.Errorf("schema %q expects version %q but no migration writes a version marker for it", schema, want)
		case v != want:
			t.Errorf("schema %q: migrations end at version %q, ExpectedSchemaVersions says %q — bump whichever is stale, in lockstep", schema, v, want)
		}
	}
	for schema, v := range got {
		if _, ok := ExpectedSchemaVersions[schema]; !ok {
			t.Errorf("migrations write version %q for schema %q, which ExpectedSchemaVersions does not declare", v, schema)
		}
	}
}

// versionsAfterAllUpMigrations replays only the Up half of every migration, in filename order, and
// returns the final version each schema is left at.
func versionsAfterAllUpMigrations(t *testing.T) map[string]string {
	t.Helper()

	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		// Deliberately fatal rather than skipped: a silent skip would turn this guard off exactly when the
		// layout changes, which is when it is most needed.
		t.Fatalf("read %s: %v (this test must be able to read the connector-postgres migration set)", migrationsDir, err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	if len(files) == 0 {
		t.Fatalf("no .sql migrations found in %s", migrationsDir)
	}
	// Goose applies in version order, which for this set is filename order (zero-padded numeric prefixes).
	sort.Strings(files)

	versions := make(map[string]string)
	for _, name := range files {
		raw, err := os.ReadFile(filepath.Join(migrationsDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		up := upSection(string(raw))
		if up == "" {
			t.Errorf("%s: no `-- +goose Up` section found", name)
			continue
		}
		for _, m := range insertVersionRe.FindAllStringSubmatch(up, -1) {
			versions[m[1]] = m[2]
		}
		for _, m := range updateVersionRe.FindAllStringSubmatch(up, -1) {
			// UPDATE captures the version first, then the schema.
			versions[m[2]] = m[1]
		}
	}
	return versions
}

// upSection returns the text between `-- +goose Up` and `-- +goose Down` (to end of file when there is no
// Down). Only the Up half may be considered: the Down half contains the inverse version writes, and
// counting those would cancel out the very bumps under test.
func upSection(sql string) string {
	i := strings.Index(sql, "-- +goose Up")
	if i < 0 {
		return ""
	}
	rest := sql[i+len("-- +goose Up"):]
	if j := strings.Index(rest, "-- +goose Down"); j >= 0 {
		return rest[:j]
	}
	return rest
}

// TestMigrationsDeclareDownVersionRevert keeps the up -> down -> up round trip honest at the version
// marker: a migration that bumps a schema in its Up must put the version back in its Down, or a rollback
// leaves the database claiming a version whose tables are gone.
func TestMigrationsDeclareDownVersionRevert(t *testing.T) {
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		t.Fatalf("read %s: %v", migrationsDir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(migrationsDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		sql := string(raw)
		up := upSection(sql)
		touchesVersion := insertVersionRe.MatchString(up) || updateVersionRe.MatchString(up)
		if !touchesVersion {
			continue
		}
		down := downSection(sql)
		if down == "" {
			t.Errorf("%s: writes a schema version in Up but has no `-- +goose Down` section", e.Name())
			continue
		}
		// Either form of revert is acceptable: DELETE the marker (schema-creating migration) or UPDATE it
		// back to the prior version (schema-bumping migration).
		reverts := strings.Contains(down, "connector_platform.schema_versions")
		if !reverts {
			t.Errorf("%s: writes a schema version in Up but its Down never touches connector_platform.schema_versions", e.Name())
		}
	}
}

func downSection(sql string) string {
	i := strings.Index(sql, "-- +goose Down")
	if i < 0 {
		return ""
	}
	return sql[i+len("-- +goose Down"):]
}
