package store

import (
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorcredential"
)

// The credentials migration and this file's own source. Both are read rather than assumed because the
// store package has no database to test against: these checks are the only thing standing between a
// mistyped column and a runtime failure in a customer's cluster.
const (
	credentialsMigration = "../../../connector-postgres/migrations/0011_connector_platform_collector_credentials.sql"
	credentialsSource    = "collectorcredentials.go"
)

// columnDefRe matches a column definition line inside CREATE TABLE, and nothing else. Table-level
// constraints are excluded by requiring a type keyword.
var columnDefRe = regexp.MustCompile(`(?m)^\s+([a-z_][a-z0-9_]*)\s+(TEXT|TIMESTAMPTZ|JSONB|BIGSERIAL|BOOLEAN|INTEGER)\b`)

func migrationColumns(t *testing.T) []string {
	t.Helper()

	raw, err := os.ReadFile(credentialsMigration)
	if err != nil {
		// Fatal rather than skipped: a silent skip turns this guard off exactly when the layout changes,
		// which is when it is most needed.
		t.Fatalf("read %s: %v", credentialsMigration, err)
	}
	sql := string(raw)

	// Only the Up half. The Down half drops things and would contribute no columns, but bounding the
	// search keeps a future ALTER in Down from being read as a definition.
	up := sql
	if i := strings.Index(sql, "-- +goose Down"); i >= 0 {
		up = sql[:i]
	}

	var cols []string
	for _, m := range columnDefRe.FindAllStringSubmatch(up, -1) {
		cols = append(cols, m[1])
	}
	if len(cols) == 0 {
		t.Fatalf("no column definitions found in %s; the parser or the migration format changed", credentialsMigration)
	}
	sort.Strings(cols)
	return cols
}

func sourceText(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(credentialsSource)
	if err != nil {
		t.Fatalf("read %s: %v", credentialsSource, err)
	}
	return string(raw)
}

func splitColumnList(list string) []string {
	var out []string
	for _, part := range strings.Split(list, ",") {
		if name := strings.TrimSpace(part); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// TestSelectColumnsMatchTheMigration: credentialColumns drives every read in this package, and the
// scan is positional. A column added to the table without being added here is invisible; a column named
// here that does not exist fails every read at runtime, and there is no test database to catch it.
func TestSelectColumnsMatchTheMigration(t *testing.T) {
	want := migrationColumns(t)

	got := splitColumnList(credentialColumns)
	sort.Strings(got)

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("credentialColumns and the migration disagree:\n store:     %v\n migration: %v", got, want)
	}
}

// TestInsertNamesRealColumns catches a typo in the INSERT, which otherwise surfaces as a failed
// issuance in a running connector.
func TestInsertNamesRealColumns(t *testing.T) {
	known := map[string]bool{}
	for _, c := range migrationColumns(t) {
		known[c] = true
	}

	for _, col := range insertColumns(t) {
		if !known[col] {
			t.Errorf("the INSERT names %q, which %s does not define", col, credentialsMigration)
		}
	}
}

// TestInsertColumnCountMatchesItsPlaceholders: a column list and a VALUES list that drift apart by one
// shift every subsequent value into the wrong column — a binding silently assembled from the wrong
// fields, which the gateway then refuses for reasons nobody can trace back to here.
func TestInsertColumnCountMatchesItsPlaceholders(t *testing.T) {
	cols := insertColumns(t)

	src := sourceText(t)
	start := strings.Index(src, "INSERT INTO `+credentialsTable+`")
	if start < 0 {
		t.Fatal("could not find the credential INSERT in the source")
	}
	stmt := src[start:]
	if end := strings.Index(stmt, "`,"); end > 0 {
		stmt = stmt[:end]
	}

	highest := 0
	for _, m := range regexp.MustCompile(`\$(\d+)`).FindAllStringSubmatch(stmt, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("placeholder %q: %v", m[1], err)
		}
		if n > highest {
			highest = n
		}
	}

	if highest != len(cols) {
		t.Errorf("the INSERT names %d columns but binds %d placeholders", len(cols), highest)
	}
}

// insertColumns pulls the column list out of the credential INSERT.
func insertColumns(t *testing.T) []string {
	t.Helper()

	src := sourceText(t)
	marker := "INSERT INTO `+credentialsTable+`"
	start := strings.Index(src, marker)
	if start < 0 {
		t.Fatalf("could not find %q in %s", marker, credentialsSource)
	}
	rest := src[start:]
	open := strings.Index(rest, "(")
	close := strings.Index(rest, ")")
	if open < 0 || close < open {
		t.Fatal("could not read the INSERT column list")
	}
	cols := splitColumnList(rest[open+1 : close])
	if len(cols) == 0 {
		t.Fatal("the INSERT column list parsed as empty")
	}
	return cols
}

