package interrogation

// The tests in this file check what the session writes rely on in ADK's
// session store and that no document or test of adk-go promises: how a stale
// or overlapping append is refused, that update_time is the event's stamp,
// that state is stored whole as JSONB, and how a row lock of ours meets ADK's
// append. Each is a race that broke a first version of the design, run again
// here. Run them against Postgres on every adk-go upgrade.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"
	"gorm.io/gorm"

	apstore "github.com/odigos-io/odigos/insights/internal/autopilot/store"
	"github.com/odigos-io/odigos/insights/internal/db/postgres"
	"github.com/odigos-io/odigos/insights/internal/engine"
)

// testGraph is a three-node workflow in the shape of the interrogation's:
// entry starts the run's clock, or ends the invocation when the run is
// closed; prepare is the first step with an effect outside the session, and
// can be held open; finish is the last step.
type testGraph struct {
	mu       sync.Mutex
	prepared map[string]int
	held     map[string]chan struct{}
	entered  map[string]chan struct{}
}

func newTestGraph() *testGraph {
	return &testGraph{prepared: map[string]int{}, held: map[string]chan struct{}{}, entered: map[string]chan struct{}{}}
}

// hold makes prepare wait for the returned release once it has signalled
// entered.
func (g *testGraph) hold(id string) (entered <-chan struct{}, release func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	h, e := make(chan struct{}), make(chan struct{})
	g.held[id], g.entered[id] = h, e
	return e, func() { close(h) }
}

func (g *testGraph) timesPrepared(id string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.prepared[id]
}

