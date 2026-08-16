package store

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
)

// buildObjectRe / jsonbSetRe locate the two forms inventory.go uses to write a metadata key:
// jsonb_build_object('k1', …, 'k2', …) — which may carry several pairs in one call — and
// jsonb_set(…, '{key}', …). buildObjectKeyRe then pulls each key out of a build_object argument list.
var (
	buildObjectRe    = regexp.MustCompile(`(?s)jsonb_build_object\((.*?)\)\s*(?:,|\n)`)
	buildObjectKeyRe = regexp.MustCompile(`'([a-z_]+)'\s*,`)
	jsonbSetRe       = regexp.MustCompile(`jsonb_set\([^,]+,\s*'\{([a-z_]+)\}'`)
)

// metadataKeysWritten extracts every metadata key written by SQL in src.
func metadataKeysWritten(src string) map[string]bool {
	out := map[string]bool{}
	for _, m := range buildObjectRe.FindAllStringSubmatch(src, -1) {
		for _, k := range buildObjectKeyRe.FindAllStringSubmatch(m[1], -1) {
			out[k[1]] = true
		}
	}
	for _, m := range jsonbSetRe.FindAllStringSubmatch(src, -1) {
		out[m[1]] = true
	}
	return out
}

// sharedWithDiscovery are the metadata keys another writer may set even though discovery owns them, with the
// reason each is safe. Only `instrumented` qualifies: the instrumentor writes it through immediately after an
// instrument or rollback so a reconcile before the next discovery cycle sees the new state, and discovery
// re-derives the SAME fact from the function's live environment. Both writers compute one truth from one source,
// so discovery refreshing it is convergence, not data loss.
//
// Every other key belongs to exactly one writer. Adding to this set means claiming two writers cannot disagree —
// which for a conformance verdict (computed from intent discovery does not read) would be false.
var sharedWithDiscovery = map[string]bool{"instrumented": true}

// TestUpsertLambdaPreservesOtherWritersKeys guards the bug that made the whole observation record unstorable.
//
// Four writers share one metadata column: discovery (UpsertLambda), the instrumentor (phase, error), and now the
// observer (the conformance record). UpsertLambda runs on every discovery cycle and used to name the keys it
// PRESERVED — a list written when only two existed, so every key added afterwards was silently wiped minutes
// after being written, and a feature built on it would have failed intermittently with no error anywhere.
//
// It now names the keys discovery OWNS and subtracts those, which inverts the failure mode: a key added by any
// other writer is preserved by default. That inversion is the fix, so this asserts the form itself — a revert to
// an enumerated preserve-list would compile, pass every other test, and reintroduce the bug.
func TestUpsertLambdaPreservesOtherWritersKeys(t *testing.T) {
	src := readInventorySource(t)
	upsert := statementIn(t, src, "func (s *Store) UpsertLambda")

	// (a) The merge must SUBTRACT the discovery-owned key list, not enumerate what to keep.
	if !strings.Contains(upsert, "- $11::text[]") {
		t.Error("UpsertLambda no longer subtracts lambdaDiscoveryMetadataKeys from the existing metadata. " +
			"An enumerated preserve-list wipes every key a future writer adds; keep the exclusion form.")
	}
	if strings.Contains(upsert, "jsonb_build_object('instrumentation_error'") {
		t.Error("UpsertLambda is enumerating preserved keys again; that is the shape that wiped the observation record")
	}

	// (b) Every key another statement writes must be absent from the discovery-owned list, or discovery deletes
	// it on the next scan.
	owned := map[string]bool{}
	for _, k := range lambdaDiscoveryMetadataKeys {
		owned[k] = true
	}
	written := metadataKeysWritten(src)
	// The keys UpsertLambda itself writes go through Go's metadata map rather than SQL, so they are named here;
	// everything in `written` came from another writer's statement.
	for _, k := range []string{"instrumented", "state", "last_update_status"} {
		if !owned[k] {
			t.Errorf("discovery writes metadata key %q but does not own it; it will not be refreshed correctly", k)
		}
	}
	if len(written) == 0 {
		t.Fatal("no metadata key writes found in inventory.go; this test has stopped checking anything")
	}
	for k := range written {
		if owned[k] && !sharedWithDiscovery[k] {
			t.Errorf("metadata key %q is written by a non-discovery statement AND listed as discovery-owned, "+
				"so every discovery cycle deletes it", k)
		}
	}
	// The observation record specifically — the keys whose loss this bug would have hidden.
	for _, k := range []string{"conformance", "conformance_missing", "conformance_since", "observation_error"} {
		if !written[k] {
			t.Errorf("no statement writes observation key %q; the report reads it, so it must be written somewhere", k)
		}
	}
}

