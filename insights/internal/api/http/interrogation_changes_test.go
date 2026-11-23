package http

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/odigos-io/odigos/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

const changesPath = "/api/v1/interrogation-changes/"

// act posts one action on a change and returns the response.
func (s *interrogationServer) act(t *testing.T, id, action, body string) apitypes.InterrogationChangeDetail {
	t.Helper()
	w := s.do(t, http.MethodPost, changesPath+id+"/"+action, body)
	want := http.StatusAccepted
	if action == repo.ChangeKeep {
		want = http.StatusOK
	}
	if w.Code != want {
		t.Fatalf("%s %s: %d %s, want %d", action, id, w.Code, w.Body.String(), want)
	}
	return decodeBody[apitypes.InterrogationChangeDetail](t, w)
}

func TestInterrogationChangeActions(t *testing.T) {
	s := newInterrogationServer(t)
	s.evidence(t, "app", 1)
	run := s.start(t, "app", `,"allow_changes":["custom_instrumentation"]`)
	s.pickUp(t, run.Id)
	kept := s.probe(t, run.Id, "app", "a.B#kept")
	reverted := s.probe(t, run.Id, "app", "a.B#reverted")

	w := s.do(t, http.MethodGet, changesPath+kept, "")
	got := decodeBody[apitypes.InterrogationChangeDetail](t, w)
	if w.Code != http.StatusOK || got.State != repo.ChangeRequested || got.Lifetime != repo.ChangeTemporary ||
		!slices.Equal(got.Actions, []string{"keep", "revert"}) || got.Target != "a.B#kept" || got.Reason != "called from a.B#kept" ||
		got.Probe == nil || got.Probe.ClassName != "a.B" || got.Interrogation == nil || got.Interrogation.Status != repo.InterrogationRunning {
		t.Fatalf("get: %d %s", w.Code, w.Body.String())
	}

	t.Run("keep", func(t *testing.T) {
		got := s.act(t, kept, "keep", `{"actor":"dana"}`)
		if got.Lifetime != repo.ChangePermanent || got.KeptBy != "dana" || got.KeptAt == nil || got.ExpiresAt != nil ||
			!slices.Equal(got.Actions, []string{"remove"}) {
			t.Fatalf("kept %+v", got)
		}
		if again := s.act(t, kept, "keep", ""); again.KeptBy != "dana" {
			t.Errorf("a repeated keep changed the change: %+v", again)
		}
		body := wantError(t, s.do(t, http.MethodPost, changesPath+kept+"/revert", ""), http.StatusConflict, reasonInvalidChangeState)
		if body.Error.Details["state"] != repo.ChangeRequested {
			t.Errorf("details %v", body.Error.Details)
		}
	})

	t.Run("revert", func(t *testing.T) {
		got := s.act(t, reverted, "revert", `{"actor":"dana"}`)
		if got.State != repo.ChangeReverted || got.EndReason != repo.ChangeRevert || len(got.Actions) != 0 {
			t.Fatalf("reverted %+v", got)
		}
		s.act(t, reverted, "revert", "")
		wantError(t, s.do(t, http.MethodPost, changesPath+reverted+"/keep", ""), http.StatusConflict, reasonInvalidChangeState)
		wantError(t, s.do(t, http.MethodPost, changesPath+reverted+"/remove", ""), http.StatusConflict, reasonInvalidChangeState)
		other := s.change(t, run.Id, kept)
		if other.State != repo.ChangeRequested || other.Lifetime != repo.ChangePermanent {
			t.Errorf("the run's other change %+v", other)
		}
	})

	t.Run("the run counts what it has left", func(t *testing.T) {
		got := s.get(t, run.Id)
		if got.ChangeCounts.Live != 1 || got.ChangeCounts.Reverted != 1 || got.Usage.ProbesUsed != 2 {
			t.Errorf("counts %+v, probes used %d", got.ChangeCounts, got.Usage.ProbesUsed)
		}
		c := s.change(t, run.Id, reverted)
		if c.Reason == "" || c.Lifetime != repo.ChangeTemporary || len(c.Actions) != 0 || c.EndReason != repo.ChangeRevert {
			t.Errorf("inline change %+v", c)
		}
	})

	t.Run("after the run ended", func(t *testing.T) {
		later := s.probe(t, run.Id, "app", "a.B#later")
		s.end(t, run.Id, time.Now())
		if got := s.act(t, later, "revert", ""); got.State != repo.ChangeReverted || got.Interrogation.Status != "completed" {
			t.Fatalf("reverted after the end %+v", got)
		}
		got := s.act(t, kept, "remove", `{"actor":"dana"}`)
		if got.State != repo.ChangeReverted || got.EndReason != repo.ChangeRemove || got.Lifetime != repo.ChangePermanent {
			t.Fatalf("removed %+v", got)
		}
		s.act(t, kept, "remove", "")
		if c := s.get(t, run.Id).ChangeCounts; c.Live != 0 || c.Reverted != 3 {
			t.Errorf("counts after the end %+v", c)
		}
	})

	t.Run("refused", func(t *testing.T) {
		for _, path := range []string{"chg_00000000000000000000", "nope", "%00"} {
			for _, action := range []string{"", "/keep", "/revert", "/remove", "/undo"} {
				method := http.MethodPost
				if action == "" {
					method = http.MethodGet
				}
				if w := s.do(t, method, changesPath+path+action, ""); w.Code != http.StatusNotFound {
					t.Errorf("%s %s%s: %d %s", method, path, action, w.Code, w.Body.String())
				}
			}
		}
		body := wantError(t, s.do(t, http.MethodPost, changesPath+kept+"/undo", ""), http.StatusUnprocessableEntity, reasonNotSupportedYet)
		if body.Error.Details["option"] != "operation=undoInterrogationChange" {
			t.Errorf("undo details %v", body.Error.Details)
		}
		long := `{"actor":"` + strings.Repeat("a", 201) + `"}`
		wantError(t, s.do(t, http.MethodPost, changesPath+kept+"/keep", long), http.StatusUnprocessableEntity, reasonInvalidArgument)
	})
}

