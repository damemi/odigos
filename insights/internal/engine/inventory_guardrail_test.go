package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/odigos-io/odigos/insights/internal/repo"
	"github.com/odigos-io/odigos/insights/internal/risk"
)

// defaultIdentityFingerprint is the stamp an engine built by New carries, since
// its system snapshot starts from the compiled defaults.
func defaultIdentityFingerprint() string {
	return repo.IdentityDimensionsFingerprint(repo.DefaultSystemSettings().TransactionIdentityDimensions)
}

func TestForbiddenTransactions(t *testing.T) {
	const scheme = "scheme-a"
	allowed := repo.TransactionIdentity(repo.KindHTTP, "GET /products [http.response.status_code=200]")
	known := repo.Transaction{
		Service: "shop", Namespace: "ns", Operation: "GET /products [http.response.status_code=200]", Kind: repo.KindHTTP,
	}
	guardrail := repo.ServiceGuardrail{
		RuleLists:        map[repo.RuleKey][]string{risk.RuleAllowedTransactions: {allowed}},
		RuleFingerprints: map[repo.RuleKey]string{risk.RuleAllowedTransactions: scheme},
	}
	if got := forbiddenTransactions(known, guardrail, scheme); len(got) != 0 {
		t.Fatalf("known identity should pass, got %v", got)
	}

	unknown := repo.Transaction{
		Service: "shop", Namespace: "ns", Operation: "GET /products [http.response.status_code=403]", Kind: repo.KindHTTP,
	}
	empty := repo.ServiceGuardrail{
		RuleLists:        map[repo.RuleKey][]string{risk.RuleAllowedTransactions: {}},
		RuleFingerprints: map[repo.RuleKey]string{risk.RuleAllowedTransactions: scheme},
	}
	got := forbiddenTransactions(unknown, empty, scheme)
	want := repo.TransactionIdentity(repo.KindHTTP, "GET /products [http.response.status_code=403]")
	if len(got) != 1 || got[0] != want {
		t.Fatalf("unknown identity = %v, want [%q]", got, want)
	}

	// Bare operation (no dimensions) must not match an allowlist seeded with dimensions.
	bare := repo.Transaction{Service: "shop", Namespace: "ns", Operation: "GET /products", Kind: repo.KindHTTP}
	if got := forbiddenTransactions(bare, guardrail, scheme); len(got) == 0 {
		t.Fatal("bare operation should not match dimensioned allowlist entry")
	}
}

// A replica that has picked up an identity-dimension change while still holding
// the pre-change allowlist must not flag anything: this is the multi-replica case
// where the guardrail snapshot has not been reloaded yet.
func TestForbiddenTransactionsSkipsForeignIdentityScheme(t *testing.T) {
	guardrail := repo.ServiceGuardrail{
		RuleLists: map[repo.RuleKey][]string{
			risk.RuleAllowedTransactions: {repo.TransactionIdentity(repo.KindHTTP, "GET /products")},
		},
		RuleFingerprints: map[repo.RuleKey]string{risk.RuleAllowedTransactions: "scheme-a"},
	}
	tx := repo.Transaction{
		Service: "shop", Namespace: "ns", Operation: "GET /products [deployment.environment=prod]", Kind: repo.KindHTTP,
	}
	if got := forbiddenTransactions(tx, guardrail, "scheme-b"); len(got) != 0 {
		t.Fatalf("allowlist from another identity scheme must not be enforced, got %v", got)
	}
	// Same allowlist, matching scheme: enforcement is back on.
	if got := forbiddenTransactions(tx, guardrail, "scheme-a"); len(got) != 1 {
		t.Fatalf("matching scheme should enforce, got %v", got)
	}
}

// An allowlist with no stamp has unknown provenance, so it is not enforced.
func TestForbiddenTransactionsSkipsUnstampedAllowlist(t *testing.T) {
	guardrail := repo.ServiceGuardrail{
		RuleLists: map[repo.RuleKey][]string{
			risk.RuleAllowedTransactions: {repo.TransactionIdentity(repo.KindHTTP, "GET /products")},
		},
	}
	tx := repo.Transaction{Service: "shop", Namespace: "ns", Operation: "GET /other", Kind: repo.KindHTTP}
	if got := forbiddenTransactions(tx, guardrail, "scheme-a"); len(got) != 0 {
		t.Fatalf("unstamped allowlist must not be enforced, got %v", got)
	}
}

