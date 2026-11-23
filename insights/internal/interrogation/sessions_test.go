package interrogation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/adk/v2/platform"
	"google.golang.org/adk/v2/session"
	"gorm.io/gorm"

	apstore "github.com/odigos-io/odigos/insights/internal/autopilot/store"
	"github.com/odigos-io/odigos/insights/internal/db/postgres/migrate"
	"github.com/odigos-io/odigos/insights/internal/db/postgres/pgtest"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

const applicationName = "odigos-insights-adk"

// wantMaxConns is the per-replica connection budget.
const wantMaxConns = 4

func testDSN(t *testing.T, params ...string) string {
	t.Helper()
	u, err := url.Parse(pgtest.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	for i := 0; i+1 < len(params); i += 2 {
		q.Set(params[i], params[i+1])
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func openTestSessions(t *testing.T, dsn string) *Sessions {
	t.Helper()
	s, err := OpenSessions(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open sessions: %v", err)
	}
	t.Cleanup(s.Close)
	s.maxOpen, s.maxPerDay = math.MaxInt32, math.MaxInt32
	return s
}

// adkBackends counts this database's connections opened by an ADK pool, read
// on a connection of the test's own.
func adkBackends(t *testing.T, conn *pgx.Conn) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(context.Background(), `
SELECT count(*) FROM pg_stat_activity
WHERE datname = current_database() AND application_name = $1`, applicationName).Scan(&n); err != nil {
		t.Fatalf("count backends: %v", err)
	}
	return n
}

func observer(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestOpenSessions_Pool(t *testing.T) {
	dsn := testDSN(t)

	t.Run("session settings", func(t *testing.T) {
		s := openTestSessions(t, testDSN(t, "search_path", "insights_custom, public"))
		var searchPath, isolation, appName string
		err := s.db.Raw(`SELECT current_setting('search_path'), current_setting('default_transaction_isolation'), current_setting('application_name')`).
			Row().Scan(&searchPath, &isolation, &appName)
		if err != nil {
			t.Fatal(err)
		}
		if want := "adk, insights_custom, public"; searchPath != want {
			t.Errorf("search_path = %q, want %q", searchPath, want)
		}
		if isolation != "repeatable read" {
			t.Errorf("default_transaction_isolation = %q, want repeatable read", isolation)
		}
		if appName != applicationName {
			t.Errorf("application_name = %q, want %q", appName, applicationName)
		}
	})

	t.Run("default search path kept", func(t *testing.T) {
		s := openTestSessions(t, dsn)
		var searchPath string
		if err := s.db.Raw(`SHOW search_path`).Row().Scan(&searchPath); err != nil {
			t.Fatal(err)
		}
		if want := `adk, "$user", public`; searchPath != want {
			t.Errorf("search_path = %q, want %q", searchPath, want)
		}
	})

	t.Run("isolation per transaction", func(t *testing.T) {
		s := openTestSessions(t, dsn)
		for _, tc := range []struct {
			opts *sql.TxOptions
			want string
		}{
			{nil, "repeatable read"},
			{&sql.TxOptions{Isolation: sql.LevelReadCommitted}, "read committed"},
		} {
			var got string
			err := s.db.Transaction(func(tx *gorm.DB) error {
				return tx.Raw(`SHOW transaction_isolation`).Row().Scan(&got)
			}, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("transaction_isolation = %q, want %q", got, tc.want)
			}
		}
	})

	t.Run("at most four connections", func(t *testing.T) {
		// A database of its own: the count below sees every ADK pool on it.
		own := pgtest.NewDatabase(t)
		s := openTestSessions(t, own)
		obs := observer(t, own)

		var held atomic.Int32
		var wg sync.WaitGroup
		errs := make(chan error, 16)
		for range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- s.db.Transaction(func(tx *gorm.DB) error {
					held.Add(1)
					defer held.Add(-1)
					return tx.Exec(`SELECT pg_sleep(0.1)`).Error
				})
			}()
		}
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()

		maxBackends, maxHeld := 0, int32(0)
	sample:
		for {
			maxBackends = max(maxBackends, adkBackends(t, obs))
			maxHeld = max(maxHeld, held.Load())
			select {
			case <-done:
				break sample
			case <-time.After(10 * time.Millisecond):
			}
		}
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		if maxBackends > wantMaxConns || maxHeld > wantMaxConns {
			t.Errorf("held %d transactions on %d connections, want at most %d", maxHeld, maxBackends, wantMaxConns)
		}
		if maxHeld < wantMaxConns {
			t.Errorf("at most %d transactions ran at once, want %d: the pool was never full", maxHeld, wantMaxConns)
		}
	})
}

func TestOpenSessions_DeadlineBoundsConnect(t *testing.T) {
	// 192.0.2.0/24 is reserved for documentation (RFC 5737): a connect there
	// gets no answer and would wait for connect_timeout without the context.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	s, err := OpenSessions(ctx, "postgres://insights:insightspass@192.0.2.1:5432/insights?sslmode=disable&connect_timeout=30")
	if err == nil {
		s.Close()
		t.Fatal("open succeeded against an unreachable server")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("open returned after %v, want it bounded by the 500ms context", elapsed)
	}
}

// testDB is a migrated database of this test's own, with a plain pool for
// the checks.
type testDB struct {
	dsn string
	sql *pgxpool.Pool
}

func newTestDB(t *testing.T) testDB {
	t.Helper()
	dsn := pgtest.NewDatabase(t)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return testDB{dsn: dsn, sql: pool}
}

// replica opens another session store on the database, as another insights
// replica would.
func (d testDB) replica(t *testing.T) *Sessions {
	t.Helper()
	return openTestSessions(t, d.dsn)
}

func (d testDB) state(t *testing.T, app, id string) map[string]any {
	t.Helper()
	var s map[string]any
	if err := d.sql.QueryRow(context.Background(),
		`SELECT state FROM adk.sessions WHERE app_name = $1 AND user_id = $2 AND id = $2`, app, id).Scan(&s); err != nil {
		t.Fatalf("read %s session %s: %v", app, id, err)
	}
	return s
}

func (d testDB) control(t *testing.T, id string) control {
	t.Helper()
	var c control
	var raw []byte
	if err := d.sql.QueryRow(context.Background(),
		`SELECT state FROM adk.sessions WHERE app_name = $1 AND user_id = $2 AND id = $2`, ControlAppName, id).Scan(&raw); err != nil {
		t.Fatalf("read control session %s: %v", id, err)
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func (d testDB) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := d.sql.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (d testDB) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := d.sql.Exec(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func (d testDB) sessions(t *testing.T, id string) (runs, controls int) {
	t.Helper()
	const q = `SELECT count(*) FROM adk.sessions WHERE app_name = $1 AND user_id = $2 AND id = $2`
	return d.count(t, q, AppName, id), d.count(t, q, ControlAppName, id)
}

func testRequest(name string, limitSeconds int) repo.InterrogationRequest {
	return repo.InterrogationRequest{
		Source:       repo.InterrogationSource{Namespace: "ns", Kind: "Deployment", Name: name},
		Goal:         "observability",
		AllowChanges: []string{"custom_instrumentation"},
		Budget:       repo.InterrogationBudget{MaxProbes: 5, TimeLimitSeconds: limitSeconds, MaxRounds: 5},
		Trigger:      repo.InterrogationTrigger{Kind: "manual", Actor: "alice"},
	}
}

func mustStart(t *testing.T, ctx context.Context, s *Sessions, id string, limitSeconds int) {
	t.Helper()
	if err := s.Start(ctx, id, testRequest(id, limitSeconds)); err != nil {
		t.Fatalf("start %s: %v", id, err)
	}
}

// runnerAppend appends to a run's session the way the workflow's runner
// does: through the service, outside any transaction of ours.
func runnerAppend(ctx context.Context, svc session.Service, id string, delta map[string]any) error {
	got, err := svc.Get(ctx, &session.GetRequest{AppName: AppName, UserID: id, SessionID: id})
	if err != nil {
		return err
	}
	ev := session.NewEvent(ctx, "runner")
	ev.Author = "interrogation"
	ev.Actions.StateDelta = delta
	return svc.AppendEvent(ctx, got.Session, ev)
}

// markStarted records the run's start as the workflow's first node does.
func markStarted(t *testing.T, s *Sessions, id string, at time.Time) {
	t.Helper()
	if err := runnerAppend(context.Background(), s.Service(), id, map[string]any{"run": map[string]any{"started_at": at}}); err != nil {
		t.Fatalf("mark %s started: %v", id, err)
	}
}

// replicaClock is ctx as seen by a replica whose clock is off by d. ADK takes
// every timestamp from it.
func replicaClock(ctx context.Context, d time.Duration) context.Context {
	return platform.WithTimeProvider(ctx, func() time.Time { return time.Now().Add(d) })
}

func pgCode(err error) (code, constraint string) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code, pgErr.ConstraintName
	}
	return "", ""
}

func TestStart(t *testing.T) {
	db := newTestDB(t)
	s := db.replica(t)
	ctx := context.Background()

	req := testRequest("app", 3600)
	if err := s.Start(ctx, "run-1", req); err != nil {
		t.Fatal(err)
	}
	if runs, controls := db.sessions(t, "run-1"); runs != 1 || controls != 1 {
		t.Fatalf("stored %d run and %d control sessions, want 1 and 1", runs, controls)
	}
	state := db.state(t, AppName, "run-1")
	if state["workload"] != "ns/Deployment/app" || state["open"] != true {
		t.Errorf("state = %v", state)
	}
	var stored repo.InterrogationRequest
	b, _ := json.Marshal(state["request"])
	if err := json.Unmarshal(b, &stored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored, req) {
		t.Errorf("stored request %+v, want %+v", stored, req)
	}
	if got := db.state(t, ControlAppName, "run-1"); len(got) != 0 {
		t.Errorf("control state = %v, want empty", got)
	}

	err := s.Start(ctx, "run-2", req)
	if !errors.Is(err, ErrSourceBusy) {
		t.Fatalf("second start: %v, want ErrSourceBusy", err)
	}
	if code, constraint := pgCode(err); code != "23505" || constraint != openWorkloadIndex {
		t.Errorf("second start refused with %s on %q", code, constraint)
	}
	if runs, controls := db.sessions(t, "run-2"); runs != 0 || controls != 0 {
		t.Errorf("a refused start stored %d run and %d control sessions", runs, controls)
	}
	if err := s.Start(ctx, "run-3", testRequest("other", 3600)); err != nil {
		t.Errorf("start on another workload: %v", err)
	}
}

func TestCloseQueued(t *testing.T) {
	db := newTestDB(t)
	s := db.replica(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	stop := Closed{Reason: "stopped_by_user", Actor: "bob", Changes: ChangesKeep, At: at}

	t.Run("queued", func(t *testing.T) {
		mustStart(t, ctx, s, "queued", 3600)
		closed, err := s.CloseQueued(ctx, "queued", stop)
		if err != nil || !closed {
			t.Fatalf("CloseQueued = %v, %v; want true", closed, err)
		}
		state := db.state(t, AppName, "queued")
		c, _ := state["closed"].(map[string]any)
		if state["open"] != false || c["reason"] != "stopped_by_user" || c["actor"] != "bob" || c["at"] != at.Format(time.RFC3339) {
			t.Errorf("state after close = %v", state)
		}
		// The workload is free again.
		if err := s.Start(ctx, "queued-next", testRequest("queued", 3600)); err != nil {
			t.Errorf("start after the close: %v", err)
		}
	})

	t.Run("picked up", func(t *testing.T) {
		mustStart(t, ctx, s, "running", 3600)
		markStarted(t, s, "running", time.Now())
		closed, err := s.CloseQueued(ctx, "running", stop)
		if err != nil || closed {
			t.Fatalf("CloseQueued = %v, %v; want false", closed, err)
		}
		if state := db.state(t, AppName, "running"); state["open"] != true || state["closed"] != nil {
			t.Errorf("state = %v, want the run untouched", state)
		}
	})

	t.Run("picked up by a newer binary", func(t *testing.T) {
		mustStart(t, ctx, s, "newer", 3600)
		if err := runnerAppend(ctx, s.Service(), "newer", map[string]any{"run": map[string]any{"v": 2, "started_at": 1794900000}}); err != nil {
			t.Fatal(err)
		}
		closed, err := s.CloseQueued(ctx, "newer", stop)
		if err != nil || closed {
			t.Fatalf("CloseQueued = %v, %v; want false", closed, err)
		}
	})

	t.Run("closed", func(t *testing.T) {
		mustStart(t, ctx, s, "ended", 3600)
		if err := runnerAppend(ctx, s.Service(), "ended", map[string]any{"run": map[string]any{"status": "completed"}, "open": false}); err != nil {
			t.Fatal(err)
		}
		closed, err := s.CloseQueued(ctx, "ended", stop)
		if err != nil || closed {
			t.Fatalf("CloseQueued = %v, %v; want false", closed, err)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		if _, err := s.CloseQueued(ctx, "missing", stop); !errors.Is(err, session.ErrNotFound) {
			t.Errorf("CloseQueued of an unknown run: %v, want session.ErrNotFound", err)
		}
	})
}

// Two stops on a queued run with no leader: exactly one closes it.
func TestStop_TwoOnQueued(t *testing.T) {
	db := newTestDB(t)
	replicas := []*Sessions{db.replica(t), db.replica(t)}
	ctx := context.Background()
	for i := range 50 {
		id := fmt.Sprintf("run-%d", i)
		mustStart(t, ctx, replicas[0], id, 3600)
		var closed [2]bool
		var errs [2]error
		var wg sync.WaitGroup
		for j, s := range replicas {
			wg.Go(func() {
				closed[j], errs[j] = s.CloseQueued(ctx, id, Closed{Reason: "stopped_by_user", Actor: fmt.Sprint(j)})
			})
		}
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("trial %d: %v, %v", i, errs[0], errs[1])
		}
		if closed[0] == closed[1] {
			t.Fatalf("trial %d: closed %v, want exactly one", i, closed)
		}
		if db.state(t, AppName, id)["open"] != false {
			t.Fatalf("trial %d: run still open", i)
		}
	}
}

func TestStop(t *testing.T) {
	db := newTestDB(t)
	s := db.replica(t)
	ctx := context.Background()
	first := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	later := first.Add(time.Minute)

	mustStart(t, ctx, s, "run", 3600)
	markStarted(t, s, "run", time.Now())

	steps := []struct {
		stop Stop
		want Stop
	}{
		{Stop{Changes: ChangesKeep, Actor: "alice", At: first}, Stop{Changes: ChangesKeep, Actor: "alice", At: first}},
		// A second keep changes nothing.
		{Stop{Changes: ChangesKeep, Actor: "bob", At: later}, Stop{Changes: ChangesKeep, Actor: "alice", At: first}},
		// A revert overrides the keep and keeps the first stop's time and actor.
		{Stop{Changes: ChangesRevert, Actor: "bob", At: later}, Stop{Changes: ChangesRevert, Actor: "alice", At: first}},
		// A keep does not override a revert.
		{Stop{Changes: ChangesKeep, Actor: "carol", At: later}, Stop{Changes: ChangesRevert, Actor: "alice", At: first}},
	}
	for i, step := range steps {
		if err := s.Stop(ctx, "run", step.stop); err != nil {
			t.Fatalf("stop %d: %v", i, err)
		}
		got := db.control(t, "run").Stop
		if got == nil || got.Changes != step.want.Changes || got.Actor != step.want.Actor || !got.At.Equal(step.want.At) {
			t.Errorf("after stop %d: control stop = %+v, want %+v", i, got, step.want)
		}
	}
	// The run itself is not written: the workflow applies the stop.
	if state := db.state(t, AppName, "run"); state["open"] != true || state["closed"] != nil {
		t.Errorf("run state = %v, want it open and untouched", state)
	}

	t.Run("revert first", func(t *testing.T) {
		mustStart(t, ctx, s, "revert", 3600)
		if err := s.Stop(ctx, "revert", Stop{Changes: ChangesRevert, Actor: "alice", At: first}); err != nil {
			t.Fatal(err)
		}
		if got := db.control(t, "revert").Stop; got == nil || got.Changes != ChangesRevert {
			t.Errorf("control stop = %+v", got)
		}
	})
}

// requestedProbe is a probe for the run's workload as RequestProbe stores it.
func requestedProbe(id, target string) apstore.RequestedProbe {
	return apstore.RequestedProbe{
		Recommendation: apstore.Recommendation{
			Namespace: "ns", WorkloadKind: "Deployment", WorkloadName: id, ContainerName: "app",
			TransactionID: "42", Language: "java", Target: target, Function: target,
		},
		ChangeID:        "chg_" + id + "_" + target,
		InterrogationID: id,
		Round:           1,
	}
}

func openProbeStore(t *testing.T, dsn string) *apstore.Client {
	t.Helper()
	c, err := apstore.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// wrongExpiries counts the run's approved probes whose expiry is not the
// run's deadline: started_at + the time limit + the extension.
func (d testDB) wrongExpiries(t *testing.T, id string) int {
	t.Helper()
	return d.count(t, `
SELECT count(*) FROM interrogation_requested_probes p
JOIN adk.sessions r ON r.app_name = 'interrogation' AND r.user_id = p.interrogation_id AND r.id = p.interrogation_id
JOIN adk.sessions c ON c.app_name = 'interrogation.ctl' AND c.user_id = r.user_id AND c.id = r.id
WHERE p.interrogation_id = $1 AND p.state = 'approved'
	AND p.expires_at <> (r.state->'run'->>'started_at')::timestamptz
		+ make_interval(secs => (r.state->'request'->'budget'->>'time_limit_seconds')::int
			+ coalesce((c.state->>'extend_seconds')::int, 0))`, id)
}

func TestExtend_MovesProbesWithDeadline(t *testing.T) {
	db := newTestDB(t)
	s := db.replica(t)
	probes := openProbeStore(t, db.dsn)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	mustStart(t, ctx, s, "run", 600)
	markStarted(t, s, "run", now)
	for _, target := range []string{"a.B.c", "a.B.d"} {
		if _, err := probes.InsertRequestedProbe(ctx, requestedProbe("run", target), 5, now); err != nil {
			t.Fatal(err)
		}
	}
	// A probe the user reverted keeps its expiry.
	if _, err := probes.InsertRequestedProbe(ctx, requestedProbe("run", "a.B.e"), 5, now); err != nil {
		t.Fatal(err)
	}
	db.exec(t, `UPDATE interrogation_requested_probes SET state = 'reverted' WHERE change_id = 'chg_run_a.B.e'`)

	for i := 1; i <= 3; i++ {
		if err := s.Extend(ctx, "run", 60, 28800, now); err != nil {
			t.Fatalf("extend %d: %v", i, err)
		}
		if got := db.control(t, "run").ExtendSeconds; got != 60*i {
			t.Errorf("after extend %d: extend_seconds = %d, want %d", i, got, 60*i)
		}
		if n := db.wrongExpiries(t, "run"); n != 0 {
			t.Errorf("after extend %d: %d probes off the run's deadline", i, n)
		}
		// One transaction wrote the extension and the probes.
		if n := db.count(t, `
SELECT count(DISTINCT x) FROM (
	SELECT xmin::text AS x FROM adk.sessions WHERE app_name = 'interrogation.ctl' AND id = 'run'
	UNION ALL SELECT xmin::text FROM interrogation_requested_probes WHERE interrogation_id = 'run' AND state = 'approved') t`); n != 1 {
			t.Errorf("after extend %d: control session and probes written by %d transactions", i, n)
		}
	}
	var reverted time.Time
	if err := db.sql.QueryRow(ctx, `SELECT expires_at FROM interrogation_requested_probes WHERE change_id = 'chg_run_a.B.e'`).Scan(&reverted); err != nil {
		t.Fatal(err)
	}
	if want := now.Add(600 * time.Second); !reverted.Equal(want) {
		t.Errorf("reverted probe expires at %v, want %v unchanged", reverted, want)
	}

	expiresAt := func(t *testing.T, changeID string) time.Time {
		t.Helper()
		var at time.Time
		if err := db.sql.QueryRow(ctx, `SELECT expires_at FROM interrogation_requested_probes WHERE change_id = $1`, changeID).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}

	// A probe already expired has been or is being removed: it is not given
	// the run's time again.
	t.Run("expired probe", func(t *testing.T) {
		mustStart(t, ctx, s, "expired", 600)
		markStarted(t, s, "expired", now)
		if _, err := probes.InsertRequestedProbe(ctx, requestedProbe("expired", "a.B.c"), 5, now); err != nil {
			t.Fatal(err)
		}
		expired := now.Add(-time.Minute)
		db.exec(t, `UPDATE interrogation_requested_probes SET expires_at = $1 WHERE change_id = 'chg_expired_a.B.c'`, expired)
		if err := s.Extend(ctx, "expired", 60, 28800, now); err != nil {
			t.Fatal(err)
		}
		if got := expiresAt(t, "chg_expired_a.B.c"); !got.Equal(expired) {
			t.Errorf("expired probe expires at %v, want %v unchanged", got, expired)
		}
	})

	t.Run("deadline over a day away", func(t *testing.T) {
		mustStart(t, ctx, s, "long", 600)
		markStarted(t, s, "long", now)
		if _, err := probes.InsertRequestedProbe(ctx, requestedProbe("long", "a.B.c"), 5, now); err != nil {
			t.Fatal(err)
		}
		if err := s.Extend(ctx, "long", 90000, 100000, now); err != nil {
			t.Fatal(err)
		}
		if got, want := expiresAt(t, "chg_long_a.B.c"), now.Add(24*time.Hour); !got.Equal(want) {
			t.Errorf("probe expires at %v, want %v", got, want)
		}
	})
}

// The insights tables are found through the search_path the database or the
// role sets, which the DSN does not name.
func TestExtend_DatabaseSearchPath(t *testing.T) {
	for name, set := range map[string]string{
		"database": `ALTER DATABASE %I SET search_path = ins`,
		"role":     `ALTER ROLE current_user IN DATABASE %I SET search_path = ins`,
	} {
		t.Run(name, func(t *testing.T) {
			dsn := pgtest.NewEmptyDatabase(t)
			ctx := context.Background()
			conn := observer(t, dsn)
			for _, q := range []string{
				`CREATE SCHEMA ins`,
				`DO $$ BEGIN EXECUTE format('` + set + `', current_database()); END $$`,
			} {
				if _, err := conn.Exec(ctx, q); err != nil {
					t.Fatalf("%s: %v", q, err)
				}
			}
			if err := migrate.Apply(dsn); err != nil {
				t.Fatalf("migrate: %v", err)
			}
			s := openTestSessions(t, dsn)
			probes := openProbeStore(t, dsn)
			now := time.Now().UTC().Truncate(time.Microsecond)

			mustStart(t, ctx, s, "run", 600)
			markStarted(t, s, "run", now)
			if _, err := probes.InsertRequestedProbe(ctx, requestedProbe("run", "a.B.c"), 5, now); err != nil {
				t.Fatal(err)
			}
			if err := s.Extend(ctx, "run", 60, 28800, now); err != nil {
				t.Fatalf("extend: %v", err)
			}
			var expiresAt time.Time
			if err := conn.QueryRow(ctx, `SELECT expires_at FROM ins.interrogation_requested_probes WHERE change_id = 'chg_run_a.B.c'`).Scan(&expiresAt); err != nil {
				t.Fatal(err)
			}
			if want := now.Add(660 * time.Second); !expiresAt.Equal(want) {
				t.Errorf("probe expires at %v, want %v", expiresAt, want)
			}
		})
	}
}

func TestExtend_RefusedAfterDeadline(t *testing.T) {
	db := newTestDB(t)
	s := db.replica(t)
	probes := openProbeStore(t, db.dsn)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	mustStart(t, ctx, s, "run", 600)
	markStarted(t, s, "run", now.Add(-600*time.Second))
	if _, err := probes.InsertRequestedProbe(ctx, requestedProbe("run", "a.B.c"), 5, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.Extend(ctx, "run", 60, 28800, now); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("extend at the deadline: %v, want ErrNotRunning", err)
	}
	if got := db.control(t, "run").ExtendSeconds; got != 0 {
		t.Errorf("extend_seconds = %d, want 0", got)
	}
	if n := db.wrongExpiries(t, "run"); n != 0 {
		t.Errorf("%d probes moved", n)
	}
	if err := s.Extend(ctx, "run", 60, 28800, now.Add(-time.Second)); err != nil {
		t.Errorf("extend a second before the deadline: %v", err)
	}
}

func TestExtend_Refused(t *testing.T) {
	db := newTestDB(t)
	s := db.replica(t)
	ctx := context.Background()
	now := time.Now()

	t.Run("over the total", func(t *testing.T) {
		mustStart(t, ctx, s, "total", 3600)
		markStarted(t, s, "total", now)
		if err := s.Extend(ctx, "total", 3600, 7200, now); err != nil {
			t.Fatalf("extend to the total: %v", err)
		}
		if err := s.Extend(ctx, "total", 1, 7200, now); !errors.Is(err, ErrOverTotal) {
			t.Errorf("extend over the total: %v, want ErrOverTotal", err)
		}
		if got := db.control(t, "total").ExtendSeconds; got != 3600 {
			t.Errorf("extend_seconds = %d, want 3600", got)
		}
	})

	t.Run("stop recorded", func(t *testing.T) {
		mustStart(t, ctx, s, "stopped", 3600)
		markStarted(t, s, "stopped", now)
		if err := s.Stop(ctx, "stopped", Stop{Changes: ChangesKeep, At: now}); err != nil {
			t.Fatal(err)
		}
		if err := s.Extend(ctx, "stopped", 60, 28800, now); !errors.Is(err, ErrNotRunning) {
			t.Errorf("extend after a stop: %v, want ErrNotRunning", err)
		}
	})

	t.Run("closed", func(t *testing.T) {
		mustStart(t, ctx, s, "closed", 3600)
		markStarted(t, s, "closed", now)
		if err := runnerAppend(ctx, s.Service(), "closed", map[string]any{"open": false}); err != nil {
			t.Fatal(err)
		}
		if err := s.Extend(ctx, "closed", 60, 28800, now); !errors.Is(err, ErrNotRunning) {
			t.Errorf("extend of a closed run: %v, want ErrNotRunning", err)
		}
		if got := db.control(t, "closed").ExtendSeconds; got != 0 {
			t.Errorf("extend_seconds = %d, want 0", got)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		if err := s.Extend(ctx, "missing", 60, 28800, now); !errors.Is(err, session.ErrNotFound) {
			t.Errorf("extend of an unknown run: %v, want session.ErrNotFound", err)
		}
	})

	t.Run("queued", func(t *testing.T) {
		mustStart(t, ctx, s, "queued", 3600)
		if err := s.Extend(ctx, "queued", 60, 28800, now); err != nil {
			t.Fatalf("extend of a queued run: %v", err)
		}
		if got := db.control(t, "queued").ExtendSeconds; got != 60 {
			t.Errorf("extend_seconds = %d, want 60", got)
		}
	})
}

func TestCloseOverdue(t *testing.T) {
	db := newTestDB(t)
	s := db.replica(t)
	ctx := context.Background()
	c := Closed{Reason: "interrupted", Error: "did not finish by its deadline", At: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}

	mustStart(t, ctx, s, "run", 3600)
	markStarted(t, s, "run", time.Now())
	if err := s.CloseOverdue(ctx, "run", c); err != nil {
		t.Fatal(err)
	}
	state := db.state(t, AppName, "run")
	closed, _ := state["closed"].(map[string]any)
	if state["open"] != false || closed["reason"] != "interrupted" || closed["error"] != c.Error {
		t.Errorf("state = %v", state)
	}
	if state["run"] == nil {
		t.Error("the close removed the workflow's state")
	}
	if err := s.Start(ctx, "next", testRequest("run", 3600)); err != nil {
		t.Errorf("start after the close: %v", err)
	}

	const events = `SELECT count(*) FROM adk.events WHERE app_name = 'interrogation' AND session_id = 'run'`
	before := db.count(t, events)
	if err := s.CloseOverdue(ctx, "run", Closed{Reason: "stopped_by_user"}); err != nil {
		t.Fatal(err)
	}
	if after := db.count(t, events); after != before {
		t.Errorf("second close appended %d events", after-before)
	}
	if again := db.state(t, AppName, "run"); !reflect.DeepEqual(again, state) {
		t.Errorf("second close changed the state to %v", again)
	}

	// A rollback meets a run whose workflow state a newer binary wrote.
	t.Run("newer run state", func(t *testing.T) {
		mustStart(t, ctx, s, "newer", 3600)
		if err := runnerAppend(ctx, s.Service(), "newer", map[string]any{"run": map[string]any{"v": 2, "started_at": 1794900000}}); err != nil {
			t.Fatal(err)
		}
		if err := s.CloseOverdue(ctx, "newer", c); err != nil {
			t.Fatal(err)
		}
		state := db.state(t, AppName, "newer")
		run, _ := state["run"].(map[string]any)
		if state["open"] != false || state["closed"] == nil || run["v"] != float64(2) {
			t.Errorf("state = %v", state)
		}
		if err := s.Start(ctx, "newer-next", testRequest("newer", 3600)); err != nil {
			t.Errorf("start after the close: %v", err)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		if err := s.CloseOverdue(ctx, "missing", c); !errors.Is(err, session.ErrNotFound) {
			t.Errorf("CloseOverdue of an unknown run: %v, want session.ErrNotFound", err)
		}
	})

	// A read that fails on reaching the session is not a session ADK cannot
	// load: nothing is written.
	t.Run("read fails", func(t *testing.T) {
		mustStart(t, ctx, s, "locked", 3600)
		const row = `SELECT state::text || update_time::text FROM adk.sessions WHERE app_name = 'interrogation' AND id = 'locked'`
		var before string
		if err := db.sql.QueryRow(ctx, row).Scan(&before); err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(db.dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("lock_timeout", "100ms")
		u.RawQuery = q.Encode()
		impatient := openTestSessions(t, u.String())

		tx, err := db.sql.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `LOCK TABLE adk.events IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatal(err)
		}
		err = impatient.CloseOverdue(ctx, "locked", c)
		if code, _ := pgCode(err); code != "55P03" {
			t.Errorf("CloseOverdue while the read waits on a lock: %v, want SQLSTATE 55P03", err)
		}
		_ = tx.Rollback(ctx)

		var after string
		if err := db.sql.QueryRow(ctx, row).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if after != before {
			t.Errorf("session row changed from %s to %s", before, after)
		}
	})
}

func TestSupervisor_ClosesUnreadableSession(t *testing.T) {
	db := newTestDB(t)
	s := db.replica(t)
	ctx := context.Background()

	mustStart(t, ctx, s, "run", 3600)
	markStarted(t, s, "run", time.Now())
	var updated time.Time
	if err := db.sql.QueryRow(ctx, `SELECT update_time FROM adk.sessions WHERE app_name = 'interrogation' AND id = 'run'`).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	db.exec(t, `UPDATE adk.events SET actions = convert_to('not json', 'UTF8') WHERE app_name = 'interrogation' AND session_id = 'run'`)
	if _, err := s.Service().Get(ctx, &session.GetRequest{AppName: AppName, UserID: "run", SessionID: "run"}); err == nil {
		t.Fatal("ADK still loads the session; the test corrupts nothing")
	}

	if err := s.CloseOverdue(ctx, "run", Closed{Reason: "interrupted", Error: "unreadable"}); err != nil {
		t.Fatal(err)
	}
	state := db.state(t, AppName, "run")
	closed, _ := state["closed"].(map[string]any)
	if state["open"] != false || closed["reason"] != "interrupted" || state["run"] == nil || state["workload"] != "ns/Deployment/run" {
		t.Errorf("state = %v", state)
	}
	var after time.Time
	if err := db.sql.QueryRow(ctx, `SELECT update_time FROM adk.sessions WHERE app_name = 'interrogation' AND id = 'run'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.After(updated) {
		t.Errorf("update_time %v not after %v: a copy a runner holds would not be stale", after, updated)
	}
	if err := s.Start(ctx, "next", testRequest("run", 3600)); err != nil {
		t.Errorf("start after the close: %v", err)
	}
	// Closed already: the statement matches nothing.
	if err := s.CloseOverdue(ctx, "run", Closed{Reason: "stopped_by_user"}); err != nil {
		t.Fatal(err)
	}
	if again := db.state(t, AppName, "run"); !reflect.DeepEqual(again, state) {
		t.Errorf("second close changed the state to %v", again)
	}
}

// Two runners holding one session append at once: ADK refuses one, and the
// other's write is kept.
func TestSessions_ConcurrentAppendRefused(t *testing.T) {
	db := newTestDB(t)
	replicas := []*Sessions{db.replica(t), db.replica(t)}
	ctx := context.Background()

	for i := range 20 {
		id := fmt.Sprintf("run-%d", i)
		mustStart(t, ctx, replicas[0], id, 3600)
		var copies [2]session.Session
		for j, s := range replicas {
			got, err := s.Service().Get(ctx, &session.GetRequest{AppName: AppName, UserID: id, SessionID: id})
			if err != nil {
				t.Fatal(err)
			}
			copies[j] = got.Session
		}
		var errs [2]error
		var wg sync.WaitGroup
		for j, s := range replicas {
			wg.Go(func() {
				ev := session.NewEvent(ctx, "runner")
				ev.Actions.StateDelta = map[string]any{"run": map[string]any{"writer": j}}
				errs[j] = s.Service().AppendEvent(ctx, copies[j], ev)
			})
		}
		wg.Wait()

		accepted := -1
		for j, err := range errs {
			switch code, _ := pgCode(err); {
			case err == nil:
				accepted = j
			case code == "40001" || strings.Contains(err.Error(), "stale session error"):
			default:
				t.Fatalf("trial %d: writer %d: %v", i, j, err)
			}
		}
		if accepted < 0 || errs[0] == nil && errs[1] == nil {
			t.Fatalf("trial %d: errors %v, want exactly one refused", i, errs)
		}
		run, _ := db.state(t, AppName, id)["run"].(map[string]any)
		if run["writer"] != float64(accepted) {
			t.Fatalf("trial %d: stored run %v, want writer %d's", i, run, accepted)
		}
	}
}
