package interrogation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/platform"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"

	"github.com/odigos-io/odigos/insights/internal/autopilot/loop"
	apstore "github.com/odigos-io/odigos/insights/internal/autopilot/store"
	"github.com/odigos-io/odigos/insights/internal/db/postgres"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

var _ Runs = (repo.Repository)(nil)

// One case per row of the supervisor's decision table, and the backoff that
// holds every message back.
func TestNext(t *testing.T) {
	now := testStart.Add(10 * time.Minute)
	started := runHead{Graph: GraphVersion, StartedAt: testStart, DeadlineAt: testStart.Add(time.Hour)}
	parked := func() runHead {
		h := started
		h.Wait = &Wait{Node: "wait_observe", Until: now.Add(time.Minute), InterruptID: "wait_observe-3"}
		return h
	}
	stop := &Stop{Changes: ChangesKeep, Actor: "alice", At: now.Add(-time.Minute)}
	run := func(edit func(*openRun)) openRun {
		r := openRun{id: "run", updated: now, limit: 3600, run: started}
		if edit != nil {
			edit(&r)
		}
		return r
	}
	tests := []struct {
		name   string
		run    openRun
		flight *activeRun
		retry  *retry
		want   actionKind
		closed Closed
	}{
		{name: "in flight, stop not applied", run: run(func(r *openRun) { r.ctl.Stop = stop }), flight: &activeRun{}, want: actCancel},
		{name: "in flight, revert over an applied keep", flight: &activeRun{}, want: actCancel, run: run(func(r *openRun) {
			r.ctl.Stop = &Stop{Changes: ChangesRevert, At: now}
			r.run.Stop = &Stop{Changes: ChangesKeep, At: now}
		})},
		{name: "in flight, carrying the stop", run: run(func(r *openRun) { r.ctl.Stop = stop }), flight: &activeRun{stop: stop}, want: actNone},
		{name: "in flight, carrying a keep, revert recorded since", flight: &activeRun{stop: stop}, want: actCancel, run: run(func(r *openRun) {
			r.ctl.Stop = &Stop{Changes: ChangesRevert, Actor: "alice", At: now}
		})},
		{name: "in flight, stop applied", flight: &activeRun{}, want: actNone, run: run(func(r *openRun) {
			r.ctl.Stop, r.run.Stop = stop, stop
		})},
		{name: "in flight", run: run(nil), flight: &activeRun{}, want: actNone},
		{name: "queued", run: openRun{id: "run", queued: true, limit: 3600}, want: actStart},
		{name: "newer state version", run: run(func(r *openRun) { r.unreadable = "state version 2, this server reads 1" }),
			want: actClose, closed: Closed{Reason: repo.InterrogationInterrupted, Error: "state version 2, this server reads 1", At: now}},
		{name: "unreadable and overdue", run: run(func(r *openRun) { r.limit = 300; r.unreadable = "state unreadable: bad wait" }),
			want: actClose, closed: Closed{Reason: repo.InterrogationInterrupted, Error: "state unreadable: bad wait", At: now}},
		{name: "other graph version, parked", run: run(func(r *openRun) { r.run = parked(); r.run.Graph = GraphVersion + 1; r.run.Wait.Until = now }), want: actFail},
		{name: "no graph version yet", run: run(func(r *openRun) { r.run.Graph = 0; r.updated = now.Add(-resumeAfter) }), want: actResume},
		{name: "overdue after the deadline", want: actClose,
			run:    run(func(r *openRun) { r.limit = 300; r.run.LastError = "gate: connection refused" }),
			closed: Closed{Reason: repo.InterrogationInterrupted, Error: "did not finish by its deadline: gate: connection refused", At: now}},
		{name: "overdue deadline counts the recorded extension", want: actNone,
			run: run(func(r *openRun) { r.limit, r.ctl.ExtendSeconds = 300, 600; r.updated = now }),
		},
		{name: "overdue after the stop", want: actClose,
			run:    run(func(r *openRun) { r.ctl.Stop = &Stop{Changes: ChangesRevert, Actor: "alice", At: now.Add(-closeGrace)} }),
			closed: Closed{Reason: repo.InterrogationStoppedByUser, Actor: "alice", Changes: ChangesRevert, At: now}},
		{name: "overdue and in flight", run: run(func(r *openRun) { r.limit = 300 }), flight: &activeRun{}, want: actCancel},
		{name: "parked, due", run: run(func(r *openRun) { r.run = parked(); r.run.Wait.Until = now }), want: actAnswer},
		{name: "parked, stop to deliver", run: run(func(r *openRun) { r.run = parked(); r.ctl.Stop = stop }), want: actAnswer},
		{name: "parked, extension to deliver", run: run(func(r *openRun) { r.run = parked(); r.ctl.ExtendSeconds = 60 }), want: actAnswer},
		{name: "parked, nothing to deliver", run: run(func(r *openRun) { r.run = parked() }), want: actNone},
		{name: "parked, its answer matched nothing", run: run(func(r *openRun) { r.run = parked() }), retry: &retry{at: now, unmatched: "wait_observe-3"}, want: actResume},
		{name: "parked, an older answer matched nothing", run: run(func(r *openRun) { r.run = parked() }), retry: &retry{at: now, unmatched: "wait_baseline-1"}, want: actNone},
		{name: "not parked, updated 20 s ago", run: run(func(r *openRun) { r.updated = now.Add(-resumeAfter) }), want: actResume},
		{name: "not parked, updated recently", run: run(func(r *openRun) { r.updated = now.Add(-time.Second) }), want: actNone},
		{name: "not parked, this process saw it end", run: run(nil), retry: &retry{at: now}, want: actResume},
		{name: "backoff holds a resume", run: run(nil), retry: &retry{at: now.Add(time.Second)}, want: actNone},
		{name: "backoff holds an answer", run: run(func(r *openRun) { r.run = parked(); r.run.Wait.Until = now }), retry: &retry{at: now.Add(time.Second)}, want: actNone},
		{name: "backoff holds a pickup", run: openRun{id: "run", queued: true}, retry: &retry{at: now.Add(time.Second)}, want: actNone},
		{name: "backoff does not hold a close", run: run(func(r *openRun) { r.limit = 300 }), retry: &retry{at: now.Add(time.Minute), lastErr: "model call: 502"},
			want: actClose, closed: Closed{Reason: repo.InterrogationInterrupted, Error: "did not finish by its deadline: model call: 502", At: now}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := next(tc.run, tc.flight, tc.retry, now)
			if got.kind != tc.want || got.closed != tc.closed {
				t.Errorf("next = %+v, want kind %d closed %+v", got, tc.want, tc.closed)
			}
		})
	}
}

