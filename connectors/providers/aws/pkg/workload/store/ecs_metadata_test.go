package store

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func readECSSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("ecs_services.go")
	if err != nil {
		t.Fatalf("read ecs_services.go: %v", err)
	}
	return string(b)
}

// TestECSObservationKeysSurviveDiscovery is the ecs_services counterpart of the lambda_functions allowlist
// guard, and the reason this one is cheap: UpsertECSService MERGES metadata (`metadata || EXCLUDED.metadata`)
// rather than replacing it, so a key written by any other writer survives a discovery refresh by construction.
//
// That is the property being pinned. The lambda upsert took the other approach — enumerate what to preserve —
// and every key added after it was written would have been silently wiped on the next scan. If this ever
// becomes a replace or grows a preserve-list, the whole observation record starts disappearing minutes after
// each pass writes it, with no error anywhere.
func TestECSObservationKeysSurviveDiscovery(t *testing.T) {
	upsert := statementIn(t, readECSSource(t), "func (s *Store) UpsertECSService")
	if !strings.Contains(upsert, "metadata = connector_aws.ecs_services.metadata || EXCLUDED.metadata") {
		t.Error("UpsertECSService no longer merges metadata. A replace (or a preserve-list) wipes the observation " +
			"record and the instrumentor's phase/error on every discovery cycle.")
	}
	if strings.Contains(upsert, "metadata = EXCLUDED.metadata") {
		t.Error("UpsertECSService replaces metadata wholesale; every other writer's keys are lost on the next scan")
	}
}

// TestSetECSServiceObservationTouchesOnlyObservationState pins the observer's write boundary. It is the
// read-only path, and the one thing it does write must not reach into another writer's state.
//
// The instrumented flag is the deliberate exception and it is written through a DIFFERENT method
// (SetECSServiceInstrumented), which is what keeps the exception explicit rather than buried in this patch.
func TestSetECSServiceObservationTouchesOnlyObservationState(t *testing.T) {
	src := readECSSource(t)
	i := strings.Index(src, "func (s *Store) SetECSServiceObservation")
	if i < 0 {
		t.Fatal("SetECSServiceObservation not found; this test has stopped checking anything")
	}
	body := src[i:]
	if j := strings.Index(body, "\nfunc "); j >= 0 {
		body = body[:j]
	}
	// The patch map is built from the metadata-key CONSTANTS, so the identifiers are what appear in the
	// source. metaConformance carries a colon to keep it from matching metaConformanceMissing/Since.
	for _, forbidden := range []string{
		"metaInstrumented", "metaInstrumentationPhase", "metaInstrumentationError",
		"metaRuntimeLanguage", "metaRuntimeVersion", "metaContainers", "metaOdigosTaskDefinition",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("SetECSServiceObservation writes %s, which belongs to discovery or the instrumentor", forbidden)
		}
	}
	for _, want := range []string{
		"metaConformance:", "metaConformanceMissing", "metaConformanceSince", "metaObservationError", "metaSettling",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("SetECSServiceObservation does not write %s; the report reads it", want)
		}
	}
	// last_seen_at is discovery's liveness signal and the prune cutoff: refreshing it here would let an
	// observation keep a deleted service alive in the inventory forever.
	if strings.Contains(body, "last_seen_at") {
		t.Error("SetECSServiceObservation touches last_seen_at, which belongs to discovery")
	}
}

// TestECSObservationColsAndScanAgree: ecsObservationCols is selected by two different queries (ListECSServices
// and ListWorkloads) and read by two different scans. A column added to the constant without updating both
// scans is a runtime failure of the report path with no compile-time signal.
func TestECSObservationColsAndScanAgree(t *testing.T) {
	const wantCols = 5 // conformance, missing, since, error, settling
	if got := topLevelCommas(ecsObservationCols); got != wantCols {
		t.Errorf("ecsObservationCols selects %d columns, the scans read %d", got, wantCols)
	}
	// ListWorkloads pads it to the shared shape with trailing NULLs for the Lambda-only layers and routing-note columns; the
	// full-shape assertion lives in TestListWorkloadsQueriesShareColumnShape.
	src := readInventorySource(t)
	if !strings.Contains(src, "ecsObservationCols+`, NULL") && !strings.Contains(src, "ecsObservationCols + `, NULL") {
		t.Error("the ecs_services report query no longer pads ecsObservationCols with the layers column")
	}
}

func TestECSRoutingNoteDecode(t *testing.T) {
	got := decodeRoutingNote([]byte(`{"code":"collector-unreachable","message":"falling back","transient":true}`))
	if got == nil || got.Code != "collector-unreachable" || got.Message != "falling back" || !got.Transient {
		t.Fatalf("decoded routing note = %+v", got)
	}
	if decodeRoutingNote(nil) != nil || decodeRoutingNote([]byte(`null`)) != nil {
		t.Fatal("empty routing note must decode as nil")
	}
	if decodeRoutingNote([]byte(`not-json`)) != nil {
		t.Fatal("malformed routing note must degrade to nil")
	}
}

// TestCutoverFailureRoundTrips: the rollout orders retries by the recorded time, so it has to come back as
// written, and a garbled record must cost a log line rather than the whole service listing.
func TestCutoverFailureRoundTrips(t *testing.T) {
	want := CutoverFailure{Reason: "register task definition: AccessDenied", At: time.Date(2026, 10, 11, 9, 30, 0, 0, time.UTC)}
	raw, err := json.Marshal(storeCutoverFailure(want))
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeCutoverFailure(raw); got.Reason != want.Reason || !got.At.Equal(want.At) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
	for _, raw := range []string{"", "null", "not-json"} {
		if got := decodeCutoverFailure([]byte(raw)); got != (CutoverFailure{}) {
			t.Errorf("decode(%q) = %+v, want none", raw, got)
		}
	}
	if got := decodeCutoverFailure([]byte(`{"reason":"throttled","at":"yesterday"}`)); got.Reason != "throttled" || !got.At.IsZero() {
		t.Errorf("an unreadable time = %+v, want the reason kept and a zero time", got)
	}
}

func TestSetECSServiceRoutingMergesAndClears(t *testing.T) {
	src := readECSSource(t)
	body := funcBody(t, src, "func (s *Store) SetECSServiceRouting")
	for _, want := range []string{"COALESCE(metadata", "|| $4::jsonb", "- $5::text[]", "metaRoutingVia", "metaRoutingNote"} {
		if !strings.Contains(body, want) {
			t.Errorf("SetECSServiceRouting is missing %s; route updates must preserve unrelated metadata and clear stale route state", want)
		}
	}
}