// TestSetLambdaObservationTouchesOnlyObservationState pins the observer's write boundary. It is the read-only
// path, and the one thing it does write must not reach into another writer's state: setting `instrumented` here
// would let a conformance verdict override discovery's view of whether instrumentation exists, and refreshing
// last_seen_at would let an observation keep a deleted function alive past the prune cutoff.
func TestSetLambdaObservationTouchesOnlyObservationState(t *testing.T) {
	stmt := statementIn(t, readInventorySource(t), "func (s *Store) SetLambdaObservation")
	for _, forbidden := range []string{"'instrumented'", "{instrumented}", "last_seen_at", "deleted_at = NULL",
		"instrumentation_phase", "instrumentation_error"} {
		if strings.Contains(stmt, forbidden) {
			t.Errorf("SetLambdaObservation writes %q, which belongs to discovery or the instrumentor", forbidden)
		}
	}
	// It must write the two fingerprint columns: they are what make the drift clock's write-skip safe.
	for _, want := range []string{"env_hash", "config_hash"} {
		if !strings.Contains(stmt, want) {
			t.Errorf("SetLambdaObservation does not write %s", want)
		}
	}
}

// TestSettlingDerivedInTheReportQuery covers the fact discovery newly captures, at the place the REPORT reads it.
// A configuration change still being applied looks identical to a half-applied instrumentation, so this
// derivation is what stops every customer deploy from flashing a drift error while it lands.
//
// It asserts the SQL against the lifecycle constants rather than re-implementing the predicate in Go: a second Go
// copy would be a duplicate definition of "settling" that could quietly disagree with the query that actually
// runs. (The observer answers the same question over its own live read — instrumentor.lifecycleSettling — where
// the SDK enums are available and the answer is seconds fresh rather than a discovery cycle old.)
func TestSettlingDerivedInTheReportQuery(t *testing.T) {
	lambdaSQL := workloadQueries()[0].sql
	for _, want := range []string{
		"metadata->>'state','') = '" + lambdaStatePending + "'",
		"metadata->>'last_update_status','') = '" + lambdaUpdateInProgress + "'",
	} {
		if !strings.Contains(lambdaSQL, want) {
			t.Errorf("the lambda report query does not derive settling from %q", want)
		}
	}
	// The two must be OR-ed: either signal alone means a change is in flight, and requiring both would report a
	// settling function as drifted for the whole window.
	if !regexp.MustCompile(`(?s)metadata->>'state'.*?\bOR\b.*?metadata->>'last_update_status'`).MatchString(lambdaSQL) {
		t.Error("settling must be state = Pending OR lastUpdateStatus = InProgress; requiring both misses most of the window")
	}
	// The constants must match what AWS actually returns, since nothing else compares them to the SDK.
	if lambdaStatePending != string(lambdatypes.StatePending) {
		t.Errorf("lambdaStatePending = %q, AWS returns %q", lambdaStatePending, lambdatypes.StatePending)
	}
	if lambdaUpdateInProgress != string(lambdatypes.LastUpdateStatusInProgress) {
		t.Errorf("lambdaUpdateInProgress = %q, AWS returns %q", lambdaUpdateInProgress, lambdatypes.LastUpdateStatusInProgress)
	}
}

