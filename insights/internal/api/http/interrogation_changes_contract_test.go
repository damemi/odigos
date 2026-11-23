package http

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// The wire types the frontend's GraphQL layer decodes these responses into
// (frontend/services/insights/interrogations_types.go in odigos-io/odigos#6486),
// cut to the fields it reads. Copied, not imported: the frontend is its own
// module.
type (
	feSource struct {
		Namespace string `json:"namespace"`
		Kind      string `json:"kind"`
		Name      string `json:"name"`
	}
	feTrigger struct {
		Kind  string `json:"kind"`
		Actor string `json:"actor,omitempty"`
	}
	feProbe struct {
		Language   string `json:"language"`
		ClassName  string `json:"class_name,omitempty"`
		MethodName string `json:"method_name,omitempty"`
	}
	feChangeRef struct {
		ID        string    `json:"id"`
		DisplayID string    `json:"display_id"`
		Title     string    `json:"title"`
		Status    string    `json:"status"`
		Round     int       `json:"round"`
		Trigger   feTrigger `json:"trigger"`
	}
	feChange struct {
		ID                         string       `json:"id"`
		InterrogationID            string       `json:"interrogation_id"`
		Interrogation              *feChangeRef `json:"interrogation,omitempty"`
		Source                     feSource     `json:"source"`
		Kind                       string       `json:"kind"`
		Target                     string       `json:"target"`
		Probe                      *feProbe     `json:"probe,omitempty"`
		Category                   string       `json:"category,omitempty"`
		Round                      int          `json:"round"`
		Reason                     string       `json:"reason"`
		Checks                     []string     `json:"checks"`
		State                      string       `json:"state"`
		Lifetime                   string       `json:"lifetime"`
		DenyReason                 string       `json:"deny_reason,omitempty"`
		BlockReason                string       `json:"block_reason,omitempty"`
		EndReason                  string       `json:"end_reason,omitempty"`
		AlsoRecommendedByAutopilot bool         `json:"also_recommended_by_autopilot,omitempty"`
		ProposedAt                 string       `json:"proposed_at"`
		AppliedAt                  *string      `json:"applied_at,omitempty"`
		ExpiresAt                  *string      `json:"expires_at,omitempty"`
		KeptAt                     *string      `json:"kept_at,omitempty"`
		KeptBy                     string       `json:"kept_by,omitempty"`
		EndedAt                    *string      `json:"ended_at,omitempty"`
		EndedBy                    string       `json:"ended_by,omitempty"`
		EndNote                    string       `json:"end_note,omitempty"`
		RuleName                   string       `json:"rule_name,omitempty"`
		Actions                    []string     `json:"actions"`
	}
	feChangesBySource struct {
		Source    feSource `json:"source"`
		Language  string   `json:"language"`
		Live      int      `json:"live"`
		Probes    int      `json:"probes"`
		MaxProbes int      `json:"max_probes"`
	}
	feChangesSummary struct {
		Live               int                 `json:"live"`
		Temporary          int                 `json:"temporary"`
		Permanent          int                 `json:"permanent"`
		ByKind             map[string]int      `json:"by_kind"`
		NextExpiryAt       *string             `json:"next_expiry_at,omitempty"`
		NextExpiryChangeID string              `json:"next_expiry_change_id,omitempty"`
		BySource           []feChangesBySource `json:"by_source"`
	}
	feRevertAllResult struct {
		Count int `json:"count"`
	}
)

// feDecode decodes a 2xx response as the frontend's client does.
func feDecode[T any](t *testing.T, s *interrogationServer, method, path, body string) T {
	t.Helper()
	w := s.do(t, method, path, body)
	if w.Code < 200 || w.Code > 299 {
		t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
	}
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("%s %s does not decode into the frontend's type: %v", method, path, err)
	}
	return v
}

