package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/adk/v2/platform"
	"google.golang.org/adk/v2/session"

	apstore "github.com/odigos-io/odigos/insights/internal/autopilot/store"
	"github.com/odigos-io/odigos/insights/internal/db/postgres"
	"github.com/odigos-io/odigos/insights/internal/db/postgres/pgtest"
	"github.com/odigos-io/odigos/insights/internal/engine"
	"github.com/odigos-io/odigos/insights/internal/interrogation"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

// interrogationDB is a migrated database of the test's own. Runs are written
// by the session store and ADK's session service, never with SQL.
type interrogationDB struct {
	repo     *postgres.Repo
	sessions *interrogation.Sessions
	probes   *apstore.Client
	sql      *pgxpool.Pool
}

func newInterrogationDB(t *testing.T) interrogationDB {
	t.Helper()
	dsn := pgtest.NewDatabase(t)
	ctx := context.Background()
	r, err := postgres.Open(dsn, engine.NewBaselineCodec())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	s, err := interrogation.OpenSessions(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	probes, err := apstore.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = probes.Close() })
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return interrogationDB{repo: r, sessions: s, probes: probes, sql: pool}
}

func interrogationRequest(name string) repo.InterrogationRequest {
	return repo.InterrogationRequest{
		Source:       repo.InterrogationSource{Namespace: "ns", Kind: "Deployment", Name: name},
		Goal:         "observability",
		Question:     "why is " + name + " slow?",
		AllowChanges: []string{"custom_instrumentation"},
		Budget:       repo.InterrogationBudget{MaxProbes: 20, TimeLimitSeconds: 3600, MaxRounds: 5},
		Trigger:      repo.InterrogationTrigger{Kind: "manual", Actor: "alice"},
	}
}

// start stores a queued run of workload ns/Deployment/<workload>.
func (d interrogationDB) start(t *testing.T, ctx context.Context, id, workload string) {
	t.Helper()
	if err := d.sessions.Start(ctx, id, interrogationRequest(workload)); err != nil {
		t.Fatalf("start %s: %v", id, err)
	}
}

// appendRun appends an event to the run's session as the workflow's runner
// does, with items as its API events.
func (d interrogationDB) appendRun(t *testing.T, id string, delta map[string]any, items ...map[string]any) {
	t.Helper()
	d.appendRunAt(t, id, time.Now(), delta, items...)
}

