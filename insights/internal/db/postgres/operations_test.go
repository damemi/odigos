package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// TestDeleteTransactionCascade asserts DeleteTransaction removes every row tied
// to the transaction in one go and leaves other transactions alone.
func TestDeleteTransactionCascade(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	svc := uniqueService("cascade")
	tx, err := r.UpsertTransaction(ctx, svc, "ns", "GET /del", repo.KindHTTP, map[string]string{"k8s.namespace.name": "ns"})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	other, err := r.UpsertTransaction(ctx, svc, "ns", "GET /keep", repo.KindHTTP, testSourceIdentity)
	if err != nil {
		t.Fatalf("upsert other: %v", err)
	}
	seedTransactionState(t, r, tx.ID)
	seedTransactionState(t, r, other.ID)

	if err := r.DeleteTransaction(ctx, tx.ID); err != nil {
		t.Fatalf("delete transaction: %v", err)
	}
	if _, ok, err := r.GetTransactionByID(ctx, tx.ID); err != nil || ok {
		t.Fatalf("transaction still present: ok=%v err=%v", ok, err)
	}
	assertTransactionStateGone(t, r, tx.ID)

	if _, ok, err := r.GetTransactionByID(ctx, other.ID); err != nil || !ok {
		t.Fatalf("other transaction must survive: ok=%v err=%v", ok, err)
	}
	if rows, err := r.GetBaselineForTransaction(ctx, other.ID); err != nil || len(rows) == 0 {
		t.Fatalf("other transaction baseline must survive: rows=%d err=%v", len(rows), err)
	}
}

// TestDeleteAllTransactionsKeepsGlobalPolicies wipes the inventory and checks
// global and service policies stay while transaction-scoped ones go.
func TestDeleteAllTransactionsKeepsGlobalPolicies(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	tx, err := r.UpsertTransaction(ctx, uniqueService("wipe"), "ns", "GET /wipe", repo.KindHTTP, testSourceIdentity)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	seedTransactionState(t, r, tx.ID)
	svcKey := "ns/" + uniqueService("svc-policy")
	if err := r.UpsertAlertPolicy(ctx, repo.Policy{Scope: repo.ScopeService, ScopeKey: svcKey, Name: "svc", FireAtScore: 30, Enabled: true}); err != nil {
		t.Fatalf("upsert service policy: %v", err)
	}

	if err := r.DeleteAllTransactions(ctx); err != nil {
		t.Fatalf("delete all: %v", err)
	}
	txs, err := r.ListTransactions(ctx, 10)
	if err != nil || len(txs) != 0 {
		t.Fatalf("transactions after wipe = %d (err=%v), want 0", len(txs), err)
	}
	assertTransactionStateGone(t, r, tx.ID)
	policies, err := r.ListAlertPolicies(ctx)
	if err != nil {
		t.Fatalf("list policies: %v", err)
	}
	var global, service bool
	for _, p := range policies {
		switch {
		case p.Scope == repo.ScopeGlobal:
			global = true
		case p.Scope == repo.ScopeService && p.ScopeKey == svcKey:
			service = true
		case p.Scope == repo.ScopeTransaction:
			t.Fatalf("transaction-scoped policy survived the wipe: %+v", p)
		}
	}
	if !global || !service {
		t.Fatalf("global=%v service=%v, want both kept", global, service)
	}
	_ = r.DeleteAlertPolicy(ctx, repo.ScopeService, svcKey)
}

// seedTransactionState writes one row into every per-transaction table.
func seedTransactionState(t *testing.T, r interface {
	repo.Repository
}, txID int64) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	scopeKey := repo.TransactionScopeKey(txID)
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	_, err := r.ApplyBaselineDeltas(ctx, []repo.BaselineDelta{
		{TransactionID: txID, Class: repo.D5Libraries, Contribution: json.RawMessage(`{"libs":[{"ns":"lib","ver":"1"}]}`), CountDelta: 1, LearningStartedAt: now},
		{TransactionID: txID, Class: repo.D7PayloadSize, Contribution: json.RawMessage(`{"request":{"buckets":[1]},"response":{"buckets":[1]}}`), CountDelta: 1, LearningStartedAt: now},
	}, nil)
	must("baselines", err)
	must("activity", r.RecordActivity(ctx, []repo.ActivityDelta{{TransactionID: txID, Volume: 3, LastSeen: now}}))
	must("observations", r.InsertObservations(ctx, []repo.Observation{
		{TransactionID: txID, TraceID: fmt.Sprintf("t-%d", txID), ObservedAt: now, SampleReason: repo.RelationSampleReason, RawOTLP: []byte("x")},
	}))
	must("anomaly", r.RecordAnomalyOccurrences(ctx, []repo.AnomalyOccurrence{
		{TransactionID: txID, Signature: "sig", Occurrences: 1, MaxScore: 10, LastSeen: now},
	}))
	must("recommendation", r.UpsertRecommendations(ctx, []repo.Recommendation{
		{ID: fmt.Sprintf("rec-%d", txID), Kind: repo.RecommendationKindAttributeCorrelation, TransactionID: txID},
	}))
	must("live counts", r.AddRecommendationLiveCounts(ctx, []repo.RecommendationLiveDelta{
		{ID: fmt.Sprintf("rec-%d", txID), TransactionID: txID, Held: 1, FirstAt: now, LastAt: now},
	}))
	must("mining run", r.RecordRecommendationMiningRun(ctx, repo.RecommendationMiningRun{TransactionID: txID, ClaimedAt: now}))
	must("learning policy", r.UpsertLearningPolicy(ctx, repo.LearningPolicy{
		Class: repo.D1CallEdges, Mode: repo.LearningModeAny, MinMatches: 1, Scope: repo.ScopeTransaction, ScopeKey: scopeKey,
	}))
	must("alert policy", r.UpsertAlertPolicy(ctx, repo.Policy{Scope: repo.ScopeTransaction, ScopeKey: scopeKey, FireAtScore: 50, Enabled: true}))
}

