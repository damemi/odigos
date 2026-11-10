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
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := migrationFiles.ReadFile("migrations/" + e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for lineNo, line := range strings.Split(string(data), "\n") {
			// Drop single-quoted values first so a legit ';' or '--' inside a
			// string literal is not mistaken for a comment with a ';'.
			sql := stripSingleQuoted(line)
			if idx := strings.Index(sql, "--"); idx >= 0 && strings.Contains(sql[idx:], ";") {
				t.Errorf("%s:%d: ';' in a comment breaks the ClickHouse migration splitter; remove it: %q",
					e.Name(), lineNo+1, strings.TrimRight(line, "\r"))
			}
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