// appendRunAt is appendRun with the event stamped at; ADK sets the session's
// update_time to the stamp of its last event.
func (d interrogationDB) appendRunAt(t *testing.T, id string, at time.Time, delta map[string]any, items ...map[string]any) {
	t.Helper()
	ctx := context.Background()
	svc := d.sessions.Service()
	got, err := svc.Get(ctx, &session.GetRequest{AppName: interrogation.AppName, UserID: id, SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	ev := session.NewEvent(ctx, "runner")
	ev.Author = "interrogation"
	ev.Timestamp = at
	ev.Actions.StateDelta = delta
	if len(items) > 0 {
		ev.CustomMetadata = map[string]any{"interrogation": items, "interrogation_seq": items[len(items)-1]["seq"]}
	}
	if err := svc.AppendEvent(ctx, got.Session, ev); err != nil {
		t.Fatalf("append to %s: %v", id, err)
	}
}

func (d interrogationDB) markStarted(t *testing.T, id string, at time.Time) {
	t.Helper()
	d.appendRun(t, id, map[string]any{"run": map[string]any{"status": "running", "started_at": at}})
}

// end appends the workflow's last event: its terminal state and open false.
func (d interrogationDB) end(t *testing.T, id string, run map[string]any) {
	t.Helper()
	d.appendRun(t, id, map[string]any{"run": run, "open": false})
}

// request stores a probe of the run's workload as RequestProbe does and
// returns its change id.
func (d interrogationDB) request(t *testing.T, id, workload, target string, now time.Time) string {
	t.Helper()
	p := apstore.RequestedProbe{
		Recommendation: apstore.Recommendation{
			Namespace: "ns", WorkloadKind: "Deployment", WorkloadName: workload, ContainerName: "app",
			TransactionID: "42", Language: "java", Target: target, Function: target,
		},
		ChangeID:        "chg_" + id + "_" + target,
		InterrogationID: id,
		Round:           1,
	}
	got, err := d.probes.InsertRequestedProbe(context.Background(), p, 20, now)
	if err != nil {
		t.Fatalf("request %s on %s: %v", target, id, err)
	}
	return got.ChangeID
}

func (d interrogationDB) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := d.sql.Exec(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

// setChange sets columns of one change row; the reconciler and the workflow
// write these.
func (d interrogationDB) setChange(t *testing.T, changeID, set string, args ...any) {
	t.Helper()
	d.exec(t, `UPDATE interrogation_requested_probes SET `+set+` WHERE change_id = $1`, append([]any{changeID}, args...)...)
}

// planUses reports whether the plan of query, with sequential scans off,
// reads index.
func (d interrogationDB) planUses(t *testing.T, index, query string, args ...any) bool {
	t.Helper()
	var plan []string
	err := pgx.BeginFunc(context.Background(), d.sql, func(tx pgx.Tx) error {
		ctx := context.Background()
		if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `EXPLAIN `+query, args...)
		if err != nil {
			return err
		}
		plan, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(plan, "\n"), index) {
		t.Logf("plan:\n%s", strings.Join(plan, "\n"))
		return false
	}
	return true
}

func mustGet(t *testing.T, r *postgres.Repo, id string, now time.Time) repo.Interrogation {
	t.Helper()
	in, ok, err := r.GetInterrogation(context.Background(), id, now)
	if err != nil || !ok {
		t.Fatalf("get %s: ok=%v err=%v", id, ok, err)
	}
	return in
}

func TestGetInterrogation(t *testing.T) {
	d := newInterrogationDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	if _, ok, err := d.repo.GetInterrogation(ctx, "unknown", now); ok || err != nil {
		t.Fatalf("unknown run: ok=%v err=%v", ok, err)
	}

	d.start(t, ctx, "run", "app")
	in := mustGet(t, d.repo, "run", now)
	if status, _, _ := in.Status(); status != "queued" || !in.Open || in.Started || in.Run != nil {
		t.Errorf("queued run: status %q, open %v, started %v, run %s", status, in.Open, in.Started, in.Run)
	}
	if want := interrogationRequest("app"); in.Request.Source != want.Source || in.Request.Question != want.Question ||
		in.Request.Budget != want.Budget || in.Request.Trigger != want.Trigger {
		t.Errorf("request = %+v, want %+v", in.Request, want)
	}
	if in.CreatedAt.IsZero() || in.Closed != nil || in.Control.Stop != nil {
		t.Errorf("queued run = %+v", in)
	}

	d.markStarted(t, "run", now)
	d.appendRun(t, "run", map[string]any{"run": map[string]any{"status": "running", "started_at": now, "seq": 7}})
	if err := d.sessions.Extend(ctx, "run", 600, 28800, now); err != nil {
		t.Fatal(err)
	}
	in = mustGet(t, d.repo, "run", now)
	if status, _, _ := in.Status(); status != "running" || !in.Started || in.Seq != 7 || in.Control.ExtendSeconds != 600 {
		t.Errorf("running run: status %q, started %v, seq %d, control %+v", status, in.Started, in.Seq, in.Control)
	}
	if !strings.Contains(string(in.Run), `"seq": 7`) && !strings.Contains(string(in.Run), `"seq":7`) {
		t.Errorf("run = %s, want the workflow's state", in.Run)
	}

	if err := d.sessions.Stop(ctx, "run", interrogation.Stop{Changes: interrogation.ChangesRevert, Actor: "bob", At: now}); err != nil {
		t.Fatal(err)
	}
	in = mustGet(t, d.repo, "run", now)
	if status, _, _ := in.Status(); status != "stopping" || in.Control.Stop == nil || in.Control.Stop.Actor != "bob" {
		t.Errorf("stopped run: status %q, control %+v", status, in.Control)
	}

	d.end(t, "run", map[string]any{"status": "stopped", "end_reason": "stopped_by_user", "started_at": now, "seq": 9})
	in = mustGet(t, d.repo, "run", now)
	if status, reason, _ := in.Status(); status != "stopped" || reason != "stopped_by_user" || in.Open || in.Seq != 9 {
		t.Errorf("ended run: status %q, reason %q, open %v, seq %d", status, reason, in.Open, in.Seq)
	}

	d.start(t, ctx, "overdue", "other")
	d.markStarted(t, "overdue", now)
	if err := d.sessions.CloseOverdue(ctx, "overdue", interrogation.Closed{Reason: "interrupted", Error: "late", At: now}); err != nil {
		t.Fatal(err)
	}
	in = mustGet(t, d.repo, "overdue", now)
	if status, reason, errText := in.Status(); status != "failed" || reason != "interrupted" || errText != "late" || in.Closed == nil || !in.Closed.At.Equal(now) {
		t.Errorf("overdue run: %q %q %q, closed %+v", status, reason, errText, in.Closed)
	}
}

// One case per counter: each stored change is in exactly one wire state, and
// the counts agree with State(now) of the rows.
func TestGetInterrogation_ChangeCounts(t *testing.T) {
	d := newInterrogationDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	d.start(t, ctx, "run", "app")
	d.markStarted(t, "run", now)

	ids := map[string]string{}
	for _, name := range []string{"requested", "blocked", "active", "kept", "reverting", "failed", "reverted", "expired"} {
		ids[name] = d.request(t, "run", "app", "a.B."+name, now)
	}
	d.setChange(t, ids["blocked"], `block_reason = 'rule_disabled'`)
	d.setChange(t, ids["active"], `in_rule = true`)
	d.setChange(t, ids["kept"], `state = 'kept', expires_at = 'epoch', in_rule = true`)
	d.setChange(t, ids["reverting"], `state = 'reverted', end_reason = 'stop', in_rule = true`)
	d.setChange(t, ids["failed"], `state = 'reverted', end_reason = 'not_applied_in_time'`)
	d.setChange(t, ids["reverted"], `state = 'reverted', end_reason = 'stop'`)
	d.setChange(t, ids["expired"], `expires_at = $2`, now.Add(-time.Minute))

	got := mustGet(t, d.repo, "run", now).Changes
	for _, tc := range []struct {
		counter   string
		got, want int
	}{
		{"stored", got.Stored, 8},
		{"live", got.Live, 4},
		{"reverting", got.Reverting, 1},
		{"reverted", got.Reverted, 1},
		{"expired", got.Expired, 1},
		{"failed", got.Failed, 1},
	} {
		t.Run(tc.counter, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("%s = %d, want %d", tc.counter, tc.got, tc.want)
			}
		})
	}
	if want := now.Add(time.Hour); !got.NextExpiryAt.Equal(want) {
		t.Errorf("next expiry %v, want %v", got.NextExpiryAt, want)
	}

	changes, err := d.repo.ListInterrogationChanges(ctx, "run")
	if err != nil {
		t.Fatal(err)
	}
	tally := map[string]int{}
	for _, c := range changes {
		tally[c.State(now)]++
	}
	derived := repo.InterrogationChangeCounts{
		Stored:    len(changes),
		Live:      tally["requested"] + tally["blocked"] + tally["active"],
		Reverting: tally["reverting"], Reverted: tally["reverted"], Expired: tally["expired"], Failed: tally["failed"],
	}
	got.NextExpiryAt = time.Time{}
	if got != derived {
		t.Errorf("counts %+v, from State(now) %+v", got, derived)
	}

	d.start(t, ctx, "none", "empty")
	if c := mustGet(t, d.repo, "none", now).Changes; c != (repo.InterrogationChangeCounts{}) {
		t.Errorf("a run with no change counts %+v", c)
	}
}

func TestListInterrogations(t *testing.T) {
	d := newInterrogationDB(t)
	ctx := context.Background()
	t1 := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Minute)
	at := func(ts time.Time) context.Context {
		return platform.WithTimeProvider(ctx, func() time.Time { return ts })
	}
	// Runs that share a create_time are ordered by id.
	for i, id := range []string{"b", "a", "e", "c", "d"} {
		ts := t1
		if i >= 2 {
			ts = t2
		}
		d.start(t, at(ts), id, "w"+id)
	}
	want := []string{"e", "d", "c", "b", "a"}
	now := time.Now()

	all, err := d.repo.ListInterrogations(ctx, "", 50, now)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, in := range all {
		got = append(got, in.ID)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("list = %v, want %v", got, want)
	}

	for _, size := range []int{1, 2, 3} {
		var paged []string
		before := ""
		for range 10 {
			page, err := d.repo.ListInterrogations(ctx, before, size, now)
			if err != nil {
				t.Fatal(err)
			}
			for _, in := range page {
				paged = append(paged, in.ID)
			}
			if len(page) < size {
				break
			}
			before = page[len(page)-1].ID
		}
		if !slices.Equal(paged, want) {
			t.Errorf("pages of %d = %v, want %v", size, paged, want)
		}
	}

	if page, err := d.repo.ListInterrogations(ctx, "gone", 50, now); err != nil || len(page) != 0 {
		t.Errorf("before an unknown id: %d runs, err %v", len(page), err)
	}

	q, a := postgres.ListInterrogationsQuery("c", 2, now)
	if !d.planUses(t, "interrogation_created_idx", q, a...) {
		t.Error("the list does not read interrogation_created_idx")
	}
}