// A run whose request or control another version wrote in a shape this one
// cannot decode is closed, not skipped while it holds its workload.
func TestReadOpenRun_UndecodableIsClosed(t *testing.T) {
	now := testStart.Add(10 * time.Hour)
	runState := `{"v":1,"graph":1,"started_at":"` + testStart.Format(time.RFC3339Nano) + `"}`
	for name, o := range map[string]repo.OpenInterrogation{
		"request": {ID: "run", UpdateTime: testStart, State: []byte(`{"open":true,"request":{"budget":{"time_limit_seconds":"3600"}},"run":` + runState + `}`)},
		"control": {ID: "run", UpdateTime: testStart, State: []byte(`{"open":true,"request":{},"run":` + runState + `}`), Control: []byte(`{"stop":"yes"}`)},
	} {
		t.Run(name, func(t *testing.T) {
			r, err := readOpenRun(o)
			if err != nil {
				t.Fatalf("readOpenRun: %v", err)
			}
			if got := next(r, nil, nil, now); got.kind != actClose || got.closed.Reason != repo.InterrogationInterrupted {
				t.Errorf("next = %+v, want a close as interrupted", got)
			}
		})
	}
}

// pgLoop stands in for *loop.Manager on a test database. RequestProbe stores
// the row through the autopilot store as the real one does after its checks.
type pgLoop struct {
	*loop.Manager
	probes *apstore.Client
	clock  *testClock

	mu        sync.Mutex
	calls     map[string]int // Instrumented calls by workload name
	requests  int
	onInstr   func(workload string, n int)
	onRequest func()
}

func (l *pgLoop) Instrumented(_ context.Context, source apstore.SourceKey) (bool, error) {
	l.mu.Lock()
	l.calls[source.WorkloadName]++
	n, hook := l.calls[source.WorkloadName], l.onInstr
	l.mu.Unlock()
	if hook != nil {
		hook(source.WorkloadName, n)
	}
	return true, nil
}

func (l *pgLoop) instrumented(workload string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls[workload]
}

func (l *pgLoop) ProbesInEffect(context.Context, apstore.SourceKey) ([]loop.CustomProbe, error) {
	return nil, nil
}

