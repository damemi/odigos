package store

import (
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	collectorsMigration = "testdata/managed_collectors.sql"
	collectorsSource    = "collectors.go"
)

func collectorsMigrationColumns(t *testing.T) []string {
	t.Helper()

	raw, err := os.ReadFile(collectorsMigration)
	if err != nil {
		t.Fatalf("read %s: %v", collectorsMigration, err)
	}
	up := string(raw)
	if i := strings.Index(up, "-- +goose Down"); i >= 0 {
		up = up[:i]
	}

	var cols []string
	for _, m := range columnDefRe.FindAllStringSubmatch(up, -1) {
		cols = append(cols, m[1])
	}
	if len(cols) == 0 {
		t.Fatalf("no column definitions found in %s; the parser or the migration format changed", collectorsMigration)
	}
	sort.Strings(cols)
	return cols
}

func collectorsSourceText(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(collectorsSource)
	if err != nil {
		t.Fatalf("read %s: %v", collectorsSource, err)
	}
	return string(raw)
}

// TestCollectorColumnsMatchTheMigration: collectorColumns drives every read and the scan is positional.
// A column added to the table but not here is invisible; one named here that does not exist fails every
// read at runtime, and there is no test database to catch either.
func TestCollectorColumnsMatchTheMigration(t *testing.T) {
	want := collectorsMigrationColumns(t)

	got := splitColumnList(collectorColumns)
	sort.Strings(got)

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("collectorColumns and the migration disagree:\n store:     %v\n migration: %v", got, want)
	}
}

// TestCollectorScanCoversEveryColumn: the scan is positional, so a column list and a scan that differ by
// one shift every subsequent value into the wrong field — a Collector's endpoint read as its cluster ref,
// with no error anywhere.
func TestCollectorScanCoversEveryColumn(t *testing.T) {
	src := collectorsSourceText(t)

	start := strings.Index(src, "if err := row.Scan(")
	if start < 0 {
		t.Fatal("could not find the row scan in collectors.go")
	}
	end := strings.Index(src[start:], "); err != nil {")
	if end < 0 {
		t.Fatal("could not find the end of the row scan")
	}
	scan := src[start : start+end]

	targets := strings.Count(scan, "&")
	if want := len(splitColumnList(collectorColumns)); targets != want {
		t.Errorf("the scan reads %d values but collectorColumns names %d", targets, want)
	}
}

// writerSets returns each concern-scoped writer and the columns it assigns.
func writerSets(t *testing.T) map[string][]string {
	t.Helper()
	src := collectorsSourceText(t)

	// Every write goes through s.update(ctx, key, "<concern>", `<set clause>`, ...) or is one of the two
	// statements that manage the row's existence.
	updateRe := regexp.MustCompile(`(?s)s\.update\(ctx, key, "([a-z]+)", ` + "`" + `(.*?)` + "`")
	assignRe := regexp.MustCompile(`(?m)^\s*([a-z_]+)\s*=`)

	sets := map[string][]string{}
	for _, m := range updateRe.FindAllStringSubmatch(src, -1) {
		concern, clause := m[1], m[2]
		for _, a := range assignRe.FindAllStringSubmatch(clause, -1) {
			sets[concern] = append(sets[concern], a[1])
		}
	}
	if len(sets) == 0 {
		t.Fatal("found no concern-scoped writers; the parser or the accessor shape changed")
	}
	return sets
}

// TestWritersDoNotOverlap is the safe-concurrency property, and the reason the accessors are split by
// concern at all.
//
// Several loops write one row at once: the deployer converges infrastructure, the OpAMP server records
// liveness, the config pusher records a push, the observer records replica counts. Because each names
// only its own columns, two of them writing at the same instant cannot clobber each other — which a
// whole-row UPSERT from each would, silently, by writing back whatever it last read.
func TestWritersDoNotOverlap(t *testing.T) {
	owner := map[string]string{}
	for concern, columns := range writerSets(t) {
		for _, col := range columns {
			if prev, taken := owner[col]; taken {
				t.Errorf("column %q is written by both the %q and %q writers; two loops writing this row "+
					"at once would clobber each other", col, prev, concern)
				continue
			}
			owner[col] = concern
		}
	}

	// updated_at is the deliberate exception, and it is stamped by s.update itself rather than by any
	// concern's SET clause — so no writer can forget it and none can disagree about it.
	if _, claimed := owner["updated_at"]; claimed {
		t.Error("a concern writes updated_at directly; s.update owns it so every write stamps it identically")
	}
	if !strings.Contains(collectorsSourceText(t), "SET %s, updated_at = now()") {
		t.Error("s.update no longer stamps updated_at, so a row's age would depend on which writer ran")
	}
}

