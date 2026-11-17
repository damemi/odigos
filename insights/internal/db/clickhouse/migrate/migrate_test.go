package migrate

import (
	"io/fs"
	"strings"
	"testing"
)

// TestMigrationsHaveNoSemicolonInComments guards against a footgun in the
// golang-migrate ClickHouse driver: with MultiStatementEnabled it splits files
// on every ';' using a scanner that is blind to comments, so a ';' inside a
// comment is treated as a statement terminator and produces an empty query
// (ClickHouse error 62 "Empty query"), leaving the database dirty. Rather than
// rely on people remembering, fail loudly at build time.
func TestMigrationsHaveNoSemicolonInComments(t *testing.T) {
	for _, set := range []struct {
		files fs.FS
		dir   string
	}{
		{migrationFiles, "migrations"},
		{autopilotFiles, "autopilot"},
	} {
		entries, err := fs.ReadDir(set.files, set.dir)
		if err != nil {
			t.Fatalf("read %s dir: %v", set.dir, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := set.dir + "/" + e.Name()
			data, err := fs.ReadFile(set.files, name)
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			checkNoSemicolonInComments(t, name, data)
		}
	}
}

func checkNoSemicolonInComments(t *testing.T, name string, data []byte) {
	t.Helper()
	for lineNo, line := range strings.Split(string(data), "\n") {
		// Drop single-quoted values first so a legit ';' or '--' inside a
		// string literal is not mistaken for a comment with a ';'.
		sql := stripSingleQuoted(line)
		if idx := strings.Index(sql, "--"); idx >= 0 && strings.Contains(sql[idx:], ";") {
			t.Errorf("%s:%d: ';' in a comment breaks the ClickHouse migration splitter; remove it: %q",
				name, lineNo+1, strings.TrimRight(line, "\r"))
		}
	}
}

// autopilotTables is the ClickHouse schema the Redis trie flusher and
// LLM loop expect. The migrate Job must create every name here so serving
// replicas never run CREATE TABLE.
var autopilotTables = []string{"tx_runs", "tx_call_trie_structure", "tx_recommendations"}

func TestAutopilotMigrationsCoverKnownTables(t *testing.T) {
	found := map[string]bool{}
	entries, err := fs.ReadDir(autopilotFiles, "autopilot")
	if err != nil {
		t.Fatalf("read autopilot dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		data, err := fs.ReadFile(autopilotFiles, "autopilot/"+e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		sql := string(data)
		for _, table := range autopilotTables {
			if strings.Contains(sql, "CREATE TABLE IF NOT EXISTS "+table+" ") ||
				strings.Contains(sql, "CREATE TABLE IF NOT EXISTS "+table+"\n") ||
				strings.Contains(sql, "CREATE TABLE IF NOT EXISTS "+table+"(") {
				found[table] = true
			}
		}
	}
	for _, table := range autopilotTables {
		if !found[table] {
			t.Errorf("autopilot migrations missing CREATE TABLE IF NOT EXISTS %s", table)
		}
	}
}

// stripSingleQuoted blanks out the contents of single-quoted string literals on
// a single line so comment/terminator detection ignores characters that live
// inside a value.
func stripSingleQuoted(line string) string {
	var b strings.Builder
	b.Grow(len(line))
	inString := false
	for i := 0; i < len(line); i++ {
		if line[i] == '\'' {
			inString = !inString
			b.WriteByte('\'')
			continue
		}
		if inString {
			b.WriteByte(' ')
			continue
		}
		b.WriteByte(line[i])
	}
	return b.String()
}