func (l *pgLoop) RequestProbe(ctx context.Context, req loop.ProbeRequest) (apstore.RequestedProbe, error) {
	rec := req.Recommendation
	target := loop.ProbeTarget(rec)
	id := loop.ChangeID(req.InterrogationID, rec.Language, target)
	now := l.clock.Now()
	if prev, ok, err := l.probes.GetRequestedProbe(ctx, id); err != nil || ok && prev.Live(now) {
		return prev, err
	}
	row, err := l.probes.InsertRequestedProbe(ctx, apstore.RequestedProbe{
		Recommendation: apstore.Recommendation{
			Namespace: req.Source.Namespace, WorkloadKind: req.Source.WorkloadKind, WorkloadName: req.Source.WorkloadName,
			ContainerName: req.Transactions[0].ContainerName, TransactionID: req.Transactions[0].TransactionID,
			Language: rec.Language, Target: target, Function: target, ClassName: rec.Probe.ClassName, MethodName: rec.Probe.MethodName,
		},
		ChangeID:        id,
		InterrogationID: req.InterrogationID,
		Round:           req.Round,
	}, req.RunMaxProbes, now)
	l.mu.Lock()
	l.requests++
	hook := l.onRequest
	l.mu.Unlock()
	if err == nil && hook != nil {
		hook()
	}
	return row, err
}

// roundModel answers by the round the request names, so a call lost to a
// killed leader does not shift the script: a probe in round 1, done after.
type roundModel struct {
	clock  *testClock
	t      *testing.T
	onCall func(ctx context.Context, n int) error

	mu    sync.Mutex
	calls int
}

func (m *roundModel) Name() string { return "round" }

func (m *roundModel) GenerateContent(ctx context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.mu.Lock()
		n := m.calls
		m.calls++
		hook := m.onCall
		m.mu.Unlock()
		if hook != nil {
			if err := hook(ctx, n); err != nil {
				yield(nil, err)
				return
			}
		}
		if err := ctx.Err(); err != nil {
			yield(nil, err)
			return
		}
		var r Request
		_ = json.Unmarshal([]byte(req.Contents[len(req.Contents)-1].Parts[0].Text), &r)
		edit := done(80)
		if r.Round == 1 {
			edit = nil
		}
		m.clock.Advance(20 * time.Second)
		yield(&model.LLMResponse{
			Content:      genai.NewContentFromText(replyJSON(m.t, edit).text, genai.RoleModel),
			FinishReason: genai.FinishReasonStop,
			TurnComplete: true,
		}, nil)
	}
}

func (m *roundModel) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// hookedService is a leader's session service with hooks around appends:
// before can refuse an append, as a dead process or a lost database would.
type hookedService struct {
	session.Service
	before func(ctx context.Context, ev *session.Event) error
	after  func(ev *session.Event)
}

func (s hookedService) AppendEvent(ctx context.Context, sess session.Session, ev *session.Event) error {
	if s.before != nil {
		if err := s.before(ctx, ev); err != nil {
			return err
		}
	}
	err := s.Service.AppendEvent(ctx, sess, ev)
	if err == nil && s.after != nil {
		s.after(ev)
	}
	return err
}

// eventNode is the graph node that wrote ev, "" for a message.
func eventNode(ev *session.Event) string {
	if ev.NodeInfo == nil || ev.NodeInfo.Path == "" {
		return ""
	}
	return nodeOf(ev.NodeInfo.Path)
}

// pgHarness runs interrogations on a test database with the real session
// store, repo and probe store, the leader's supervisor, and fakes for the
// cluster, the evidence and the model.
type pgHarness struct {
	t      *testing.T
	db     testDB
	api    *Sessions
	repo   *postgres.Repo
	probes *apstore.Client
	clock  *testClock
	// ctx stamps ADK's events with the test clock.
	ctx   context.Context
	model model.LLM
	round *roundModel
	store *fakeStore
	loop  *pgLoop
	id    string
}

func newPGHarness(t *testing.T) *pgHarness {
	t.Helper()
	db := newTestDB(t)
	clock := &testClock{t: time.Now().UTC().Truncate(time.Microsecond)}
	h := &pgHarness{
		t: t, db: db, clock: clock, id: testRunID,
		api:    db.replica(t),
		repo:   openRepo(t, db.dsn),
		probes: openProbeStore(t, db.dsn),
		ctx:    platform.WithTimeProvider(context.Background(), clock.Now),
		store:  newFakeStore(),
	}
	h.loop = &pgLoop{Manager: &loop.Manager{}, probes: h.probes, clock: clock, calls: map[string]int{}}
	h.round = &roundModel{clock: clock, t: t}
	h.model = h.round
	h.store.add(testNamespace, testKind, testWorkload, "app", apstore.Transaction{ID: "8841", RootSpanName: "POST /claims", RootSpanKind: "server"}, testEdges...)
	return h
}