// TestEveryWriterIsScopedToOneRow: an UPDATE that forgot the regional key would write every Collector in
// the connector, and every region's Collector would end up with the last one's endpoint.
func TestEveryWriterIsScopedToOneRow(t *testing.T) {
	src := collectorsSourceText(t)

	for _, stmt := range []string{"UPDATE %s.managed_collectors", "DELETE FROM %s.managed_collectors"} {
		idx := 0
		for {
			at := strings.Index(src[idx:], stmt)
			if at < 0 {
				break
			}
			at += idx
			idx = at + len(stmt)

			end := strings.Index(src[at:], "`")
			if end < 0 {
				t.Fatalf("could not find the end of a %q statement", stmt)
			}
			body := src[at : at+end]
			// ClearCollectorCredential is keyed by collector_id instead, which is its whole purpose: one
			// Collector identity can span more than one row after a network is re-keyed.
			if strings.Contains(body, "collector_id = $2") {
				if !strings.Contains(body, "connector_name = $1") {
					t.Errorf("a statement is not scoped to this connector: %s", body)
				}
				continue
			}
			for _, key := range []string{"connector_name = $1", "region = $2"} {
				if !strings.Contains(body, key) {
					t.Errorf("a %q statement is missing %q from its key:\n%s", stmt, key, body)
				}
			}
		}
	}
}

// TestOriginIsNeverOverwritten: origin records who CREATED the infrastructure, which no later event
// changes. Re-running a converge over an adopted stack must not relabel it as Odigos-created — that is
// the difference between a stack teardown removes and one it leaves alone.
func TestOriginIsNeverOverwritten(t *testing.T) {
	src := collectorsSourceText(t)

	start := strings.Index(src, "ON CONFLICT (connector_name, region) DO UPDATE SET")
	if start < 0 {
		t.Fatal("could not find the upsert conflict clause")
	}
	end := strings.Index(src[start:], "`")
	clause := src[start : start+end]

	if regexp.MustCompile(`(?m)^\s*origin\s*=`).MatchString(clause) {
		t.Error("the upsert overwrites origin; a converge pass would relabel an adopted stack as Odigos-created")
	}
	// adopted_at is likewise written once. Re-stamping it on every converge would make "when Odigos took
	// this over" mean "the last time anything reconciled".
	if !strings.Contains(clause, "COALESCE(managed_collectors.adopted_at") {
		t.Error("the upsert re-stamps adopted_at rather than keeping the first value")
	}
}

// TestNoCredentialMaterialIsStored is the "complete, non-reversible credential metadata" criterion.
//
// This table holds refs: where the value lives and which issuance is in force. The value exists only
// between minting and the secret-store write, and its hash lives in collector_credentials — so a report
// built from this table, or a backup of it, carries nothing that authenticates.
func TestNoCredentialMaterialIsStored(t *testing.T) {
	for _, col := range collectorsMigrationColumns(t) {
		for _, banned := range []string{"token", "secret_value", "sha256", "password", "credential_value"} {
			if strings.Contains(col, banned) {
				t.Errorf("managed_collectors has a column %q; this table holds refs, never credential material", col)
			}
		}
	}

	// And the two credential columns that DO exist must be refs by name, so nobody reaches for them
	// expecting a value.
	cols := strings.Join(collectorsMigrationColumns(t), ",")
	for _, want := range []string{"credential_ref", "active_credential_id", "license_secret_ref"} {
		if !strings.Contains(cols, want) {
			t.Errorf("managed_collectors is missing %q", want)
		}
	}
}

// TestRecordedIsTheDeletionGate: an object may be deleted only when its tag says Odigos created it AND
// its ref is recorded here. A row that lost a ref costs delete authority rather than granting it, which
// is the safe direction — leaked infrastructure is visible and recoverable, a deleted customer resource
// is not.
func TestRecordedIsTheDeletionGate(t *testing.T) {
	rec := CollectorRecord{
		WorkloadRef:   "provider://runtime/collector",
		RevisionRef:   "provider://runtime/collector/revisions/7",
		RuntimeRef:    "provider://runtime",
		FrontendRef:   "provider://frontend/collector",
		IngressRef:    "provider://ingress/collector",
		BackendRefs:   []string{"provider://backend/otlp-grpc"},
		PlacementRefs: []string{"provider://placement/a", "provider://placement/b"},
	}

	for _, ref := range []string{
		rec.WorkloadRef, rec.RevisionRef, rec.RuntimeRef, rec.FrontendRef,
		rec.IngressRef, rec.BackendRefs[0], rec.PlacementRefs[0], rec.PlacementRefs[1],
	} {
		if !rec.Recorded(ref) {
			t.Errorf("Recorded(%q) is false, so teardown would refuse to delete an object Odigos created", ref)
		}
	}

	for _, ref := range []string{
		"", "provider://ingress/someone-elses", "provider://runtime/customers-own",
	} {
		if rec.Recorded(ref) {
			t.Errorf("Recorded(%q) is true, which would grant delete authority over an unrecorded object", ref)
		}
	}

	// An empty record grants nothing. Losing this table loses delete authority until a tag scan
	// re-records the refs — the correct direction to fail in.
	empty := CollectorRecord{}
	for _, ref := range []string{"", "provider://ingress/collector", "anything"} {
		if empty.Recorded(ref) {
			t.Errorf("an empty record granted delete authority over %q", ref)
		}
	}
}