func TestOpenInterrogations(t *testing.T) {
	d := newInterrogationDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	// Created against id order: the list is oldest first, not by id.
	d.start(t, ctx, "running", "r")
	d.start(t, ctx, "queued", "q")
	d.markStarted(t, "running", now)
	if err := d.sessions.Stop(ctx, "running", interrogation.Stop{Changes: interrogation.ChangesKeep, Actor: "bob", At: now}); err != nil {
		t.Fatal(err)
	}
	d.start(t, ctx, "ended", "e")
	d.end(t, "ended", map[string]any{"status": "completed", "end_reason": "done"})

	open, err := d.repo.ListOpenInterrogations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 2 || open[0].ID != "running" || open[1].ID != "queued" {
		t.Fatalf("open runs = %+v", open)
	}
	if !strings.Contains(string(open[0].Control), `"bob"`) || !strings.Contains(string(open[0].State), `"started_at"`) || open[0].UpdateTime.IsZero() {
		t.Errorf("open run %+v", open[0])
	}
	if !d.planUses(t, "interrogation_open_workload_idx", postgres.OpenInterrogationsQuery) {
		t.Error("the open runs do not come from interrogation_open_workload_idx")
	}

	h, ok, err := d.repo.InterrogationWorkloadHolder(ctx, "ns/Deployment/q")
	if err != nil || !ok || h.ID != "queued" || h.Started || h.CreatedAt.IsZero() {
		t.Errorf("holder of a queued run: %+v ok=%v err=%v", h, ok, err)
	}
	if h, ok, _ := d.repo.InterrogationWorkloadHolder(ctx, "ns/Deployment/r"); !ok || h.ID != "running" || !h.Started {
		t.Errorf("holder of a started run: %+v ok=%v", h, ok)
	}
	if _, ok, err := d.repo.InterrogationWorkloadHolder(ctx, "ns/Deployment/e"); ok || err != nil {
		t.Errorf("an ended run holds its workload: ok=%v err=%v", ok, err)
	}
}