func (h *pgHarness) start() {
	h.t.Helper()
	if err := h.api.Start(h.ctx, h.id, newScript().st.Request); err != nil {
		h.t.Fatal(err)
	}
}

func (h *pgHarness) deps() Deps {
	return Deps{Model: h.model, Loop: h.loop, Store: h.store, Changes: h.repo, MaxModelCalls: maxModelCalls, Now: h.clock.Now}
}

// leader is a new leader on its own session store. wrap, when set, wraps the
// session service its runner writes through.
func (h *pgHarness) leader(wrap func(session.Service) session.Service) *Supervisor {
	h.t.Helper()
	sessions := h.db.replica(h.t)
	s, err := NewSupervisor(SupervisorConfig{Deps: h.deps(), Runs: h.repo, Sessions: sessions})
	if err != nil {
		h.t.Fatal(err)
	}
	if wrap != nil {
		dp := h.deps()
		dp.Control = sessions.Control
		wf, err := BuildWorkflow(dp)
		if err != nil {
			h.t.Fatal(err)
		}
		if s.runner, err = runner.New(runner.Config{AppName: AppName, Agent: wf, SessionService: wrap(sessions.Service())}); err != nil {
			h.t.Fatal(err)
		}
	}
	s.poll = 10 * time.Millisecond
	return s
}

// step is one poll of s and the end of every invocation it started.
func (h *pgHarness) step(ctx context.Context, s *Supervisor) {
	s.pollOnce(ctx)
	for len(s.running) > 0 {
		s.finished(<-s.results)
	}
}

// place puts every approved probe in the rule, as the reconciler would.
func (h *pgHarness) place() {
	h.db.exec(h.t, `UPDATE interrogation_requested_probes SET in_rule = true, applied_at = $1 WHERE state = 'approved' AND NOT in_rule`, h.clock.Now())
}

// state is the run's stored workflow state, nil while queued, and whether
// the run is open.
func (h *pgHarness) state() (*State, bool) {
	h.t.Helper()
	raw := h.db.state(h.t, AppName, h.id)
	open := raw["open"] == true
	if raw["run"] == nil {
		return nil, open
	}
	b, _ := json.Marshal(raw["run"])
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		h.t.Fatal(err)
	}
	return &st, open
}

// drive steps s until stop holds, the run closes or ctx ends, moving the
// clock to the end of each wait and backoff as time would.
func (h *pgHarness) drive(ctx context.Context, s *Supervisor, stop func(*State) bool) *State {
	h.t.Helper()
	for range 100 {
		h.place()
		st, open := h.state()
		if !open || ctx.Err() != nil || st != nil && stop != nil && stop(st) {
			return st
		}
		if st != nil && st.Wait != nil {
			h.clock.Set(st.Wait.Until)
		}
		if rt := s.retries[h.id]; rt != nil {
			h.clock.Set(rt.at)
		}
		h.step(ctx, s)
	}
	st, _ := h.state()
	h.t.Fatalf("run did not end in 100 polls: state %+v, retry %+v", st, s.retries[h.id])
	return nil
}

// leaderLost moves the clock past the time a new leader waits before it
// resumes a run that is not parked.
func (h *pgHarness) leaderLost() { h.clock.Advance(resumeAfter + time.Second) }

func (h *pgHarness) killable() (context.Context, context.CancelFunc) {
	return context.WithCancel(h.ctx)
}

// runOutcome is what an interrupted run must end with to equal an uninterrupted
// one: how it ended, its rounds, its probes and their stored rows.
type runOutcome struct {
	Status    Status
	EndReason EndReason
	Rounds    int
	Probes    []string
	Rows      []string
	Answer    string
}

func (h *pgHarness) outcome(st *State) runOutcome {
	h.t.Helper()
	o := runOutcome{Status: st.Status, EndReason: st.EndReason, Rounds: len(st.Rounds)}
	for _, p := range st.Probes {
		o.Probes = append(o.Probes, p.ChangeID+":"+string(p.Status))
	}
	if st.Answer != nil {
		o.Answer = fmt.Sprintf("%s %d final=%v", st.Answer.Text, st.Answer.Certainty, st.Answer.Final)
	}
	rows, err := h.db.sql.Query(h.ctx, `SELECT change_id || ':' || state || ':' || end_reason FROM interrogation_requested_probes WHERE interrogation_id = $1 ORDER BY change_id`, h.id)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			h.t.Fatal(err)
		}
		o.Rows = append(o.Rows, r)
	}
	return o
}

