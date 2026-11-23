package interrogation

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/v2/session"

	"github.com/odigos-io/odigos/insights/internal/db/postgres"
	"github.com/odigos-io/odigos/insights/internal/engine"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

func memorySession(t *testing.T, id string, state map[string]any) session.Session {
	t.Helper()
	created, err := session.InMemoryService().Create(context.Background(), &session.CreateRequest{AppName: AppName, UserID: id, SessionID: id, State: state})
	if err != nil {
		t.Fatal(err)
	}
	return created.Session
}

func TestLoad_NewerVersion(t *testing.T) {
	// A newer binary may have changed any field; the version is read first.
	sess := memorySession(t, testRunID, map[string]any{
		requestKey: newScript().st.Request,
		runKey:     map[string]any{"v": StateVersion + 1, "rounds": "a shape this binary does not know"},
	})
	_, _, err := load(sess)
	if err == nil || !strings.Contains(err.Error(), "state version 2, this server reads 1") {
		t.Fatalf("load = %v, want the version refused", err)
	}
}

// TestStoredRun_ReadByRepo appends a scripted run with ADK's service and
// reads it back with the repo's reads of a run and its event log.
func TestStoredRun_ReadByRepo(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	sessions := openTestSessions(t, db.dsn)
	r, err := postgres.Open(db.dsn, engine.NewBaselineCodec())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })

	s := scriptedRun()
	if err := sessions.Start(ctx, testRunID, s.st.Request); err != nil {
		t.Fatal(err)
	}
	svc := sessions.Service()
	appendEvent := func(ev *session.Event) {
		t.Helper()
		got, err := svc.Get(ctx, &session.GetRequest{AppName: AppName, UserID: testRunID, SessionID: testRunID})
		if err != nil {
			t.Fatal(err)
		}
		ev.Author = AppName
		if err := svc.AppendEvent(ctx, got.Session, ev); err != nil {
			t.Fatal(err)
		}
	}

	half := len(s.events) / 2
	for _, ev := range s.events[:half] {
		appendEvent(ev)
	}
	run, ok, err := r.GetInterrogation(ctx, testRunID, time.Now())
	if err != nil || !ok {
		t.Fatalf("GetInterrogation: %v, found %v", err, ok)
	}
	midSeq := s.events[half-1].Actions.StateDelta[runKey].(State).Seq
	if status, _, _ := run.Status(); status != repo.InterrogationRunning || run.Seq != midSeq || !run.Open {
		t.Fatalf("mid-run: status %s, seq %d, open %v; want running at %d", status, run.Seq, run.Open, midSeq)
	}

	for _, ev := range s.events[half:] {
		appendEvent(ev)
	}
	run, ok, err = r.GetInterrogation(ctx, testRunID, time.Now())
	if err != nil || !ok {
		t.Fatalf("GetInterrogation: %v, found %v", err, ok)
	}
	status, endReason, errText := run.Status()
	if status != string(StatusCompleted) || endReason != string(EndDone) || errText != "" || run.Open || run.Seq != s.st.Seq {
		t.Fatalf("ended: status %s, end reason %s, error %q, open %v, seq %d", status, endReason, errText, run.Open, run.Seq)
	}
	var stored State
	if err := json.Unmarshal(run.Run, &stored); err != nil {
		t.Fatal(err)
	}
	if want := *s.st; !reflect.DeepEqual(stored.Rounds, want.Rounds) || !reflect.DeepEqual(stored.Answer, want.Answer) {
		t.Fatalf("stored rounds or answer differ from the run's")
	}

	var want []Event
	for _, ev := range s.events {
		want = append(want, apiEvents(ev)...)
	}
	// Small pages cut ADK events in the middle; together they are the log.
	var got []Event
	after := int64(0)
	for {
		page, ok, err := r.ListInterrogationEvents(ctx, testRunID, after, 4)
		if err != nil || !ok {
			t.Fatalf("ListInterrogationEvents: %v, found %v", err, ok)
		}
		for _, item := range page.Items {
			var e Event
			if err := json.Unmarshal(item.JSON, &e); err != nil {
				t.Fatal(err)
			}
			got = append(got, e)
		}
		after = page.LastSequence
		if page.Complete {
			break
		}
		if len(page.Items) == 0 {
			t.Fatalf("empty page after %d and not complete", after)
		}
	}
	if after != s.st.Seq || len(got) != len(want) {
		t.Fatalf("read %d events up to %d, want %d up to %d", len(got), after, len(want), s.st.Seq)
	}
	for i := range want {
		wb, _ := json.Marshal(want[i])
		gb, _ := json.Marshal(got[i])
		if string(wb) != string(gb) {
			t.Fatalf("event %d read back as\n%s\nwant\n%s", i, gb, wb)
		}
	}
}