func TestInterrogationChangeLists(t *testing.T) {
	s := newInterrogationServer(t)
	s.evidence(t, "app", 1)
	s.evidence(t, "web", 1)
	run := s.start(t, "app", `,"allow_changes":["custom_instrumentation"]`)
	s.pickUp(t, run.Id)
	other := s.start(t, "web", `,"allow_changes":["custom_instrumentation"]`)
	s.pickUp(t, other.Id)
	kept := s.probe(t, run.Id, "app", "a.B#kept")
	live := s.probe(t, run.Id, "app", "a.B#live")
	web := s.probe(t, other.Id, "web", "w.W#web")
	st := runState("running", time.Now())
	st["probes"] = []map[string]any{{
		"change_id": "chg_denied", "language": "java", "target": "a.B#denied", "status": "denied",
		"deny_reason": "not_application_code", "round": 1, "proposed_at": time.Now().Add(time.Minute),
	}}
	if err := s.appendRun(context.Background(), run.Id, map[string]any{"run": st}); err != nil {
		t.Fatal(err)
	}
	s.act(t, kept, "keep", "")

	w := s.do(t, http.MethodGet, "/api/v1/interrogations/"+run.Id+"/changes", "")
	runChanges := decodeBody[apitypes.InterrogationChangeList](t, w)
	var ids []string
	for _, c := range runChanges.Items {
		ids = append(ids, c.Id)
		if c.Interrogation == nil || c.Interrogation.Id != run.Id || c.InterrogationId != run.Id || c.Source.Name != "app" {
			t.Errorf("run change %+v", c)
		}
	}
	if w.Code != http.StatusOK || !slices.Equal(ids, []string{kept, live, "chg_denied"}) || runChanges.Items[2].DenyReason != "not_application_code" {
		t.Fatalf("run changes %d %s", w.Code, w.Body.String())
	}

	w = s.do(t, http.MethodGet, "/api/v1/interrogation-changes?limit=10", "")
	cluster := decodeBody[apitypes.InterrogationChangeList](t, w)
	ids = nil
	for _, c := range cluster.Items {
		ids = append(ids, c.Id)
	}
	if w.Code != http.StatusOK || len(ids) != 3 || ids[2] != kept {
		t.Fatalf("live changes %v, want three with the kept one last: %s", ids, w.Body.String())
	}

	w = s.do(t, http.MethodGet, "/api/v1/interrogation-changes/summary", "")
	sum := decodeBody[apitypes.InterrogationChangesSummary](t, w)
	if w.Code != http.StatusOK || sum.Live != 3 || sum.Temporary != 2 || sum.Permanent != 1 || len(sum.BySource) != 2 {
		t.Fatalf("summary %d %s", w.Code, w.Body.String())
	}

	wantError(t, s.do(t, http.MethodPost, changesPath+"revert-all", `{"source":{"namespace":"ns"}}`), http.StatusUnprocessableEntity, reasonInvalidArgument)
	revertAll := func(body string) int {
		t.Helper()
		w := s.do(t, http.MethodPost, changesPath+"revert-all", body)
		if w.Code != http.StatusAccepted {
			t.Fatalf("revert all %s: %d %s", body, w.Code, w.Body.String())
		}
		return decodeBody[apitypes.InterrogationChangesReverted](t, w).Count
	}
	if n := revertAll(`{"source":{"namespace":"ns","kind":"Deployment","name":"web"},"actor":"dana"}`); n != 1 {
		t.Errorf("revert all on web: %d", n)
	}
	if c := s.change(t, run.Id, live); c.State != repo.ChangeRequested {
		t.Errorf("app's change after revert all on web: %s", c.State)
	}
	if n := revertAll(""); n != 1 {
		t.Errorf("revert all: %d", n)
	}
	if n := revertAll(""); n != 0 {
		t.Errorf("revert all again: %d", n)
	}
	if c := s.change(t, run.Id, kept); c.State != repo.ChangeRequested || c.Lifetime != repo.ChangePermanent {
		t.Errorf("kept change after revert all: %+v", c)
	}
	if c := s.change(t, other.Id, web); c.EndReason != repo.ChangeRevert {
		t.Errorf("web's change: %+v", c)
	}
	if w := s.do(t, http.MethodGet, "/api/v1/interrogations/nope/changes", ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown run's changes: %d", w.Code)
	}
}