// sessionEvents are the run's stored ADK events, oldest first.
func (h *pgHarness) sessionEvents() []*session.Event {
	h.t.Helper()
	got, err := h.api.Service().Get(h.ctx, &session.GetRequest{AppName: AppName, UserID: h.id, SessionID: h.id})
	if err != nil {
		h.t.Fatal(err)
	}
	return slices.Collect(got.Session.Events().All())
}

// apiEvents are the run's API events of type typ.
func (h *pgHarness) apiEvents(typ string) []Event {
	h.t.Helper()
	var out []Event
	for _, ev := range h.sessionEvents() {
		raw, ok := ev.CustomMetadata[eventsKey]
		if !ok {
			continue
		}
		b, _ := json.Marshal(raw)
		var items []Event
		if err := json.Unmarshal(b, &items); err != nil {
			h.t.Fatal(err)
		}
		for _, e := range items {
			if e.Type == typ {
				out = append(out, e)
			}
		}
	}
	return out
}

// uninterrupted runs the two-round interrogation with one leader.
func uninterrupted(t *testing.T) (runOutcome, int) {
	h := newPGHarness(t)
	h.start()
	st := h.drive(h.ctx, h.leader(nil), nil)
	return h.outcome(st), h.loop.instrumented(testWorkload)
}

// A new leader finds a run parked by the old one and answers its wait when
// due: no node runs again.
func TestResume_AtWait(t *testing.T) {
	want, wantInstrumented := uninterrupted(t)
	if want.Status != StatusCompleted || want.EndReason != EndDone || want.Rounds != 2 || len(want.Rows) != 1 {
		t.Fatalf("uninterrupted run %+v", want)
	}

	for _, wait := range []string{"wait_baseline", "wait_apply", "wait_observe"} {
		t.Run(wait, func(t *testing.T) {
			h := newPGHarness(t)
			h.start()
			ctx, kill := h.killable()
			h.drive(ctx, h.leader(nil), parkedIn(wait))
			kill()
			calls, requests := h.round.count(), h.loop.requests

			st := h.drive(h.ctx, h.leader(nil), nil)
			if got := h.outcome(st); !equalOutcome(got, want) {
				t.Errorf("outcome %+v, want %+v", got, want)
			}
			if n := h.loop.instrumented(testWorkload); n != wantInstrumented {
				t.Errorf("Instrumented called %d times, want %d", n, wantInstrumented)
			}
			if st.ModelCalls != 2 || h.round.count() != 2 || h.loop.requests != 1 {
				t.Errorf("model calls %d (%d sent, %d before the new leader), probe requests %d (%d before); want 2 and 1",
					st.ModelCalls, h.round.count(), calls, h.loop.requests, requests)
			}
			if n := len(h.apiEvents(EventErrorRaised)); n != 0 {
				t.Errorf("%d error.raised events", n)
			}
		})
	}
}

func equalOutcome(a, b runOutcome) bool {
	return a.Status == b.Status && a.EndReason == b.EndReason && a.Rounds == b.Rounds &&
		slices.Equal(a.Probes, b.Probes) && slices.Equal(a.Rows, b.Rows) && a.Answer == b.Answer
}