// runner builds a runner of this graph on s, as a leader would.
func (g *testGraph) runner(t *testing.T, s *Sessions) *runner.Runner {
	t.Helper()
	runState := func(ctx agent.Context) map[string]any {
		v, err := ctx.Session().State().Get("run")
		if err != nil {
			return map[string]any{}
		}
		m, _ := v.(map[string]any)
		return maps.Clone(m)
	}
	setRun := func(ctx agent.Context, emit func(*session.Event) error, run map[string]any, route string) error {
		ev := session.NewEvent(ctx, ctx.InvocationID())
		ev.Actions.StateDelta = map[string]any{"run": run}
		if route != "" {
			ev.Routes = []string{route}
		}
		return emit(ev)
	}

	entry := workflow.NewEmittingFunctionNode("entry", func(ctx agent.Context, _ any, emit func(*session.Event) error) (any, error) {
		if open, _ := ctx.Session().State().Get("open"); open != true {
			ev := session.NewEvent(ctx, ctx.InvocationID())
			ev.Routes = []string{"closed"}
			return nil, emit(ev)
		}
		run := runState(ctx)
		if run["started_at"] == nil {
			run["started_at"] = time.Now().UTC()
		}
		run["step"] = "prepare"
		return nil, setRun(ctx, emit, run, "prepare")
	}, workflow.NodeConfig{})

	prepare := workflow.NewEmittingFunctionNode("prepare", func(ctx agent.Context, _ any, emit func(*session.Event) error) (any, error) {
		id := ctx.Session().ID()
		g.mu.Lock()
		held, entered := g.held[id], g.entered[id]
		g.mu.Unlock()
		if held != nil {
			close(entered)
			select {
			case <-held:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		g.mu.Lock()
		g.prepared[id]++
		g.mu.Unlock()
		run := runState(ctx)
		run["step"] = "finish"
		return nil, setRun(ctx, emit, run, "")
	}, workflow.NodeConfig{})

	finish := workflow.NewEmittingFunctionNode("finish", func(ctx agent.Context, _ any, emit func(*session.Event) error) (any, error) {
		run := runState(ctx)
		run["step"] = "done"
		return nil, setRun(ctx, emit, run, "")
	}, workflow.NodeConfig{})

	root, err := workflowagent.New(workflowagent.Config{
		Name: "interrogation",
		Edges: workflow.NewEdgeBuilder().
			Add(workflow.Start, entry).
			AddRoute(entry, prepare, workflow.StringRoute("prepare")).
			Add(prepare, finish).
			Build(),
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := runner.New(runner.Config{AppName: AppName, Agent: root, SessionService: s.Service()})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// invoke sends text to the run's session and returns the first error the
// invocation yields.
func invoke(ctx context.Context, r *runner.Runner, id, text string) error {
	var first error
	for _, err := range r.Run(ctx, id, id, genai.NewContentFromText(text, genai.RoleUser), agent.RunConfig{}) {
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}

func sleepUpTo(d time.Duration) {
	time.Sleep(rand.N(d))
}

// Concurrent starts of one workload from two replicas: one run is stored and
// every other start gets ErrSourceBusy.
func TestADKContract_OneOpenRunPerWorkload(t *testing.T) {
	db := newTestDB(t)
	replicas := []*Sessions{db.replica(t), db.replica(t)}
	ctx := context.Background()

	for trial := range 20 {
		workload := fmt.Sprintf("w%d", trial)
		var started, busy atomic.Int32
		var wg sync.WaitGroup
		gate := make(chan struct{})
		for i := range 30 {
			wg.Go(func() {
				<-gate
				err := replicas[i%2].Start(ctx, fmt.Sprintf("%s-%d", workload, i), testRequest(workload, 3600))
				code, constraint := pgCode(err)
				switch {
				case err == nil:
					started.Add(1)
				case errors.Is(err, ErrSourceBusy) && code == "23505" && constraint == openWorkloadIndex:
					busy.Add(1)
				default:
					t.Errorf("trial %d: start %d: %v", trial, i, err)
				}
			})
		}
		close(gate)
		wg.Wait()
		runs := db.count(t, `SELECT count(*) FROM adk.sessions WHERE app_name = $1 AND id LIKE $2`, AppName, workload+"-%")
		controls := db.count(t, `SELECT count(*) FROM adk.sessions WHERE app_name = $1 AND id LIKE $2`, ControlAppName, workload+"-%")
		if started.Load() != 1 || busy.Load() != 29 || runs != 1 || controls != 1 {
			t.Fatalf("trial %d: %d started, %d busy; stored %d runs and %d control sessions; want 1, 29, 1, 1",
				trial, started.Load(), busy.Load(), runs, controls)
		}
	}

	mustStart(t, ctx, replicas[0], "first", 3600)
	if err := runnerAppend(ctx, replicas[0].Service(), "first", map[string]any{"run": map[string]any{"status": "completed"}}); err != nil {
		t.Fatal(err)
	}
	if err := replicas[1].Start(ctx, "second", testRequest("first", 3600)); !errors.Is(err, ErrSourceBusy) {
		t.Fatalf("start after an event that left open set: %v, want ErrSourceBusy", err)
	}
	if runs, controls := db.sessions(t, "second"); runs != 0 || controls != 0 {
		t.Fatalf("the refused start stored %d run and %d control sessions", runs, controls)
	}
	if err := runnerAppend(ctx, replicas[0].Service(), "first", map[string]any{"open": false}); err != nil {
		t.Fatal(err)
	}
	if err := replicas[1].Start(ctx, "second", testRequest("first", 3600)); err != nil {
		t.Fatalf("start after the closing event: %v", err)
	}
}

// Control writes from replicas whose clocks disagree. ADK's own stale check
// compares the writers' clocks and lost an accepted write with a replica 5 ms
// ahead; the write rule must not.
func TestADKContract_ControlWritesUnderSkew(t *testing.T) {
	db := newTestDB(t)
	a, b, c := db.replica(t), db.replica(t), db.replica(t)
	ctx := context.Background()
	const trials = 100

	var refusals atomic.Int32
	// increment adds 60 to the control session's extend_seconds by the write
	// rule, and checks every refusal on the way.
	increment := func(ctx context.Context, s *Sessions, id string) error {
		return s.inTx(ctx, nil, func(_ *gorm.DB, svc session.Service) error {
			sess, err := getSession(ctx, svc, ControlAppName, id)
			if err != nil {
				return err
			}
			var cur control
			if err := decodeState(sess, &cur); err != nil {
				return err
			}
			err = appendState(ctx, svc, sess, map[string]any{"extend_seconds": cur.ExtendSeconds + 60})
			if err != nil {
				refusals.Add(1)
				if code, _ := pgCode(err); code != "40001" {
					t.Errorf("append refused with %v, want SQLSTATE 40001", err)
				}
			}
			return err
		})
	}

	for _, skew := range []time.Duration{5 * time.Millisecond, 50 * time.Millisecond, 500 * time.Millisecond} {
		t.Run(skew.String(), func(t *testing.T) {
			for i := range trials {
				id := fmt.Sprintf("rule-%s-%d", skew, i)
				mustStart(t, ctx, a, id, 3600)
				if err := increment(replicaClock(ctx, skew), a, id); err != nil {
					t.Fatal(err)
				}
				var errs [2]error
				var wg sync.WaitGroup
				for j, s := range []*Sessions{b, c} {
					wg.Go(func() {
						if j == 1 {
							sleepUpTo(4 * time.Millisecond)
						}
						errs[j] = increment(ctx, s, id)
					})
				}
				wg.Wait()
				if errs[0] != nil || errs[1] != nil {
					t.Fatalf("trial %d: %v, %v", i, errs[0], errs[1])
				}
				if got := db.control(t, id).ExtendSeconds; got != 180 {
					t.Fatalf("trial %d: extend_seconds = %d after three accepted writes of 60", i, got)
				}
			}

			// The same with the API's writers: a stop and an extend at once
			// after a write by the replica that is ahead.
			now := time.Now()
			for i := range trials {
				id := fmt.Sprintf("api-%s-%d", skew, i)
				mustStart(t, ctx, a, id, 3600)
				markStarted(t, a, id, now)
				if err := a.Extend(replicaClock(ctx, skew), id, 60, 28800, now); err != nil {
					t.Fatal(err)
				}
				var extendErr, stopErr error
				var wg sync.WaitGroup
				wg.Go(func() { extendErr = b.Extend(ctx, id, 60, 28800, now) })
				wg.Go(func() {
					sleepUpTo(4 * time.Millisecond)
					stopErr = c.Stop(ctx, id, Stop{Changes: ChangesRevert, Actor: "c", At: now})
				})
				wg.Wait()
				if stopErr != nil || extendErr != nil && !errors.Is(extendErr, ErrNotRunning) {
					t.Fatalf("trial %d: extend %v, stop %v", i, extendErr, stopErr)
				}
				want := 120
				if extendErr != nil {
					want = 60
				}
				got := db.control(t, id)
				if got.ExtendSeconds != want || got.Stop == nil || got.Stop.Changes != ChangesRevert {
					t.Fatalf("trial %d: control %+v (extend error %v), want extend_seconds %d and the stop", i, got, extendErr, want)
				}
			}
		})
	}
	t.Logf("%d appends refused and retried", refusals.Load())
}

// A stop on a queued run racing the leader's pickup, the replica that took the
// start 2 s ahead of the others.
func TestADKContract_QueuedCloseAgainstPickup(t *testing.T) {
	db := newTestDB(t)
	creator, leader, handler := db.replica(t), db.replica(t), db.replica(t)
	g := newTestGraph()
	r := g.runner(t, leader)
	ctx := context.Background()

	var closedOnly, ranOnly int
	for i := range 200 {
		id := fmt.Sprintf("run-%d", i)
		mustStart(t, replicaClock(ctx, 2*time.Second), creator, id, 3600)
		var closed bool
		var closeErr error
		var wg sync.WaitGroup
		wg.Go(func() {
			sleepUpTo(3 * time.Millisecond)
			_ = invoke(ctx, r, id, "start")
		})
		wg.Go(func() {
			sleepUpTo(6 * time.Millisecond)
			closed, closeErr = handler.CloseQueued(ctx, id, Closed{Reason: "stopped_by_user"})
		})
		wg.Wait()
		if closeErr != nil {
			t.Fatalf("race %d: %v", i, closeErr)
		}
		ran := g.timesPrepared(id) > 0
		state := db.state(t, AppName, id)
		switch {
		case closed && ran:
			t.Fatalf("race %d: closed as queued and also run: %v", i, state)
		case closed:
			closedOnly++
		case ran:
			ranOnly++
		default:
			t.Fatalf("race %d: neither closed nor run: %v", i, state)
		}
	}
	t.Logf("200 races: closed while queued %d, run %d", closedOnly, ranOnly)
}

// Two leaders send a queued run its first message at once.
func TestADKContract_TwoLeadersPickUp(t *testing.T) {
	db := newTestDB(t)
	a, b := db.replica(t), db.replica(t)
	g := newTestGraph()
	runners := []*runner.Runner{g.runner(t, a), g.runner(t, b)}
	ctx := context.Background()

	for i := range 60 {
		id := fmt.Sprintf("run-%d", i)
		mustStart(t, ctx, a, id, 3600)
		var errs [2]error
		var wg sync.WaitGroup
		gate := make(chan struct{})
		for j, r := range runners {
			wg.Go(func() {
				<-gate
				errs[j] = invoke(ctx, r, id, "start")
			})
		}
		close(gate)
		wg.Wait()
		if n := g.timesPrepared(id); n != 1 {
			t.Fatalf("trial %d: prepare ran %d times (invocation errors %v)", i, n, errs)
		}
		if errs[0] != nil && errs[1] != nil {
			t.Fatalf("trial %d: both invocations failed: %v", i, errs)
		}
		if n := db.count(t, `SELECT count(*) FROM adk.events WHERE app_name = $1 AND session_id = $2 AND author = 'user'`, AppName, id); n != 1 {
			t.Fatalf("trial %d: %d start messages stored", i, n)
		}
	}
}

// Control written while the runner holds a step open does not make the
// runner's next append fail.
func TestADKContract_RunnerUndisturbedByControl(t *testing.T) {
	db := newTestDB(t)
	leader, api := db.replica(t), db.replica(t)
	g := newTestGraph()
	r := g.runner(t, leader)
	ctx := context.Background()

	mustStart(t, ctx, api, "run", 3600)
	entered, release := g.hold("run")
	done := make(chan error, 1)
	go func() { done <- invoke(ctx, r, "run", "start") }()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("invocation ended before prepare: %v", err)
	}
	if err := api.Extend(ctx, "run", 60, 28800, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := api.Stop(ctx, "run", Stop{Changes: ChangesKeep, Actor: "alice", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("invocation: %v", err)
	}
	if n := g.timesPrepared("run"); n != 1 {
		t.Errorf("prepare ran %d times", n)
	}
	run, _ := db.state(t, AppName, "run")["run"].(map[string]any)
	if run["step"] != "done" {
		t.Errorf("run = %v, want it at the end of the graph", run)
	}
	if c := db.control(t, "run"); c.ExtendSeconds != 60 || c.Stop == nil {
		t.Errorf("control = %+v", c)
	}
}

// An extend racing the run's closing event is applied while the run is open or
// refused, and the closing append never fails.
func TestADKContract_ExtendAgainstClose(t *testing.T) {
	db := newTestDB(t)
	api, leader := db.replica(t), db.replica(t)
	probes := openProbeStore(t, db.dsn)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	setup := func(id string) {
		mustStart(t, ctx, api, id, 600)
		markStarted(t, api, id, now)
		if _, err := probes.InsertRequestedProbe(ctx, requestedProbe(id, "a.B.c"), 5, now); err != nil {
			t.Fatal(err)
		}
	}

	setup("closed-first")
	if err := runnerAppend(ctx, leader.Service(), "closed-first", map[string]any{"open": false}); err != nil {
		t.Fatal(err)
	}
	if err := api.Extend(ctx, "closed-first", 60, 28800, now); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("extend of a closed run: %v, want ErrNotRunning", err)
	}
	if c := db.control(t, "closed-first"); c.ExtendSeconds != 0 || db.wrongExpiries(t, "closed-first") != 0 {
		t.Fatalf("a refused extend changed something: control %+v", c)
	}

	var applied, refused int
	for i := range 200 {
		id := fmt.Sprintf("run-%d", i)
		setup(id)
		var extendErr, closeErr error
		var wg sync.WaitGroup
		gate := make(chan struct{})
		wg.Go(func() {
			<-gate
			sleepUpTo(2500 * time.Microsecond)
			extendErr = api.Extend(ctx, id, 60, 28800, now)
		})
		wg.Go(func() {
			<-gate
			sleepUpTo(2500 * time.Microsecond)
			closeErr = runnerAppend(ctx, leader.Service(), id, map[string]any{"open": false})
		})
		close(gate)
		wg.Wait()
		if closeErr != nil {
			t.Fatalf("race %d: the closing append failed: %v", i, closeErr)
		}
		want := 0
		switch {
		case extendErr == nil:
			applied++
			want = 60
		case errors.Is(extendErr, ErrNotRunning):
			refused++
		default:
			t.Fatalf("race %d: extend: %v", i, extendErr)
		}
		if got := db.control(t, id).ExtendSeconds; got != want {
			t.Fatalf("race %d: extend_seconds = %d, want %d (extend error %v)", i, got, want, extendErr)
		}
		if n := db.wrongExpiries(t, id); n != 0 {
			t.Fatalf("race %d: %d probes off the run's deadline", i, n)
		}
	}
	t.Logf("200 races: extend applied %d, refused %d", applied, refused)
}

// Extends racing the loop's probe insert leave no probe expiring before the
// run's deadline, lose no extension, and do not deadlock.
func TestADKContract_ExtendAgainstProbeInsert(t *testing.T) {
	db := newTestDB(t)
	a, b := db.replica(t), db.replica(t)
	probes := openProbeStore(t, db.dsn)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	for _, extenders := range [][]*Sessions{{a}, {a, b}} {
		t.Run(fmt.Sprintf("%d extends", len(extenders)), func(t *testing.T) {
			for i := range 200 {
				id := fmt.Sprintf("run-%d-%d", len(extenders), i)
				mustStart(t, ctx, a, id, 600)
				markStarted(t, a, id, now)
				var mu sync.Mutex
				var errs []error
				record := func(err error) {
					if err != nil {
						mu.Lock()
						errs = append(errs, err)
						mu.Unlock()
					}
				}
				var wg sync.WaitGroup
				gate := make(chan struct{})
				for _, s := range extenders {
					wg.Go(func() {
						<-gate
						sleepUpTo(2500 * time.Microsecond)
						record(s.Extend(ctx, id, 60, 28800, now))
					})
				}
				wg.Go(func() {
					<-gate
					sleepUpTo(2500 * time.Microsecond)
					_, err := probes.InsertRequestedProbe(ctx, requestedProbe(id, "a.B.c"), 5, now)
					record(err)
				})
				close(gate)
				wg.Wait()
				for _, err := range errs {
					code, _ := pgCode(err)
					t.Fatalf("race %d: %v (SQLSTATE %q)", i, err, code)
				}
				if got, want := db.control(t, id).ExtendSeconds, 60*len(extenders); got != want {
					t.Fatalf("race %d: extend_seconds = %d, want %d", i, got, want)
				}
				if n := db.count(t, `SELECT count(*) FROM interrogation_requested_probes WHERE interrogation_id = $1`, id); n != 1 {
					t.Fatalf("race %d: %d probes stored", i, n)
				}
				if n := db.wrongExpiries(t, id); n != 0 {
					t.Fatalf("race %d: the probe expires off the run's deadline", i)
				}
			}
		})
	}
}

// The leader closes a run without the workflow while an old leader still
// appends to the copy of the session its runner holds. No append may land
// after the close returned.
func TestADKContract_CloseAgainstOldLeader(t *testing.T) {
	db := newTestDB(t)
	creator, oldLeader, leader := db.replica(t), db.replica(t), db.replica(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name                       string
		creatorSkew, oldLeaderSkew time.Duration
	}{
		{"creator 2s ahead", 2 * time.Second, 0},
		{"old leader 2s ahead", 0, 2 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var refused int
			for i := range 100 {
				id := fmt.Sprintf("%s-%d", tc.name, i)
				mustStart(t, replicaClock(ctx, tc.creatorSkew), creator, id, 3600)
				oldCtx := replicaClock(ctx, tc.oldLeaderSkew)
				got, err := oldLeader.Service().Get(oldCtx, &session.GetRequest{AppName: AppName, UserID: id, SessionID: id})
				if err != nil {
					t.Fatal(err)
				}

				var closedAt atomic.Bool
				late := make(chan int, 1)
				go func() {
					n := 0
					defer func() { late <- n }()
					sleepUpTo(10 * time.Millisecond)
					for step := 1; step <= 12; step++ {
						ev := session.NewEvent(oldCtx, "old")
						ev.Actions.StateDelta = map[string]any{"run": map[string]any{"step": step}}
						closedBefore := closedAt.Load()
						if err := oldLeader.Service().AppendEvent(oldCtx, got.Session, ev); err != nil {
							return
						}
						if closedBefore {
							n++
						}
						time.Sleep(5*time.Millisecond + rand.N(10*time.Millisecond))
					}
				}()

				sleepUpTo(30 * time.Millisecond)
				// The supervisor tries again at its next poll after a refusal.
				for attempt := 0; ; attempt++ {
					err := leader.CloseOverdue(ctx, id, Closed{Reason: "interrupted"})
					if err == nil {
						break
					}
					if !serializationFailure(err) || attempt == 10 {
						t.Fatalf("trial %d: close: %v", i, err)
					}
				}
				closedAt.Store(true)
				if n := <-late; n != 0 {
					t.Fatalf("trial %d: %d appends of the old leader landed after the close", i, n)
				}
				state := db.state(t, AppName, id)
				if state["open"] != false {
					t.Fatalf("trial %d: run still open: %v", i, state)
				}
				if run, _ := state["run"].(map[string]any); run["step"] != float64(12) {
					refused++
				}
			}
			t.Logf("old leader refused before its last append in %d of 100 trials", refused)
		})
	}
}

// What the API and the index read is where ADK leaves it after its own
// appends.
func TestADKContract_StateLayout(t *testing.T) {
	db := newTestDB(t)
	api, leader := db.replica(t), db.replica(t)
	g := newTestGraph()
	r := g.runner(t, leader)
	ctx := context.Background()

	req := testRequest("app", 3600)
	if err := api.Start(ctx, "run", req); err != nil {
		t.Fatal(err)
	}
	type times struct{ run, control time.Time }
	created := func() times {
		var ct times
		if err := db.sql.QueryRow(ctx, `
SELECT (SELECT create_time FROM adk.sessions WHERE app_name = 'interrogation' AND id = 'run'),
	(SELECT create_time FROM adk.sessions WHERE app_name = 'interrogation.ctl' AND id = 'run')`).Scan(&ct.run, &ct.control); err != nil {
			t.Fatal(err)
		}
		return ct
	}
	before := created()

	if err := invoke(ctx, r, "run", "start"); err != nil {
		t.Fatal(err)
	}
	if err := api.Extend(ctx, "run", 60, 28800, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := api.Stop(ctx, "run", Stop{Changes: ChangesKeep, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := leader.CloseOverdue(ctx, "run", Closed{Reason: "stopped_by_user"}); err != nil {
		t.Fatal(err)
	}

	var typ string
	var keys []string
	if err := db.sql.QueryRow(ctx, `
SELECT jsonb_typeof(state), (SELECT array_agg(k ORDER BY k) FROM jsonb_object_keys(state) k)
FROM adk.sessions WHERE app_name = 'interrogation' AND id = 'run'`).Scan(&typ, &keys); err != nil {
		t.Fatal(err)
	}
	if typ != "object" {
		t.Fatalf("state is a JSON %s", typ)
	}
	for _, k := range []string{"workload", "open", "request", "run", "closed"} {
		if !slices.Contains(keys, k) {
			t.Errorf("state keys %v, want %q at the top level", keys, k)
		}
	}
	// The index and the reads find the values at those keys, as JSON.
	if n := db.count(t, `
SELECT count(*) FROM adk.sessions
WHERE app_name = 'interrogation' AND id = 'run' AND state->>'workload' = 'ns/Deployment/app' AND state->>'open' = 'false'
	AND state->'request'->'budget'->>'time_limit_seconds' = '3600' AND state->'run'->>'step' = 'done'
	AND state->'closed'->>'reason' = 'stopped_by_user'`); n != 1 {
		t.Errorf("state read by key: %v", db.state(t, AppName, "run"))
	}
	if n := db.count(t, `SELECT (SELECT count(*) FROM adk.app_states) + (SELECT count(*) FROM adk.user_states)`); n != 0 {
		t.Errorf("%d rows in adk.app_states and adk.user_states", n)
	}
	if after := created(); !after.run.Equal(before.run) || !after.control.Equal(before.control) {
		t.Errorf("create_time changed from %+v to %+v", before, after)
	}
}

// openRepo opens the insights repo on the test's database, as every replica
// does.
func openRepo(t *testing.T, dsn string) *postgres.Repo {
	t.Helper()
	r, err := postgres.Open(dsn, engine.NewBaselineCodec())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// liveApproved counts the run's temporary probes that are live at now.
func (d testDB) liveApproved(t *testing.T, id string, now time.Time) int {
	t.Helper()
	return d.count(t, `SELECT count(*) FROM interrogation_requested_probes WHERE interrogation_id = $1 AND state = 'approved' AND expires_at > $2`, id, now)
}

// stopHandler is the stop handler with changes=revert: it records the stop,
// reads the run again, and reverts itself when the run has closed.
func stopHandler(ctx context.Context, s *Sessions, r *postgres.Repo, id string, now time.Time) error {
	if err := s.Stop(ctx, id, Stop{Changes: ChangesRevert, Actor: "alice", At: now}); err != nil {
		return err
	}
	in, _, err := r.GetInterrogation(ctx, id, now)
	if err != nil || in.Open {
		return err
	}
	return r.RevertInterrogationProbes(ctx, id, now)
}

// The stop with revert is recorded while the run is open, so the handler
// leaves the revert to the run; the run's closing event commits and its leader
// stops before doing anything else. The next leader's poll finds no open run,
// and only the sweep reverts the probe.
func TestADKContract_RevertSurvivesCloserDeath(t *testing.T) {
	db := newTestDB(t)
	api, leader := db.replica(t), db.replica(t)
	r := openRepo(t, db.dsn)
	probes := openProbeStore(t, db.dsn)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	// run starts a run with one probe.
	run := func(id string) {
		mustStart(t, ctx, api, id, 3600)
		markStarted(t, api, id, now)
		if _, err := probes.InsertRequestedProbe(ctx, requestedProbe(id, "a.B.c"), 5, now); err != nil {
			t.Fatal(err)
		}
	}
	stop := func(id, changes string) {
		if err := api.Stop(ctx, id, Stop{Changes: changes, Actor: "alice", At: now}); err != nil {
			t.Fatal(err)
		}
	}
	closeRun := func(id string) {
		if err := runnerAppend(ctx, leader.Service(), id, map[string]any{"run": map[string]any{"status": "stopped"}, "open": false}); err != nil {
			t.Fatal(err)
		}
	}

	run("lost")
	if _, err := probes.InsertRequestedProbe(ctx, requestedProbe("lost", "a.B.expired"), 5, now); err != nil {
		t.Fatal(err)
	}
	db.exec(t, `UPDATE interrogation_requested_probes SET expires_at = $2 WHERE change_id = $1`, "chg_lost_a.B.expired", now.Add(-time.Minute))
	stop("lost", ChangesRevert)
	if in, _, err := r.GetInterrogation(ctx, "lost", now); err != nil || !in.Open {
		t.Fatalf("the handler's second read: open %v, err %v; want the run open", in.Open, err)
	}
	closeRun("lost")
	// Not reverted: a run stopped with keep, an open run stopped with revert.
	run("keep")
	stop("keep", ChangesKeep)
	closeRun("keep")
	run("open")
	stop("open", ChangesRevert)

	if open, err := r.ListOpenInterrogations(ctx); err != nil || len(open) != 1 || open[0].ID != "open" {
		t.Fatalf("the next leader's poll: %+v %v", open, err)
	}
	n, err := r.RevertStoppedInterrogationProbes(ctx, now)
	if err != nil || n != 1 {
		t.Fatalf("sweep reverted %d (%v), want 1", n, err)
	}
	if left := db.liveApproved(t, "lost", now); left != 0 {
		t.Errorf("%d live temporary probes left on the run stopped with revert", left)
	}
	c, _, _ := r.GetInterrogationChange(ctx, "chg_lost_a.B.c")
	if c.EndReason != "stop" {
		t.Errorf("reverted change %+v, want end reason stop", c)
	}
	if c, _, _ := r.GetInterrogationChange(ctx, "chg_lost_a.B.expired"); c.State(now) != "expired" || c.EndReason != "" {
		t.Errorf("expired change %+v, want it left expired", c)
	}
	for _, id := range []string{"keep", "open"} {
		if left := db.liveApproved(t, id, now); left != 1 {
			t.Errorf("run %s: %d live temporary probes, want its 1 untouched", id, left)
		}
	}
	if n, err := r.RevertStoppedInterrogationProbes(ctx, now); err != nil || n != 0 {
		t.Errorf("a second sweep reverted %d (%v), want 0", n, err)
	}
}

// A stop with revert against the run's close, both sides alive. Whichever
// order they commit in, the handler, the run's conclude or the next poll's
// sweep reverts the probe.
func TestStop_RacesClose(t *testing.T) {
	db := newTestDB(t)
	api, leader := db.replica(t), db.replica(t)
	r := openRepo(t, db.dsn)
	probes := openProbeStore(t, db.dsn)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	var swept int64
	for i := range 200 {
		id := fmt.Sprintf("run-%d", i)
		mustStart(t, ctx, api, id, 3600)
		markStarted(t, api, id, now)
		if _, err := probes.InsertRequestedProbe(ctx, requestedProbe(id, "a.B.c"), 5, now); err != nil {
			t.Fatal(err)
		}
		var stopErr, closeErr error
		var wg sync.WaitGroup
		gate := make(chan struct{})
		wg.Go(func() {
			<-gate
			sleepUpTo(3 * time.Millisecond)
			stopErr = stopHandler(ctx, api, r, id, now)
		})
		wg.Go(func() {
			<-gate
			sleepUpTo(3 * time.Millisecond)
			// conclude reverts when the stop it was handed says so; this one
			// was handed none, as when the stop came after its last read.
			closeErr = runnerAppend(ctx, leader.Service(), id, map[string]any{"run": map[string]any{"status": "completed"}, "open": false})
		})
		close(gate)
		wg.Wait()
		if stopErr != nil || closeErr != nil {
			t.Fatalf("race %d: stop %v, close %v", i, stopErr, closeErr)
		}
		n, err := r.RevertStoppedInterrogationProbes(ctx, now)
		if err != nil {
			t.Fatal(err)
		}
		swept += n
		if left := db.liveApproved(t, id, now); left != 0 {
			t.Fatalf("race %d: %d live temporary probes after the sweep", i, left)
		}
	}
	t.Logf("200 races: the sweep reverted what the handler left in %d", swept)
}

// The probe insert of a second leader, a stop with revert and the run's
// conclude (its revert, then the closing event), all at once. An insert that
// commits after conclude's revert and before the stop reached the control
// session is neither refused nor reverted by conclude; the sweep at the next
// poll reverts it.
func TestRequestProbe_RacesClose(t *testing.T) {
	db := newTestDB(t)
	api, leader := db.replica(t), db.replica(t)
	r := openRepo(t, db.dsn)
	probes := openProbeStore(t, db.dsn)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	var inserted, refused int
	var swept int64
	for i := range 200 {
		id := fmt.Sprintf("run-%d", i)
		mustStart(t, ctx, api, id, 3600)
		markStarted(t, api, id, now)
		var insertErr, stopErr, closeErr error
		var wg sync.WaitGroup
		gate := make(chan struct{})
		wg.Go(func() {
			<-gate
			sleepUpTo(3 * time.Millisecond)
			_, insertErr = probes.InsertRequestedProbe(ctx, requestedProbe(id, "a.B.c"), 5, now)
		})
		wg.Go(func() {
			<-gate
			sleepUpTo(3 * time.Millisecond)
			stopErr = api.Stop(ctx, id, Stop{Changes: ChangesRevert, Actor: "alice", At: now})
		})
		wg.Go(func() {
			<-gate
			sleepUpTo(3 * time.Millisecond)
			if err := r.RevertInterrogationProbes(ctx, id, now); err != nil {
				closeErr = err
				return
			}
			sleepUpTo(3 * time.Millisecond)
			closeErr = runnerAppend(ctx, leader.Service(), id, map[string]any{"run": map[string]any{"status": "stopped"}, "open": false})
		})
		close(gate)
		wg.Wait()
		switch {
		case stopErr != nil || closeErr != nil:
			t.Fatalf("race %d: stop %v, close %v", i, stopErr, closeErr)
		case insertErr == nil:
			inserted++
		case errors.Is(insertErr, apstore.ErrRunNotOpen):
			refused++
		default:
			t.Fatalf("race %d: insert: %v", i, insertErr)
		}
		n, err := r.RevertStoppedInterrogationProbes(ctx, now)
		if err != nil {
			t.Fatal(err)
		}
		swept += n
		if left := db.liveApproved(t, id, now); left != 0 {
			t.Fatalf("race %d: %d live temporary probes after the sweep", i, left)
		}
	}
	t.Logf("200 races: inserted %d, refused %d; the sweep reverted %d", inserted, refused, swept)
}