// TestObservationScanDegradesGracefully: the report path must never fail a whole listing over one unreadable
// value. A garbled component list or timestamp costs that workload its detail, not every workload its report.
func TestObservationScanDegradesGracefully(t *testing.T) {
	if got := decodeStringSlice([]byte(`{"not":"an array"}`), "conformance_missing"); got != nil {
		t.Errorf("unreadable component list = %v, want nil", got)
	}
	if got := decodeStringSlice(nil, "layers"); got != nil {
		t.Errorf("absent layers = %v, want nil", got)
	}
	if got := decodeStringSlice([]byte(`["a","b"]`), "layers"); len(got) != 2 {
		t.Errorf("valid layers = %v, want 2 entries", got)
	}

	if got := parseObservedAt(strPtr("not a timestamp")); !got.IsZero() {
		t.Errorf("unparsable timestamp = %v, want the zero time (reported as unknown, not as 1970)", got)
	}
	if got := parseObservedAt(nil); !got.IsZero() {
		t.Errorf("absent timestamp = %v, want the zero time", got)
	}
	want := time.Date(2026, 8, 1, 9, 30, 0, 0, time.UTC)
	if got := parseObservedAt(strPtr(want.Format(time.RFC3339))); !got.Equal(want) {
		t.Errorf("timestamp = %v, want %v", got, want)
	}

	// A NULL conformance column is Unknown, not a scan error: rows written before observation existed are the
	// normal case, and Unknown is the honest verdict for them.
	var c api.Conformance
	if err := (&conformanceTarget{&c}).Scan(nil); err != nil || c != api.ConformanceUnknown {
		t.Errorf("NULL conformance = (%q, %v), want (Unknown, nil)", c, err)
	}
	if err := (&conformanceTarget{&c}).Scan([]byte("Partial")); err != nil || c != api.ConformancePartial {
		t.Errorf("bytes conformance = (%q, %v), want (Partial, nil)", c, err)
	}
	if err := (&conformanceTarget{&c}).Scan("Conformant"); err != nil || c != api.ConformanceConformant {
		t.Errorf("string conformance = (%q, %v), want (Conformant, nil)", c, err)
	}
}

// TestListWorkloadsQueriesShareColumnShape: all three queries are read by ONE Scan (scanWorkloadRows), so a
// column added to one and not the others is a runtime failure of the entire report path — every workload of
// every kind stops being reported — with no compile-time signal whatsoever. This wave added six columns to each
// query, which is exactly when that goes wrong.
//
// It runs against the assembled SQL rather than the source text, because the column lists are built from shared
// constants (noObservationCols) that source-text inspection cannot see through.
func TestListWorkloadsQueriesShareColumnShape(t *testing.T) {
	queries := workloadQueries()
	if len(queries) < 2 {
		t.Fatalf("found %d workload queries; nothing to compare", len(queries))
	}
	selectList := func(sql string) string {
		m := regexp.MustCompile(`(?s)SELECT (.*?)\s+FROM connector_aws`).FindStringSubmatch(sql)
		if m == nil {
			t.Fatalf("no SELECT ... FROM connector_aws in %q", sql)
		}
		return m[1]
	}
	want := topLevelCommas(selectList(queries[0].sql))
	// Sanity floor: the scan reads 19 columns. If a refactor drops the shared shape to something trivially
	// small, the comparison below would still pass while checking nothing.
	if want < 10 {
		t.Fatalf("query 0 selects only %d columns; this test has stopped checking anything", want)
	}
	for i, q := range queries[1:] {
		if got := topLevelCommas(selectList(q.sql)); got != want {
			t.Errorf("query %d (%s) selects %d columns, query 0 selects %d; one Scan reads all of them",
				i+1, q.kind, got, want)
		}
	}
}

// topLevelCommas counts commas outside parentheses and quotes — i.e. the column separators of a select list.
func topLevelCommas(sel string) int {
	depth, quoted, n := 0, false, 1
	for _, r := range sel {
		switch {
		case r == '\'':
			quoted = !quoted
		case quoted:
		case r == '(':
			depth++
		case r == ')':
			depth--
		case r == ',' && depth == 0:
			n++
		}
	}
	return n
}

func readInventorySource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("inventory.go")
	if err != nil {
		t.Fatalf("read inventory.go: %v", err)
	}
	return string(b)
}

// statementIn returns the SQL literal inside the function starting at marker — the text between the first pair
// of backticks after it. Bounding it to the literal matters: reading to the next `func` would sweep in the
// package-level constants declared between functions, and lambdaSelectCols mentions `instrumented`, which would
// make SetLambdaObservation look like it writes a key it only ever reads.
func statementIn(t *testing.T, src, marker string) string {
	t.Helper()
	i := strings.Index(src, marker)
	if i < 0 {
		t.Fatalf("%q not found in inventory.go; this test has stopped checking anything", marker)
	}
	rest := src[i+len(marker):]
	open := strings.Index(rest, "`")
	if open < 0 {
		t.Fatalf("no SQL literal found in %q", marker)
	}
	rest = rest[open+1:]
	close := strings.Index(rest, "`")
	if close < 0 {
		t.Fatalf("unterminated SQL literal in %q", marker)
	}
	return rest[:close]
}

func strPtr(s string) *string { return &s }
