package notify

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// memRepo is the slice of repo.Repository the dispatcher touches, in memory.
type memRepo struct {
	repo.Repository // nil: any untouched method panics, which is the point

	mu           sync.Mutex
	integrations []repo.Integration
	policies     []repo.Policy
	guardrails   []repo.ServiceGuardrail
	anomalies    []repo.AnomalyIssue
	violations   []repo.GuardrailViolationGroup
	ledger       map[string]repo.NotificationDispatch // integration|key
	attempts     []repo.NotificationAttempt
	cursors      map[string]repo.NotificationCursor
}

func newMemRepo() *memRepo {
	return &memRepo{ledger: map[string]repo.NotificationDispatch{}, cursors: map[string]repo.NotificationCursor{}}
}

func (m *memRepo) ListIntegrations(context.Context) ([]repo.Integration, error) {
	return m.integrations, nil
}
func (m *memRepo) ListEnabledPolicies(context.Context) ([]repo.Policy, error) { return m.policies, nil }
func (m *memRepo) ListServiceGuardrails(context.Context) ([]repo.ServiceGuardrail, error) {
	return m.guardrails, nil
}
func (m *memRepo) ListAnomalyIssues(context.Context, repo.FindingListFilter) ([]repo.AnomalyIssue, error) {
	return m.anomalies, nil
}
func (m *memRepo) ListGuardrailViolations(context.Context, repo.FindingListFilter) ([]repo.GuardrailViolationGroup, error) {
	return m.violations, nil
}
func (m *memRepo) GetAnomalyIssue(_ context.Context, txn int64, sig string) (repo.AnomalyIssue, bool, error) {
	for _, a := range m.anomalies {
		if a.TransactionID == txn && a.Signature == sig {
			return a, true, nil
		}
	}
	return repo.AnomalyIssue{}, false, nil
}
func (m *memRepo) GetGuardrailViolation(_ context.Context, scope string, rule repo.RuleKey, off string) (repo.GuardrailViolationGroup, bool, error) {
	for _, g := range m.violations {
		if g.ScopeKey == scope && g.RuleKey == rule && g.Offending == off {
			return g, true, nil
		}
	}
	return repo.GuardrailViolationGroup{}, false, nil
}
func (m *memRepo) ListNotificationDispatch(_ context.Context, f repo.DispatchListFilter) ([]repo.NotificationDispatch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []repo.NotificationDispatch
	for _, d := range m.ledger {
		if d.IntegrationID != f.IntegrationID {
			continue
		}
		if f.State != "" && d.State != f.State {
			continue
		}
		if !f.DueBefore.IsZero() && d.NextAttemptAt.After(f.DueBefore) {
			continue
		}
		if len(f.FindingKeys) > 0 {
			found := false
			for _, k := range f.FindingKeys {
				if k == d.FindingKey {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		out = append(out, d)
	}
	return out, nil
}
func (m *memRepo) UpsertNotificationDispatch(_ context.Context, rows []repo.NotificationDispatch) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range rows {
		m.ledger[r.IntegrationID+"|"+r.FindingKey] = r
	}
	return nil
}
func (m *memRepo) RecordNotificationAttempts(_ context.Context, a []repo.NotificationAttempt) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attempts = append(m.attempts, a...)
	return nil
}
func (m *memRepo) GetNotificationCursor(_ context.Context, id string) (repo.NotificationCursor, bool, error) {
	c, ok := m.cursors[id]
	return c, ok, nil
}
func (m *memRepo) SetNotificationCursor(_ context.Context, c repo.NotificationCursor) error {
	m.cursors[c.IntegrationID] = c
	return nil
}

func (m *memRepo) row(integration, key string) repo.NotificationDispatch {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ledger[integration+"|"+key]
}

// fakeSink records deliveries and answers with a scripted error.
type fakeSink struct {
	mu    sync.Mutex
	calls [][]Event
	err   *DeliveryError
}

func (f *fakeSink) Deliver(_ context.Context, events []Event, _ ExternalIDHint) []DeliveryResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]Event(nil), events...))
	status := 200
	if f.err != nil {
		status = f.err.HTTPStatus
	}
	return AllResults(events, status, f.err)
}
func (f *fakeSink) Test(context.Context) error { return nil }