func assertTransactionStateGone(t *testing.T, r repo.Repository, txID int64) {
	t.Helper()
	ctx := context.Background()
	scopeKey := repo.TransactionScopeKey(txID)
	if rows, err := r.GetBaselineForTransaction(ctx, txID); err != nil || len(rows) != 0 {
		t.Fatalf("baselines left: %d (err=%v)", len(rows), err)
	}
	if counts, err := r.CountObservationsByReason(ctx, txID); err != nil || len(counts) != 0 {
		t.Fatalf("observations left: %v (err=%v)", counts, err)
	}
	if _, ok, err := r.GetAnomalyIssue(ctx, txID, "sig"); err != nil || ok {
		t.Fatalf("anomaly left: ok=%v err=%v", ok, err)
	}
	if recs, err := r.ListRecommendations(ctx, repo.RecommendationFilter{TransactionID: txID, IncludeStale: true}); err != nil || len(recs) != 0 {
		t.Fatalf("recommendations left: %d (err=%v)", len(recs), err)
	}
	lps, err := r.ListLearningPolicies(ctx)
	if err != nil {
		t.Fatalf("list learning policies: %v", err)
	}
	for _, lp := range lps {
		if lp.Scope == repo.ScopeTransaction && lp.ScopeKey == scopeKey {
			t.Fatalf("transaction learning policy left: %+v", lp)
		}
	}
	aps, err := r.ListAlertPolicies(ctx)
	if err != nil {
		t.Fatalf("list alert policies: %v", err)
	}
	for _, p := range aps {
		if p.Scope == repo.ScopeTransaction && p.ScopeKey == scopeKey {
			t.Fatalf("transaction alert policy left: %+v", p)
		}
	}
}

// TestActivityAndServiceStats covers the activity counters and the service and
// transaction roll-ups built on them.
func TestActivityAndServiceStats(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	svc := uniqueService("stats")
	busy, err := r.UpsertTransaction(ctx, svc, "ns", "GET /busy", repo.KindHTTP, testSourceIdentity)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	quiet, err := r.UpsertTransaction(ctx, svc, "ns", "GET /quiet", repo.KindHTTP, testSourceIdentity)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	early := time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)
	late := time.Now().UTC().Truncate(time.Millisecond)
	if err := r.RecordActivity(ctx, []repo.ActivityDelta{{TransactionID: busy.ID, Volume: 5, LastSeen: late}}); err != nil {
		t.Fatalf("activity: %v", err)
	}
	// An older last_seen must not move it back; volumes add up.
	if err := r.RecordActivity(ctx, []repo.ActivityDelta{{TransactionID: busy.ID, Volume: 2, LastSeen: early}}); err != nil {
		t.Fatalf("activity: %v", err)
	}

	stats, err := r.ListTransactionStats(ctx, repo.TransactionListParams{Namespace: "ns", Service: svc, Limit: 10})
	if err != nil || len(stats) != 2 {
		t.Fatalf("transaction stats: rows=%d err=%v", len(stats), err)
	}
	if stats[0].ID != busy.ID || stats[0].Volume != 7 || !stats[0].LastSeen.Equal(late) {
		t.Fatalf("busiest first with volume 7 and newest last_seen, got %+v", stats[0])
	}
	if stats[1].ID != quiet.ID || stats[1].Volume != 0 {
		t.Fatalf("quiet transaction = %+v, want volume 0", stats[1])
	}
	byName, err := r.ListTransactionStats(ctx, repo.TransactionListParams{Namespace: "ns", Service: svc, Limit: 10, SortBy: repo.SortByLastSeen, SortOrder: repo.SortAsc})
	if err != nil || len(byName) != 2 || byName[0].ID != quiet.ID {
		t.Fatalf("sort by last_seen asc should put the inactive one first: %+v err=%v", byName, err)
	}
	if _, err := r.ListTransactionStats(ctx, repo.TransactionListParams{SortBy: "nope"}); err == nil {
		t.Fatalf("unknown sort must be rejected")
	}

	services, err := r.ListServiceStats(ctx, 10000)
	if err != nil {
		t.Fatalf("service stats: %v", err)
	}
	found := false
	for _, s := range services {
		if s.Service == svc {
			found = true
			if s.TransactionCount != 2 || s.Volume != 7 {
				t.Fatalf("service stat = %+v, want 2 transactions / volume 7", s)
			}
		}
	}
	if !found {
		t.Fatalf("service %s missing from stats", svc)
	}
	names, err := r.ListServiceNames(ctx)
	if err != nil || !slices.Contains(names, svc) {
		t.Fatalf("service names missing %s (err=%v)", svc, err)
	}
	refs, err := r.ListTransactionRefs(ctx)
	if err != nil {
		t.Fatalf("refs: %v", err)
	}
	refFound := false
	for _, ref := range refs {
		if ref.ID == busy.ID && ref.Service == svc && ref.Namespace == "ns" {
			refFound = true
		}
	}
	if !refFound {
		t.Fatalf("transaction ref for %d missing", busy.ID)
	}
	got, ok, err := r.GetTransactionByID(ctx, busy.ID)
	if err != nil || !ok || got.Operation != "GET /busy" || got.Kind != repo.KindHTTP {
		t.Fatalf("get by id = %+v ok=%v err=%v", got, ok, err)
	}
}