func TestEngine_AllowedTransactionsFires(t *testing.T) {
	known := repo.TransactionIdentity(repo.KindHTTP, "POST /buy")
	guardrail := repo.ServiceGuardrail{
		Scope:            repo.ScopeService,
		ScopeKey:         "ns/shop",
		RuleLists:        map[repo.RuleKey][]string{risk.RuleAllowedTransactions: {known}},
		RuleModes:        map[repo.RuleKey]repo.RuleMode{risk.RuleAllowedTransactions: repo.RuleModeEnforce},
		RuleFingerprints: map[repo.RuleKey]string{risk.RuleAllowedTransactions: defaultIdentityFingerprint()},
	}

	r := newFakeRepo()
	eng := New(r, nil, nil, nil)
	eng.clock = func() time.Time { return time.Unix(0, 0).UTC() }
	eng.SetGuardrailConfig(guardrailConfigFrom(guardrail))

	root := &Span{
		SpanID: "root", Name: "GET /malicious/request", Service: "shop",
		Kind:      tracepb.Span_SPAN_KIND_SERVER,
		SpanAttrs: map[string]string{"http.method": "GET"}, ResourceAttrs: k8sResourceAttrs("ns"), SourceIdentity: k8sSourceIdentity("ns"),
	}
	eng.Process(context.Background(), &Trace{TraceID: "tid", Root: root, Spans: []*Span{root}})
	if err := eng.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.guardrailHits) != 1 {
		t.Fatalf("hits = %d, want 1", len(r.guardrailHits))
	}
	want := repo.TransactionIdentity(repo.KindHTTP, "GET /malicious/request")
	if r.guardrailHits[0].RuleKey != risk.RuleAllowedTransactions || r.guardrailHits[0].Offending != want {
		t.Fatalf("hit = %+v, want rule=%s offending=%q", r.guardrailHits[0], risk.RuleAllowedTransactions, want)
	}
	if r.guardrailHits[0].MaxScore < 35 || r.guardrailHits[0].MaxScore > 49 {
		t.Fatalf("score = %d, want medium band 35-49", r.guardrailHits[0].MaxScore)
	}
}

// The HA case end to end: this replica has picked up an identity-dimension
// change but is still holding the allowlist that was seeded before it. Nothing
// may fire, without waiting for any reload.
func TestEngine_AllowedTransactionsSilentUnderStaleAllowlist(t *testing.T) {
	staleScheme := repo.IdentityDimensionsFingerprint([]repo.TransactionIdentityDimension{
		{Key: repo.IdentityDimHTTPStatusCode, Enabled: true},
	})
	guardrail := repo.ServiceGuardrail{
		Scope:     repo.ScopeService,
		ScopeKey:  "ns/shop",
		RuleLists: map[repo.RuleKey][]string{risk.RuleAllowedTransactions: {"HTTP\tGET /known"}},
		RuleModes: map[repo.RuleKey]repo.RuleMode{risk.RuleAllowedTransactions: repo.RuleModeEnforce},
		// Seeded under a scheme with only the status-code dimension.
		RuleFingerprints: map[repo.RuleKey]string{risk.RuleAllowedTransactions: staleScheme},
	}

	r := newFakeRepo()
	eng := New(r, nil, nil, nil)
	eng.SetGuardrailConfig(guardrailConfigFrom(guardrail))
	// This replica now also folds an extra dimension into identity.
	eng.SetSystemConfig(newSystemConfig(repo.SystemSettings{
		TransactionIdentityDimensions: []repo.TransactionIdentityDimension{
			{Key: repo.IdentityDimHTTPStatusCode, Enabled: true},
			{Key: "deployment.environment", Enabled: true},
		},
	}))

	root := &Span{
		SpanID: "root", Name: "GET /anything", Service: "shop",
		Kind: tracepb.Span_SPAN_KIND_SERVER,
		SpanAttrs: map[string]string{
			"http.method":            "GET",
			"deployment.environment": "prod",
		},
		ResourceAttrs: k8sResourceAttrs("ns"), SourceIdentity: k8sSourceIdentity("ns"),
	}
	eng.Process(context.Background(), &Trace{TraceID: "tid", Root: root, Spans: []*Span{root}})
	if err := eng.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.guardrailHits) != 0 {
		t.Fatalf("stale allowlist must not fire, got %d hits: %+v", len(r.guardrailHits), r.guardrailHits)
	}
}

