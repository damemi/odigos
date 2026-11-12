package clickhouse_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/odigos-io/odigos/insights/internal/db/clickhouse"
	"github.com/odigos-io/odigos/insights/internal/db/clickhouse/migrate"
	"github.com/odigos-io/odigos/insights/internal/engine"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

// defaultTestDSN points at the local dev container started for these tests.
// Override with CLICKHOUSE_TEST_DSN. If nothing is reachable the tests skip, so
// `go test ./...` stays green on machines without ClickHouse.
const defaultTestDSN = "clickhouse://insights:insightspass@localhost:19000/insights"

func testRepo(t *testing.T) *clickhouse.Repo {
	t.Helper()
	dsn := os.Getenv("CLICKHOUSE_TEST_DSN")
	if dsn == "" {
		dsn = defaultTestDSN
	}
	r, err := clickhouse.Open(dsn, engine.NewBaselineCodec())
	if err != nil {
		t.Skipf("clickhouse not configured (%v)", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := r.Ping(ctx); err != nil {
		t.Skipf("clickhouse not reachable at %s (%v)", dsn, err)
	}
	if err := migrate.Apply(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return r
}

// uniqueService keeps each test's rows isolated from previous runs (the shared
// container persists data; the transaction id is a hash of the identity).
func uniqueService(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), rand.Int63())
}

// TestMigrateIsIdempotent guards the property the migration Job relies on:
// re-running Apply against an already-migrated database is a no-op, not an
// error (golang-migrate reports ErrNoChange, which Apply swallows).
func TestMigrateIsIdempotent(t *testing.T) {
	_ = testRepo(t) // reachable + migrated once
	dsn := os.Getenv("CLICKHOUSE_TEST_DSN")
	if dsn == "" {
		dsn = defaultTestDSN
	}
	if err := migrate.Apply(dsn); err != nil {
		t.Fatalf("second migrate should be a no-op: %v", err)
	}
}

func TestTransactions(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	svc := uniqueService("txn")

	tx, err := r.UpsertTransaction(ctx, svc, "ns", "GET /a", repo.KindHTTP)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Idempotent: same identity → same id, no duplicate.
	again, err := r.UpsertTransaction(ctx, svc, "ns", "GET /a", repo.KindHTTP)
	if err != nil {
		t.Fatalf("upsert again: %v", err)
	}
	if tx.ID != again.ID {
		t.Fatalf("ids differ across upserts: %d vs %d", tx.ID, again.ID)
	}
	if tx.ID < 0 {
		t.Fatalf("id must be non-negative, got %d", tx.ID)
	}

	txs, err := r.ListTransactions(ctx, 10000)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !containsTxn(txs, tx.ID) {
		t.Fatalf("listed transactions missing id %d", tx.ID)
	}

	stats, err := r.ListTransactionStats(ctx, repo.TransactionListParams{Namespace: "ns", Service: svc, Limit: 10})
	if err != nil || len(stats) != 1 {
		t.Fatalf("list transaction stats before baseline: rows=%d err=%v", len(stats), err)
	}
	if stats[0].HasBaseline || stats[0].Promoted {
		t.Fatalf("new transaction promotion = has:%v promoted:%v, want false/false", stats[0].HasBaseline, stats[0].Promoted)
	}

	if err := r.PromoteBaselineClass(ctx, tx.ID, repo.D1CallEdges); err != nil {
		t.Fatalf("promote baseline: %v", err)
	}
	stats, err = r.ListTransactionStats(ctx, repo.TransactionListParams{Namespace: "ns", Service: svc, Limit: 10})
	if err != nil || len(stats) != 1 {
		t.Fatalf("list transaction stats after promotion: rows=%d err=%v", len(stats), err)
	}
	if !stats[0].HasBaseline || !stats[0].Promoted {
		t.Fatalf("promoted transaction = has:%v promoted:%v, want true/true", stats[0].HasBaseline, stats[0].Promoted)
	}
}

// TestSetBaselineUnionAndIdempotency covers the core additive property for set
// classes: two flushes union, a replayed flush is a no-op (ReplacingMergeTree),
// and observation_count sums.
func TestSetBaselineUnionAndIdempotency(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	txID := hashID()

	edge := func(fromOp, toOp string) string {
		return fmt.Sprintf(`{"from":{"svc":"a","op":%q},"to":{"svc":"b","op":%q}}`, fromOp, toOp)
	}
	d1 := func(edges ...string) json.RawMessage {
		return json.RawMessage(`{"edges":[` + join(edges) + `]}`)
	}

	// Flush 1: {e1}
	if _, err := r.ApplyBaselineDeltas(ctx, []repo.BaselineDelta{{
		TransactionID: txID, Class: repo.D1CallEdges,
		Contribution: d1(edge("x", "y")), CountDelta: 1, DataSchemaVersion: 1,
		LearningStartedAt: time.Now(),
	}}, nil); err != nil {
		t.Fatalf("flush 1: %v", err)
	}
	// Flush 2: full set {e1,e2} (set classes re-send the full value each flush)
	if _, err := r.ApplyBaselineDeltas(ctx, []repo.BaselineDelta{{
		TransactionID: txID, Class: repo.D1CallEdges,
		Contribution: d1(edge("x", "y"), edge("p", "q")), CountDelta: 1, DataSchemaVersion: 1,
	}}, nil); err != nil {
		t.Fatalf("flush 2: %v", err)
	}
	// Flush 3: replay flush 2 exactly - must not add members (idempotent) but
	// DOES add to observation_count (accepted counter behavior).
	if _, err := r.ApplyBaselineDeltas(ctx, []repo.BaselineDelta{{
		TransactionID: txID, Class: repo.D1CallEdges,
		Contribution: d1(edge("x", "y"), edge("p", "q")), CountDelta: 1, DataSchemaVersion: 1,
	}}, nil); err != nil {
		t.Fatalf("flush 3: %v", err)
	}

	got := getClass(t, r, txID, repo.D1CallEdges)
	var d struct {
		Edges []struct {
			From struct{ Svc, Op string } `json:"from"`
			To   struct{ Svc, Op string } `json:"to"`
		} `json:"edges"`
	}
	if err := json.Unmarshal(got.Data, &d); err != nil {
		t.Fatalf("unmarshal data %s: %v", got.Data, err)
	}
	if len(d.Edges) != 2 {
		t.Fatalf("expected 2 unioned edges, got %d (%s)", len(d.Edges), got.Data)
	}
	if got.ObservationCount != 3 {
		t.Fatalf("expected observation_count 3, got %d", got.ObservationCount)
	}
}

// TestAdditiveHistogram covers D7: two delta contributions sum bucket-wise.
func TestAdditiveHistogram(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	txID := hashID()

	// request bucket 3 = 2 observations; response bucket 5 = 1.
	hist := func(reqBucket3, respBucket5 uint64) json.RawMessage {
		reqB := make([]uint64, 32)
		respB := make([]uint64, 32)
		reqB[3] = reqBucket3
		respB[5] = respBucket5
		b, _ := json.Marshal(map[string]any{
			"request":  map[string]any{"buckets": reqB},
			"response": map[string]any{"buckets": respB},
		})
		return b
	}

	for i := 0; i < 2; i++ {
		if _, err := r.ApplyBaselineDeltas(ctx, []repo.BaselineDelta{{
			TransactionID: txID, Class: repo.D7PayloadSize,
			Contribution: hist(1, 1), CountDelta: 1, DataSchemaVersion: 1, LearningStartedAt: time.Now(),
		}}, nil); err != nil {
			t.Fatalf("flush %d: %v", i, err)
		}
	}

	got := getClass(t, r, txID, repo.D7PayloadSize)
	var d struct {
		Request  struct{ Buckets []uint64 } `json:"request"`
		Response struct{ Buckets []uint64 } `json:"response"`
	}
	if err := json.Unmarshal(got.Data, &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.Request.Buckets[3] != 2 {
		t.Fatalf("expected request bucket 3 == 2, got %d", d.Request.Buckets[3])
	}
	if d.Response.Buckets[5] != 2 {
		t.Fatalf("expected response bucket 5 == 2, got %d", d.Response.Buckets[5])
	}
}

// TestMultiReplicaConverges simulates two replicas flushing disjoint members
// for the same transaction; the read must see the union and summed count.
func TestMultiReplicaConverges(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	txID := hashID()

	lib := func(ns string) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"libs":[{"ns":%q,"ver":"1"}]}`, ns))
	}
	// replica A learns libA, replica B learns libB
	if _, err := r.ApplyBaselineDeltas(ctx, []repo.BaselineDelta{{
		TransactionID: txID, Class: repo.D5Libraries, Contribution: lib("libA"), CountDelta: 5, DataSchemaVersion: 1, LearningStartedAt: time.Now(),
	}}, nil); err != nil {
		t.Fatalf("replica A: %v", err)
	}
	if _, err := r.ApplyBaselineDeltas(ctx, []repo.BaselineDelta{{
		TransactionID: txID, Class: repo.D5Libraries, Contribution: lib("libB"), CountDelta: 7, DataSchemaVersion: 1, LearningStartedAt: time.Now(),
	}}, nil); err != nil {
		t.Fatalf("replica B: %v", err)
	}

	got := getClass(t, r, txID, repo.D5Libraries)
	var d struct {
		Libs []struct{ Ns, Ver string } `json:"libs"`
	}
	if err := json.Unmarshal(got.Data, &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(d.Libs) != 2 {
		t.Fatalf("expected 2 unioned libs, got %d (%s)", len(d.Libs), got.Data)
	}
	if got.ObservationCount != 12 {
		t.Fatalf("expected summed count 12, got %d", got.ObservationCount)
	}
}

func TestObservationsAndPolicies(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	txID := hashID()

	obs := repo.Observation{
		TraceID: fmt.Sprintf("trace-%d", rand.Int63()), TransactionID: txID,
		ObservedAt: time.Now(), DurationUs: 42_000, SampleReason: "example", RawOTLP: []byte("rawbytes"),
	}
	if err := r.InsertObservations(ctx, []repo.Observation{obs}); err != nil {
		t.Fatalf("insert observation: %v", err)
	}
	// Replaying the same (transaction, reason, trace) must not duplicate.
	if err := r.InsertObservations(ctx, []repo.Observation{obs}); err != nil {
		t.Fatalf("insert observation again: %v", err)
	}
	list, err := r.ListObservationsByTransaction(ctx, txID, "example", 100)
	if err != nil {
		t.Fatalf("list observations: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 observation after dedup, got %d", len(list))
	}
	if len(list[0].RawOTLP) != 0 {
		t.Fatalf("list path must not load raw_otlp, got %q", list[0].RawOTLP)
	}
	full, ok, err := r.GetObservationByTrace(ctx, txID, list[0].TraceID)
	if err != nil || !ok {
		t.Fatalf("get observation by trace: ok=%v err=%v", ok, err)
	}
	if string(full.RawOTLP) != "rawbytes" {
		t.Fatalf("raw_otlp round-trip failed: %q", full.RawOTLP)
	}

	// Seeded policies present.
	policies, err := r.ListEnabledPolicies(ctx)
	if err != nil {
		t.Fatalf("list policies: %v", err)
	}
	if len(policies) == 0 {
		t.Fatalf("expected at least the default alert policy")
	}
	lps, err := r.ListLearningPolicies(ctx)
	if err != nil {
		t.Fatalf("list learning policies: %v", err)
	}
	if len(lps) != 8 {
		t.Fatalf("expected 8 seeded learning policies, got %d", len(lps))
	}
}

// TestAnomalyIssueLifecycle exercises the deduped anomaly-issue counter: many
// firings of the same (transaction, signature) fold into one issue whose
// occurrence count sums, and an operator decision (dismiss/reopen) sticks on the
// AggregatingMergeTree while the count keeps rising.
func TestAnomalyIssueLifecycle(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	svc := uniqueService("anom")
	tx, err := r.UpsertTransaction(ctx, svc, "ns", "POST /x", repo.KindHTTP)
	if err != nil {
		t.Fatalf("upsert txn: %v", err)
	}
	sig := fmt.Sprintf("sig-%d", rand.Int63())
	since := time.Now().Add(-time.Hour)

	find := func() *repo.AnomalyIssue {
		issues, err := r.ListAnomalyIssues(ctx, repo.FindingListSince(since, 1000))
		if err != nil {
			t.Fatalf("list issues: %v", err)
		}
		for i := range issues {
			if issues[i].TransactionID == tx.ID && issues[i].Signature == sig {
				return &issues[i]
			}
		}
		return nil
	}
	record := func(n int64) {
		if err := r.RecordAnomalyOccurrences(ctx, []repo.AnomalyOccurrence{{
			TransactionID: tx.ID, Signature: sig, PolicyID: 1,
			TriggeredClasses: []repo.DeviationClass{repo.D1CallEdges, repo.D2Egress},
			Offending:        "d2_egress=evil.example.com", Evidence: json.RawMessage(`{"k":"v"}`),
			Occurrences: n, MaxScore: 40, FirstSeen: time.Now(), LastSeen: time.Now(),
			LastTraceID: fmt.Sprintf("t-%d", rand.Int63()),
		}}); err != nil {
			t.Fatalf("record occurrences: %v", err)
		}
	}

	// A fresh issue is open with its occurrence count and identity resolved.
	record(3)
	g := find()
	if g == nil {
		t.Fatalf("issue not listed after record")
	}
	if g.Status != repo.AnomalyIssueOpen {
		t.Fatalf("fresh issue status = %q, want open", g.Status)
	}
	if g.Occurrences != 3 {
		t.Fatalf("occurrences = %d, want 3", g.Occurrences)
	}
	if g.Service != svc {
		t.Fatalf("issue service mismatch: %q vs %q", g.Service, svc)
	}

	// List path must not materialize evidence; detail loads it on demand.
	if len(g.Evidence) != 0 {
		t.Fatalf("list path must not load evidence, got %s", g.Evidence)
	}
	got, ok, err := r.GetAnomalyIssue(ctx, tx.ID, sig)
	if err != nil || !ok {
		t.Fatalf("get issue: ok=%v err=%v", ok, err)
	}
	if len(got.TriggeredClasses) != 2 {
		t.Fatalf("triggered classes = %v, want 2", got.TriggeredClasses)
	}
	if string(got.Evidence) != `{"k":"v"}` {
		t.Fatalf("detail evidence = %s, want {\"k\":\"v\"}", got.Evidence)
	}

	// Dismissing must STICK and the count must keep summing as it fires again.
	if err := r.ResolveAnomalyIssue(ctx, tx.ID, sig, repo.AnomalyResolveDismiss); err != nil {
		t.Fatalf("resolve dismiss: %v", err)
	}
	if g = find(); g == nil || g.Status != repo.AnomalyIssueDismissed {
		t.Fatalf("after dismiss got %+v, want status dismissed", g)
	}
	record(2)
	if g = find(); g == nil || g.Status != repo.AnomalyIssueDismissed {
		t.Fatalf("after more occurrences got %+v, want status still dismissed", g)
	}
	if g.Occurrences != 5 {
		t.Fatalf("occurrences = %d, want 5 (count keeps summing)", g.Occurrences)
	}

	// Reopen returns it to open for re-triage.
	if err := r.ResolveAnomalyIssue(ctx, tx.ID, sig, repo.AnomalyResolveReopen); err != nil {
		t.Fatalf("resolve reopen: %v", err)
	}
	if g = find(); g == nil || g.Status != repo.AnomalyIssueOpen {
		t.Fatalf("after reopen got %+v, want status open", g)
	}
}

// TestListAnomalyIssuesByTransactionScoped is the regression guard for the
// deduped-anomaly read fragment. It asserts the transaction-scoped read (a) runs
// at all - a shadowing alias once made argMax read an aggregate, failing with
// ILLEGAL_AGGREGATION (error 184) - and (b) returns only the requested
// transaction's issues, since the page filters in SQL, not in Go.
func TestListAnomalyIssuesByTransactionScoped(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	since := time.Now().Add(-time.Hour)

	mkTxnWithIssue := func(op string) (int64, string) {
		tx, err := r.UpsertTransaction(ctx, uniqueService("anom-scope"), "ns", op, repo.KindHTTP)
		if err != nil {
			t.Fatalf("upsert txn: %v", err)
		}
		sig := fmt.Sprintf("sig-%d", rand.Int63())
		if err := r.RecordAnomalyOccurrences(ctx, []repo.AnomalyOccurrence{{
			TransactionID: tx.ID, Signature: sig, PolicyID: 1,
			TriggeredClasses: []repo.DeviationClass{repo.D2Egress},
			Offending:        "d2_egress=x", Evidence: json.RawMessage(`{}`),
			Occurrences: 4, MaxScore: 35, FirstSeen: time.Now(), LastSeen: time.Now(),
			LastTraceID: fmt.Sprintf("t-%d", rand.Int63()),
		}}); err != nil {
			t.Fatalf("record occurrences: %v", err)
		}
		return tx.ID, sig
	}

	txA, sigA := mkTxnWithIssue("POST /a")
	txB, _ := mkTxnWithIssue("POST /b")

	issues, err := r.ListAnomalyIssuesByTransaction(ctx, txA, repo.FindingListSince(since, 100))
	if err != nil {
		t.Fatalf("list by transaction: %v", err) // error 184 would surface here
	}
	if len(issues) != 1 {
		t.Fatalf("expected exactly txA's 1 issue, got %d", len(issues))
	}
	if issues[0].TransactionID != txA || issues[0].Signature != sigA {
		t.Fatalf("scoped read leaked another transaction: got tx=%d sig=%s", issues[0].TransactionID, issues[0].Signature)
	}
	if issues[0].Occurrences != 4 {
		t.Fatalf("occurrences = %d, want 4", issues[0].Occurrences)
	}
	// txB's issue must not appear in txA's scoped read.
	for _, is := range issues {
		if is.TransactionID == txB {
			t.Fatalf("txB issue leaked into txA scope")
		}
	}
}

// TestDeleteAnomalyIssues is the guard for the re-learn invalidation: deleting a
// transaction's anomaly issues removes them from both the feed and the
// single-issue read, and a later firing (a re-fire against the new baseline)
// starts a fresh issue rather than resurrecting the deleted one.
func TestDeleteAnomalyIssues(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	tx, err := r.UpsertTransaction(ctx, uniqueService("del-anom"), "ns", "POST /buy", repo.KindHTTP)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	sig := fmt.Sprintf("sig-%d", rand.Int63())
	since := time.Now().Add(-time.Hour)

	record := func() {
		if err := r.RecordAnomalyOccurrences(ctx, []repo.AnomalyOccurrence{{
			TransactionID: tx.ID, Signature: sig, PolicyID: 1,
			TriggeredClasses: []repo.DeviationClass{repo.D1CallEdges},
			Offending:        "d1_call_edges=x", Evidence: json.RawMessage(`{}`),
			Occurrences: 1, MaxScore: 40, FirstSeen: time.Now(), LastSeen: time.Now(),
			LastTraceID: fmt.Sprintf("t-%d", rand.Int63()),
		}}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	listed := func() bool {
		issues, err := r.ListAnomalyIssues(ctx, repo.FindingListSince(since, 1000))
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, is := range issues {
			if is.TransactionID == tx.ID && is.Signature == sig {
				return true
			}
		}
		return false
	}

	record()
	if !listed() {
		t.Fatalf("issue must appear in the feed before delete")
	}

	if err := r.DeleteAnomalyIssues(ctx, tx.ID); err != nil {
		t.Fatalf("delete anomaly issues: %v", err)
	}
	if listed() {
		t.Fatalf("deleted issue must not appear in the feed")
	}
	if _, ok, err := r.GetAnomalyIssue(ctx, tx.ID, sig); err != nil || ok {
		t.Fatalf("deleted issue must not be fetchable: ok=%v err=%v", ok, err)
	}

	// A firing after the delete (re-fire against the new baseline) starts fresh.
	record()
	if !listed() {
		t.Fatalf("a re-fire after re-learn must create a fresh issue")
	}
	got, ok, err := r.GetAnomalyIssue(ctx, tx.ID, sig)
	if err != nil || !ok {
		t.Fatalf("get after re-fire: ok=%v err=%v", ok, err)
	}
	if got.Occurrences != 1 {
		t.Fatalf("re-fired issue occurrences = %d, want 1 (not resurrected)", got.Occurrences)
	}
}

func TestSystemSettingsRoundTrip(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()

	got, err := r.GetSystemSettings(ctx)
	if err != nil {
		t.Fatalf("get defaults: %v", err)
	}
	if got.ExamplesPerTransaction != repo.DefaultExamplesPerTransaction {
		t.Fatalf("seed examples = %d, want %d", got.ExamplesPerTransaction, repo.DefaultExamplesPerTransaction)
	}

	next := got
	next.ExamplesPerTransaction = 7
	next.ObservationRetentionDays = 14
	next.TransactionIdentityDimensions = []repo.TransactionIdentityDimension{
		{Key: repo.IdentityDimHTTPStatusCode, Enabled: true},
		{Key: repo.IdentityDimRPCGRPCStatus, Enabled: false},
	}
	if err := r.UpsertSystemSettings(ctx, next); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err = r.GetSystemSettings(ctx)
	if err != nil {
		t.Fatalf("get after upsert: %v", err)
	}
	if got.ExamplesPerTransaction != 7 || got.ObservationRetentionDays != 14 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if len(got.TransactionIdentityDimensions) != 2 ||
		got.TransactionIdentityDimensions[0].Key != repo.IdentityDimHTTPStatusCode ||
		!got.TransactionIdentityDimensions[0].Enabled ||
		got.TransactionIdentityDimensions[1].Enabled {
		t.Fatalf("identity dims round-trip mismatch: %+v", got.TransactionIdentityDimensions)
	}
}

// TestObservationCountAndBoundedRead covers the batched insert,
// CountObservationsByReason and the bounded list read (at most `limit` rows
// with raw_otlp intact) that keeps the transaction page from blowing query
// memory.
func TestObservationCountAndBoundedRead(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	tx, err := r.UpsertTransaction(ctx, uniqueService("obs-cap"), "ns", "GET /c", repo.KindHTTP)
	if err != nil {
		t.Fatalf("upsert txn: %v", err)
	}

	const keepN = 3
	base := time.Now().Add(-time.Hour)
	total := keepN + 2
	batch := make([]repo.Observation, 0, total+1)
	for i := 0; i < total; i++ {
		batch = append(batch, repo.Observation{
			TransactionID: tx.ID, TraceID: fmt.Sprintf("cap-%d-%d", tx.ID, i),
			ObservedAt: base.Add(time.Duration(i) * time.Second), DurationUs: int64(i),
			SampleReason: "example", RawOTLP: []byte(fmt.Sprintf("blob-%d", i)),
		})
	}
	batch = append(batch, repo.Observation{
		TransactionID: tx.ID, TraceID: fmt.Sprintf("cap-%d-evidence", tx.ID),
		ObservedAt: base, SampleReason: "anomaly:sig", RawOTLP: []byte("evidence"),
	})
	if err := r.InsertObservations(ctx, batch); err != nil {
		t.Fatalf("insert observations: %v", err)
	}

	counts, err := r.CountObservationsByReason(ctx, tx.ID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts["example"] != total || counts["anomaly:sig"] != 1 {
		t.Fatalf("counts = %v, want example=%d anomaly:sig=1", counts, total)
	}

	list, err := r.ListObservationsByTransaction(ctx, tx.ID, "example", keepN)
	if err != nil {
		t.Fatalf("list observations: %v", err)
	}
	if len(list) != keepN {
		t.Fatalf("bounded read returned %d, want %d", len(list), keepN)
	}
	if want := fmt.Sprintf("cap-%d-%d", tx.ID, total-1); list[0].TraceID != want {
		t.Fatalf("newest trace_id = %q, want %q", list[0].TraceID, want)
	}
	if len(list[0].RawOTLP) != 0 {
		t.Fatalf("list path must not load raw_otlp")
	}
	full, ok, err := r.GetObservationByTrace(ctx, tx.ID, list[0].TraceID)
	if err != nil || !ok {
		t.Fatalf("get observation by trace: ok=%v err=%v", ok, err)
	}
	if want := fmt.Sprintf("blob-%d", total-1); string(full.RawOTLP) != want {
		t.Fatalf("newest raw_otlp = %q, want %q", full.RawOTLP, want)
	}
}

// TestGuardrailViolationResolutionSticks is the regression guard for the
// aggregate-counter table's engine choice. A resolution (allow/dismiss) is
// written as a row with occurrences=0 carrying the decision timestamp; a
// SummingMergeTree drops any all-zero-summing-column row on merge, which would
// silently discard the decision and leave the group "open" forever. The table is
// therefore an AggregatingMergeTree - this test asserts a resolution sticks and
// the count still sums.
func TestGuardrailViolationResolutionSticks(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	scopeKey := "ns/" + uniqueService("gv")
	rule := repo.RuleKey("allowed_egress")
	offending := "evil.example.com:4444"
	since := time.Now().Add(-time.Hour)

	find := func() *repo.GuardrailViolationGroup {
		groups, err := r.ListGuardrailViolations(ctx, repo.FindingListSince(since, 1000))
		if err != nil {
			t.Fatalf("list violations: %v", err)
		}
		for i := range groups {
			if groups[i].ScopeKey == scopeKey && groups[i].Offending == offending {
				return &groups[i]
			}
		}
		return nil
	}
	record := func(n int64) {
		if err := r.RecordGuardrailViolations(ctx, []repo.GuardrailViolationHit{{
			Scope: repo.ScopeService, ScopeKey: scopeKey, RuleKey: rule, Offending: offending,
			Occurrences: n, MaxScore: 55, LastSeen: time.Now(),
		}}); err != nil {
			t.Fatalf("record violations: %v", err)
		}
	}

	// A fresh violation is open with its occurrence count.
	record(3)
	g := find()
	if g == nil {
		t.Fatalf("violation not listed after record")
	}
	if g.Status != repo.GuardrailViolationOpen {
		t.Fatalf("fresh violation status = %q, want open", g.Status)
	}
	if g.Occurrences != 3 {
		t.Fatalf("occurrences = %d, want 3", g.Occurrences)
	}

	// Accepting it must STICK (this is the property SummingMergeTree broke).
	if err := r.ResolveGuardrailViolation(ctx, repo.ScopeService, scopeKey, rule, offending, repo.GuardrailResolveAccept); err != nil {
		t.Fatalf("resolve accept: %v", err)
	}
	if g = find(); g == nil || g.Status != repo.GuardrailViolationAccepted {
		t.Fatalf("after accept got %+v, want status accepted", g)
	}

	// The decision stays sticky as the edge keeps firing, and the count sums.
	record(2)
	if g = find(); g == nil || g.Status != repo.GuardrailViolationAccepted {
		t.Fatalf("after more occurrences got %+v, want status still accepted", g)
	}
	if g.Occurrences != 5 {
		t.Fatalf("occurrences = %d, want 5 (count keeps summing)", g.Occurrences)
	}
}

// TestGuardrailViolationReopen covers the dismissed → open transition. Reopen
// can't clear dismissed_at (max-merge won't lower it), so it stamps a newer
// reopened_at that outdates the dismiss; the group must return to open.
func TestGuardrailViolationReopen(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	scopeKey := "ns/" + uniqueService("gvr")
	rule := repo.RuleKey("allowed_egress")
	offending := "leak.example.com:443"
	since := time.Now().Add(-time.Hour)

	find := func() *repo.GuardrailViolationGroup {
		groups, err := r.ListGuardrailViolations(ctx, repo.FindingListSince(since, 1000))
		if err != nil {
			t.Fatalf("list violations: %v", err)
		}
		for i := range groups {
			if groups[i].ScopeKey == scopeKey && groups[i].Offending == offending {
				return &groups[i]
			}
		}
		return nil
	}
	resolve := func(res repo.GuardrailResolution) {
		if err := r.ResolveGuardrailViolation(ctx, repo.ScopeService, scopeKey, rule, offending, res); err != nil {
			t.Fatalf("resolve %s: %v", res, err)
		}
		time.Sleep(2 * time.Millisecond) // keep decision timestamps strictly ordered
	}

	if err := r.RecordGuardrailViolations(ctx, []repo.GuardrailViolationHit{{
		Scope: repo.ScopeService, ScopeKey: scopeKey, RuleKey: rule, Offending: offending,
		Occurrences: 1, MaxScore: 55, LastSeen: time.Now(),
	}}); err != nil {
		t.Fatalf("record: %v", err)
	}

	resolve(repo.GuardrailResolveDismiss)
	if g := find(); g == nil || g.Status != repo.GuardrailViolationDismissed {
		t.Fatalf("after dismiss got %+v, want dismissed", g)
	}

	resolve(repo.GuardrailResolveReopen)
	if g := find(); g == nil || g.Status != repo.GuardrailViolationOpen {
		t.Fatalf("after reopen got %+v, want open", g)
	}
}

// --- small helpers ---

func hashID() int64 { return rand.Int63() }

func getClass(t *testing.T, r *clickhouse.Repo, txID int64, class repo.DeviationClass) repo.BaselineClass {
	t.Helper()
	rows, err := r.GetBaselineForTransaction(context.Background(), txID)
	if err != nil {
		t.Fatalf("get baseline: %v", err)
	}
	for _, b := range rows {
		if b.Class == class {
			return b
		}
	}
	t.Fatalf("class %s not found for txn %d (%d rows)", class, txID, len(rows))
	return repo.BaselineClass{}
}

func join(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ","
		}
		out += p
	}
	return out
}

func containsTxn(txs []repo.Transaction, id int64) bool {
	for _, t := range txs {
		if t.ID == id {
			return true
		}
	}
	return false
}