func TestInterrogationSummary(t *testing.T) {
	d := newInterrogationDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	empty, err := d.repo.InterrogationSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Total != 0 || empty.Averaged != 0 || empty.RunningSources == nil || empty.Model != "" {
		t.Errorf("empty summary %+v", empty)
	}

	d.start(t, ctx, "queued", "q")
	d.start(t, ctx, "running", "r")
	d.markStarted(t, "running", now)
	d.start(t, ctx, "done", "d")
	d.end(t, "done", map[string]any{"status": "completed", "end_reason": "done", "round": 2,
		"started_at": now.Add(-10 * time.Minute), "ended_at": now, "model_calls": 3, "input_tokens": 100, "output_tokens": 10, "model": "m1"})
	d.start(t, ctx, "overdue", "o")
	d.appendRun(t, "overdue", map[string]any{"run": map[string]any{"status": "running", "round": 4,
		"started_at": now.Add(-30 * time.Minute), "model_calls": 1, "input_tokens": 50, "output_tokens": 5, "model": "m2"}})
	if err := d.sessions.CloseOverdue(ctx, "overdue", interrogation.Closed{Reason: "interrupted", At: now}); err != nil {
		t.Fatal(err)
	}
	d.start(t, ctx, "stopped", "s")
	if ok, err := d.sessions.CloseQueued(ctx, "stopped", interrogation.Closed{Reason: "stopped_by_user", At: now}); !ok || err != nil {
		t.Fatalf("close queued: %v %v", ok, err)
	}

	s, err := d.repo.InterrogationSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.Queued != 1 || s.Running != 1 || s.Total != 5 || s.Ended != 3 || s.Averaged != 2 {
		t.Errorf("summary counts %+v", s)
	}
	if len(s.RunningSources) != 1 || s.RunningSources[0] != (repo.InterrogationSource{Namespace: "ns", Kind: "Deployment", Name: "r"}) {
		t.Errorf("running sources %+v", s.RunningSources)
	}
	if s.AvgRounds != 3 || s.AvgDurationSeconds != 20*60 {
		t.Errorf("averages: rounds %v, duration %v; want 3 and 1200", s.AvgRounds, s.AvgDurationSeconds)
	}
	if s.ModelCalls != 4 || s.InputTokens != 150 || s.OutputTokens != 15 || s.Model != "m2" {
		t.Errorf("model usage %+v", s)
	}
}

// item is one API event as the workflow stores it.
func item(seq int64) map[string]any {
	return map[string]any{"seq": seq, "type": "node.finished", "time": time.Now().UTC(), "round": 1, "message": fmt.Sprint("event ", seq), "payload": map[string]any{}}
}

func eventSeqs(page repo.InterrogationEvents) []int64 {
	seqs := []int64{}
	for _, e := range page.Items {
		seqs = append(seqs, e.Seq)
	}
	return seqs
}