// TestCollectorsTableIsProviderScoped: unlike collector_credentials, this table is per provider schema —
// the same structure across providers with only the schema name differing, exactly like sources and
// destinations.
func TestCollectorsTableIsProviderScoped(t *testing.T) {
	src := collectorsSourceText(t)
	if strings.Contains(src, "connector_platform.managed_collectors") {
		t.Error("managed_collectors is addressed in the platform schema; it belongs to the provider schema")
	}
	if !strings.Contains(src, "%s.managed_collectors") {
		t.Error("managed_collectors is not addressed through the provider schema")
	}
	// The schema name is interpolated rather than bound, so it must come from the validated field and
	// never from a caller.
	if regexp.MustCompile(`fmt\.Sprintf\([^)]*managed_collectors`).MatchString(src) &&
		!strings.Contains(src, "s.schema)") {
		t.Error("the provider schema is interpolated from something other than the validated s.schema")
	}
}

// TestPlaceholdersAreContiguous: s.update prepends the two row-key parameters, so a SET clause must start
// at $3 and use every number up to its last. A gap or a repeat binds the wrong value to a column.
func TestPlaceholdersAreContiguous(t *testing.T) {
	src := collectorsSourceText(t)
	updateRe := regexp.MustCompile(`(?s)s\.update\(ctx, key, "([a-z]+)", ` + "`" + `(.*?)` + "`")
	numRe := regexp.MustCompile(`\$(\d+)`)

	for _, m := range updateRe.FindAllStringSubmatch(src, -1) {
		concern, clause := m[1], m[2]
		seen := map[int]bool{}
		highest := 2
		for _, n := range numRe.FindAllStringSubmatch(clause, -1) {
			v, err := strconv.Atoi(n[1])
			if err != nil {
				t.Fatalf("%s: placeholder %q: %v", concern, n[1], err)
			}
			if v < 3 {
				t.Errorf("%s writer uses %s, which s.update reserves for the row key", concern, n[0])
			}
			seen[v] = true
			if v > highest {
				highest = v
			}
		}
		for i := 3; i <= highest; i++ {
			if !seen[i] {
				t.Errorf("%s writer skips $%d, so every later value binds to the wrong column", concern, i)
			}
		}
	}
}

// TestTheLatchColumnsOnlyEverTakeTheirFirstValue.
//
// Two columns on this row are LATCHES rather than fields — `idle_since` and `first_ready_at` — and both
// record when something FIRST happened. A writer that assigned unconditionally would move them on every
// pass, which is not a cosmetic difference in either case:
//
//   - idle_since is a deadline. A value that advanced every pass would push the retention grace out by
//     one reconcile interval per pass, so a Collector configured to be deleted after 48 hours idle would
//     never be deleted at all, and the policy would silently never apply.
//   - first_ready_at is when a Collector first worked, which is what the cutover announcement and any
//     "ready since" reporting rest on. A moving value makes the row say a Collector became ready a
//     moment ago, for ever.
//
// A source guard rather than a behaviour test because this package has no database: what it pins is that
// the SET clause keeps the existing value, which is the only place the rule can be expressed.
func TestTheLatchColumnsOnlyEverTakeTheirFirstValue(t *testing.T) {
	src := collectorsSourceText(t)

	for _, latch := range []struct{ writer, column string }{
		{"RecordCollectorIdle", "idle_since"},
		{"RecordCollectorFirstReady", "first_ready_at"},
	} {
		start := strings.Index(src, "func (s *Store) "+latch.writer+"(")
		if start < 0 {
			t.Errorf("could not find %s", latch.writer)
			continue
		}
		body := src[start:]
		if end := strings.Index(body, "\n}\n"); end > 0 {
			body = body[:end]
		}
		if !strings.Contains(body, "COALESCE("+latch.column) {
			t.Errorf("%s assigns %s without COALESCE, so it moves on every pass rather than latching:\n%s",
				latch.writer, latch.column, body)
		}
	}
}