// TestBaselinePromotionAndReset covers the promotion sweep's candidate query,
// promotion, and the per-class and whole-transaction resets.
func TestBaselinePromotionAndReset(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	txID := rand.Int63()
	started := time.Now().Add(-48 * time.Hour)
	lib := json.RawMessage(`{"libs":[{"ns":"lib","ver":"1"}]}`)
	if _, err := r.ApplyBaselineDeltas(ctx, []repo.BaselineDelta{
		{TransactionID: txID, Class: repo.D5Libraries, Contribution: lib, CountDelta: 10, LearningStartedAt: started,
			LastChangedAt: started, ObservationCountAtLastChange: 1},
		{TransactionID: txID, Class: repo.D1CallEdges, Contribution: json.RawMessage(`{"edges":[]}`), CountDelta: 1, LearningStartedAt: time.Now()},
	}, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}

	cands, err := r.ListUnpromotedBaselines(ctx, repo.UnpromotedBaselineFilter{
		MinDurationEnabled: true, MinDurationCutoff: time.Now().Add(-24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("list unpromoted: %v", err)
	}
	if !hasCandidate(cands, txID, repo.D5Libraries) || hasCandidate(cands, txID, repo.D1CallEdges) {
		t.Fatalf("min-duration filter should return only the old class: %+v", cands)
	}
	stable, err := r.ListUnpromotedBaselines(ctx, repo.UnpromotedBaselineFilter{
		StableDurationEnabled: true, StableDurationCutoff: time.Now().Add(-time.Hour),
	})
	if err != nil || !hasCandidate(stable, txID, repo.D5Libraries) {
		t.Fatalf("stable-duration filter should return the stable class: %+v err=%v", stable, err)
	}

	if err := r.PromoteBaselineClass(ctx, txID, repo.D5Libraries); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if got := getClass(t, r, txID, repo.D5Libraries); !got.Promoted || got.ObservationCount != 10 || !got.LearningStartedAt.Before(time.Now().Add(-47*time.Hour)) {
		t.Fatalf("promotion must flip the flag only: %+v", got)
	}
	cands, err = r.ListUnpromotedBaselines(ctx, repo.UnpromotedBaselineFilter{
		MinDurationEnabled: true, MinDurationCutoff: time.Now().Add(-24 * time.Hour),
	})
	if err != nil || hasCandidate(cands, txID, repo.D5Libraries) {
		t.Fatalf("promoted class must leave the candidate list: err=%v", err)
	}

	if err := r.ResetBaselineClass(ctx, txID, repo.D5Libraries); err != nil {
		t.Fatalf("reset class: %v", err)
	}
	rows, err := r.GetBaselineForTransaction(ctx, txID)
	if err != nil || len(rows) != 1 || rows[0].Class != repo.D1CallEdges {
		t.Fatalf("after class reset rows = %+v err=%v, want only D1", rows, err)
	}
	if err := r.ResetTransactionBaselines(ctx, txID); err != nil {
		t.Fatalf("reset transaction: %v", err)
	}
	if rows, err := r.GetBaselineForTransaction(ctx, txID); err != nil || len(rows) != 0 {
		t.Fatalf("after transaction reset rows = %d err=%v", len(rows), err)
	}
}

func hasCandidate(cands []repo.UnpromotedBaselineCandidate, txID int64, class repo.DeviationClass) bool {
	for _, c := range cands {
		if c.TransactionID == txID && c.Class == class {
			return true
		}
	}
	return false
}

// TestConcurrentFlushesConverge runs overlapping flushes from several
// "replicas" at once: none may fail (deadlocks are retried) and the totals must
// equal the sum of every flush.
func TestConcurrentFlushesConverge(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	txA, txB := rand.Int63(), rand.Int63()
	const replicas, flushes = 6, 20

	var wg sync.WaitGroup
	errs := make(chan error, replicas*flushes)
	for rep := 0; rep < replicas; rep++ {
		wg.Add(1)
		go func(rep int) {
			defer wg.Done()
			for i := 0; i < flushes; i++ {
				// Alternate the order so replicas touch the rows in opposite orders.
				deltas := []repo.BaselineDelta{
					{TransactionID: txA, Class: repo.D7PayloadSize, Contribution: json.RawMessage(`{"request":{"buckets":[1,1]},"response":{"buckets":[1]}}`), CountDelta: 1},
					{TransactionID: txB, Class: repo.D5Libraries, Contribution: json.RawMessage(fmt.Sprintf(`{"libs":[{"ns":"r%d","ver":"1"}]}`, rep)), CountDelta: 1},
				}
				if (rep+i)%2 == 1 {
					deltas[0], deltas[1] = deltas[1], deltas[0]
				}
				if _, err := r.ApplyBaselineDeltas(ctx, deltas, nil); err != nil {
					errs <- err
				}
				if err := r.RecordActivity(ctx, []repo.ActivityDelta{{TransactionID: txA, Volume: 1}, {TransactionID: txB, Volume: 1}}); err != nil {
					errs <- err
				}
			}
		}(rep)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent flush failed: %v", err)
	}

	total := int64(replicas * flushes)
	if got := getClass(t, r, txA, repo.D7PayloadSize); got.ObservationCount != total {
		t.Fatalf("D7 count = %d, want %d", got.ObservationCount, total)
	}
	d5 := getClass(t, r, txB, repo.D5Libraries)
	var libs struct {
		Libs []json.RawMessage `json:"libs"`
	}
	if err := json.Unmarshal(d5.Data, &libs); err != nil || len(libs.Libs) != replicas {
		t.Fatalf("D5 libs = %s (err=%v), want %d members", d5.Data, err, replicas)
	}
	if d5.ObservationCount != total {
		t.Fatalf("D5 count = %d, want %d", d5.ObservationCount, total)
	}
}

// TestRecommendationsLifecycle covers write, list filters, live counters (whole
// and per rule), state changes, the mining candidate query and deletion.
func TestRecommendationsLifecycle(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	svc := uniqueService("rec")
	tx, err := r.UpsertTransaction(ctx, svc, "ns", "POST /pay", repo.KindHTTP, testSourceIdentity)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	corrID := fmt.Sprintf("corr-%d", tx.ID)
	svcRecID := fmt.Sprintf("svc-%d", tx.ID)
	recs := []repo.Recommendation{
		{
			ID: corrID, Kind: repo.RecommendationKindAttributeCorrelation, TransactionID: tx.ID, Rank: 1,
			Spec: repo.CorrelationSpec{Name: "user matches", Relation: "equals"}, Observed: 50, Held: 49, Distinct: 10, SampleCount: 50,
			Examples: []repo.RecommendationExample{{}}, MinedAt: now,
		},
		{
			ID: svcRecID, Kind: repo.RecommendationKindServiceGuardrail, Scope: repo.ScopeService, ScopeKey: "ns/" + svc,
			Namespace: "ns", Service: svc, Rank: 2, MinedAt: now,
			Rules: []repo.RecommendationRule{{Rule: "allowed_callers", Items: []string{"a"}}, {Rule: "allowed_egress", Items: []string{"b:443"}}},
		},
	}
	if err := r.UpsertRecommendations(ctx, recs); err != nil {
		t.Fatalf("upsert recommendations: %v", err)
	}
	// Re-mining the same ids updates in place.
	recs[0].Held = 50
	if err := r.UpsertRecommendations(ctx, recs[:1]); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}

	if err := r.AddRecommendationLiveCounts(ctx, []repo.RecommendationLiveDelta{
		{ID: corrID, TransactionID: tx.ID, Held: 3, Broken: 1, FirstAt: now.Add(-time.Minute), LastAt: now},
		{ID: corrID, TransactionID: tx.ID, Held: 2, FirstAt: now.Add(-2 * time.Minute), LastAt: now.Add(-time.Minute)},
		{ID: svcRecID, Scope: repo.ScopeService, ScopeKey: "ns/" + svc, Rule: "allowed_egress", Held: 4, Broken: 2, FirstAt: now, LastAt: now},
	}); err != nil {
		t.Fatalf("live counts: %v", err)
	}

	got, ok, err := r.GetRecommendation(ctx, corrID)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.Held != 50 || got.Spec.Name != "user matches" || got.Service != svc || got.Operation != "POST /pay" {
		t.Fatalf("correlation rec = %+v", got)
	}
	if got.LiveHeld != 5 || got.LiveBroken != 1 || !got.LiveSince.Equal(now.Add(-2*time.Minute)) || !got.LiveLast.Equal(now) {
		t.Fatalf("live counters = held %d broken %d since %v last %v", got.LiveHeld, got.LiveBroken, got.LiveSince, got.LiveLast)
	}
	svcRec, ok, err := r.GetRecommendation(ctx, svcRecID)
	if err != nil || !ok {
		t.Fatalf("get service rec: ok=%v err=%v", ok, err)
	}
	if svcRec.Service != svc || svcRec.Namespace != "ns" {
		t.Fatalf("service rec identity = %s/%s", svcRec.Namespace, svcRec.Service)
	}
	for _, rule := range svcRec.Rules {
		switch rule.Rule {
		case "allowed_egress":
			if rule.LiveChecked != 6 || rule.LiveViolated != 2 || !rule.LiveLast.Equal(now) {
				t.Fatalf("per-rule live counts = %+v", rule)
			}
		default:
			if rule.LiveChecked != 0 {
				t.Fatalf("rule without counters got %+v", rule)
			}
		}
	}

	list, err := r.ListRecommendations(ctx, repo.RecommendationFilter{Namespace: "ns", Service: svc})
	if err != nil || len(list) != 2 {
		t.Fatalf("list by service: %d err=%v", len(list), err)
	}
	if list, err := r.ListRecommendations(ctx, repo.RecommendationFilter{Kind: repo.RecommendationKindServiceGuardrail, Service: svc}); err != nil || len(list) != 1 {
		t.Fatalf("list by kind: %d err=%v", len(list), err)
	}

	if err := r.SetRecommendationState(ctx, corrID, repo.RecommendationDismissed); err != nil {
		t.Fatalf("set state: %v", err)
	}
	if list, err := r.ListRecommendations(ctx, repo.RecommendationFilter{State: repo.RecommendationDismissed, Service: svc}); err != nil || len(list) != 1 || list[0].ID != corrID {
		t.Fatalf("dismissed list = %+v err=%v", list, err)
	}
	if err := r.SetRecommendationState(ctx, "missing-id", repo.RecommendationApplied); err == nil {
		t.Fatalf("unknown recommendation must error")
	}

	if err := r.DeleteRecommendations(ctx, tx.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, err := r.GetRecommendation(ctx, corrID); err != nil || ok {
		t.Fatalf("transaction recommendation left: ok=%v err=%v", ok, err)
	}
	_, _, _ = r.GetRecommendation(ctx, svcRecID)
}

// TestRecommendationMiningCandidates covers the miner's queue: only
// finished-learning transactions with new relation samples qualify.
func TestRecommendationMiningCandidates(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	txID := rand.Int63()
	now := time.Now()
	if _, err := r.ApplyBaselineDeltas(ctx, []repo.BaselineDelta{
		{TransactionID: txID, Class: repo.D5Libraries, Contribution: json.RawMessage(`{"libs":[]}`), CountDelta: 1, Promote: true},
		{TransactionID: txID, Class: repo.D1CallEdges, Contribution: json.RawMessage(`{"edges":[]}`), CountDelta: 1, Saturate: true},
	}, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var obs []repo.Observation
	for i := 0; i < 3; i++ {
		obs = append(obs, repo.Observation{TransactionID: txID, TraceID: fmt.Sprintf("rel-%d", i), ObservedAt: now, SampleReason: repo.RelationSampleReason, RawOTLP: []byte("x")})
	}
	if err := r.InsertObservations(ctx, obs); err != nil {
		t.Fatalf("observations: %v", err)
	}

	find := func(scored int) *repo.RecommendationMiningCandidate {
		cands, err := r.ListRecommendationMiningCandidates(ctx, scored, 100000)
		if err != nil {
			t.Fatalf("candidates: %v", err)
		}
		for i := range cands {
			if cands[i].TransactionID == txID {
				return &cands[i]
			}
		}
		return nil
	}
	if c := find(2); c == nil || c.SampleCount != 3 {
		t.Fatalf("settled transaction with 3 samples must be a candidate, got %+v", c)
	}
	if c := find(3); c != nil {
		t.Fatalf("fewer settled classes than required must not qualify")
	}
	if err := r.RecordRecommendationMiningRun(ctx, repo.RecommendationMiningRun{TransactionID: txID, ClaimedAt: now, MinedAt: now, SampleCount: 3}); err != nil {
		t.Fatalf("record run: %v", err)
	}
	if c := find(2); c != nil {
		t.Fatalf("a run that saw every sample must clear the candidate")
	}
	if err := r.InsertObservations(ctx, []repo.Observation{{TransactionID: txID, TraceID: "rel-new", ObservedAt: now, SampleReason: repo.RelationSampleReason, RawOTLP: []byte("x")}}); err != nil {
		t.Fatalf("observation: %v", err)
	}
	if c := find(2); c == nil || c.SampleCount != 4 {
		t.Fatalf("a new sample must requeue the transaction, got %+v", c)
	}
	_ = r.DeleteRecommendations(ctx, txID)
	_ = r.ResetTransactionBaselines(ctx, txID)
}

// TestPoliciesCRUD covers alert and learning policy writes, replaces and deletes.
func TestPoliciesCRUD(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	key := "ns/" + uniqueService("pol")

	p := repo.Policy{
		Scope: repo.ScopeService, ScopeKey: key, Name: "svc", FireAtScore: 35, Enabled: false,
		SignalWeights: map[repo.SignalSource]int{"d2_egress": 70},
		EnricherLists: map[repo.EnricherListKey][]string{"egress_allow": {"a.com"}},
		Delivery:      repo.Delivery{Muted: true, ExcludedIntegrationIDs: []string{"i1"}},
	}
	if err := r.UpsertAlertPolicy(ctx, p); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	findPolicy := func(enabledOnly bool) *repo.Policy {
		var (
			list []repo.Policy
			err  error
		)
		if enabledOnly {
			list, err = r.ListEnabledPolicies(ctx)
		} else {
			list, err = r.ListAlertPolicies(ctx)
		}
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for i := range list {
			if list[i].Scope == repo.ScopeService && list[i].ScopeKey == key {
				return &list[i]
			}
		}
		return nil
	}
	got := findPolicy(false)
	if got == nil || got.ID <= 1 || got.SignalWeights["d2_egress"] != 70 || got.EnricherLists["egress_allow"][0] != "a.com" ||
		!got.Delivery.Muted || got.Delivery.ExcludedIntegrationIDs[0] != "i1" {
		t.Fatalf("policy round trip = %+v", got)
	}
	if findPolicy(true) != nil {
		t.Fatalf("disabled policy listed as enabled")
	}
	p.Enabled, p.FireAtScore = true, 60
	if err := r.UpsertAlertPolicy(ctx, p); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got := findPolicy(true); got == nil || got.FireAtScore != 60 {
		t.Fatalf("replaced policy = %+v", got)
	}
	if err := r.DeleteAlertPolicy(ctx, repo.ScopeService, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if findPolicy(false) != nil {
		t.Fatalf("deleted policy still listed")
	}

	lp := repo.LearningPolicy{
		Class: repo.D2Egress, Mode: repo.LearningModeKOf, MinMatches: 2, Scope: repo.ScopeService, ScopeKey: key,
		Conditions: []repo.LearningCondition{{Type: repo.ConditionMinObservations, MinObservations: 100}},
	}
	if err := r.UpsertLearningPolicy(ctx, lp); err != nil {
		t.Fatalf("upsert learning: %v", err)
	}
	lp.MinMatches = 1
	if err := r.UpsertLearningPolicy(ctx, lp); err != nil {
		t.Fatalf("replace learning: %v", err)
	}
	lps, err := r.ListLearningPolicies(ctx)
	if err != nil {
		t.Fatalf("list learning: %v", err)
	}
	matches := 0
	for _, l := range lps {
		if l.ScopeKey == key {
			matches++
			if l.MinMatches != 1 || len(l.Conditions) != 1 || l.Conditions[0].MinObservations != 100 {
				t.Fatalf("learning policy round trip = %+v", l)
			}
		}
	}
	if matches != 1 {
		t.Fatalf("learning policies for scope = %d, want 1", matches)
	}
	if err := r.DeleteLearningPolicy(ctx, repo.D2Egress, repo.ScopeService, key); err != nil {
		t.Fatalf("delete learning: %v", err)
	}
}

// TestServiceGuardrailsAndProfile covers guardrail CRUD and the service profile
// derived from D1/D2 baselines.
func TestServiceGuardrailsAndProfile(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	svc := uniqueService("gr")
	key := "ns/" + svc

	g := repo.ServiceGuardrail{
		Scope: repo.ScopeService, ScopeKey: key,
		RuleLists:        map[repo.RuleKey][]string{"allowed_callers": {"frontend"}},
		RuleModes:        map[repo.RuleKey]repo.RuleMode{"allowed_callers": repo.RuleModeEnforce},
		RuleOrigins:      map[repo.RuleKey]string{"allowed_callers": "manual"},
		RuleFingerprints: map[repo.RuleKey]string{"allowed_callers": "fp"},
		RuleCorrelations: map[repo.RuleKey][]repo.CorrelationSpec{"attribute_correlation": {{Name: "c1", Relation: "equals"}}},
		Delivery:         repo.Delivery{ExcludedIntegrationIDs: []string{"x"}},
	}
	if err := r.UpsertServiceGuardrail(ctx, g); err != nil {
		t.Fatalf("upsert guardrail: %v", err)
	}
	list, err := r.ListServiceGuardrails(ctx)
	if err != nil {
		t.Fatalf("list guardrails: %v", err)
	}
	var got *repo.ServiceGuardrail
	for i := range list {
		if list[i].ScopeKey == key {
			got = &list[i]
		}
	}
	if got == nil || got.RuleLists["allowed_callers"][0] != "frontend" || got.RuleModes["allowed_callers"] != repo.RuleModeEnforce ||
		got.RuleOrigins["allowed_callers"] != "manual" || got.RuleFingerprints["allowed_callers"] != "fp" ||
		got.RuleCorrelations["attribute_correlation"][0].Name != "c1" || got.Delivery.ExcludedIntegrationIDs[0] != "x" {
		t.Fatalf("guardrail round trip = %+v", got)
	}
	if err := r.DeleteServiceGuardrail(ctx, repo.ScopeService, key); err != nil {
		t.Fatalf("delete guardrail: %v", err)
	}
	list, _ = r.ListServiceGuardrails(ctx)
	for _, g := range list {
		if g.ScopeKey == key {
			t.Fatalf("deleted guardrail still listed")
		}
	}

	// Profile: caller -> svc and svc -> callee edges, plus egress owned by svc.
	caller, callee := uniqueService("caller"), uniqueService("callee")
	tx, err := r.UpsertTransaction(ctx, svc, "ns", "GET /p", repo.KindHTTP, testSourceIdentity)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	callerTx := rand.Int63()
	d1 := func(from, to string) string {
		return fmt.Sprintf(`{"from":{"svc":%q,"op":"x"},"to":{"svc":%q,"op":"y"}}`, from, to)
	}
	if _, err := r.ApplyBaselineDeltas(ctx, []repo.BaselineDelta{
		{TransactionID: callerTx, Class: repo.D1CallEdges, Contribution: json.RawMessage(`{"edges":[` + d1(caller, svc) + `]}`), CountDelta: 1},
		{TransactionID: tx.ID, Class: repo.D1CallEdges, Contribution: json.RawMessage(`{"edges":[` + d1(svc, callee) + `,` + d1(svc, svc) + `]}`), CountDelta: 1},
		{TransactionID: tx.ID, Class: repo.D2Egress, Contribution: json.RawMessage(`{"peers":[` +
			`{"host":"api.stripe.com","port":443,"owner":"` + svc + `","external":true},` +
			`{"host":"internal-db","port":0,"owner":"` + svc + `","external":true},` +
			`{"host":"other.com","port":443,"owner":"someone-else","external":true},` +
			`{"host":"` + svc + `.ns.svc","port":80,"owner":"` + svc + `","external":true}]}`), CountDelta: 1},
	}, nil); err != nil {
		t.Fatalf("apply baselines: %v", err)
	}
	profile, err := r.GetServiceProfile(ctx, "ns", svc)
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	if !slices.Equal(profile.Callers, []string{caller}) || !slices.Equal(profile.Callees, []string{callee}) {
		t.Fatalf("callers=%v callees=%v", profile.Callers, profile.Callees)
	}
	if !slices.Equal(profile.Egress, []string{"api.stripe.com:443", "internal-db"}) {
		t.Fatalf("egress = %v", profile.Egress)
	}
	if !slices.Equal(profile.Transactions, []string{repo.TransactionIdentity(repo.KindHTTP, "GET /p")}) {
		t.Fatalf("transactions = %v", profile.Transactions)
	}
	_ = r.ResetTransactionBaselines(ctx, callerTx)
	_ = r.DeleteTransaction(ctx, tx.ID)
}

// TestGuardrailViolationFiltersAndDelete covers namespace/service filtering on
// scope keys, the single-group read, and deleting by rule.
func TestGuardrailViolationFiltersAndDelete(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	ns, svc := uniqueService("gvns"), uniqueService("gvsvc")
	rule := repo.RuleKey(fmt.Sprintf("rule-%d", rand.Int63()))
	since := time.Now().Add(-time.Hour)
	if err := r.RecordGuardrailViolations(ctx, []repo.GuardrailViolationHit{
		{Scope: repo.ScopeService, ScopeKey: ns + "/" + svc, RuleKey: rule, Offending: "a", Occurrences: 1, MaxScore: 10},
		{Scope: repo.ScopeService, ScopeKey: ns + "/" + svc, RuleKey: rule, Offending: "a", Occurrences: 2, MaxScore: 30, LastTraceID: "newest"},
		{Scope: repo.ScopeService, ScopeKey: "other/" + svc, RuleKey: rule, Offending: "b", Occurrences: 1, MaxScore: 10},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	f := repo.FindingListSince(since, 100)
	f.Namespace, f.Service = ns, svc
	groups, err := r.ListGuardrailViolations(ctx, f)
	if err != nil || len(groups) != 1 {
		t.Fatalf("filtered groups = %+v err=%v", groups, err)
	}
	if groups[0].Occurrences != 3 || groups[0].MaxScore != 30 || groups[0].Namespace != ns || groups[0].Service != svc || groups[0].LastTraceID != "newest" {
		t.Fatalf("merged group = %+v", groups[0])
	}
	one, ok, err := r.GetGuardrailViolation(ctx, ns+"/"+svc, rule, "a")
	if err != nil || !ok || one.Occurrences != 3 {
		t.Fatalf("get group = %+v ok=%v err=%v", one, ok, err)
	}
	statusFilter := repo.FindingListSince(since, 100)
	statusFilter.Service, statusFilter.Status = svc, string(repo.GuardrailViolationAccepted)
	if accepted, err := r.ListGuardrailViolations(ctx, statusFilter); err != nil || len(accepted) != 0 {
		t.Fatalf("accepted filter = %d err=%v, want 0", len(accepted), err)
	}
	if err := r.DeleteGuardrailViolationsForRule(ctx, rule); err != nil {
		t.Fatalf("delete for rule: %v", err)
	}
	if _, ok, err := r.GetGuardrailViolation(ctx, ns+"/"+svc, rule, "a"); err != nil || ok {
		t.Fatalf("group left after delete: ok=%v err=%v", ok, err)
	}
}

// TestIntegrationsLedgerAndHealth covers integrations CRUD, the delivery
// ledger, the attempt audit trail, the derived health and the cursor.
func TestIntegrationsLedgerAndHealth(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	id := uniqueService("int")
	in := repo.Integration{
		ID: id, Name: "slack", Type: "webhook", Enabled: true, Config: json.RawMessage(`{"url":"http://x"}`),
		Filters: repo.IntegrationFilters{MinSeverity: "high", Kinds: []string{"anomaly"}, NotifyOnResolve: true},
	}
	if err := r.UpsertIntegration(ctx, in); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, ok, err := r.GetIntegration(ctx, id)
	if err != nil || !ok || string(got.Config) != `{"url":"http://x"}` || got.Filters.MinSeverity != "high" || got.CreatedAt.IsZero() {
		t.Fatalf("get = %+v ok=%v err=%v", got, ok, err)
	}
	in.Name, in.CreatedAt = "renamed", got.CreatedAt
	if err := r.UpsertIntegration(ctx, in); err != nil {
		t.Fatalf("update: %v", err)
	}
	all, err := r.ListIntegrations(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if idx := slices.IndexFunc(all, func(i repo.Integration) bool { return i.ID == id }); idx < 0 || all[idx].Name != "renamed" {
		t.Fatalf("listed integration missing or not renamed")
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := r.UpsertNotificationDispatch(ctx, []repo.NotificationDispatch{
		{IntegrationID: id, FindingKey: "f1", LastAction: repo.DispatchFiring, State: repo.DispatchSent, SentAt: now, OccurrencesAtSend: 4, ExternalID: "ext"},
		{IntegrationID: id, FindingKey: "f2", LastAction: repo.DispatchFiring, State: repo.DispatchFailed, Attempts: 3, NextAttemptAt: now.Add(time.Hour), LastError: "boom"},
		{IntegrationID: id, FindingKey: "f3", LastAction: repo.DispatchFiring, State: repo.DispatchDead, Attempts: 9},
	}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	rows, err := r.ListNotificationDispatch(ctx, repo.DispatchListFilter{IntegrationID: id, FindingKeys: []string{"f1", "f2"}})
	if err != nil || len(rows) != 2 {
		t.Fatalf("dispatch by keys = %d err=%v", len(rows), err)
	}
	for _, d := range rows {
		if d.FindingKey == "f1" && (d.ExternalID != "ext" || !d.SentAt.Equal(now) || !d.NextAttemptAt.IsZero()) {
			t.Fatalf("sent row = %+v", d)
		}
	}
	if due, err := r.ListNotificationDispatch(ctx, repo.DispatchListFilter{IntegrationID: id, State: repo.DispatchFailed, DueBefore: now}); err != nil || len(due) != 0 {
		t.Fatalf("failed row is not due yet: %d err=%v", len(due), err)
	}
	if err := r.ResetNotificationDispatch(ctx, id, nil); err != nil {
		t.Fatalf("reset: %v", err)
	}
	pending, err := r.ListNotificationDispatch(ctx, repo.DispatchListFilter{IntegrationID: id, State: repo.DispatchPending})
	if err != nil || len(pending) != 2 {
		t.Fatalf("reset should re-arm failed+dead: %d err=%v", len(pending), err)
	}
	for _, d := range pending {
		if d.Attempts != 0 || d.LastError != "" {
			t.Fatalf("re-armed row = %+v", d)
		}
	}

	if err := r.RecordNotificationAttempts(ctx, []repo.NotificationAttempt{
		{IntegrationID: id, FindingKey: "f1", Action: repo.DispatchFiring, Outcome: repo.AttemptOK, HTTPStatus: 200, Duration: 12 * time.Millisecond, AttemptedAt: now.Add(-3 * time.Minute)},
		{IntegrationID: id, FindingKey: "f2", Action: repo.DispatchFiring, Outcome: repo.AttemptError, HTTPStatus: 500, Error: "first", AttemptedAt: now.Add(-2 * time.Minute)},
		{IntegrationID: id, FindingKey: "f2", Action: repo.DispatchFiring, Outcome: repo.AttemptError, HTTPStatus: 502, Error: "latest", AttemptedAt: now.Add(-time.Minute)},
	}); err != nil {
		t.Fatalf("attempts: %v", err)
	}
	attempts, err := r.ListNotificationAttempts(ctx, id, time.Time{}, 10)
	if err != nil || len(attempts) != 3 || attempts[0].Error != "latest" || attempts[2].Duration != 12*time.Millisecond {
		t.Fatalf("attempts = %+v err=%v", attempts, err)
	}
	health, err := r.ListIntegrationHealth(ctx)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	h := health[id]
	if h.ConsecutiveFailures != 2 || h.LastError != "latest" || !h.LastSuccessAt.Equal(now.Add(-3*time.Minute)) || h.Pending != 2 {
		t.Fatalf("health = %+v", h)
	}

	if _, ok, err := r.GetNotificationCursor(ctx, id); err != nil || ok {
		t.Fatalf("cursor before set: ok=%v err=%v", ok, err)
	}
	if err := r.SetNotificationCursor(ctx, repo.NotificationCursor{IntegrationID: id, Cursor: now, LastRunAt: now, Leader: "pod-a"}); err != nil {
		t.Fatalf("set cursor: %v", err)
	}
	if err := r.SetNotificationCursor(ctx, repo.NotificationCursor{IntegrationID: id, Cursor: now.Add(time.Second), LastRunAt: now, Leader: "pod-b"}); err != nil {
		t.Fatalf("advance cursor: %v", err)
	}
	cur, ok, err := r.GetNotificationCursor(ctx, id)
	if err != nil || !ok || !cur.Cursor.Equal(now.Add(time.Second)) || cur.Leader != "pod-b" {
		t.Fatalf("cursor = %+v ok=%v err=%v", cur, ok, err)
	}

	if err := r.DeleteIntegration(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, _ := r.GetIntegration(ctx, id); ok {
		t.Fatalf("integration left after delete")
	}
	if rows, _ := r.ListNotificationDispatch(ctx, repo.DispatchListFilter{IntegrationID: id}); len(rows) != 0 {
		t.Fatalf("dispatch rows left: %d", len(rows))
	}
	if attempts, _ := r.ListNotificationAttempts(ctx, id, time.Time{}, 10); len(attempts) != 0 {
		t.Fatalf("attempts left: %d", len(attempts))
	}
	if _, ok, _ := r.GetNotificationCursor(ctx, id); ok {
		t.Fatalf("cursor left after delete")
	}
}

// TestServiceGraphEdgesAndBlastRadius covers per-pod upserts, the newest-wins
// rule, the cross-pod sum and the blast-radius walk.
func TestServiceGraphEdgesAndBlastRadius(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	ns := uniqueService("sg")
	now := time.Now().UTC().Truncate(time.Millisecond)
	edge := func(client, server, pod string, req uint64, at time.Time) repo.ServiceGraphEdge {
		return repo.ServiceGraphEdge{
			ClientNamespace: ns, ClientService: client, ServerNamespace: ns, ServerService: server,
			ConnectionType: "", CollectorPod: pod, RequestCount: req, FailedCount: req / 10, ObservedAt: at,
		}
	}
	if err := r.UpsertServiceGraphEdges(ctx, []repo.ServiceGraphEdge{
		edge("frontend", "checkout", "gw-0", 100, now),
		edge("frontend", "checkout", "gw-1", 50, now),
		edge("checkout", "payments", "gw-0", 30, now),
		{ClientNamespace: ns, ClientService: "payments", ServerNamespace: "", ServerService: "postgres", ConnectionType: "database", ServerIsVirtual: true, CollectorPod: "gw-0", RequestCount: 7, ObservedAt: now},
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// An older datapoint for the same (edge, pod) must not overwrite the newer one.
	if err := r.UpsertServiceGraphEdges(ctx, []repo.ServiceGraphEdge{edge("frontend", "checkout", "gw-0", 1, now.Add(-time.Minute))}); err != nil {
		t.Fatalf("upsert stale: %v", err)
	}

	g, err := r.QueryBlastRadius(ctx, ns, "checkout", 2)
	if err != nil {
		t.Fatalf("blast radius: %v", err)
	}
	var sum uint64
	for _, e := range g.Edges {
		if e.ClientService == "frontend" && e.ServerService == "checkout" {
			sum = e.RequestCount
			if !e.LastSeen.Equal(now) {
				t.Fatalf("last_seen = %v, want %v", e.LastSeen, now)
			}
		}
	}
	if sum != 150 {
		t.Fatalf("frontend->checkout requests = %d, want 150 (summed across pods, stale ignored)", sum)
	}
	names := map[string]bool{}
	for _, n := range g.Nodes {
		names[n.Service] = n.IsVirtual
	}
	if _, ok := names["postgres"]; !ok || !names["postgres"] {
		t.Fatalf("depth-2 walk should reach the virtual postgres node: %+v", g.Nodes)
	}
	if len(g.Edges) != 3 {
		t.Fatalf("edges = %d, want 3", len(g.Edges))
	}
}

// TestRetentionPrunesExpired covers the retention job and its recorded outcome.
func TestRetentionPrunesExpired(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	if err := r.UpsertSystemSettings(ctx, repo.DefaultSystemSettings()); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	txID := rand.Int63()
	now := time.Now()
	mk := func(trace, reason string, age time.Duration) repo.Observation {
		return repo.Observation{TransactionID: txID, TraceID: trace, SampleReason: reason, ObservedAt: now.Add(-age), RawOTLP: []byte("x")}
	}
	if err := r.InsertObservations(ctx, []repo.Observation{
		mk("old-relation", repo.RelationSampleReason, 15*24*time.Hour),
		mk("new-relation", repo.RelationSampleReason, 13*24*time.Hour),
		mk("old-example", repo.ExampleSampleReason, 121*24*time.Hour),
		mk("kept-example", repo.ExampleSampleReason, 100*24*time.Hour),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	integration := uniqueService("ret")
	if err := r.RecordNotificationAttempts(ctx, []repo.NotificationAttempt{
		{IntegrationID: integration, Action: repo.DispatchFiring, Outcome: repo.AttemptOK, AttemptedAt: now.Add(-31 * 24 * time.Hour)},
		{IntegrationID: integration, Action: repo.DispatchFiring, Outcome: repo.AttemptOK, AttemptedAt: now.Add(-time.Hour)},
	}); err != nil {
		t.Fatalf("attempts: %v", err)
	}

	if err := r.PruneExpired(ctx, now); err != nil {
		t.Fatalf("prune: %v", err)
	}
	counts, err := r.CountObservationsByReason(ctx, txID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts[repo.RelationSampleReason] != 1 || counts[repo.ExampleSampleReason] != 1 {
		t.Fatalf("after prune counts = %v, want 1 relation + 1 example", counts)
	}
	if _, ok, _ := r.GetObservationByTrace(ctx, txID, "kept-example"); !ok {
		t.Fatalf("example inside retention was deleted")
	}
	attempts, err := r.ListNotificationAttempts(ctx, integration, time.Time{}, 10)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts after prune = %d err=%v, want 1", len(attempts), err)
	}

	// A shorter retention applies right away on save.
	if err := r.SetObservationRetentionDays(ctx, 30); err != nil {
		t.Fatalf("set retention: %v", err)
	}
	if _, ok, _ := r.GetObservationByTrace(ctx, txID, "kept-example"); ok {
		t.Fatalf("example older than the new retention must be deleted")
	}
	if err := r.SetObservationRetentionDays(ctx, 0); err == nil {
		t.Fatalf("retention 0 must be rejected")
	}

	h, err := r.GetStorageHealth(ctx)
	if err != nil {
		t.Fatalf("storage health: %v", err)
	}
	if !h.Retention.EverRan || !h.Retention.LastRunOK || h.Retention.ObservationsDeleted < 2 || h.Retention.AttemptsDeleted < 1 {
		t.Fatalf("retention status = %+v", h.Retention)
	}
}

// TestStorageHealth checks the Storage page snapshot reads every section.
func TestStorageHealth(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	h, err := r.GetStorageHealth(ctx)
	if err != nil {
		t.Fatalf("storage health: %v", err)
	}
	if !h.Connection.Reachable || h.Connection.Version == "" || h.Connection.Database == "" || h.Connection.UptimeSeconds <= 0 {
		t.Fatalf("connection = %+v", h.Connection)
	}
	if h.Status == repo.StorageStatusUnreachable {
		t.Fatalf("status = %s", h.Status)
	}
	if h.Disk.DatabaseBytes == 0 || h.Disk.VolumeBytes != 0 || h.Disk.Status != "ok" {
		t.Fatalf("disk = %+v", h.Disk)
	}
	if h.Connections.Max == 0 || h.Connections.Total == 0 || h.Connections.PoolMax == 0 {
		t.Fatalf("connections = %+v", h.Connections)
	}
	if h.Database.Commits == 0 || h.Database.CacheHitPercent <= 0 {
		t.Fatalf("database stats = %+v", h.Database)
	}
	tables := map[string]bool{}
	for _, tbl := range h.Tables {
		tables[tbl.Name] = true
	}
	for _, want := range []string{"transactions", "baseline_meta", "trace_observations", "anomaly_issues", "maintenance_runs"} {
		if !tables[want] {
			t.Fatalf("table %s missing from %v", want, tables)
		}
	}
}