// wantRenderable checks the fields the UI kit draws a change row from.
func wantRenderable(t *testing.T, c feChange) {
	t.Helper()
	if c.ID == "" || c.InterrogationID == "" || c.Source.Name == "" || c.Target == "" || c.State == "" || c.Lifetime == "" ||
		c.Reason == "" || c.Checks == nil || c.Actions == nil || c.ProposedAt == "" || c.Round == 0 {
		t.Errorf("change missing a field the UI draws: %+v", c)
	}
	if c.Interrogation == nil || c.Interrogation.DisplayID == "" || c.Interrogation.Title == "" || c.Interrogation.Status == "" {
		t.Errorf("change's run %+v", c.Interrogation)
	}
	if c.Probe == nil || c.Probe.ClassName == "" || c.Probe.MethodName == "" {
		t.Errorf("change's probe %+v", c.Probe)
	}
}

func TestInterrogationChanges_FrontendContract(t *testing.T) {
	s := newInterrogationServer(t)
	s.evidence(t, "app", 1)
	run := s.start(t, "app", `,"allow_changes":["custom_instrumentation"]`)
	s.pickUp(t, run.Id)
	kept := s.probe(t, run.Id, "app", "a.B#kept")
	reverted := s.probe(t, run.Id, "app", "a.B#reverted")
	st := runState("running", time.Now())
	st["probes"] = []map[string]any{{
		"change_id": "chg_denied", "language": "java", "target": "a.B.denied", "why": "denied because",
		"rec":    map[string]any{"probe": map[string]any{"className": "a.B", "methodName": "denied"}},
		"status": "denied", "deny_reason": "not_application_code", "round": 1, "proposed_at": time.Now(),
	}}
	if err := s.appendRun(context.Background(), run.Id, map[string]any{"run": st}); err != nil {
		t.Fatal(err)
	}

	type list struct {
		Items []feChange `json:"items"`
	}
	runChanges := feDecode[list](t, s, http.MethodGet, "/api/v1/interrogations/"+run.Id+"/changes", "")
	if len(runChanges.Items) != 3 {
		t.Fatalf("run changes %+v", runChanges.Items)
	}
	for _, c := range runChanges.Items {
		wantRenderable(t, c)
	}

	c := feDecode[feChange](t, s, http.MethodPost, changesPath+kept+"/keep", `{"actor":"dana"}`)
	wantRenderable(t, c)
	if c.Lifetime != "permanent" || c.KeptBy != "dana" || c.KeptAt == nil {
		t.Errorf("kept %+v", c)
	}
	c = feDecode[feChange](t, s, http.MethodPost, changesPath+reverted+"/revert", `{}`)
	wantRenderable(t, c)
	if c.EndReason != "revert" {
		t.Errorf("reverted %+v", c)
	}
	wantRenderable(t, feDecode[feChange](t, s, http.MethodGet, changesPath+kept, ""))

	live := feDecode[list](t, s, http.MethodGet, "/api/v1/interrogation-changes?limit=500", "")
	if len(live.Items) != 1 || live.Items[0].ID != kept {
		t.Fatalf("live changes %+v", live.Items)
	}
	wantRenderable(t, live.Items[0])

	sum := feDecode[feChangesSummary](t, s, http.MethodGet, "/api/v1/interrogation-changes/summary", "")
	if sum.Live != 1 || sum.Permanent != 1 || sum.ByKind["custom_instrumentation"] != 1 || len(sum.BySource) != 1 || sum.BySource[0].MaxProbes == 0 {
		t.Errorf("summary %+v", sum)
	}

	removed := feDecode[feChange](t, s, http.MethodPost, changesPath+kept+"/remove", "")
	if removed.EndReason != "remove" || len(removed.Actions) != 0 {
		t.Errorf("removed %+v", removed)
	}
	if got := feDecode[feRevertAllResult](t, s, http.MethodPost, changesPath+"revert-all",
		`{"source":{"namespace":"ns","kind":"Deployment","name":"app"},"actor":"dana"}`); got.Count != 0 {
		t.Errorf("revert all %+v", got)
	}
}