func TestListInterrogationEvents(t *testing.T) {
	d := newInterrogationDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	d.start(t, ctx, "run", "app")
	queued, ok, err := d.repo.ListInterrogationEvents(ctx, "run", 0, 10)
	if err != nil || !ok || len(queued.Items) != 0 || queued.Complete {
		t.Fatalf("queued run: %+v ok=%v err=%v; want an empty page that is not complete", queued, ok, err)
	}

	d.appendRun(t, "run", map[string]any{"run": map[string]any{"started_at": now, "seq": 3}}, item(1), item(2), item(3))
	// An ADK event that carries no API event.
	d.appendRun(t, "run", map[string]any{"ctl": map[string]any{}})
	d.appendRun(t, "run", map[string]any{"run": map[string]any{"started_at": now, "seq": 4}}, item(4))
	d.appendRun(t, "run", map[string]any{"run": map[string]any{"started_at": now, "seq": 6}}, item(5), item(6))

	for _, tc := range []struct {
		name         string
		after        int64
		limit        int
		want         []int64
		last         int64
		wantComplete bool
	}{
		{"flattened", 0, 10, []int64{1, 2, 3, 4, 5, 6}, 6, false},
		{"after in the middle of an ADK event", 2, 10, []int64{3, 4, 5, 6}, 6, false},
		{"cut at the limit", 0, 2, []int64{1, 2}, 2, false},
		{"cut inside an ADK event", 1, 3, []int64{2, 3, 4}, 4, false},
		{"after the last", 6, 10, []int64{}, 6, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, _, err := d.repo.ListInterrogationEvents(ctx, "run", tc.after, tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			if got := eventSeqs(page); !slices.Equal(got, tc.want) || page.LastSequence != tc.last || page.Complete != tc.wantComplete {
				t.Errorf("events %v, last %d, complete %v; want %v, %d, %v", got, page.LastSequence, page.Complete, tc.want, tc.last, tc.wantComplete)
			}
		})
	}
	page, _, _ := d.repo.ListInterrogationEvents(ctx, "run", 4, 10)
	var first struct {
		Seq     int64  `json:"seq"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(page.Items[0].JSON, &first); err != nil || first.Seq != 5 || first.Message != "event 5" {
		t.Errorf("item %s, want it as stored", page.Items[0].JSON)
	}

	d.end(t, "run", map[string]any{"status": "completed", "started_at": now, "seq": 7})
	// The closing event without its API event: a reader that has not seen
	// sequence 7 is not complete.
	if page, _, _ := d.repo.ListInterrogationEvents(ctx, "run", 0, 10); page.Complete {
		t.Errorf("complete before the last sequence was read: %+v", eventSeqs(page))
	}
	d.appendRun(t, "run", map[string]any{}, item(7))
	for _, tc := range []struct {
		name         string
		after        int64
		limit        int
		wantComplete bool
	}{
		{"whole log", 0, 10, true},
		{"page full", 0, 7, false},
		{"after the last", 7, 10, true},
	} {
		t.Run("ended/"+tc.name, func(t *testing.T) {
			page, _, err := d.repo.ListInterrogationEvents(ctx, "run", tc.after, tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			if page.Complete != tc.wantComplete {
				t.Errorf("complete = %v, want %v (events %v)", page.Complete, tc.wantComplete, eventSeqs(page))
			}
		})
	}

	d.start(t, ctx, "stopped", "s")
	if ok, err := d.sessions.CloseQueued(ctx, "stopped", interrogation.Closed{Reason: "stopped_by_user", At: now}); !ok || err != nil {
		t.Fatalf("close queued: %v %v", ok, err)
	}
	if page, _, _ := d.repo.ListInterrogationEvents(ctx, "stopped", 0, 10); len(page.Items) != 0 || !page.Complete {
		t.Errorf("run closed while queued: %+v, want an empty page that is complete", page)
	}

	d.start(t, ctx, "overdue", "o")
	d.appendRun(t, "overdue", map[string]any{"run": map[string]any{"started_at": now, "seq": 2}}, item(1), item(2))
	if err := d.sessions.CloseOverdue(ctx, "overdue", interrogation.Closed{Reason: "interrupted", At: now}); err != nil {
		t.Fatal(err)
	}
	if page, _, _ := d.repo.ListInterrogationEvents(ctx, "overdue", 0, 10); !slices.Equal(eventSeqs(page), []int64{1, 2}) || !page.Complete {
		t.Errorf("run closed without the workflow: %v complete %v", eventSeqs(page), page.Complete)
	}

	if _, ok, err := d.repo.ListInterrogationEvents(ctx, "unknown", 0, 10); ok || err != nil {
		t.Errorf("unknown run: ok=%v err=%v", ok, err)
	}
}

func TestInterrogationSources(t *testing.T) {
	d := newInterrogationDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	seen := now.Add(-time.Minute)

	// One source per workload; its id is what the call tree syncer keys nodes by.
	for i, workload := range []string{"app", "other"} {
		id := int64(i + 1)
		d.exec(t, `INSERT INTO sources (source_id, platform) VALUES ($1, 'k8s')`, id)
		d.exec(t, `INSERT INTO k8s_source_identity (source_id, namespace, workload_kind, workload_name, container_name)
			VALUES ($1, 'ns', 'Deployment', $2, 'app')`, id, workload)
	}
	for _, tr := range []struct {
		source   int64
		tx       int64
		function string
	}{
		{1, 1, "a.B.c"}, {1, 1, "a.B.d"}, {1, 2, "a.B.c"},
		{2, math.MaxInt64, "x.Y.z"},
	} {
		d.exec(t, `INSERT INTO autopilot_call_tree_nodes (source_id, transaction_id, sample_type, node_id, parent, function_name, frame_type)
			VALUES ($1, $2, 'samples', $3, 'root', $3, 'jvm')`, tr.source, tr.tx, tr.function)
	}
	for _, tx := range []struct {
		id        int64
		source    int64
		operation string
		volume    int64
	}{{1, 1, "GET /a", 10}, {2, 1, "POST /b", 50}, {math.MaxInt64, 2, "GET /z", 5}} {
		d.exec(t, `INSERT INTO transactions (transaction_id, source_id, service, operation, entry_kind) VALUES ($1, $2, 'svc', $3, 'HTTP')`,
			tx.id, tx.source, tx.operation)
		d.exec(t, `INSERT INTO transaction_activity (transaction_id, volume, last_seen) VALUES ($1, $2, $3)`, tx.id, tx.volume, seen)
	}
	d.start(t, ctx, "run", "app")
	d.markStarted(t, "run", now)
	d.request(t, "run", "app", "a.B.c", now)
	ended := d.request(t, "run", "app", "a.B.d", now)
	d.setChange(t, ended, `state = 'reverted', end_reason = 'stop'`)

	sources, err := d.repo.ListInterrogationSources(ctx, 100, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 {
		t.Fatalf("sources %+v", sources)
	}
	app, other := sources[0], sources[1]
	if app.Source.Name != "app" || app.Transactions != 2 || !app.LastSeen.Equal(seen) || app.OpenID != "run" || app.LiveChanges != 1 {
		t.Errorf("app = %+v", app)
	}
	if other.Source.Name != "other" || other.Transactions != 1 || other.OpenID != "" || other.LiveChanges != 0 {
		t.Errorf("other = %+v", other)
	}

	txs, err := d.repo.ListInterrogationSourceTransactions(ctx, repo.InterrogationSource{Namespace: "ns", Kind: "Deployment", Name: "app"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(txs) != 2 || txs[0].TransactionID != 2 || txs[0].Volume != 50 || txs[0].Functions != 1 ||
		txs[1].TransactionID != 1 || txs[1].Operation != "GET /a" || txs[1].Functions != 2 || !txs[1].LastSeen.Equal(seen) {
		t.Errorf("transactions %+v", txs)
	}
	if txs, err := d.repo.ListInterrogationSourceTransactions(ctx, repo.InterrogationSource{Namespace: "ns", Kind: "Deployment", Name: "none"}, 100); err != nil || len(txs) != 0 {
		t.Errorf("unknown source: %+v %v", txs, err)
	}
}

// The changes of a run as the workflow and the run view read them, and the
// two writes outside the reconciler: confirm giving up on a change, and a
// stop with revert.
func TestInterrogationChanges(t *testing.T) {
	d := newInterrogationDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	d.start(t, ctx, "run", "app")
	d.markStarted(t, "run", now)
	first := d.request(t, "run", "app", "a.B.first", now)
	failed := d.request(t, "run", "app", "a.B.failed", now.Add(time.Second))
	expired := d.request(t, "run", "app", "a.B.expired", now.Add(2*time.Second))
	d.setChange(t, expired, `expires_at = $2`, now.Add(-time.Minute))

	got, ok, err := d.repo.GetInterrogationChange(ctx, first)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.ChangeID != first || got.Round != 1 || got.Language != "java" || got.Desired != "approved" ||
		!got.ExpiresAt.Equal(now.Add(time.Hour)) || got.AppliedAt != (time.Time{}) {
		t.Errorf("change = %+v", got)
	}
	if _, ok, err := d.repo.GetInterrogationChange(ctx, "chg_unknown"); ok || err != nil {
		t.Errorf("unknown change: ok=%v err=%v", ok, err)
	}

	if err := d.repo.EndInterrogationChange(ctx, failed, "not_applied_in_time", now); err != nil {
		t.Fatal(err)
	}
	if err := d.repo.EndInterrogationChange(ctx, failed, "rule_disabled", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if c, _, _ := d.repo.GetInterrogationChange(ctx, failed); c.State(now) != "failed" || c.EndReason != "not_applied_in_time" || !c.UpdatedAt.Equal(now) {
		t.Errorf("given up change, ended twice: %+v", c)
	}

	if err := d.repo.RevertInterrogationProbes(ctx, "run", now); err != nil {
		t.Fatal(err)
	}
	if err := d.repo.RevertInterrogationProbes(ctx, "run", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	run, err := d.repo.ListInterrogationChanges(ctx, "run")
	if err != nil {
		t.Fatal(err)
	}
	var states []string
	for _, c := range run {
		states = append(states, c.ChangeID+"/"+c.State(now)+"/"+c.EndReason)
	}
	want := []string{first + "/reverted/stop", failed + "/failed/not_applied_in_time", expired + "/expired/"}
	if !slices.Equal(states, want) {
		t.Errorf("run's changes after a stop with revert, oldest first: %v, want %v", states, want)
	}
}

func TestInterrogationChangeActions(t *testing.T) {
	d := newInterrogationDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	d.start(t, ctx, "run", "app")
	d.markStarted(t, "run", now)
	d.start(t, ctx, "other", "web")
	d.markStarted(t, "other", now)
	keep := d.request(t, "run", "app", "a.B.keep", now)
	revert := d.request(t, "run", "app", "a.B.revert", now.Add(time.Second))
	expired := d.request(t, "run", "app", "a.B.expired", now.Add(2*time.Second))
	d.setChange(t, expired, `expires_at = $2`, now.Add(-time.Minute))
	web := d.request(t, "other", "web", "a.W.web", now)
	webLater := d.request(t, "other", "web", "a.W.later", now)
	d.setChange(t, webLater, `expires_at = $2`, now.Add(2*time.Hour))

	c, ok, err := d.repo.KeepInterrogationChange(ctx, keep, "dana", now)
	if err != nil || !ok {
		t.Fatalf("keep: ok=%v err=%v", ok, err)
	}
	if c.Desired != repo.ChangeKept || c.KeptBy != "dana" || !c.KeptAt.Equal(now) || !c.ExpiresAt.IsZero() ||
		c.Lifetime() != repo.ChangePermanent || c.Interrogation == nil || c.Interrogation.ID != "run" {
		t.Errorf("kept change = %+v", c)
	}
	if _, ok, err := d.repo.KeepInterrogationChange(ctx, keep, "dana", now); ok || err != nil {
		t.Errorf("keeping a kept change: ok=%v err=%v", ok, err)
	}
	if _, ok, err := d.repo.KeepInterrogationChange(ctx, expired, "dana", now); ok || err != nil {
		t.Errorf("keeping an expired change: ok=%v err=%v", ok, err)
	}
	if _, ok, err := d.repo.RevertInterrogationChange(ctx, keep, repo.ChangeApproved, repo.ChangeRevert, now); ok || err != nil {
		t.Errorf("reverting a kept change: ok=%v err=%v", ok, err)
	}

	sum, err := d.repo.InterrogationChangesSummary(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Live != 4 || sum.Temporary != 3 || sum.Permanent != 1 || sum.NextExpiryChangeID == keep {
		t.Errorf("summary = %+v", sum)
	}

	listed, err := d.repo.ListClusterInterrogationChanges(ctx, 10, now)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, c := range listed {
		order = append(order, c.ChangeID)
	}
	// web and revert expire together, and the id breaks the tie.
	if want := []string{web, revert, webLater, keep}; !slices.Equal(order, want) {
		t.Errorf("cluster changes %v, want %v: soonest expiry first, kept last", order, want)
	}

	if c, ok, err := d.repo.RevertInterrogationChange(ctx, revert, repo.ChangeApproved, repo.ChangeRevert, now); err != nil || !ok ||
		c.State(now) != repo.ChangeReverted || c.EndReason != repo.ChangeRevert {
		t.Errorf("revert: %+v ok=%v err=%v", c, ok, err)
	}
	if _, ok, err := d.repo.RevertInterrogationChange(ctx, revert, repo.ChangeApproved, repo.ChangeRevert, now); ok || err != nil {
		t.Errorf("reverting twice: ok=%v err=%v", ok, err)
	}
	if c, ok, err := d.repo.RevertInterrogationChange(ctx, keep, repo.ChangeKept, repo.ChangeRemove, now); err != nil || !ok ||
		c.State(now) != repo.ChangeReverted || c.Lifetime() != repo.ChangePermanent {
		t.Errorf("remove: %+v ok=%v err=%v", c, ok, err)
	}

	n, err := d.repo.RevertAllInterrogationChanges(ctx, repo.InterrogationSource{Namespace: "ns", Kind: "Deployment", Name: "app"}, now)
	if err != nil || n != 0 {
		t.Errorf("revert all on app, which has nothing live: %d, %v", n, err)
	}
	if n, err = d.repo.RevertAllInterrogationChanges(ctx, repo.InterrogationSource{}, now); err != nil || n != 2 {
		t.Errorf("revert all: %d, %v", n, err)
	}

	d.exec(t, `DELETE FROM adk.sessions WHERE user_id = 'other'`)
	if c, ok, err := d.repo.GetInterrogationChange(ctx, web); err != nil || !ok || c.Interrogation != nil || c.Source.Name != "web" {
		t.Errorf("a change whose run is gone: %+v ok=%v err=%v", c, ok, err)
	}
}

// retentionLater is a retention pass that sees runs and changes of now as
// older than 30 days.
func retentionLater(now time.Time) time.Time {
	return now.AddDate(0, 0, 31)
}

// prune runs retention at now and returns the counts it recorded.
func (d interrogationDB) prune(t *testing.T, now time.Time) map[string]int64 {
	t.Helper()
	ctx := context.Background()
	if err := d.repo.PruneExpired(ctx, now); err != nil {
		t.Fatalf("prune: %v", err)
	}
	var deleted map[string]int64
	if err := d.sql.QueryRow(ctx, `SELECT deleted_rows FROM maintenance_runs WHERE job = 'retention'`).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	return deleted
}

// stored counts the run's session, its control session and their events.
type stored struct{ run, ctl, events int }

func (d interrogationDB) stored(t *testing.T, id string) stored {
	t.Helper()
	var s stored
	err := d.sql.QueryRow(context.Background(), `
SELECT count(*) FILTER (WHERE app_name = 'interrogation'), count(*) FILTER (WHERE app_name = 'interrogation.ctl'),
	(SELECT count(*) FROM adk.events WHERE user_id = $1 AND session_id = $1)
FROM adk.sessions WHERE user_id = $1 AND id = $1`, id).Scan(&s.run, &s.ctl, &s.events)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRetention_KeepsOpenRuns(t *testing.T) {
	d := newInterrogationDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	d.start(t, ctx, "queued", "a")
	d.start(t, ctx, "started", "b")
	d.markStarted(t, "started", now)
	d.appendRun(t, "started", nil, item(1))
	if err := d.sessions.Stop(ctx, "started", interrogation.Stop{Changes: interrogation.ChangesKeep, Actor: "bob", At: now}); err != nil {
		t.Fatal(err)
	}
	before := map[string]stored{"queued": d.stored(t, "queued"), "started": d.stored(t, "started")}
	if s := before["started"]; s.run != 1 || s.ctl != 1 || s.events < 3 {
		t.Fatalf("started run before retention: %+v", s)
	}

	deleted := d.prune(t, retentionLater(now))
	for id, want := range before {
		if got := d.stored(t, id); got != want {
			t.Errorf("%s after retention: %+v, want %+v", id, got, want)
		}
	}
	if deleted["interrogation_sessions"] != 0 || deleted["interrogation_control_sessions"] != 0 {
		t.Errorf("deleted: %v", deleted)
	}
}

func TestRetention_DeletesEndedRuns(t *testing.T) {
	d := newInterrogationDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	d.start(t, ctx, "old", "a")
	d.markStarted(t, "old", now)
	d.appendRun(t, "old", nil, item(1))
	d.end(t, "old", map[string]any{"status": "completed", "end_reason": "done", "started_at": now})
	d.start(t, ctx, "stopped", "b")
	if ok, err := d.sessions.CloseQueued(ctx, "stopped", interrogation.Closed{Reason: "stopped_by_user", At: now}); !ok || err != nil {
		t.Fatalf("close queued: %v %v", ok, err)
	}
	// Ended a day after the cutoff of the pass below.
	d.start(t, ctx, "young", "c")
	d.markStarted(t, "young", now)
	d.appendRunAt(t, "young", now.AddDate(0, 0, 2), map[string]any{"run": map[string]any{"status": "completed", "end_reason": "done"}, "open": false})
	if s := d.stored(t, "old"); s.run != 1 || s.ctl != 1 || s.events < 3 {
		t.Fatalf("old run before retention: %+v", s)
	}
	young := d.stored(t, "young")

	deleted := d.prune(t, retentionLater(now))
	for _, id := range []string{"old", "stopped"} {
		if got := d.stored(t, id); got != (stored{}) {
			t.Errorf("%s after retention: %+v, want nothing", id, got)
		}
		if _, ok, err := d.repo.GetInterrogation(ctx, id, now); ok || err != nil {
			t.Errorf("get %s after retention: ok=%v err=%v", id, ok, err)
		}
	}
	if got := d.stored(t, "young"); got != young {
		t.Errorf("young after retention: %+v, want %+v", got, young)
	}
	for key, want := range map[string]int64{
		"interrogation_sessions": 2, "interrogation_control_sessions": 2, "interrogation_changes": 0,
		"trace_observations": 0, "notification_attempts": 0,
	} {
		if got, ok := deleted[key]; !ok || got != want {
			t.Errorf("deleted[%s] = %d (recorded %v), want %d", key, got, ok, want)
		}
	}
}

// A run closed before a leader picked it up has no result and is deleted
// after an hour; one that ran is kept for the full retention.
func TestRetention_DeletesRunsClosedBeforePickup(t *testing.T) {
	d := newInterrogationDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	d.start(t, ctx, "stopped", "a")
	if ok, err := d.sessions.CloseQueued(ctx, "stopped", interrogation.Closed{Reason: "stopped_by_user", At: now}); !ok || err != nil {
		t.Fatalf("close queued: %v %v", ok, err)
	}
	d.start(t, ctx, "ran", "b")
	d.markStarted(t, "ran", now)
	d.end(t, "ran", map[string]any{"status": "completed", "end_reason": "done", "started_at": now})
	stopped, ran := d.stored(t, "stopped"), d.stored(t, "ran")

	d.prune(t, now.Add(30*time.Minute))
	if got := d.stored(t, "stopped"); got != stopped {
		t.Errorf("stopped after 30 minutes: %+v, want %+v", got, stopped)
	}
	deleted := d.prune(t, now.Add(2*time.Hour))
	if got := d.stored(t, "stopped"); got != (stored{}) {
		t.Errorf("stopped after 2 hours: %+v, want nothing", got)
	}
	if got := d.stored(t, "ran"); got != ran {
		t.Errorf("ran after 2 hours: %+v, want %+v", got, ran)
	}
	if deleted["interrogation_sessions"] != 1 || deleted["interrogation_control_sessions"] != 1 {
		t.Errorf("deleted: %v", deleted)
	}
}

func TestRetention_Changes(t *testing.T) {
	d := newInterrogationDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	d.start(t, ctx, "run", "w")
	d.markStarted(t, "run", now)
	reverted := d.request(t, "run", "w", "p.Q.reverted", now)
	d.setChange(t, reverted, `state = 'reverted', end_reason = 'stop'`)
	expired := d.request(t, "run", "w", "p.Q.expired", now)
	d.setChange(t, expired, `expires_at = $2`, now.Add(-time.Minute))
	inRule := d.request(t, "run", "w", "p.Q.inRule", now)
	d.setChange(t, inRule, `state = 'reverted', in_rule = true`)
	// Younger than the cutoff of the pass below, by their last change and by
	// their expiry.
	updated := d.request(t, "run", "w", "p.Q.updated", now)
	d.setChange(t, updated, `state = 'reverted', updated_at = $2`, now.AddDate(0, 0, 2))
	expiresLater := d.request(t, "run", "w", "p.Q.expiresLater", now)
	d.setChange(t, expiresLater, `expires_at = $2`, now.AddDate(0, 0, 2))
	kept := d.request(t, "run", "w", "p.Q.kept", now)
	d.setChange(t, kept, `state = 'kept', expires_at = 'epoch'`)

	deleted := d.prune(t, retentionLater(now))
	for id, want := range map[string]bool{reverted: false, expired: false, inRule: true, updated: true, expiresLater: true, kept: true} {
		if _, ok, err := d.repo.GetInterrogationChange(ctx, id); ok != want || err != nil {
			t.Errorf("%s after retention: stored=%v err=%v, want %v", id, ok, err, want)
		}
	}
	if deleted["interrogation_changes"] != 2 {
		t.Errorf("deleted: %v", deleted)
	}
}