func (f *fakeSink) sent() []Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Event
	for _, c := range f.calls {
		out = append(out, c...)
	}
	return out
}

func (f *fakeSink) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

type fixture struct {
	repo *memRepo
	sink *fakeSink
	d    *Dispatcher
	now  time.Time
}

func newFixture(t *testing.T, caps Capabilities, filters repo.IntegrationFilters) *fixture {
	t.Helper()
	sink := &fakeSink{}
	reg := NewRegistry(DefaultDeps())
	reg.Register(Descriptor{Type: "fake", Name: "Fake", Capabilities: caps}, func(Config, Deps) (Sink, error) { return sink, nil })
	r := newMemRepo()
	r.integrations = []repo.Integration{{ID: "int-1", Name: "Fake", Type: "fake", Enabled: true, Config: json.RawMessage(`{}`), Filters: filters}}
	r.policies = []repo.Policy{{ID: 1, Scope: repo.ScopeGlobal, IntegrationIDs: []string{"int-1"}}}
	r.anomalies = []repo.AnomalyIssue{{
		TransactionID: 42, Signature: "s", Service: "svc", Namespace: "ns", Operation: "GET /x",
		PolicyID: 1, Occurrences: 2, MaxScore: 60, Status: repo.AnomalyIssueOpen,
		FirstSeen: time.Now().Add(-time.Minute), LastSeen: time.Now(),
	}}
	f := &fixture{repo: r, sink: sink, now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	f.d = NewDispatcher(r, reg, nil, "pod-a", PollInterval, DefaultOverlap)
	f.d.now = func() time.Time { return f.now }
	return f
}

func (f *fixture) tick(t *testing.T) {
	t.Helper()
	f.now = f.now.Add(PollInterval)
	if err := f.d.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
}

const anomalyKey = "anomaly:42:s"

func TestDispatcher_NewFindingFiresOnce(t *testing.T) {
	f := newFixture(t, Capabilities{}, repo.IntegrationFilters{})
	f.tick(t)
	sent := f.sink.sent()
	if len(sent) != 1 || sent[0].Action != repo.DispatchFiring || sent[0].Key != anomalyKey {
		t.Fatalf("first tick sent %+v", sent)
	}
	row := f.repo.row("int-1", anomalyKey)
	if row.State != repo.DispatchSent || row.SentAt.IsZero() || row.Attempts != 0 {
		t.Fatalf("ledger row = %+v", row)
	}
	if c := f.repo.cursors["int-1"]; c.Leader != "pod-a" || !c.Cursor.Equal(f.now) {
		t.Fatalf("cursor = %+v", c)
	}

	f.sink.reset()
	f.tick(t)
	if sent := f.sink.sent(); len(sent) != 0 {
		t.Fatalf("non-heartbeat sink re-sent an unchanged finding: %+v", sent)
	}
}

func TestDispatcher_HeartbeatResendsWhileActive(t *testing.T) {
	f := newFixture(t, Capabilities{NeedsHeartbeat: true, SupportsResolve: true}, repo.IntegrationFilters{})
	f.tick(t)
	f.tick(t)
	if sent := f.sink.sent(); len(sent) != 2 {
		t.Fatalf("heartbeat sink should re-send each tick, got %d", len(sent))
	}
}

func TestDispatcher_ResolveSentOnceForCapableBackend(t *testing.T) {
	f := newFixture(t, Capabilities{SupportsResolve: true}, repo.IntegrationFilters{})
	f.tick(t)
	f.sink.reset()

	f.repo.anomalies[0].Status = repo.AnomalyIssueAccepted
	f.tick(t)
	sent := f.sink.sent()
	if len(sent) != 1 || sent[0].Action != repo.DispatchResolved || sent[0].Status != "accepted" {
		t.Fatalf("resolve tick sent %+v", sent)
	}
	f.sink.reset()
	f.tick(t)
	if sent := f.sink.sent(); len(sent) != 0 {
		t.Fatalf("resolve sent twice: %+v", sent)
	}

	// Reopen fires again.
	f.repo.anomalies[0].Status = repo.AnomalyIssueOpen
	f.tick(t)
	if sent := f.sink.sent(); len(sent) != 1 || sent[0].Action != repo.DispatchFiring {
		t.Fatalf("reopen should fire: %+v", sent)
	}
}

func TestDispatcher_NoResolveWithoutCapabilityUnlessOptedIn(t *testing.T) {
	f := newFixture(t, Capabilities{}, repo.IntegrationFilters{})
	f.tick(t)
	f.sink.reset()
	f.repo.anomalies[0].Status = repo.AnomalyIssueDismissed
	f.tick(t)
	if sent := f.sink.sent(); len(sent) != 0 {
		t.Fatalf("fire-and-forget sink got a resolve without opt-in: %+v", sent)
	}

	g := newFixture(t, Capabilities{}, repo.IntegrationFilters{NotifyOnResolve: true})
	g.tick(t)
	g.sink.reset()
	g.repo.anomalies[0].Status = repo.AnomalyIssueDismissed
	g.tick(t)
	if sent := g.sink.sent(); len(sent) != 1 || sent[0].Action != repo.DispatchResolved {
		t.Fatalf("opted-in sink should get a resolve: %+v", sent)
	}
}

func TestDispatcher_RetryableFailureBacksOffThenDies(t *testing.T) {
	f := newFixture(t, Capabilities{}, repo.IntegrationFilters{})
	f.sink.err = &DeliveryError{Retryable: true, HTTPStatus: 503, Msg: "unavailable"}

	f.tick(t)
	row := f.repo.row("int-1", anomalyKey)
	if row.State != repo.DispatchFailed || row.Attempts != 1 || !row.NextAttemptAt.Equal(f.now.Add(Backoff(1))) {
		t.Fatalf("after first failure: %+v", row)
	}
	if len(f.repo.attempts) != 1 || f.repo.attempts[0].Outcome != repo.AttemptError || f.repo.attempts[0].HTTPStatus != 503 {
		t.Fatalf("attempts = %+v", f.repo.attempts)
	}

	// Not due yet on the next tick when backoff exceeds poll (attempt 2 waits 30s).
	f.tick(t) // due: 15s backoff passed → attempt 2, backoff 30s
	f.sink.reset()
	f.tick(t) // 15s later: not due
	if sent := f.sink.sent(); len(sent) != 0 {
		t.Fatalf("retried before backoff elapsed: %+v", sent)
	}

	// Drive until dead.
	for i := 0; i < 400 && f.repo.row("int-1", anomalyKey).State != repo.DispatchDead; i++ {
		f.tick(t)
	}
	row = f.repo.row("int-1", anomalyKey)
	if row.State != repo.DispatchDead || row.Attempts != MaxAttempts {
		t.Fatalf("expected dead after %d attempts: %+v", MaxAttempts, row)
	}
	last := f.repo.attempts[len(f.repo.attempts)-1]
	if last.Outcome != repo.AttemptDead || last.Attempt != MaxAttempts {
		t.Fatalf("last attempt = %+v", last)
	}

	// Dead rows are left alone until re-armed.
	f.sink.reset()
	f.tick(t)
	if sent := f.sink.sent(); len(sent) != 0 {
		t.Fatalf("dead row was retried: %+v", sent)
	}

	// Re-arm (what ResetNotificationDispatch does) and recover.
	row.State = repo.DispatchPending
	row.Attempts = 0
	_ = f.repo.UpsertNotificationDispatch(context.Background(), []repo.NotificationDispatch{row})
	f.sink.err = nil
	f.tick(t)
	if got := f.repo.row("int-1", anomalyKey); got.State != repo.DispatchSent || got.Attempts != 0 {
		t.Fatalf("after re-arm: %+v", got)
	}
}

func TestDispatcher_PermanentFailureIsDeadImmediately(t *testing.T) {
	f := newFixture(t, Capabilities{}, repo.IntegrationFilters{})
	f.sink.err = &DeliveryError{Retryable: false, HTTPStatus: 401, Msg: "unauthorized"}
	f.tick(t)
	row := f.repo.row("int-1", anomalyKey)
	if row.State != repo.DispatchDead || row.LastError == "" {
		t.Fatalf("row = %+v", row)
	}
}

func TestDispatcher_RoutingAndFilters(t *testing.T) {
	f := newFixture(t, Capabilities{}, repo.IntegrationFilters{MinSeverity: "critical"})
	f.tick(t)
	if sent := f.sink.sent(); len(sent) != 0 {
		t.Fatalf("high-severity finding passed a critical filter: %+v", sent)
	}

	g := newFixture(t, Capabilities{}, repo.IntegrationFilters{Kinds: []string{"violation"}})
	g.tick(t)
	if sent := g.sink.sent(); len(sent) != 0 {
		t.Fatalf("anomaly passed a violation-only filter: %+v", sent)
	}

	h := newFixture(t, Capabilities{}, repo.IntegrationFilters{})
	h.repo.policies = []repo.Policy{
		{ID: 1, Scope: repo.ScopeGlobal, IntegrationIDs: []string{"int-1"}},
		{ID: 7, Scope: repo.ScopeService, ScopeKey: "ns/svc", IntegrationIDs: []string{"other"}},
	}
	h.repo.anomalies[0].PolicyID = 7
	h.tick(t)
	if sent := h.sink.sent(); len(sent) != 0 {
		t.Fatalf("anomaly under a policy routed elsewhere was delivered: %+v", sent)
	}

	// Violations route by guardrail scope, falling back to the global policy.
	v := newFixture(t, Capabilities{}, repo.IntegrationFilters{})
	v.repo.anomalies = nil
	v.repo.violations = []repo.GuardrailViolationGroup{{
		Service: "svc", Namespace: "ns", ScopeKey: "ns/svc", RuleKey: "allowed_callers", Offending: "ns/bad",
		Occurrences: 1, MaxScore: 50, Status: repo.GuardrailViolationOpen, LastSeen: time.Now(),
	}}
	v.tick(t)
	if sent := v.sink.sent(); len(sent) != 1 || sent[0].Kind != KindViolation {
		t.Fatalf("violation not delivered via global route: %+v", sent)
	}
	v.repo.guardrails = []repo.ServiceGuardrail{{Scope: repo.ScopeService, ScopeKey: "ns/svc", IntegrationIDs: []string{"other"}}}
	v.repo.violations[0].Offending = "ns/bad2"
	v.sink.reset()
	v.tick(t)
	if sent := v.sink.sent(); len(sent) != 0 {
		t.Fatalf("violation routed to another integration was delivered: %+v", sent)
	}
}

func TestDispatcher_DisabledIntegrationIsSkipped(t *testing.T) {
	f := newFixture(t, Capabilities{}, repo.IntegrationFilters{})
	f.repo.integrations[0].Enabled = false
	f.tick(t)
	if sent := f.sink.sent(); len(sent) != 0 {
		t.Fatalf("disabled integration got deliveries: %+v", sent)
	}
}

func TestDispatcher_BatchesByCapability(t *testing.T) {
	f := newFixture(t, Capabilities{MaxBatch: 2}, repo.IntegrationFilters{})
	for i := int64(1); i <= 4; i++ {
		f.repo.anomalies = append(f.repo.anomalies, repo.AnomalyIssue{
			TransactionID: 100 + i, Signature: "s", PolicyID: 1, Occurrences: 1, MaxScore: 60, Status: repo.AnomalyIssueOpen, LastSeen: time.Now(),
		})
	}
	f.tick(t)
	if len(f.sink.calls) != 3 {
		t.Fatalf("5 events with MaxBatch=2 should take 3 calls, got %d", len(f.sink.calls))
	}
	for _, c := range f.sink.calls {
		if len(c) > 2 {
			t.Fatalf("batch exceeded MaxBatch: %d", len(c))
		}
	}
}

func TestDispatcher_InvalidConfigRecordsAttempt(t *testing.T) {
	f := newFixture(t, Capabilities{}, repo.IntegrationFilters{})
	f.repo.integrations[0].Type = "nope"
	f.tick(t)
	if len(f.repo.attempts) != 1 || f.repo.attempts[0].Outcome != repo.AttemptError {
		t.Fatalf("attempts = %+v", f.repo.attempts)
	}
}