// The old leader dies inside a step. The new leader resumes the run from its
// recorded step 20 s after its last update; the step repeats and the run ends
// as an uninterrupted one does, with no probe row twice.
func TestResume_MidStep(t *testing.T) {
	want, _ := uninterrupted(t)

	killed := errors.New("leader killed")
	tests := []struct {
		name string
		// arm sets the kill point up on the old leader; kill ends it.
		arm func(h *pgHarness, kill func()) func(session.Service) session.Service
		// closed is whether the run closed before the kill.
		closed bool
	}{
		{name: "before entry's first event", arm: func(h *pgHarness, kill func()) func(session.Service) session.Service {
			return func(svc session.Service) session.Service {
				return hookedService{Service: svc, before: func(_ context.Context, ev *session.Event) error {
					if eventNode(ev) == nodeEntry {
						kill()
						return killed
					}
					return nil
				}}
			}
		}},
		{name: "during the model call", arm: func(h *pgHarness, kill func()) func(session.Service) session.Service {
			h.round.onCall = func(ctx context.Context, n int) error {
				if n == 0 {
					kill()
					<-ctx.Done()
					return ctx.Err()
				}
				return nil
			}
			return nil
		}},
		{name: "between RequestProbe and its event", arm: func(h *pgHarness, kill func()) func(session.Service) session.Service {
			var once sync.Once
			h.loop.onRequest = func() { once.Do(kill) }
			return nil
		}},
		{name: "during conclude", arm: func(h *pgHarness, kill func()) func(session.Service) session.Service {
			return func(svc session.Service) session.Service {
				return hookedService{Service: svc, before: func(_ context.Context, ev *session.Event) error {
					if eventNode(ev) == "conclude" {
						kill()
						return killed
					}
					return nil
				}}
			}
		}},
		{name: "right after the closing event", closed: true, arm: func(h *pgHarness, kill func()) func(session.Service) session.Service {
			return func(svc session.Service) session.Service {
				return hookedService{Service: svc, after: func(ev *session.Event) {
					if eventNode(ev) == "conclude" {
						kill()
					}
				}}
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newPGHarness(t)
			h.start()
			ctx, kill := h.killable()
			old := h.leader(tc.arm(h, kill))
			for i := 0; ctx.Err() == nil; i++ {
				if i == 100 {
					t.Fatal("the kill point was not reached")
				}
				h.drive(ctx, old, nil)
				if _, open := h.state(); !open && ctx.Err() == nil {
					t.Fatal("the run ended before the kill point")
				}
			}
			if _, open := h.state(); open == tc.closed {
				t.Fatalf("open = %v after the kill, want %v", open, !tc.closed)
			}
			h.round.onCall, h.loop.onRequest = nil, nil

			h.leaderLost()
			st := h.drive(h.ctx, h.leader(nil), nil)
			if got := h.outcome(st); !equalOutcome(got, want) {
				t.Errorf("outcome %+v, want %+v", got, want)
			}
			if n := h.db.count(t, `SELECT count(*) FROM interrogation_requested_probes WHERE interrogation_id = $1`, h.id); n != 1 {
				t.Errorf("%d probe rows, want 1", n)
			}
			if n := len(h.apiEvents(EventEnded)); n != 1 {
				t.Errorf("%d interrogation.ended events, want 1", n)
			}
		})
	}
}

// answersTo counts the stored answers to interrupt and reports whether a
// plain resume follows the last of them.
func (h *pgHarness) answersTo(interrupt string) (answers int, resumedAfter bool) {
	for _, ev := range h.sessionEvents() {
		if ev.Content == nil {
			continue
		}
		for _, p := range ev.Content.Parts {
			switch {
			case p.FunctionResponse != nil && p.FunctionResponse.ID == interrupt:
				answers++
				resumedAfter = false
			case answers > 0 && ev.Author == "user" && p.Text == "resume":
				resumedAfter = true
			}
		}
	}
	return answers, resumedAfter
}

// The answer to a wait is stored and then not acted on: the old leader died
// before the wait node's event, or a second answer comes after the wait has
// passed. Either way the run goes on and no node runs twice.
func TestResume_AnswerConsumed(t *testing.T) {
	// The waits re-run on resume, and ADK retries a re-entry whose run failed
	// with the same answer, so the new leader's answer is taken.
	t.Run("wait node's event lost", func(t *testing.T) {
		h := newPGHarness(t)
		h.start()
		ctx, kill := h.killable()
		answered := false
		old := h.leader(func(svc session.Service) session.Service {
			return hookedService{Service: svc, before: func(_ context.Context, ev *session.Event) error {
				if answered && eventNode(ev) != "" {
					kill()
					return errors.New("leader killed")
				}
				if ev.Content != nil && slices.ContainsFunc(ev.Content.Parts, func(p *genai.Part) bool { return p.FunctionResponse != nil }) {
					answered = true
				}
				return nil
			}}
		})
		st := h.drive(ctx, old, parkedIn("wait_baseline"))
		interrupt := st.Wait.InterruptID
		h.clock.Set(st.Wait.Until)
		h.step(ctx, old)
		if !answered || ctx.Err() == nil {
			t.Fatal("the old leader did not die after storing the answer")
		}
		if st, _ := h.state(); st.Wait == nil || st.Wait.InterruptID != interrupt {
			t.Fatalf("wait %+v after the kill, want still %s", st.Wait, interrupt)
		}

		st = h.drive(h.ctx, h.leader(nil), nil)
		if st.Status != StatusCompleted || st.EndReason != EndDone {
			t.Fatalf("run ended %s/%s (%s)", st.Status, st.EndReason, st.Error)
		}
		if answers, _ := h.answersTo(interrupt); answers != 2 {
			t.Errorf("%d answers to %s, want the old leader's and the new one's", answers, interrupt)
		}
		if n := len(h.apiEvents(EventStarted)); n != 1 || h.round.count() != 2 {
			t.Errorf("%d started events, %d model calls; want 1 and 2", n, h.round.count())
		}
	})

	// A leader that read the run before another moved it on answers a wait
	// the run has left. ADK runs that wait again or matches the answer to no
	// waiting node, depending on how it orders the session's events; either
	// way the run does not change, also at that leader's next poll.
	t.Run("wait already passed", func(t *testing.T) {
		h := newPGHarness(t)
		h.start()
		s := h.leader(nil)
		st := h.drive(h.ctx, s, parkedIn("wait_baseline"))
		passed := *st.Wait
		before := h.drive(h.ctx, s, parkedIn("wait_apply"))

		stale := h.leader(nil)
		stale.launch(h.ctx, openRun{id: h.id, run: runHead{Wait: &passed}}, action{kind: actAnswer})
		res := <-stale.results
		if res.err != nil && !errors.Is(res.err, workflow.ErrNothingToResume) {
			t.Fatalf("invocation: %v", res.err)
		}
		stale.finished(res)
		h.step(h.ctx, stale)
		if answers, _ := h.answersTo(passed.InterruptID); answers != 2 {
			t.Fatalf("%d answers to %s stored, want 2", answers, passed.InterruptID)
		}
		after, open := h.state()
		if !open || after.Seq != before.Seq || after.Wait == nil || *after.Wait != *before.Wait || h.round.count() != 1 {
			t.Fatalf("the stale answer changed the run: seq %d -> %d, wait %+v -> %+v, model calls %d",
				before.Seq, after.Seq, before.Wait, after.Wait, h.round.count())
		}
		st = h.drive(h.ctx, s, nil)
		if st.Status != StatusCompleted || st.EndReason != EndDone || h.round.count() != 2 || h.loop.requests != 1 {
			t.Errorf("run ended %s/%s, %d model calls, %d probe requests; want done, 2, 1", st.Status, st.EndReason, h.round.count(), h.loop.requests)
		}
	})
}

// A stop written by another replica, its clock 2 s ahead, reaches a parked
// run with the answer to its wait and a running step by cancelling it.
func TestStop_FromOtherReplica(t *testing.T) {
	t.Run("parked", func(t *testing.T) {
		h := newPGHarness(t)
		h.start()
		s := h.leader(nil)
		h.drive(h.ctx, s, parkedIn("wait_observe"))
		other := h.db.replica(t)
		ahead := platform.WithTimeProvider(context.Background(), func() time.Time { return h.clock.Now().Add(2 * time.Second) })
		if err := other.Stop(ahead, h.id, Stop{Changes: ChangesRevert, Actor: "alice", At: h.clock.Now()}); err != nil {
			t.Fatal(err)
		}
		h.step(h.ctx, s)
		st, open := h.state()
		if open || st.Status != StatusStopped || st.EndReason != EndStoppedByUser {
			t.Fatalf("after one poll: open %v, %s/%s", open, st.Status, st.EndReason)
		}
		if p := st.Probes; len(p) != 1 || p[0].Status != ProbeReverted {
			t.Errorf("probes %+v, want the probe reverted", p)
		}
		if n := h.db.liveApproved(t, h.id, h.clock.Now()); n != 0 {
			t.Errorf("%d live probes", n)
		}
	})

	t.Run("mid-step", func(t *testing.T) {
		h := newPGHarness(t)
		h.start()
		s := h.leader(nil)
		h.drive(h.ctx, s, parkedIn("wait_baseline"))
		other := h.db.replica(t)
		ahead := platform.WithTimeProvider(context.Background(), func() time.Time { return h.clock.Now().Add(2 * time.Second) })
		h.round.onCall = func(ctx context.Context, n int) error {
			if err := other.Stop(ahead, h.id, Stop{Changes: ChangesKeep, Actor: "alice", At: h.clock.Now()}); err != nil {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(10 * time.Second):
				return errors.New("the step was not cancelled")
			}
		}
		h.clock.Advance(baselineWait)
		ctx, cancel := context.WithCancel(h.ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.Run(ctx)
		}()
		defer func() {
			cancel()
			<-done
		}()
		if !waitFor(t, 10*time.Second, func() bool { _, open := h.state(); return !open }) {
			t.Fatal("the run did not close")
		}
		st, _ := h.state()
		if st.Status != StatusStopped || st.EndReason != EndStoppedByUser || st.Stop == nil || st.Stop.Actor != "alice" {
			t.Fatalf("run ended %s/%s, stop %+v", st.Status, st.EndReason, st.Stop)
		}
		if n := len(h.apiEvents(EventErrorRaised)); n != 0 {
			t.Errorf("the cancelled step counted as %d failures", n)
		}
	})
}

// The graph cannot append any more. closeGrace after the deadline, or after
// the stop, the supervisor closes the run itself and frees the workload.
func TestSupervisor_ClosesOverdueRun(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(fmt.Sprintf("stopped=%v", stopped), func(t *testing.T) {
			h := newPGHarness(t)
			h.start()
			var failing bool
			s := h.leader(func(svc session.Service) session.Service {
				return hookedService{Service: svc, before: func(context.Context, *session.Event) error {
					if failing {
						return errors.New("disk full")
					}
					return nil
				}}
			})
			st := h.drive(h.ctx, s, parkedIn("wait_observe"))
			failing = true
			if stopped {
				if err := h.api.Stop(h.ctx, h.id, Stop{Changes: ChangesRevert, Actor: "alice", At: h.clock.Now()}); err != nil {
					t.Fatal(err)
				}
			} else {
				h.clock.Set(st.DeadlineAt)
			}
			for range 3 {
				h.step(h.ctx, s)
				if rt := s.retries[h.id]; rt != nil {
					h.clock.Set(rt.at)
				}
			}
			if _, open := h.state(); !open {
				t.Fatal("closed before the grace")
			}
			if stopped {
				h.clock.Advance(closeGrace)
			} else {
				h.clock.Set(st.DeadlineAt.Add(closeGrace))
			}
			h.step(h.ctx, s)

			state := h.db.state(t, AppName, h.id)
			closed, _ := state["closed"].(map[string]any)
			switch {
			case state["open"] != false:
				t.Fatalf("still open: %v", state)
			case stopped && (closed["reason"] != repo.InterrogationStoppedByUser || closed["actor"] != "alice" || closed["error"] != ""):
				t.Errorf("closed %v, want stopped_by_user by alice", closed)
			case !stopped && (closed["reason"] != repo.InterrogationInterrupted ||
				!strings.HasPrefix(fmt.Sprint(closed["error"]), "did not finish by its deadline: ") || !strings.HasSuffix(fmt.Sprint(closed["error"]), "disk full")):
				t.Errorf("closed %v, want interrupted with the last error", closed)
			}
			events, _, err := h.repo.ListInterrogationEvents(h.ctx, h.id, 0, 1000)
			if err != nil || !events.Complete {
				t.Errorf("events complete %v (%v)", events.Complete, err)
			}
			in, _, err := h.repo.GetInterrogation(h.ctx, h.id, h.clock.Now())
			if status, _, _ := in.Status(); err != nil || status == repo.InterrogationRunning {
				t.Errorf("status %s (%v)", status, err)
			}
			if stopped {
				if n := h.db.liveApproved(t, h.id, h.clock.Now()); n != 0 {
					t.Errorf("%d live probes after the poll's sweep", n)
				}
			}
			if err := h.api.Start(h.ctx, "next-run", newScript().st.Request); err != nil {
				t.Errorf("the workload is not free: %v", err)
			}
		})
	}
}