// Counts gathered before another replica deleted all transactions must not be
// written afterwards, while rules that do not name a transaction still are.
func TestFlushViolationsDropsCountsFromWipedInventory(t *testing.T) {
	const (
		epochBeforeReset = 7
		epochAfterReset  = 8
	)
	r := newFakeRepo()
	settings := repo.DefaultSystemSettings()
	settings.TransactionInventoryEpoch = epochAfterReset
	if err := r.UpsertSystemSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	eng := New(r, nil, nil, nil)
	eng.SetGuardrailConfig(guardrailConfigFrom())

	now := time.Unix(0, 0).UTC()
	buf := eng.guardrails.buf
	// This replica counted these while it still believed in the pre-reset
	// inventory, which is the state a not-yet-reloaded replica is in.
	buf.add(repo.ScopeService, "ns/shop", risk.RuleAllowedTransactions, "HTTP\tGET /old", 40, "t1", now, epochBeforeReset)
	buf.add(repo.ScopeService, "ns/shop", risk.RuleAllowedCallees, "pricing", 40, "t1", now, epochBeforeReset)

	eng.guardrails.flushViolations(context.Background())
	if len(r.guardrailHits) != 1 {
		t.Fatalf("hits written = %d, want 1: %+v", len(r.guardrailHits), r.guardrailHits)
	}
	if r.guardrailHits[0].RuleKey != risk.RuleAllowedCallees {
		t.Fatalf("kept hit = %s, want %s", r.guardrailHits[0].RuleKey, risk.RuleAllowedCallees)
	}

	// Counts gathered after the reset are written normally.
	buf.add(repo.ScopeService, "ns/shop", risk.RuleAllowedTransactions, "HTTP\tGET /new", 40, "t2", now, epochAfterReset)
	eng.guardrails.flushViolations(context.Background())
	if len(r.guardrailHits) != 2 {
		t.Fatalf("hits written = %d, want 2: %+v", len(r.guardrailHits), r.guardrailHits)
	}
	if r.guardrailHits[1].Offending != "HTTP\tGET /new" {
		t.Fatalf("second hit = %+v, want the post-reset transaction hit", r.guardrailHits[1])
	}
}

// Anomaly firings counted before another replica deleted all transactions must
// not be written afterwards - every anomaly names a transaction that is gone.
func TestAnomalyFlushDropsFiringsFromWipedInventory(t *testing.T) {
	const (
		epochBeforeReset = 3
		epochAfterReset  = 4
	)
	r := newFakeRepo()
	settings := repo.DefaultSystemSettings()
	settings.TransactionInventoryEpoch = epochAfterReset
	if err := r.UpsertSystemSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	eng := New(r, nil, nil, nil)

	now := time.Unix(0, 0).UTC()
	buf := eng.anomalies.buf
	stale := repo.AnomalyOccurrence{
		TransactionID: 11, Signature: "sig-old", PolicyID: 1,
		TriggeredClasses: []repo.DeviationClass{repo.D1CallEdges},
		Offending:        "d1_call_edges=old", MaxScore: 40, LastSeen: now,
	}
	buf.add(stale, epochBeforeReset)

	eng.anomalies.flush(context.Background())
	if len(r.anomalies) != 0 {
		t.Fatalf("firings written = %d, want 0: %+v", len(r.anomalies), r.anomalies)
	}

	fresh := stale
	fresh.TransactionID = 12
	fresh.Signature = "sig-new"
	fresh.Offending = "d1_call_edges=new"
	buf.add(fresh, epochAfterReset)
	eng.anomalies.flush(context.Background())
	if len(r.anomalies) != 1 {
		t.Fatalf("firings written = %d, want 1: %+v", len(r.anomalies), r.anomalies)
	}
	if r.anomalies[0].Signature != "sig-new" {
		t.Fatalf("kept firing = %+v, want the post-reset one", r.anomalies[0])
	}
}

