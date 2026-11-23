package store_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/adk/v2/session"

	"github.com/odigos-io/odigos/insights/internal/autopilot/store"
	"github.com/odigos-io/odigos/insights/internal/db/postgres/pgtest"
	"github.com/odigos-io/odigos/insights/internal/interrogation"
)

// requestedEnv is a database of its own with the loop's store and ADK's
// session store on it. Runs are written through ADK's session service, as the
// start handler and the workflow write them.
type requestedEnv struct {
	db       *store.Client
	sessions session.Service
	dsn      string
}

func newRequestedEnv(t *testing.T) requestedEnv {
	t.Helper()
	dsn := pgtest.NewDatabase(t)
	s, err := interrogation.OpenSessions(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return requestedEnv{db: openClient(t, dsn), sessions: s.Service(), dsn: dsn}
}

// startRun creates the run's control session and its session as a start
// does, then records started_at as the workflow's first node does. A zero
// limitSeconds leaves the time limit out of the request.
func (e requestedEnv) startRun(t *testing.T, id string, startedAt time.Time, limitSeconds int) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.sessions.Create(ctx, &session.CreateRequest{AppName: "interrogation.ctl", UserID: id, SessionID: id}); err != nil {
		t.Fatal(err)
	}
	budget := map[string]any{"max_probes": 5}
	if limitSeconds > 0 {
		budget["time_limit_seconds"] = limitSeconds
	}
	if _, err := e.sessions.Create(ctx, &session.CreateRequest{
		AppName: "interrogation", UserID: id, SessionID: id,
		State: map[string]any{
			"workload": "ns/Deployment/" + id,
			"open":     true,
			"request": map[string]any{
				"source": map[string]any{"namespace": "ns", "kind": "Deployment", "name": id},
				"budget": budget,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	e.appendState(t, "interrogation", id, map[string]any{"run": map[string]any{"started_at": startedAt}})
}

func (e requestedEnv) appendState(t *testing.T, app, id string, delta map[string]any) {
	t.Helper()
	ctx := context.Background()
	got, err := e.sessions.Get(ctx, &session.GetRequest{AppName: app, UserID: id, SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	ev := session.NewEvent(ctx, "test")
	ev.Author = "test"
	ev.Actions.StateDelta = delta
	if err := e.sessions.AppendEvent(ctx, got.Session, ev); err != nil {
		t.Fatal(err)
	}
}

func (e requestedEnv) count(t *testing.T, runID string) int {
	t.Helper()
	var n int
	if err := pgtest.Connect(t, e.dsn).QueryRow(context.Background(),
		`SELECT count(*) FROM interrogation_requested_probes WHERE interrogation_id = $1`, runID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e requestedEnv) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := pgtest.Connect(t, e.dsn).Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func requestedProbe(runID, target string) store.RequestedProbe {
	dot := strings.LastIndexByte(target, '.')
	return store.RequestedProbe{
		Recommendation: store.Recommendation{
			Namespace: "ns", WorkloadKind: "Deployment", WorkloadName: runID, ContainerName: "app",
			TransactionID: "42", Language: "java", Target: target,
			Category: "performance", Function: target, Why: "slow",
			ClassName: target[:dot], MethodName: target[dot+1:],
			AnalysisRevision: "interrogation=1/test",
		},
		ChangeID:        "chg_" + runID + "_" + target,
		InterrogationID: runID,
		Round:           1,
	}
}

// testNow is microsecond precision, as Postgres stores it.
func testNow() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

func TestInsertRequestedProbe_Idempotent(t *testing.T) {
	e := newRequestedEnv(t)
	ctx := context.Background()
	now := testNow()
	e.startRun(t, "run-1", now, 3600)

	p := requestedProbe("run-1", "com.acme.Auth.check")
	first, err := e.db.InsertRequestedProbe(ctx, p, 5, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != store.ProbeApproved || first.InRule || !first.FirstSeen.Equal(now) || !first.UpdatedAt.Equal(now) {
		t.Fatalf("stored = %+v", first)
	}
	want := p.Recommendation
	want.FirstSeen = first.FirstSeen
	if first.Recommendation != want {
		t.Fatalf("probe columns = %+v, want %+v", first.Recommendation, want)
	}
	if first.ChangeID != p.ChangeID || first.InterrogationID != "run-1" || first.Round != 1 {
		t.Fatalf("request columns = %+v", first)
	}

	p.Why = "asked again"
	again, err := e.db.InsertRequestedProbe(ctx, p, 5, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if again.ChangeID != first.ChangeID || again.Why != "slow" || !again.UpdatedAt.Equal(now) {
		t.Fatalf("second request = %+v, want the stored row", again)
	}
	if n := e.count(t, "run-1"); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

func TestInsertRequestedProbe_BudgetConcurrent(t *testing.T) {
	e := newRequestedEnv(t)
	now := testNow()
	e.startRun(t, "run-1", now, 3600)
	// A second client stands in for an overlapping leader.
	other := openClient(t, e.dsn)

	const budget, inserters = 3, 12
	var wg sync.WaitGroup
	errs := make([]error, inserters)
	for i := range inserters {
		c := e.db
		if i%2 == 1 {
			c = other
		}
		wg.Go(func() {
			p := requestedProbe("run-1", "com.acme.P.m"+string(rune('a'+i)))
			_, errs[i] = c.InsertRequestedProbe(context.Background(), p, budget, now)
		})
	}
	wg.Wait()
	var ok, used int
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, store.ErrRunBudgetUsed):
			used++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != budget || used != inserters-budget {
		t.Fatalf("accepted %d, refused %d; want %d and %d", ok, used, budget, inserters-budget)
	}
	if n := e.count(t, "run-1"); n != budget {
		t.Fatalf("rows = %d, want %d", n, budget)
	}
}

func TestInsertRequestedProbe_RunNotOpen(t *testing.T) {
	ctx := context.Background()
	now := testNow()
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, e requestedEnv)
		wantErr error
	}{
		{"unknown run", func(*testing.T, requestedEnv) {}, store.ErrRunNotOpen},
		{"closed run", func(t *testing.T, e requestedEnv) {
			e.startRun(t, "run-1", now, 3600)
			e.appendState(t, "interrogation", "run-1", map[string]any{"open": false})
		}, store.ErrRunNotOpen},
		{"stopped with revert", func(t *testing.T, e requestedEnv) {
			e.startRun(t, "run-1", now, 3600)
			e.appendState(t, "interrogation.ctl", "run-1", map[string]any{"stop": map[string]any{"changes": "revert", "actor": "a"}})
		}, store.ErrRunNotOpen},
		{"stopped with keep", func(t *testing.T, e requestedEnv) {
			e.startRun(t, "run-1", now, 3600)
			e.appendState(t, "interrogation.ctl", "run-1", map[string]any{"stop": map[string]any{"changes": "keep", "actor": "a"}})
		}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRequestedEnv(t)
			tc.prepare(t, e)
			_, err := e.db.InsertRequestedProbe(ctx, requestedProbe("run-1", "com.acme.Auth.check"), 5, now)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			want := 0
			if tc.wantErr == nil {
				want = 1
			}
			if n := e.count(t, "run-1"); n != want {
				t.Fatalf("rows = %d, want %d", n, want)
			}
		})
	}
}

// A write to the control session in flight (a stop, an extend) holds its row.
// The insert waits for it and decides on what it committed.
func TestInsertRequestedProbe_WaitsForControlWrite(t *testing.T) {
	e := newRequestedEnv(t)
	ctx := context.Background()
	now := testNow()
	e.startRun(t, "run-1", now, 3600)

	// Stands in for ADK's append of a stop with revert, which updates the row
	// the same way but cannot be held open from a test.
	tx, err := pgtest.Connect(t, e.dsn).Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
UPDATE adk.sessions SET state = state || '{"stop": {"changes": "revert"}}'
WHERE app_name = 'interrogation.ctl' AND user_id = $1 AND id = $1`, "run-1"); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := e.db.InsertRequestedProbe(ctx, requestedProbe("run-1", "com.acme.Auth.check"), 5, now)
		done <- err
	}()
	watch := pgtest.Connect(t, e.dsn)
	for deadline := time.Now().Add(5 * time.Second); ; {
		var waiting int
		if err := watch.QueryRow(ctx, `
SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("insert returned %v without waiting for the control write", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("insert is not waiting on a lock")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, store.ErrRunNotOpen) {
		t.Fatalf("err = %v, want %v", err, store.ErrRunNotOpen)
	}
	if n := e.count(t, "run-1"); n != 0 {
		t.Fatalf("rows = %d, want 0", n)
	}
}

func TestInsertRequestedProbe_ExpiresAtRunDeadline(t *testing.T) {
	ctx := context.Background()
	now := testNow()
	started := now.Add(-10 * time.Minute)

	t.Run("limit and extension", func(t *testing.T) {
		e := newRequestedEnv(t)
		e.startRun(t, "run-1", started, 3600)
		got, err := e.db.InsertRequestedProbe(ctx, requestedProbe("run-1", "com.acme.A.a"), 5, now)
		if err != nil {
			t.Fatal(err)
		}
		if want := started.Add(time.Hour); !got.ExpiresAt.Equal(want) {
			t.Fatalf("expires_at = %v, want %v", got.ExpiresAt, want)
		}

		e.appendState(t, "interrogation.ctl", "run-1", map[string]any{"extend_seconds": 600})
		got, err = e.db.InsertRequestedProbe(ctx, requestedProbe("run-1", "com.acme.B.b"), 5, now)
		if err != nil {
			t.Fatal(err)
		}
		if want := started.Add(70 * time.Minute); !got.ExpiresAt.Equal(want) {
			t.Fatalf("expires_at after extend = %v, want %v", got.ExpiresAt, want)
		}
	})

	t.Run("clamped to a day", func(t *testing.T) {
		e := newRequestedEnv(t)
		e.startRun(t, "run-1", started, 3*24*3600)
		got, err := e.db.InsertRequestedProbe(ctx, requestedProbe("run-1", "com.acme.A.a"), 5, now)
		if err != nil {
			t.Fatal(err)
		}
		if want := now.Add(24 * time.Hour); !got.ExpiresAt.Equal(want) {
			t.Fatalf("expires_at = %v, want %v", got.ExpiresAt, want)
		}
	})

	// With no deadline to read, the probe lives a day, as with a run whose
	// start is not recorded yet, rather than being stored already expired.
	t.Run("no time limit", func(t *testing.T) {
		e := newRequestedEnv(t)
		e.startRun(t, "run-1", now.Add(-time.Minute), 0)
		got, err := e.db.InsertRequestedProbe(ctx, requestedProbe("run-1", "com.acme.A.a"), 5, now)
		if err != nil {
			t.Fatal(err)
		}
		if want := now.Add(24 * time.Hour); !got.ExpiresAt.Equal(want) {
			t.Fatalf("expires_at = %v, want %v", got.ExpiresAt, want)
		}
	})
}

func get(t *testing.T, e requestedEnv, id string) store.RequestedProbe {
	t.Helper()
	p, ok, err := e.db.GetRequestedProbe(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("get %s: ok=%v err=%v", id, ok, err)
	}
	return p
}

func TestSettleRequestedProbes(t *testing.T) {
	e := newRequestedEnv(t)
	ctx := context.Background()
	now := testNow()
	e.startRun(t, "run-1", now, 3600)
	a, err := e.db.InsertRequestedProbe(ctx, requestedProbe("run-1", "com.acme.A.a"), 5, now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.db.InsertRequestedProbe(ctx, requestedProbe("run-1", "com.acme.B.b"), 5, now)
	if err != nil {
		t.Fatal(err)
	}
	read := []store.RequestedProbe{a, b}

	t1 := now.Add(time.Second)
	if err := e.db.SettleRequestedProbes(ctx, read, []string{a.ChangeID}, map[string]string{b.ChangeID: "workload_cap_reached"}, t1); err != nil {
		t.Fatal(err)
	}
	if got := get(t, e, a.ChangeID); !got.InRule || !got.AppliedAt.Equal(t1) || got.BlockReason != "" {
		t.Fatalf("placed row = in_rule %v applied_at %v block %q", got.InRule, got.AppliedAt, got.BlockReason)
	}
	if got := get(t, e, b.ChangeID); got.InRule || got.BlockReason != "workload_cap_reached" {
		t.Fatalf("blocked row = in_rule %v block %q", got.InRule, got.BlockReason)
	}

	t2 := t1.Add(time.Second)
	if err := e.db.SettleRequestedProbes(ctx, read, []string{a.ChangeID, b.ChangeID}, nil, t2); err != nil {
		t.Fatal(err)
	}
	if got := get(t, e, a.ChangeID); !got.AppliedAt.Equal(t1) {
		t.Fatalf("applied_at of a row already in the rule moved to %v", got.AppliedAt)
	}
	if got := get(t, e, b.ChangeID); !got.InRule || !got.AppliedAt.Equal(t2) || got.BlockReason != "" {
		t.Fatalf("row placed later = in_rule %v applied_at %v block %q", got.InRule, got.AppliedAt, got.BlockReason)
	}

	// a is reverted by a person; the next pass reads it (it is still in the
	// rule) and does not place it.
	e.exec(t, `UPDATE interrogation_requested_probes SET state = 'reverted', end_reason = 'revert' WHERE change_id = $1`, a.ChangeID)
	t3 := t2.Add(time.Second)
	if err := e.db.SettleRequestedProbes(ctx, read, []string{b.ChangeID}, nil, t3); err != nil {
		t.Fatal(err)
	}
	got := get(t, e, a.ChangeID)
	if got.InRule || !got.RemovedAt.Equal(t3) || got.State != store.ProbeReverted {
		t.Fatalf("ended row = in_rule %v removed_at %v state %s", got.InRule, got.RemovedAt, got.State)
	}
	listed, err := e.db.ListRequestedProbes(ctx, t3)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ChangeID != b.ChangeID {
		t.Fatalf("listed after settle = %v, want only %s", changeIDs(listed), b.ChangeID)
	}

	// Once b is flagged for a later write, a settle or flag from the older
	// read leaves it alone.
	if ok, err := e.db.MarkRequestedProbesBeingPlaced(ctx, listed); !ok || err != nil {
		t.Fatalf("mark = %v, %v", ok, err)
	}
	if err := e.db.SettleRequestedProbes(ctx, read, nil, nil, t3.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := get(t, e, b.ChangeID); !got.InRule || !got.BeingPlaced || got.PlaceGen != 1 {
		t.Fatalf("row after a stale settle = in_rule %v being_placed %v place_gen %d", got.InRule, got.BeingPlaced, got.PlaceGen)
	}
	if ok, err := e.db.MarkRequestedProbesBeingPlaced(ctx, listed); ok || err != nil {
		t.Fatalf("stale mark = %v, %v, want false", ok, err)
	}

	// A pass that does not place b keeps a fresh flag, since the flagging
	// write may still land, and clears it once it is old.
	flagged := []store.RequestedProbe{get(t, e, b.ChangeID)}
	if err := e.db.SettleRequestedProbes(ctx, flagged, nil, nil, t3); err != nil {
		t.Fatal(err)
	}
	if got := get(t, e, b.ChangeID); got.InRule || !got.BeingPlaced {
		t.Fatalf("row after a settle without it = in_rule %v being_placed %v", got.InRule, got.BeingPlaced)
	}
	e.exec(t, `UPDATE interrogation_requested_probes SET being_placed_at = now() - interval '6 minutes' WHERE change_id = $1`, b.ChangeID)
	if err := e.db.SettleRequestedProbes(ctx, flagged, nil, nil, t3); err != nil {
		t.Fatal(err)
	}
	if got := get(t, e, b.ChangeID); got.BeingPlaced {
		t.Fatal("old flag not cleared")
	}
}

func changeIDs(ps []store.RequestedProbe) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.ChangeID)
	}
	return out
}

func TestListRequestedProbes(t *testing.T) {
	e := newRequestedEnv(t)
	ctx := context.Background()
	now := testNow()
	for _, run := range []string{"run-b", "run-a"} {
		e.startRun(t, run, now, 3600)
	}
	insert := func(run, target string, firstSeen time.Time) store.RequestedProbe {
		t.Helper()
		p := requestedProbe(run, target)
		p.FirstSeen = firstSeen
		got, err := e.db.InsertRequestedProbe(ctx, p, 10, now)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	newer := insert("run-a", "com.acme.A.newer", now.Add(time.Second))
	older := insert("run-a", "com.acme.A.older", now)
	otherWorkload := insert("run-b", "com.acme.B.b", now)
	expired := insert("run-a", "com.acme.A.expired", now.Add(3*time.Second))
	expiredInRule := insert("run-a", "com.acme.A.inrule", now.Add(4*time.Second))
	reverted := insert("run-a", "com.acme.A.reverted", now.Add(5*time.Second))

	e.exec(t, `UPDATE interrogation_requested_probes SET expires_at = $2 WHERE change_id = ANY($1)`,
		[]string{expired.ChangeID, expiredInRule.ChangeID}, now)
	e.exec(t, `UPDATE interrogation_requested_probes SET in_rule = true WHERE change_id = $1`, expiredInRule.ChangeID)
	e.exec(t, `UPDATE interrogation_requested_probes SET state = 'reverted' WHERE change_id = $1`, reverted.ChangeID)

	got, err := e.db.ListRequestedProbes(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{older.ChangeID, newer.ChangeID, expiredInRule.ChangeID, otherWorkload.ChangeID}
	if strings.Join(changeIDs(got), " ") != strings.Join(want, " ") {
		t.Fatalf("listed = %v, want %v", changeIDs(got), want)
	}
	for _, p := range got {
		if p.ChangeID == expiredInRule.ChangeID && p.Live(now) {
			t.Fatal("expired row reported live")
		}
	}
}

func TestEndWorkloadProbes(t *testing.T) {
	e := newRequestedEnv(t)
	ctx := context.Background()
	now := testNow()
	e.startRun(t, "run-1", now, 3600)
	e.startRun(t, "run-2", now, 3600)
	approved, _ := e.db.InsertRequestedProbe(ctx, requestedProbe("run-1", "com.acme.A.a"), 5, now)
	reverted, _ := e.db.InsertRequestedProbe(ctx, requestedProbe("run-1", "com.acme.A.reverted"), 5, now)
	other, err := e.db.InsertRequestedProbe(ctx, requestedProbe("run-2", "com.acme.A.a"), 5, now)
	if err != nil {
		t.Fatal(err)
	}
	e.exec(t, `UPDATE interrogation_requested_probes SET state = 'reverted', end_reason = 'stop' WHERE change_id = $1`, reverted.ChangeID)

	later := now.Add(time.Minute)
	if err := e.db.EndWorkloadProbes(ctx, store.SourceKey{Namespace: "ns", WorkloadKind: "Deployment", WorkloadName: "run-1"}, later); err != nil {
		t.Fatal(err)
	}
	if got := get(t, e, approved.ChangeID); got.State != store.ProbeReverted || got.EndReason != "source_gone" || !got.UpdatedAt.Equal(later) {
		t.Errorf("approved row = state %s end_reason %s updated_at %v", got.State, got.EndReason, got.UpdatedAt)
	}
	if got := get(t, e, reverted.ChangeID); got.EndReason != "stop" {
		t.Errorf("already reverted row got end_reason %s", got.EndReason)
	}
	if got := get(t, e, other.ChangeID); got.State != store.ProbeApproved {
		t.Errorf("other workload's row = %s", got.State)
	}
}