// TestOnlyIssuanceReturnsACredentialValue is the "raw token shown only once" criterion, enforced over
// the whole Store rather than by reading the code.
//
// The value exists in exactly two returns — the issue and the replace paths — and nothing that reads
// the table can hand one back. That is not a convention that could be broken by a helpful accessor
// added later: only the hash is persisted, so a read path CANNOT produce a value, and this test fails
// if somebody adds a signature that claims otherwise.
func TestOnlyIssuanceReturnsACredentialValue(t *testing.T) {
	allowed := map[string]bool{
		"IssueCollectorCredential":   true,
		"ReplaceCollectorCredential": true,
	}

	storeType := reflect.TypeOf(&Store{})
	var carriers []string
	for i := 0; i < storeType.NumMethod(); i++ {
		m := storeType.Method(i)
		for j := 0; j < m.Type.NumOut(); j++ {
			if !carriesSecret(m.Type.Out(j), map[reflect.Type]bool{}) {
				continue
			}
			if !allowed[m.Name] {
				carriers = append(carriers, m.Name)
			}
		}
	}
	if len(carriers) != 0 {
		t.Errorf("these Store methods can return a credential value: %v — only issuance may", carriers)
	}

	// And the converse: if the allowed methods stopped returning one, the reveal-once path would be
	// gone and nothing could install a credential.
	for name := range allowed {
		m, ok := storeType.MethodByName(name)
		if !ok {
			t.Errorf("%s no longer exists; issuance is the only path that can produce a value", name)
			continue
		}
		var found bool
		for j := 0; j < m.Type.NumOut(); j++ {
			if carriesSecret(m.Type.Out(j), map[reflect.Type]bool{}) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s no longer returns a credential value", name)
		}
	}
}

// carriesSecret reports whether a value of this type can transitively hold a Secret.
func carriesSecret(t reflect.Type, seen map[reflect.Type]bool) bool {
	if t == reflect.TypeOf(collectorcredential.Secret{}) {
		return true
	}
	if seen[t] {
		return false
	}
	seen[t] = true

	switch t.Kind() {
	case reflect.Ptr, reflect.Slice, reflect.Array, reflect.Chan:
		return carriesSecret(t.Elem(), seen)
	case reflect.Map:
		return carriesSecret(t.Key(), seen) || carriesSecret(t.Elem(), seen)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			if carriesSecret(t.Field(i).Type, seen) {
				return true
			}
		}
	}
	return false
}

// TestCredentialsTableIsPlatformScoped: the gateway resolves a credential by token hash before it knows
// which provider issued it, so this table cannot live in a provider schema. A reader that pointed it at
// s.Schema() would work for one provider and break the gateway.
func TestCredentialsTableIsPlatformScoped(t *testing.T) {
	if !strings.HasPrefix(credentialsTable, "connector_platform.") {
		t.Errorf("credentialsTable = %q, want a connector_platform table", credentialsTable)
	}
	if strings.Contains(sourceText(t), "s.schema") {
		t.Error("the credential store interpolates the provider schema; this table is platform-wide")
	}
}

// TestEveryWriteIsAudited: a credential's value is never stored and its hash is not reversible, so the
// audit trail is the only account of its life. Every state-changing statement must sit in a
// transaction that also writes one.
func TestEveryWriteIsAudited(t *testing.T) {
	src := sourceText(t)

	// The exception is deliberate and worth naming: activation records that a value reached the secret
	// store, which is a step of an issuance already audited under its own credential id.
	for _, fn := range []string{"IssueCollectorCredential", "ReplaceCollectorCredential", "revoke"} {
		body := functionBody(t, src, fn)
		if !strings.Contains(body, "s.audit(ctx, tx,") {
			t.Errorf("%s changes credential state without writing an audit row", fn)
		}
		if !strings.Contains(body, "s.inTx(ctx,") {
			t.Errorf("%s writes outside a transaction, so a change could commit without its audit row", fn)
		}
	}
}

// functionBody returns the source of one function, from its declaration to the next one.
func functionBody(t *testing.T, src, name string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^func (?:\(s \*Store\) )?` + regexp.QuoteMeta(name) + `\(`)
	loc := re.FindStringIndex(src)
	if loc == nil {
		t.Fatalf("could not find func %s in %s", name, credentialsSource)
	}
	rest := src[loc[1]:]
	if end := regexp.MustCompile(`(?m)^func `).FindStringIndex(rest); end != nil {
		rest = rest[:end[0]]
	}
	return rest
}

// TestTeardownRevokesEveryLiveCredential pins the teardown rule at the one place it is implemented.
//
// EVERY live credential, not the active one. A teardown that lands during a rotation finds an active
// credential and a superseded one, and the superseded one is still accepted for the length of the old
// tasks' drain — so leaving it would mean the one credential still able to open an authenticated OpAMP
// session, and be handed the destination's own credential, is the one nobody was tracking.
//
// A source guard rather than a behaviour test because this file has no database: what it pins is that
// the filter is Live(), which is active OR superseded, rather than a comparison against one state.
func TestTeardownRevokesEveryLiveCredential(t *testing.T) {
	src := sourceText(t)

	start := strings.Index(src, "func (s *Store) RevokeCollectorCredentials(")
	if start < 0 {
		t.Fatal("could not find RevokeCollectorCredentials")
	}
	body := src[start:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}

	if !strings.Contains(body, "if !r.Live() {") {
		t.Errorf("RevokeCollectorCredentials does not filter on Live(). A teardown during a rotation has "+
			"TWO live credentials and both must go; a comparison against a single state leaves the "+
			"superseded one accepted:\n%s", body)
	}
	if !strings.Contains(body, "s.CollectorCredentials(ctx, collectorID)") {
		t.Errorf("RevokeCollectorCredentials does not read every issuance for the Collector:\n%s", body)
	}
}