// A reset wipes the stored samples, so the in-memory sample counts must go with
// them or a relearned issue reusing a pre-reset signature would never capture a
// sample.
func TestDeleteAllTransactionsClearsSampleCounts(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, nil, nil)
	ctx := context.Background()

	tx := repo.Transaction{ID: 21}
	reason := repo.AnomalySampleReason("sig-recurring")
	trace := &Trace{TraceID: "trace-1", rawOTLP: []byte("otlp")}
	if !eng.keepSample(ctx, trace, tx, reason, repo.AnomalyEvidenceSamplesPerIssue, nil) {
		t.Fatal("first evidence sample should be kept")
	}
	if eng.keepSample(ctx, trace, tx, reason, repo.AnomalyEvidenceSamplesPerIssue, nil) {
		t.Fatal("second evidence sample should hit the per-issue cap")
	}
	if err := eng.DeleteAllTransactions(ctx); err != nil {
		t.Fatal(err)
	}
	if !eng.keepSample(ctx, trace, tx, reason, repo.AnomalyEvidenceSamplesPerIssue, nil) {
		t.Fatal("sample counts should be cleared so the issue can capture fresh evidence")
	}
}

func TestEngine_AllowedTransactionsSuggestSilent(t *testing.T) {
	guardrail := repo.ServiceGuardrail{
		Scope:     repo.ScopeService,
		ScopeKey:  "ns/shop",
		RuleLists: map[repo.RuleKey][]string{risk.RuleAllowedTransactions: {}},
		RuleModes: map[repo.RuleKey]repo.RuleMode{risk.RuleAllowedTransactions: repo.RuleModeSuggest},
	}
	r := newFakeRepo()
	eng := New(r, nil, nil, nil)
	eng.SetGuardrailConfig(guardrailConfigFrom(guardrail))
	root := &Span{
		SpanID: "root", Name: "GET /x", Service: "shop",
		Kind:      tracepb.Span_SPAN_KIND_SERVER,
		SpanAttrs: map[string]string{"http.method": "GET"}, ResourceAttrs: k8sResourceAttrs("ns"), SourceIdentity: k8sSourceIdentity("ns"),
	}
	eng.Process(context.Background(), &Trace{TraceID: "tid", Root: root, Spans: []*Span{root}})
	_ = eng.Flush(context.Background())
	if len(r.guardrailHits) != 0 {
		t.Fatalf("suggest mode should be silent, got %d hits", len(r.guardrailHits))
	}
}

func TestServiceInventoryReadyTreatsUnflushedPromote(t *testing.T) {
	txs := []repo.TransactionStat{
		{Transaction: repo.Transaction{ID: 1}, Promoted: true},
		{Transaction: repo.Transaction{ID: 2}, Promoted: false}, // just promoted in memory
	}
	if serviceInventoryReady(txs, nil) {
		t.Fatal("should not be ready while txn 2 is unpromoted in DB")
	}
	if !serviceInventoryReady(txs, map[int64]struct{}{2: {}}) {
		t.Fatal("should be ready when txn 2 is treated as promoted")
	}
}