// A run stopped with revert while it concludes for another reason, whose
// leader dies right after the closing event: the next leader's first poll
// finds no open run, and its sweep reverts the probe.
func TestSupervisor_SweepAfterCloserDeath(t *testing.T) {
	h := newPGHarness(t)
	h.start()
	ctx, kill := h.killable()
	old := h.leader(func(svc session.Service) session.Service {
		return hookedService{Service: svc,
			before: func(_ context.Context, ev *session.Event) error {
				if eventNode(ev) == "conclude" {
					return h.api.Stop(h.ctx, h.id, Stop{Changes: ChangesRevert, Actor: "alice", At: h.clock.Now()})
				}
				return nil
			},
			after: func(ev *session.Event) {
				if eventNode(ev) == "conclude" {
					kill()
				}
			}}
	})
	st := h.drive(ctx, old, nil)
	if st.EndReason != EndDone || ctx.Err() == nil {
		t.Fatalf("run ended %s, leader killed %v", st.EndReason, ctx.Err() != nil)
	}
	if n := h.db.liveApproved(t, h.id, h.clock.Now()); n != 1 {
		t.Fatalf("%d live probes before the next leader, want the one the run left", n)
	}
	h.step(h.ctx, h.leader(nil))
	if n := h.db.liveApproved(t, h.id, h.clock.Now()); n != 0 {
		t.Errorf("%d live probes one poll after the next leader started", n)
	}
}