func TestProtectAndOptOutInventory(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, nil, nil)
	eng.SetGuardrailConfig(guardrailConfigFrom())
	eng.SetSystemConfig(newSystemConfig(repo.SystemSettings{AutoTransactionGuardrail: true}))

	tx, err := r.UpsertTransaction(context.Background(), "shop", "ns", "GET /products", repo.KindHTTP, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, class := range ScoredClasses() {
		if err := r.UpsertBaselineClass(context.Background(), repo.BaselineClass{
			TransactionID: tx.ID, Class: class, Data: json.RawMessage(`{}`), Promoted: true,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := eng.EnableTransactionGuardrail(context.Background(), "ns", "shop"); err != nil {
		t.Fatal(err)
	}
	g, err := eng.loadOrInitServiceGuardrail(context.Background(), "ns/shop")
	if err != nil {
		t.Fatal(err)
	}
	if !InventoryProtected(g) {
		t.Fatal("expected protected after EnableTransactionGuardrail")
	}
	if g.RuleOrigins[risk.RuleAllowedTransactions] != "" {
		t.Fatalf("manual protect should not set auto origin, got %q", g.RuleOrigins[risk.RuleAllowedTransactions])
	}
	id := repo.TransactionIdentity(repo.KindHTTP, "GET /products")
	if len(g.RuleLists[risk.RuleAllowedTransactions]) != 1 || g.RuleLists[risk.RuleAllowedTransactions][0] != id {
		t.Fatalf("allowlist = %v", g.RuleLists[risk.RuleAllowedTransactions])
	}
	if got := g.RuleFingerprints[risk.RuleAllowedTransactions]; got != defaultIdentityFingerprint() {
		t.Fatalf("fingerprint = %q, want the identity scheme the allowlist was seeded under", got)
	}

	if err := eng.DisableTransactionGuardrail(context.Background(), "ns", "shop"); err != nil {
		t.Fatal(err)
	}
	g, _ = eng.loadOrInitServiceGuardrail(context.Background(), "ns/shop")
	if !InventoryOptedOut(g) {
		t.Fatal("expected opted out after DisableTransactionGuardrail")
	}
	if err := eng.maybeAutoEnableTransactionGuardrail(context.Background(), "ns", "shop"); err != nil {
		t.Fatal(err)
	}
	g, _ = eng.loadOrInitServiceGuardrail(context.Background(), "ns/shop")
	if InventoryProtected(g) {
		t.Fatal("auto-enable must respect opt-out")
	}
}

func TestForcePromoteServiceAutoEnablesGuardrail(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, nil, nil)
	eng.SetGuardrailConfig(guardrailConfigFrom())
	eng.SetSystemConfig(newSystemConfig(repo.SystemSettings{AutoTransactionGuardrail: true}))

	tx, err := r.UpsertTransaction(context.Background(), "geolocation", "default", "GET /geo", repo.KindHTTP, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, class := range ScoredClasses() {
		if err := r.UpsertBaselineClass(context.Background(), repo.BaselineClass{
			TransactionID: tx.ID, Class: class, Data: json.RawMessage(`{}`), Promoted: false,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := eng.ForcePromoteService(context.Background(), "default", "geolocation"); err != nil {
		t.Fatal(err)
	}
	g, err := eng.loadOrInitServiceGuardrail(context.Background(), "default/geolocation")
	if err != nil {
		t.Fatal(err)
	}
	if !InventoryProtected(g) {
		t.Fatal("force-promote should auto-enable transaction guardrail")
	}
	if g.RuleOrigins[risk.RuleAllowedTransactions] != repo.GuardrailOriginAutoTransactionGuardrail {
		t.Fatalf("origin = %q, want auto", g.RuleOrigins[risk.RuleAllowedTransactions])
	}
}

func TestPromoteTransactionsBaselinesDedupesAutoEnablePerService(t *testing.T) {
	// Build the same fixture twice: ten transactions spread over two services,
	// all with unpromoted baselines so the auto-enable check has work to do.
	build := func() (*fakeRepo, *Engine, []int64) {
		r := newFakeRepo()
		eng := New(r, nil, nil, nil)
		eng.SetGuardrailConfig(guardrailConfigFrom())
		eng.SetSystemConfig(newSystemConfig(repo.SystemSettings{AutoTransactionGuardrail: true}))
		var ids []int64
		for _, service := range []string{"geolocation", "checkout"} {
			for i := 0; i < 5; i++ {
				op := "GET /" + service + "/" + string(rune('a'+i))
				tx, err := r.UpsertTransaction(context.Background(), service, "default", op, repo.KindHTTP, nil)
				if err != nil {
					t.Fatal(err)
				}
				for _, class := range ScoredClasses() {
					if err := r.UpsertBaselineClass(context.Background(), repo.BaselineClass{
						TransactionID: tx.ID, Class: class, Data: json.RawMessage(`{}`), Promoted: false,
					}); err != nil {
						t.Fatal(err)
					}
				}
				ids = append(ids, tx.ID)
			}
		}
		return r, eng, ids
	}

	countCalls := func(f *fakeRepo) int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.listStatsCalls
	}

	// Per-transaction promote: the auto-enable check runs on every transaction.
	rOld, engOld, idsOld := build()
	for _, id := range idsOld {
		if err := engOld.PromoteTransactionBaselines(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	oldCalls := countCalls(rOld)

	// Bulk promote: the auto-enable check runs once per service.
	rNew, engNew, idsNew := build()
	done, failed, err := engNew.PromoteTransactionsBaselines(context.Background(), idsNew)
	if err != nil {
		t.Fatal(err)
	}
	if done != len(idsNew) || failed != 0 {
		t.Fatalf("done=%d failed=%d, want done=%d failed=0", done, failed, len(idsNew))
	}
	newCalls := countCalls(rNew)

	// Two services, each checked once (readiness read + allowlist-seed read =
	// two reads per service) regardless of transaction count.
	if newCalls != 4 {
		t.Fatalf("bulk promote made %d ListTransactionStats calls, want 4 (2 per service)", newCalls)
	}
	if oldCalls <= newCalls {
		t.Fatalf("per-transaction path made %d calls vs deduped %d; dedupe must reduce the call count", oldCalls, newCalls)
	}

	// Behavior parity: both services still end up auto-protected.
	for _, service := range []string{"geolocation", "checkout"} {
		g, err := engNew.loadOrInitServiceGuardrail(context.Background(), repo.ServiceScopeKey("default", service))
		if err != nil {
			t.Fatal(err)
		}
		if !InventoryProtected(g) {
			t.Fatalf("service default/%s should be auto-protected after bulk promote", service)
		}
	}
}

func TestForcePromoteServiceAutoEnablesDespiteStalePromotedFlag(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, nil, nil)
	eng.SetGuardrailConfig(guardrailConfigFrom())
	eng.SetSystemConfig(newSystemConfig(repo.SystemSettings{AutoTransactionGuardrail: true}))

	tx, err := r.UpsertTransaction(context.Background(), "geolocation", "default", "GET /geo", repo.KindHTTP, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, class := range ScoredClasses() {
		if err := r.UpsertBaselineClass(context.Background(), repo.BaselineClass{
			TransactionID: tx.ID, Class: class, Data: json.RawMessage(`{}`), Promoted: false,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Simulate a stale read: promote writes succeeded but ListTransactionStats
	// still reports Promoted=false. treatAsPromoted must still enable the rule.
	if err := eng.maybeAutoEnableTransactionGuardrail(context.Background(), "default", "geolocation"); err != nil {
		t.Fatal(err)
	}
	g, _ := eng.loadOrInitServiceGuardrail(context.Background(), "default/geolocation")
	if InventoryProtected(g) {
		t.Fatal("should not enable while DB still shows unpromoted and no treatAsPromoted")
	}
	if err := eng.maybeAutoEnableTransactionGuardrail(context.Background(), "default", "geolocation", tx.ID); err != nil {
		t.Fatal(err)
	}
	g, _ = eng.loadOrInitServiceGuardrail(context.Background(), "default/geolocation")
	if !InventoryProtected(g) {
		t.Fatal("treatAsPromoted should enable despite stale Promoted=false")
	}
}

func TestPromoteBaselineClassAutoEnablesWhenLastClassPromotes(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, nil, nil)
	eng.SetGuardrailConfig(guardrailConfigFrom())
	eng.SetSystemConfig(newSystemConfig(repo.SystemSettings{AutoTransactionGuardrail: true}))

	tx, err := r.UpsertTransaction(context.Background(), "pricing", "default", "GET Price", repo.KindHTTP, nil)
	if err != nil {
		t.Fatal(err)
	}
	classes := ScoredClasses()
	for i, class := range classes {
		if err := r.UpsertBaselineClass(context.Background(), repo.BaselineClass{
			TransactionID: tx.ID, Class: class, Data: json.RawMessage(`{}`),
			Promoted: i < len(classes)-1, // last class still learning
		}); err != nil {
			t.Fatal(err)
		}
	}
	last := classes[len(classes)-1]
	if err := eng.PromoteBaselineClass(context.Background(), tx.ID, last); err != nil {
		t.Fatal(err)
	}
	g, err := eng.loadOrInitServiceGuardrail(context.Background(), "default/pricing")
	if err != nil {
		t.Fatal(err)
	}
	if !InventoryProtected(g) {
		t.Fatal("promoting the last class should auto-enable the transaction guardrail")
	}
}

func TestDeleteAllTransactionsClearsInventoryGuardrails(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, nil, nil)
	eng.SetGuardrailConfig(guardrailConfigFrom())

	scopeKey := "ns/shop"
	g := repo.ServiceGuardrail{
		Scope:    repo.ScopeService,
		ScopeKey: scopeKey,
		RuleLists: map[repo.RuleKey][]string{
			risk.RuleAllowedTransactions: {"HTTP\tGET /old"},
			risk.RuleAllowedCallees:      {"pricing"},
		},
		RuleModes: map[repo.RuleKey]repo.RuleMode{risk.RuleAllowedTransactions: repo.RuleModeEnforce},
		RuleOrigins: map[repo.RuleKey]string{
			risk.RuleAllowedTransactions: repo.GuardrailOriginAutoTransactionGuardrail,
		},
		RuleFingerprints: map[repo.RuleKey]string{risk.RuleAllowedTransactions: defaultIdentityFingerprint()},
	}
	if err := r.UpsertServiceGuardrail(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	eng.SetGuardrailConfig(guardrailConfigFrom(g))

	if _, err := r.UpsertTransaction(context.Background(), "shop", "ns", "GET /old", repo.KindHTTP, nil); err != nil {
		t.Fatal(err)
	}
	if err := eng.DeleteAllTransactions(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := eng.loadOrInitServiceGuardrail(context.Background(), scopeKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.RuleLists[risk.RuleAllowedTransactions]; ok {
		t.Fatalf("allowed_transactions list should be cleared, got %v", got.RuleLists)
	}
	if _, ok := got.RuleModes[risk.RuleAllowedTransactions]; ok {
		t.Fatalf("allowed_transactions mode should be cleared, got %v", got.RuleModes)
	}
	if _, ok := got.RuleOrigins[risk.RuleAllowedTransactions]; ok {
		t.Fatalf("allowed_transactions origin should be cleared, got %v", got.RuleOrigins)
	}
	if _, ok := got.RuleFingerprints[risk.RuleAllowedTransactions]; ok {
		t.Fatalf("allowed_transactions fingerprint should be cleared, got %v", got.RuleFingerprints)
	}
	if got.RuleLists[risk.RuleAllowedCallees][0] != "pricing" {
		t.Fatalf("allowed_callees should be preserved, got %v", got.RuleLists[risk.RuleAllowedCallees])
	}
}

func TestClearInventoryGuardrailRulePreservesOptOut(t *testing.T) {
	g := repo.ServiceGuardrail{
		RuleLists: map[repo.RuleKey][]string{risk.RuleAllowedTransactions: {"HTTP\tGET /stale"}},
		RuleModes: map[repo.RuleKey]repo.RuleMode{risk.RuleAllowedTransactions: repo.RuleModeOff},
		RuleOrigins: map[repo.RuleKey]string{
			risk.RuleAllowedTransactions: repo.GuardrailOriginAutoTransactionGuardrail,
		},
		RuleFingerprints: map[repo.RuleKey]string{risk.RuleAllowedTransactions: defaultIdentityFingerprint()},
	}
	if !clearInventoryGuardrailRule(&g) {
		t.Fatal("expected change when clearing stale list under opt-out")
	}
	if g.RuleModes[risk.RuleAllowedTransactions] != repo.RuleModeOff {
		t.Fatalf("opt-out mode should remain off, got %v", g.RuleModes[risk.RuleAllowedTransactions])
	}
	if _, ok := g.RuleLists[risk.RuleAllowedTransactions]; ok {
		t.Fatal("stale allowlist should be cleared under opt-out")
	}
	if _, ok := g.RuleOrigins[risk.RuleAllowedTransactions]; ok {
		t.Fatal("origin should be cleared under opt-out")
	}
	if _, ok := g.RuleFingerprints[risk.RuleAllowedTransactions]; ok {
		t.Fatal("fingerprint should be cleared under opt-out")
	}

	onlyOff := repo.ServiceGuardrail{
		RuleModes: map[repo.RuleKey]repo.RuleMode{risk.RuleAllowedTransactions: repo.RuleModeOff},
	}
	if clearInventoryGuardrailRule(&onlyOff) {
		t.Fatal("opt-out with no stale list/origin should be unchanged")
	}
}

func TestAllowedTransactionsWeightMedium(t *testing.T) {
	rule, ok := risk.GuardrailRuleByKey(risk.RuleAllowedTransactions)
	if !ok {
		t.Fatal("missing catalog entry")
	}
	if rule.Weight != 40 {
		t.Fatalf("weight = %d, want 40", rule.Weight)
	}
	score := risk.Assess([]risk.Signal{risk.GuardrailSignal(rule, "")}).Score
	if risk.SeverityForScore(score) != risk.SeverityMedium {
		t.Fatalf("severity = %s for score %d, want medium", risk.SeverityForScore(score), score)
	}
}
