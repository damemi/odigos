package apimap

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/odigos-io/odigos/insights/internal/api/http/apitypes"
	apschema "github.com/odigos-io/odigos/insights/internal/autopilot/schema"
	"github.com/odigos-io/odigos/insights/internal/interrogation"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

func at(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func ptr[T any](v T) *T { return &v }

// sameJSON fails unless got marshals to the same JSON value as want.
func sameJSON(t *testing.T, got any, want string) {
	t.Helper()
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var g, w any
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want: %v", err)
	}
	if !reflect.DeepEqual(g, w) {
		gi, _ := json.MarshalIndent(g, "", "  ")
		wi, _ := json.MarshalIndent(w, "", "  ")
		t.Fatalf("got:\n%s\nwant:\n%s", gi, wi)
	}
}

func claimsRequest() repo.InterrogationRequest {
	return repo.InterrogationRequest{
		Source:         repo.InterrogationSource{Namespace: "claims-demo", Kind: "Deployment", Name: "claims-intake"},
		Goal:           "observability",
		Question:       "Why is claim submission slow for some users?",
		TransactionIDs: []int64{},
		AllowChanges:   []string{"custom_instrumentation"},
		Budget:         repo.InterrogationBudget{MaxProbes: 5, TimeLimitSeconds: 3600, MaxRounds: 5},
		Trigger:        repo.InterrogationTrigger{Kind: "manual", Actor: "alon"},
	}
}

func node(t *testing.T, name, state string, visits int, entered, left string) interrogation.NodeRun {
	n := interrogation.NodeRun{Name: name, State: state, Visits: visits, EnteredAt: at(t, entered)}
	if left != "" {
		n.LeftAt = ptr(at(t, left))
	}
	return n
}

// runningState is the run of the detail example (mid-run, round 2, parked in
// the observe wait) as the workflow stores it.
func runningState(t *testing.T) interrogation.State {
	answer := "Most of the time in POST /api/claims is inside ClaimValidator.validate, which calls FraudClient.score once per line item."
	return interrogation.State{
		V: interrogation.StateVersion, Graph: interrogation.GraphVersion, ID: "0b6f6c1e-5d0a-4b53-9f3e-0c1d7a9e4f21",
		Status: interrogation.StatusRunning, Step: interrogation.StepObserve, Node: "wait_observe",
		Seq: 41, Round: 2,
		StartedAt:  at(t, "2026-11-17T09:38:03Z"),
		DeadlineAt: at(t, "2026-11-17T10:38:03Z"),
		Wait: &interrogation.Wait{
			Node: "wait_observe", Reason: interrogation.WaitObserve,
			Since: at(t, "2026-11-17T09:47:10Z"), Until: at(t, "2026-11-17T09:48:10Z"), InterruptID: "wait-7",
		},
		Probes: []interrogation.Probe{
			{ChangeID: "chg_3f9a1c07b2d44e6a8c51", Status: interrogation.ProbeApplied, Round: 1},
			{
				ChangeID: "chg_a41be09d7c2f4b318e60", Language: "java", Status: interrogation.ProbeDenied, DenyReason: "already_probed",
				Round: 1, ProposedAt: at(t, "2026-11-17T09:41:02Z"),
				Rec: apschema.Recommendation{Probe: apschema.Probe{ClassName: "com.claims.intake.ClaimRepository", MethodName: "save"}},
			},
			{ChangeID: "chg_7d20c5e9f1a84b6c93d2", Status: interrogation.ProbeApplied, Round: 2},
		},
		ModelCalls: 2, InputTokens: 17400, OutputTokens: 1160, Model: "gpt-4o-mini",
		Scope: []interrogation.ScopeTransaction{{TransactionID: 4211, Operation: "POST /api/claims", Kind: "HTTP", SelectedBy: "auto"}},
		Answer: &interrogation.Answer{
			Text: answer, Certainty: 45, Round: 2, UpdatedAt: at(t, "2026-11-17T09:46:38Z"),
		},
		Rounds: []interrogation.Round{
			{
				Round: 1, State: interrogation.RoundCompleted, Outcome: interrogation.RoundObserved,
				StartedAt: at(t, "2026-11-17T09:38:03Z"), EndedAt: ptr(at(t, "2026-11-17T09:46:00Z")),
				Nodes: []interrogation.NodeRun{
					node(t, "prepare", "done", 1, "2026-11-17T09:38:03Z", "2026-11-17T09:38:03Z"),
					node(t, "wait_baseline", "done", 1, "2026-11-17T09:38:03Z", "2026-11-17T09:38:33Z"),
					node(t, "gate", "done", 1, "2026-11-17T09:38:33Z", "2026-11-17T09:38:33Z"),
					node(t, "gather", "done", 1, "2026-11-17T09:38:33Z", "2026-11-17T09:38:35Z"),
					node(t, "analyze", "done", 1, "2026-11-17T09:38:35Z", "2026-11-17T09:40:55Z"),
					node(t, "record", "done", 1, "2026-11-17T09:40:55Z", "2026-11-17T09:40:55Z"),
					node(t, "apply", "done", 1, "2026-11-17T09:40:55Z", "2026-11-17T09:41:02Z"),
					node(t, "wait_apply", "done", 2, "2026-11-17T09:41:02Z", "2026-11-17T09:41:12Z"),
					node(t, "confirm", "done", 2, "2026-11-17T09:41:07Z", "2026-11-17T09:41:12Z"),
					node(t, "wait_observe", "done", 1, "2026-11-17T09:41:12Z", "2026-11-17T09:42:12Z"),
					node(t, "observe", "done", 1, "2026-11-17T09:42:12Z", "2026-11-17T09:42:14Z"),
				},
				Observed: &interrogation.EvidenceCounts{Transactions: 1, Functions: 57, Edges: 142, NewEdges: 142},
				Reply: &interrogation.RoundReply{
					Text: "POST /api/claims spends its time below ClaimController.submit; ClaimValidator.validate has no span.", Certainty: 30,
				},
				Proposed:      []string{"chg_3f9a1c07b2d44e6a8c51", "chg_a41be09d7c2f4b318e60"},
				Applied:       []string{"chg_3f9a1c07b2d44e6a8c51"},
				ObservedAfter: &interrogation.EvidenceCounts{Transactions: 1, Functions: 61, Edges: 146, NewEdges: 4},
			},
			{
				Round: 2, State: interrogation.RoundInProgress, StartedAt: at(t, "2026-11-17T09:42:14Z"),
				Nodes: []interrogation.NodeRun{
					node(t, "gate", "done", 1, "2026-11-17T09:42:14Z", "2026-11-17T09:42:14Z"),
					node(t, "gather", "done", 1, "2026-11-17T09:42:14Z", "2026-11-17T09:42:16Z"),
					node(t, "analyze", "done", 1, "2026-11-17T09:42:16Z", "2026-11-17T09:46:38Z"),
					node(t, "record", "done", 1, "2026-11-17T09:46:38Z", "2026-11-17T09:46:38Z"),
					node(t, "apply", "done", 1, "2026-11-17T09:46:38Z", "2026-11-17T09:46:45Z"),
					node(t, "wait_apply", "done", 3, "2026-11-17T09:46:45Z", "2026-11-17T09:47:10Z"),
					node(t, "confirm", "done", 3, "2026-11-17T09:46:50Z", "2026-11-17T09:47:10Z"),
					node(t, "wait_observe", "waiting", 1, "2026-11-17T09:47:10Z", ""),
				},
				Observed: &interrogation.EvidenceCounts{Transactions: 1, Functions: 61, Edges: 146, NewEdges: 4},
				Reply:    &interrogation.RoundReply{Text: answer, Certainty: 45},
				Proposed: []string{"chg_7d20c5e9f1a84b6c93d2"},
				Applied:  []string{"chg_7d20c5e9f1a84b6c93d2"},
			},
		},
	}
}

// storedRun is the run as the repo reads it from the two sessions: state is
// marshaled the way the workflow writes it into the session.
func storedRun(t *testing.T, st *interrogation.State, req repo.InterrogationRequest) repo.Interrogation {
	t.Helper()
	in := repo.Interrogation{
		ID:        "0b6f6c1e-5d0a-4b53-9f3e-0c1d7a9e4f21",
		CreatedAt: at(t, "2026-11-17T09:38:00Z"),
		Open:      true,
		Request:   req,
	}
	if st != nil {
		b, err := json.Marshal(st)
		if err != nil {
			t.Fatal(err)
		}
		in.Started, in.Run = true, b
		in.RunStatus, in.RunEndReason, in.RunError = string(st.Status), string(st.EndReason), st.Error
		in.Seq, in.Round = st.Seq, st.Round
	}
	return in
}

// storedChange is a row of interrogation_requested_probes in the rule.
func storedChange(t *testing.T, id string, round int, class, method, requested, applied string) repo.InterrogationChange {
	return repo.InterrogationChange{
		ChangeID: id, Round: round, FirstSeen: at(t, requested), Language: "java", ClassName: class, MethodName: method,
		Desired: repo.ChangeApproved, ExpiresAt: at(t, "2026-11-17T10:38:03Z"),
		InRule: true, AppliedAt: at(t, applied), UpdatedAt: at(t, applied),
	}
}

// The detail example of the API's documentation, from the state the workflow
// stores and the run's rows: certainty is an integer, the rounds and answer
// are as stored, a denied proposal is listed among the changes, and nothing
// unset is written.
func TestFromInterrogation_Running(t *testing.T) {
	st := runningState(t)
	in := storedRun(t, &st, claimsRequest())
	in.Changes = repo.InterrogationChangeCounts{Stored: 2, Live: 2, NextExpiryAt: at(t, "2026-11-17T10:38:03Z")}
	rows := []repo.InterrogationChange{
		storedChange(t, "chg_3f9a1c07b2d44e6a8c51", 1, "com.claims.intake.ClaimValidator", "validate", "2026-11-17T09:41:02Z", "2026-11-17T09:41:07Z"),
		storedChange(t, "chg_7d20c5e9f1a84b6c93d2", 2, "com.claims.intake.FraudClient", "score", "2026-11-17T09:46:45Z", "2026-11-17T09:47:10Z"),
	}

	got, err := FromInterrogation(in, rows, true, at(t, "2026-11-17T09:50:03Z"))
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, got, `{
  "id": "0b6f6c1e-5d0a-4b53-9f3e-0c1d7a9e4f21",
  "display_id": "INT-0B6F6C1E",
  "title": "Why is claim submission slow for some users?",
  "source": {"namespace": "claims-demo", "kind": "Deployment", "name": "claims-intake"},
  "goal": "observability",
  "question": "Why is claim submission slow for some users?",
  "transaction_ids": [],
  "allow_changes": ["custom_instrumentation"],
  "budget": {"max_probes": 5, "time_limit_seconds": 3600, "max_rounds": 5},
  "trigger": {"kind": "manual", "actor": "alon"},
  "status": "running",
  "end_reason": "",
  "created_at": "2026-11-17T09:38:00Z",
  "started_at": "2026-11-17T09:38:03Z",
  "deadline_at": "2026-11-17T10:38:03Z",
  "scope": [{"transaction_id": 4211, "operation": "POST /api/claims", "kind": "HTTP", "selected_by": "auto"}],
  "progress": {
    "round": 2, "max_rounds": 5, "node": "wait_observe",
    "wait": {"reason": "observe", "since": "2026-11-17T09:47:10Z", "deadline_at": "2026-11-17T09:48:10Z"}
  },
  "certainty": 45,
  "answer": {
    "text": "Most of the time in POST /api/claims is inside ClaimValidator.validate, which calls FraudClient.score once per line item.",
    "certainty": 45,
    "final": false,
    "round": 2,
    "updated_at": "2026-11-17T09:46:38Z"
  },
  "change_counts": {"total": 3, "live": 2, "reverting": 0, "reverted": 0, "expired": 0, "failed": 0, "denied": 1, "next_expiry_at": "2026-11-17T10:38:03Z"},
  "changes": [
    {"id": "chg_3f9a1c07b2d44e6a8c51", "round": 1, "language": "java",
     "target": {"class_name": "com.claims.intake.ClaimValidator", "method_name": "validate"},
     "state": "active",
     "checks": ["changes_allowed", "in_call_graph", "application_code", "not_already_probed", "within_workload_cap", "within_run_budget"],
     "requested_at": "2026-11-17T09:41:02Z", "applied_at": "2026-11-17T09:41:07Z", "expires_at": "2026-11-17T10:38:03Z",
     "lifetime": "temporary", "actions": ["keep", "revert"]},
    {"id": "chg_a41be09d7c2f4b318e60", "round": 1, "language": "java",
     "target": {"class_name": "com.claims.intake.ClaimRepository", "method_name": "save"},
     "state": "denied", "deny_reason": "already_probed",
     "checks": ["changes_allowed", "in_call_graph", "application_code"],
     "requested_at": "2026-11-17T09:41:02Z", "lifetime": "temporary"},
    {"id": "chg_7d20c5e9f1a84b6c93d2", "round": 2, "language": "java",
     "target": {"class_name": "com.claims.intake.FraudClient", "method_name": "score"},
     "state": "active",
     "checks": ["changes_allowed", "in_call_graph", "application_code", "not_already_probed", "within_workload_cap", "within_run_budget"],
     "requested_at": "2026-11-17T09:46:45Z", "applied_at": "2026-11-17T09:47:10Z", "expires_at": "2026-11-17T10:38:03Z",
     "lifetime": "temporary", "actions": ["keep", "revert"]}
  ],
  "usage": {"rounds": 2, "duration_seconds": 720, "probes_used": 2, "max_probes": 5, "model_calls": 2, "input_tokens": 17400, "output_tokens": 1160, "model": "gpt-4o-mini"},
  "last_sequence": 41,
  "rounds": [
    {
      "round": 1, "state": "completed", "outcome": "observed", "final": false,
      "started_at": "2026-11-17T09:38:03Z", "ended_at": "2026-11-17T09:46:00Z",
      "nodes": [
        {"name": "prepare", "state": "done", "visits": 1, "entered_at": "2026-11-17T09:38:03Z", "left_at": "2026-11-17T09:38:03Z"},
        {"name": "wait_baseline", "state": "done", "visits": 1, "entered_at": "2026-11-17T09:38:03Z", "left_at": "2026-11-17T09:38:33Z"},
        {"name": "gate", "state": "done", "visits": 1, "entered_at": "2026-11-17T09:38:33Z", "left_at": "2026-11-17T09:38:33Z"},
        {"name": "gather", "state": "done", "visits": 1, "entered_at": "2026-11-17T09:38:33Z", "left_at": "2026-11-17T09:38:35Z"},
        {"name": "analyze", "state": "done", "visits": 1, "entered_at": "2026-11-17T09:38:35Z", "left_at": "2026-11-17T09:40:55Z"},
        {"name": "record", "state": "done", "visits": 1, "entered_at": "2026-11-17T09:40:55Z", "left_at": "2026-11-17T09:40:55Z"},
        {"name": "apply", "state": "done", "visits": 1, "entered_at": "2026-11-17T09:40:55Z", "left_at": "2026-11-17T09:41:02Z"},
        {"name": "wait_apply", "state": "done", "visits": 2, "entered_at": "2026-11-17T09:41:02Z", "left_at": "2026-11-17T09:41:12Z"},
        {"name": "confirm", "state": "done", "visits": 2, "entered_at": "2026-11-17T09:41:07Z", "left_at": "2026-11-17T09:41:12Z"},
        {"name": "wait_observe", "state": "done", "visits": 1, "entered_at": "2026-11-17T09:41:12Z", "left_at": "2026-11-17T09:42:12Z"},
        {"name": "observe", "state": "done", "visits": 1, "entered_at": "2026-11-17T09:42:12Z", "left_at": "2026-11-17T09:42:14Z"}
      ],
      "observed": {"transactions": 1, "functions": 57, "edges": 142, "new_edges": 142},
      "reply": {"text": "POST /api/claims spends its time below ClaimController.submit; ClaimValidator.validate has no span.", "certainty": 30, "done": false},
      "proposed": ["chg_3f9a1c07b2d44e6a8c51", "chg_a41be09d7c2f4b318e60"],
      "applied": ["chg_3f9a1c07b2d44e6a8c51"],
      "observed_after": {"transactions": 1, "functions": 61, "edges": 146, "new_edges": 4}
    },
    {
      "round": 2, "state": "in_progress", "final": false,
      "started_at": "2026-11-17T09:42:14Z",
      "nodes": [
        {"name": "gate", "state": "done", "visits": 1, "entered_at": "2026-11-17T09:42:14Z", "left_at": "2026-11-17T09:42:14Z"},
        {"name": "gather", "state": "done", "visits": 1, "entered_at": "2026-11-17T09:42:14Z", "left_at": "2026-11-17T09:42:16Z"},
        {"name": "analyze", "state": "done", "visits": 1, "entered_at": "2026-11-17T09:42:16Z", "left_at": "2026-11-17T09:46:38Z"},
        {"name": "record", "state": "done", "visits": 1, "entered_at": "2026-11-17T09:46:38Z", "left_at": "2026-11-17T09:46:38Z"},
        {"name": "apply", "state": "done", "visits": 1, "entered_at": "2026-11-17T09:46:38Z", "left_at": "2026-11-17T09:46:45Z"},
        {"name": "wait_apply", "state": "done", "visits": 3, "entered_at": "2026-11-17T09:46:45Z", "left_at": "2026-11-17T09:47:10Z"},
        {"name": "confirm", "state": "done", "visits": 3, "entered_at": "2026-11-17T09:46:50Z", "left_at": "2026-11-17T09:47:10Z"},
        {"name": "wait_observe", "state": "waiting", "visits": 1, "entered_at": "2026-11-17T09:47:10Z"}
      ],
      "observed": {"transactions": 1, "functions": 61, "edges": 146, "new_edges": 4},
      "reply": {"text": "Most of the time in POST /api/claims is inside ClaimValidator.validate, which calls FraudClient.score once per line item.", "certainty": 45, "done": false},
      "proposed": ["chg_7d20c5e9f1a84b6c93d2"],
      "applied": ["chg_7d20c5e9f1a84b6c93d2"]
    }
  ]
}`)

	// The list leaves out what only the detail page draws.
	list, err := InterrogationList([]repo.Interrogation{in}, at(t, "2026-11-17T09:50:03Z"))
	if err != nil {
		t.Fatal(err)
	}
	if list.Count != 1 || list.Items[0].Rounds != nil || list.Items[0].Answer != nil || list.Items[0].Changes != nil || list.Items[0].Certainty == nil {
		t.Errorf("list item %+v", list.Items[0])
	}
}

// The start's answer: a queued run has no workflow state.
func TestFromInterrogation_Queued(t *testing.T) {
	req := claimsRequest()
	req.TransactionIDs = []int64{4211, 4215}
	in := storedRun(t, nil, req)
	got, err := FromInterrogation(in, nil, true, at(t, "2026-11-17T09:39:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, got, `{
  "id": "0b6f6c1e-5d0a-4b53-9f3e-0c1d7a9e4f21",
  "display_id": "INT-0B6F6C1E",
  "title": "Why is claim submission slow for some users?",
  "source": {"namespace": "claims-demo", "kind": "Deployment", "name": "claims-intake"},
  "goal": "observability",
  "question": "Why is claim submission slow for some users?",
  "transaction_ids": [4211, 4215],
  "allow_changes": ["custom_instrumentation"],
  "budget": {"max_probes": 5, "time_limit_seconds": 3600, "max_rounds": 5},
  "trigger": {"kind": "manual", "actor": "alon"},
  "status": "queued",
  "end_reason": "",
  "created_at": "2026-11-17T09:38:00Z",
  "scope": [],
  "progress": {"round": 0, "max_rounds": 5},
  "change_counts": {"total": 0, "live": 0, "reverting": 0, "reverted": 0, "expired": 0, "failed": 0, "denied": 0},
  "usage": {"rounds": 0, "duration_seconds": 0, "probes_used": 0, "max_probes": 5, "model_calls": 0, "input_tokens": 0, "output_tokens": 0},
  "last_sequence": 0
}`)
}

// The wire status and its fields follow the two sessions.
func TestFromInterrogation_Status(t *testing.T) {
	now := at(t, "2026-11-17T10:00:00Z")
	stop := &repo.InterrogationStop{Changes: "revert", Actor: "alon", At: at(t, "2026-11-17T09:50:12Z")}
	tests := []struct {
		name   string
		edit   func(*repo.Interrogation, *interrogation.State)
		status string
		check  func(*testing.T, apitypes.Interrogation)
	}{
		{
			name:   "an open run with a stop recorded is stopping",
			edit:   func(in *repo.Interrogation, _ *interrogation.State) { in.Control.Stop = stop },
			status: repo.InterrogationStopping,
			check: func(t *testing.T, out apitypes.Interrogation) {
				if out.Stop == nil || out.Stop.Changes != "revert" || out.Stop.Actor != "alon" || !out.Stop.RequestedAt.Equal(stop.At) {
					t.Errorf("stop %+v", out.Stop)
				}
				if out.Progress.Node != "wait_observe" || out.Progress.Wait == nil {
					t.Errorf("progress %+v", out.Progress)
				}
			},
		},
		{
			name: "a run the leader closed reads from closed",
			edit: func(in *repo.Interrogation, _ *interrogation.State) {
				in.Open = false
				in.Closed = &repo.InterrogationClosed{Reason: "interrupted", Error: "overdue", At: at(t, "2026-11-17T10:43:03Z")}
			},
			status: repo.InterrogationFailed,
			check: func(t *testing.T, out apitypes.Interrogation) {
				if out.EndReason != "interrupted" || out.Error != "overdue" || out.EndedAt == nil || !out.EndedAt.Equal(at(t, "2026-11-17T10:43:03Z")) {
					t.Errorf("ending %q %q %v", out.EndReason, out.Error, out.EndedAt)
				}
				if out.Progress.Node != "" || out.Progress.Wait != nil {
					t.Errorf("a closed run reports a node: %+v", out.Progress)
				}
				if out.Usage.DurationSeconds != int(at(t, "2026-11-17T10:43:03Z").Sub(at(t, "2026-11-17T09:38:03Z"))/time.Second) {
					t.Errorf("duration %d", out.Usage.DurationSeconds)
				}
			},
		},
		{
			name: "a run the workflow ended reads from its state",
			edit: func(in *repo.Interrogation, st *interrogation.State) {
				in.Open = false
				in.RunStatus, in.RunEndReason = "completed", "done"
				st.EndedAt = at(t, "2026-11-17T09:58:03Z")
				st.Wait = nil
			},
			status: "completed",
			check: func(t *testing.T, out apitypes.Interrogation) {
				if out.EndReason != "done" || out.EndedAt == nil || out.Usage.DurationSeconds != 1200 {
					t.Errorf("ending %q %v %d", out.EndReason, out.EndedAt, out.Usage.DurationSeconds)
				}
			},
		},
		{
			name:   "an extension moves the deadline",
			edit:   func(in *repo.Interrogation, _ *interrogation.State) { in.Control.ExtendSeconds = 1800 },
			status: repo.InterrogationRunning,
			check: func(t *testing.T, out apitypes.Interrogation) {
				if out.ExtendedSeconds != 1800 || !out.DeadlineAt.Equal(at(t, "2026-11-17T11:08:03Z")) || out.Budget.TimeLimitSeconds != 3600 {
					t.Errorf("deadline %v extended %d budget %d", out.DeadlineAt, out.ExtendedSeconds, out.Budget.TimeLimitSeconds)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := runningState(t)
			in := storedRun(t, &st, claimsRequest())
			tt.edit(&in, &st)
			b, err := json.Marshal(st)
			if err != nil {
				t.Fatal(err)
			}
			in.Run = b
			out, err := FromInterrogation(in, nil, true, now)
			if err != nil {
				t.Fatal(err)
			}
			if out.Status != tt.status {
				t.Fatalf("status %q, want %q", out.Status, tt.status)
			}
			tt.check(t, out)
		})
	}
}

// eventPayloads names the generated payload type of every event type.
var eventPayloads = map[string]func() any{
	interrogation.EventStarted:         func() any { return &apitypes.InterrogationEventStartedPayload{} },
	interrogation.EventStopRequested:   func() any { return &apitypes.InterrogationEventStopRequestedPayload{} },
	interrogation.EventExtended:        func() any { return &apitypes.InterrogationEventExtendedPayload{} },
	interrogation.EventEnded:           func() any { return &apitypes.InterrogationEventEndedPayload{} },
	interrogation.EventRoundStarted:    func() any { return &apitypes.InterrogationEventRoundStartedPayload{} },
	interrogation.EventRoundFinished:   func() any { return &apitypes.InterrogationEventRoundFinishedPayload{} },
	interrogation.EventNodeFinished:    func() any { return &apitypes.InterrogationEventNodeFinishedPayload{} },
	interrogation.EventWaitStarted:     func() any { return &apitypes.InterrogationEventWaitStartedPayload{} },
	interrogation.EventWaitFinished:    func() any { return &apitypes.InterrogationEventWaitFinishedPayload{} },
	interrogation.EventEvidence:        func() any { return &apitypes.InterrogationEventEvidencePayload{} },
	interrogation.EventModelCalled:     func() any { return &apitypes.InterrogationEventModelCalledPayload{} },
	interrogation.EventAnswerUpdated:   func() any { return &apitypes.InterrogationAnswer{} },
	interrogation.EventChangeProposed:  func() any { return &apitypes.InterrogationEventChangeProposedPayload{} },
	interrogation.EventChangeDenied:    func() any { return &apitypes.InterrogationEventChangeDeniedPayload{} },
	interrogation.EventChangeRequested: func() any { return &apitypes.InterrogationEventChangeRequestedPayload{} },
	interrogation.EventChangeApplied:   func() any { return &apitypes.InterrogationEventChangeAppliedPayload{} },
	interrogation.EventChangeFailed:    func() any { return &apitypes.InterrogationEventChangeFailedPayload{} },
	interrogation.EventChangeReverted:  func() any { return &apitypes.InterrogationEventChangeRevertedPayload{} },
	interrogation.EventErrorRaised:     func() any { return &apitypes.InterrogationEventErrorRaisedPayload{} },
}

// storedEvents is one event of every type, as the workflow stores them.
var storedEvents = []string{
	`{"seq":1,"type":"interrogation.started","time":"2026-11-17T09:38:03Z","round":0,"node":"prepare","message":"Interrogation started","payload":{"allow_changes":["custom_instrumentation"],"deadline_at":"2026-11-17T10:38:03Z","max_probes":5,"max_rounds":5,"revision":"interrogation=1/prompt=7"}}`,
	`{"seq":2,"type":"round.started","time":"2026-11-17T09:38:03Z","round":1,"node":"prepare","message":"Round 1 started","payload":{"final":false,"round":1}}`,
	`{"seq":3,"type":"wait.started","time":"2026-11-17T09:38:03Z","round":1,"node":"prepare","message":"Waiting: traffic","payload":{"deadline_at":"2026-11-17T09:38:33Z","reason":"traffic"}}`,
	`{"seq":4,"type":"wait.finished","time":"2026-11-17T09:38:33Z","round":1,"node":"wait_baseline","message":"Wait traffic satisfied","payload":{"outcome":"satisfied","reason":"traffic"}}`,
	`{"seq":5,"type":"evidence.collected","time":"2026-11-17T09:38:35Z","round":1,"node":"gather","message":"Read 142 call-graph edges in 1 transactions","payload":{"edges":142,"functions":57,"kind":"call_graph","new_edges":142,"sample_edges":[{"caller":"a.B.c","callee":"a.B.d"}],"transactions":1,"truncated":false}}`,
	`{"seq":6,"type":"model.called","time":"2026-11-17T09:40:55Z","round":1,"node":"analyze","message":"Model replied","payload":{"attempt":1,"duration_ms":4100,"input_tokens":8300,"model":"gpt-4o-mini","outcome":"ok","output_tokens":620}}`,
	`{"seq":7,"type":"answer.updated","time":"2026-11-17T09:40:55Z","round":1,"node":"record","message":"Answer updated, certainty 30","payload":{"certainty":30,"final":false,"round":1,"text":"t","updated_at":"2026-11-17T09:40:55Z"}}`,
	`{"seq":8,"type":"change.proposed","time":"2026-11-17T09:40:55Z","round":1,"node":"record","change_id":"chg_3f9a1c07b2d44e6a8c51","message":"Proposed a probe on a.B.c","payload":{"category":"observability","kind":"custom_instrumentation","probe":{"class_name":"a.B","language":"java","method_name":"c"},"reason":"why","target":"a.B.c"}}`,
	`{"seq":9,"type":"change.requested","time":"2026-11-17T09:41:02Z","round":1,"node":"apply","change_id":"chg_3f9a1c07b2d44e6a8c51","message":"Probe on a.B.c passed the checks","payload":{"checks":["changes_allowed","in_call_graph","application_code","not_already_probed","within_workload_cap","within_run_budget"],"kind":"custom_instrumentation","target":"a.B.c"}}`,
	`{"seq":10,"type":"change.denied","time":"2026-11-17T09:41:02Z","round":1,"node":"apply","change_id":"chg_a41be09d7c2f4b318e60","message":"Denied a probe on a.B.save: already_probed","payload":{"checks":["changes_allowed","in_call_graph","application_code"],"deny_reason":"already_probed","detail":"","kind":"custom_instrumentation","target":"a.B.save"}}`,
	`{"seq":11,"type":"node.finished","time":"2026-11-17T09:41:02Z","round":1,"node":"apply","message":"apply finished","payload":{"next":"wait_apply","node":"apply"}}`,
	`{"seq":12,"type":"change.applied","time":"2026-11-17T09:41:07Z","round":1,"node":"confirm","change_id":"chg_3f9a1c07b2d44e6a8c51","message":"Applied a probe on a.B.c","payload":{"expires_at":"2026-11-17T10:38:03Z","kind":"custom_instrumentation","rule_name":"r","target":"a.B.c"}}`,
	`{"seq":13,"type":"change.failed","time":"2026-11-17T09:42:02Z","round":1,"node":"confirm","change_id":"chg_7d20c5e9f1a84b6c93d2","message":"Probe on a.B.e failed: not_applied_in_time","payload":{"kind":"custom_instrumentation","reason":"not_applied_in_time","target":"a.B.e"}}`,
	`{"seq":14,"type":"round.finished","time":"2026-11-17T09:42:14Z","round":1,"node":"observe","message":"Round 1 finished: observed","payload":{"outcome":"observed","round":1}}`,
	`{"seq":15,"type":"error.raised","time":"2026-11-17T09:43:00Z","round":2,"node":"gather","message":"gather failed: boom","payload":{"error":"boom","node":"gather","retry_in_seconds":5}}`,
	`{"seq":16,"type":"interrogation.extended","time":"2026-11-17T09:44:00Z","round":2,"node":"gate","message":"Deadline moved 1800s later","payload":{"deadline_at":"2026-11-17T11:08:03Z","seconds":1800}}`,
	`{"seq":17,"type":"interrogation.stop_requested","time":"2026-11-17T09:45:00Z","round":2,"node":"gate","message":"Stop requested, changes: revert","payload":{"actor":"alon","changes":"revert"}}`,
	`{"seq":18,"type":"change.reverted","time":"2026-11-17T09:45:01Z","round":2,"node":"conclude","change_id":"chg_3f9a1c07b2d44e6a8c51","message":"Probe on a.B.c reverted: stop","payload":{"by":"alon","end_reason":"stop","kind":"custom_instrumentation","target":"a.B.c"}}`,
	`{"seq":19,"type":"interrogation.ended","time":"2026-11-17T09:45:01Z","round":2,"node":"conclude","message":"Interrogation stopped: stopped_by_user","payload":{"certainty":30,"end_reason":"stopped_by_user","status":"stopped"}}`,
}

// Every stored event comes out with seq renamed sequence and its payload as
// stored, which decodes strictly into the generated type its type names.
func TestInterrogationEventList(t *testing.T) {
	page := repo.InterrogationEvents{LastSequence: 19, Complete: true}
	for i, s := range storedEvents {
		page.Items = append(page.Items, repo.InterrogationEvent{Seq: int64(i + 1), JSON: json.RawMessage(s)})
	}
	got, err := InterrogationEventList(page)
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != len(storedEvents) || got.LastSequence != 19 || !got.Complete {
		t.Fatalf("page count %d last %d complete %v", got.Count, got.LastSequence, got.Complete)
	}
	seen := map[string]bool{}
	for i, ev := range got.Items {
		var stored map[string]json.RawMessage
		if err := json.Unmarshal([]byte(storedEvents[i]), &stored); err != nil {
			t.Fatal(err)
		}
		if ev.Sequence != int64(i+1) || !bytes.Equal(ev.Payload, stored["payload"]) {
			t.Errorf("event %d: sequence %d payload %s", i+1, ev.Sequence, ev.Payload)
		}
		b, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(b, &wire); err != nil {
			t.Fatal(err)
		}
		if _, ok := wire["seq"]; ok {
			t.Errorf("event %d still has seq: %s", i+1, b)
		}
		newPayload, ok := eventPayloads[ev.Type]
		if !ok {
			t.Fatalf("no payload type for %q", ev.Type)
		}
		dec := json.NewDecoder(bytes.NewReader(ev.Payload))
		dec.DisallowUnknownFields()
		if err := dec.Decode(newPayload()); err != nil {
			t.Errorf("%s payload: %v", ev.Type, err)
		}
		seen[ev.Type] = true
	}
	if len(seen) != len(eventPayloads) {
		t.Errorf("event types covered %d of %d", len(seen), len(eventPayloads))
	}
}

// The workflow's recorded two-round run maps event for event, and its last
// event is interrogation.ended.
func TestInterrogationEventList_WorkflowRun(t *testing.T) {
	b, err := os.ReadFile("../../../interrogation/testdata/run_two_rounds.json")
	if err != nil {
		t.Fatal(err)
	}
	var run struct {
		Events []json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(b, &run); err != nil {
		t.Fatal(err)
	}
	page := repo.InterrogationEvents{LastSequence: int64(len(run.Events)), Complete: true}
	for i, ev := range run.Events {
		page.Items = append(page.Items, repo.InterrogationEvent{Seq: int64(i + 1), JSON: ev})
	}
	got, err := InterrogationEventList(page)
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != len(run.Events) || !got.Complete {
		t.Fatalf("page count %d complete %v", got.Count, got.Complete)
	}
	for i, ev := range got.Items {
		if ev.Sequence != int64(i+1) {
			t.Errorf("event %d: sequence %d", i+1, ev.Sequence)
		}
		newPayload, ok := eventPayloads[ev.Type]
		if !ok {
			t.Fatalf("no payload type for %q", ev.Type)
		}
		dec := json.NewDecoder(bytes.NewReader(ev.Payload))
		dec.DisallowUnknownFields()
		if err := dec.Decode(newPayload()); err != nil {
			t.Errorf("event %d %s payload: %v", i+1, ev.Type, err)
		}
	}
	if last := got.Items[len(got.Items)-1].Type; last != interrogation.EventEnded {
		t.Errorf("last event %q", last)
	}
}

// What a stored change reports by its row, once it ended or while it cannot
// be placed.
func TestFromChange(t *testing.T) {
	now := at(t, "2026-11-17T11:00:00Z")
	removed := at(t, "2026-11-17T09:55:02Z")
	tests := []struct {
		name         string
		edit         func(*repo.InterrogationChange)
		state, ended string
		endedAt      *time.Time
	}{
		{
			name:  "expired after it was applied",
			edit:  func(c *repo.InterrogationChange) { c.InRule, c.RemovedAt = false, at(t, "2026-11-17T10:38:05Z") },
			state: "expired", ended: "expired", endedAt: ptr(at(t, "2026-11-17T10:38:05Z")),
		},
		{
			name:  "expired before it reached the rule",
			edit:  func(c *repo.InterrogationChange) { c.InRule, c.AppliedAt = false, time.Time{} },
			state: "expired", ended: "expired", endedAt: ptr(at(t, "2026-11-17T10:38:03Z")),
		},
		{
			name:  "reverted, still in the rule",
			edit:  func(c *repo.InterrogationChange) { c.Desired, c.EndReason = repo.ChangeReverted, "stop" },
			state: "reverting", ended: "stop",
		},
		{
			name: "reverted",
			edit: func(c *repo.InterrogationChange) {
				c.Desired, c.EndReason, c.InRule, c.RemovedAt = repo.ChangeReverted, "stop", false, removed
			},
			state: "reverted", ended: "stop", endedAt: &removed,
		},
		{
			name: "never applied",
			edit: func(c *repo.InterrogationChange) {
				c.Desired, c.EndReason, c.InRule, c.AppliedAt = repo.ChangeReverted, "not_applied_in_time", false, time.Time{}
				c.UpdatedAt = at(t, "2026-11-17T09:42:02Z")
			},
			state: "failed", ended: "not_applied_in_time", endedAt: ptr(at(t, "2026-11-17T09:42:02Z")),
		},
		{
			name: "blocked",
			edit: func(c *repo.InterrogationChange) {
				c.ExpiresAt, c.InRule, c.AppliedAt, c.BlockReason = at(t, "2026-11-17T12:00:00Z"), false, time.Time{}, "rule_disabled"
			},
			state: "blocked",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := storedChange(t, "chg_3f9a1c07b2d44e6a8c51", 1, "com.claims.intake.ClaimValidator", "validate", "2026-11-17T09:41:02Z", "2026-11-17T09:41:07Z")
			tt.edit(&c)
			got := fromChange(c, now)
			if got.State != tt.state || got.EndReason != tt.ended {
				t.Errorf("state %q end %q", got.State, got.EndReason)
			}
			if (got.EndedAt == nil) != (tt.endedAt == nil) || got.EndedAt != nil && !got.EndedAt.Equal(*tt.endedAt) {
				t.Errorf("ended_at %v, want %v", got.EndedAt, tt.endedAt)
			}
			if (got.BlockReason != "") != (tt.state == "blocked") || got.ExpiresAt == nil {
				t.Errorf("block_reason %q, expires_at %v", got.BlockReason, got.ExpiresAt)
			}
		})
	}
}

// A probe denied as already requested has its stored change under the same
// id, which is listed and counted once.
func TestRunChanges_DeniedAgain(t *testing.T) {
	st := runningState(t)
	st.Probes = []interrogation.Probe{{
		ChangeID: "chg_3f9a1c07b2d44e6a8c51", Status: interrogation.ProbeDenied, DenyReason: "already_requested",
		Round: 2, ProposedAt: at(t, "2026-11-17T09:46:40Z"),
	}}
	in := storedRun(t, &st, claimsRequest())
	in.Changes = repo.InterrogationChangeCounts{Stored: 1, Live: 1}
	rows := []repo.InterrogationChange{
		storedChange(t, "chg_3f9a1c07b2d44e6a8c51", 1, "com.claims.intake.ClaimValidator", "validate", "2026-11-17T09:41:02Z", "2026-11-17T09:41:07Z"),
	}
	got, err := FromInterrogation(in, rows, true, at(t, "2026-11-17T09:50:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Changes) != 1 || got.Changes[0].State != "active" || got.ChangeCounts.Total != 1 || got.ChangeCounts.Denied != 0 {
		t.Errorf("changes %+v, counts %+v", got.Changes, got.ChangeCounts)
	}
}

// The changes resource serves a run's stored changes and denied proposals
// with the run, the probed function as one string, and the model's reason
// cut to its bound; a kept change whose run is gone has no run.
func TestInterrogationRunChanges(t *testing.T) {
	st := runningState(t)
	st.Probes[1].Target, st.Probes[1].Why = "com.claims.intake.ClaimRepository.save", strings.Repeat("é", maxReason+10)
	in := storedRun(t, &st, claimsRequest())
	row := storedChange(t, "chg_3f9a1c07b2d44e6a8c51", 1, "com.claims.intake.ClaimValidator", "validate", "2026-11-17T09:41:02Z", "2026-11-17T09:41:07Z")
	row.InterrogationID, row.Source, row.Target, row.Category, row.Why = in.ID, in.Request.Source, "com.claims.intake.ClaimValidator.validate", "observability", " sits before FraudClient.score "

	got, err := InterrogationRunChanges(in, []repo.InterrogationChange{row}, at(t, "2026-11-17T09:50:03Z"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 2 {
		t.Fatalf("count %d", got.Count)
	}
	sameJSON(t, got.Items[0], `{
  "id": "chg_3f9a1c07b2d44e6a8c51",
  "interrogation_id": "0b6f6c1e-5d0a-4b53-9f3e-0c1d7a9e4f21",
  "interrogation": {"id": "0b6f6c1e-5d0a-4b53-9f3e-0c1d7a9e4f21", "display_id": "INT-0B6F6C1E",
    "title": "Why is claim submission slow for some users?", "status": "running", "round": 2,
    "trigger": {"kind": "manual", "actor": "alon"}},
  "source": {"namespace": "claims-demo", "kind": "Deployment", "name": "claims-intake"},
  "target": "com.claims.intake.ClaimValidator.validate",
  "probe": {"language": "java", "class_name": "com.claims.intake.ClaimValidator", "method_name": "validate"},
  "category": "observability",
  "round": 1,
  "reason": "sits before FraudClient.score",
  "checks": ["changes_allowed", "in_call_graph", "application_code", "not_already_probed", "within_workload_cap", "within_run_budget"],
  "state": "active",
  "lifetime": "temporary",
  "proposed_at": "2026-11-17T09:41:02Z",
  "applied_at": "2026-11-17T09:41:07Z",
  "expires_at": "2026-11-17T10:38:03Z",
  "actions": ["keep", "revert"]
}`)
	denied := got.Items[1]
	if denied.State != "denied" || denied.Target != "com.claims.intake.ClaimRepository.save" || denied.Interrogation == nil ||
		len(denied.Actions) != 0 || denied.Actions == nil || utf8.RuneCountInString(denied.Reason) != maxReason || !strings.HasSuffix(denied.Reason, "…") {
		t.Errorf("denied %+v", denied)
	}

	row.Desired, row.ExpiresAt, row.KeptBy, row.KeptAt = repo.ChangeKept, time.Time{}, "dana", at(t, "2026-11-17T09:45:00Z")
	kept := FromInterrogationChange(row, at(t, "2026-11-18T09:00:00Z"))
	if kept.Interrogation != nil || kept.Lifetime != repo.ChangePermanent || kept.KeptBy != "dana" || kept.ExpiresAt != nil ||
		kept.State != repo.ChangeActive || len(kept.Actions) != 1 || kept.Actions[0] != repo.ChangeRemove {
		t.Errorf("kept change of a run that is gone %+v", kept)
	}
}

// What observe measured is served on the change it belongs to and on its
// round, from the state as the workflow stores it.
func TestFromInterrogation_Measured(t *testing.T) {
	st := runningState(t)
	st.Probes = []interrogation.Probe{{ChangeID: "chg_3f9a1c07b2d44e6a8c51", Measured: &interrogation.Measured{
		WindowSeconds: 30, MeasuredAt: at(t, "2026-11-17T09:41:40Z"), Spans: 41, Errors: 2, AvgMs: 12.5, MaxMs: 48.25, SelfAvgMs: 9.1,
		TopErrors: []interrogation.ErrorCount{{Message: "claim rejected", Count: 2}},
	}}}
	st.Rounds[0].Timings = []interrogation.TxTiming{{TransactionID: 8841, EntryTimings: interrogation.EntryTimings{
		WindowSeconds: 30, Requests: 40, Errors: 1, AvgMs: 20, MaxMs: 90, AvgSpans: 4, UncoveredAvgMs: 2,
	}}}
	in := storedRun(t, &st, claimsRequest())
	rows := []repo.InterrogationChange{
		storedChange(t, "chg_3f9a1c07b2d44e6a8c51", 1, "com.claims.intake.ClaimValidator", "validate", "2026-11-17T09:41:02Z", "2026-11-17T09:41:07Z"),
	}
	got, err := FromInterrogation(in, rows, true, at(t, "2026-11-17T09:50:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, got.Changes[0].Measured, `{"window_seconds":30,"measured_at":"2026-11-17T09:41:40Z","spans":41,"errors":2,"avg_ms":12.5,"max_ms":48.25,"self_avg_ms":9.1,"top_errors":[{"message":"claim rejected","count":2}]}`)
	sameJSON(t, got.Rounds[0].Timings, `[{"transaction_id":8841,"window_seconds":30,"requests":40,"errors":1,"avg_ms":20,"max_ms":90,"avg_spans":4,"uncovered_avg_ms":2}]`)
}

func TestFromInterrogationSummary(t *testing.T) {
	src := repo.InterrogationSource{Namespace: "claims-demo", Kind: "Deployment", Name: "claims-intake"}
	got := FromInterrogationSummary(
		repo.InterrogationSummary{Running: 1, RunningSources: []repo.InterrogationSource{src}, Total: 7, Ended: 3,
			Averaged: 3, AvgRounds: 1.75, AvgDurationSeconds: 1320.4, ModelCalls: 38, InputTokens: 388000, OutputTokens: 24000, Model: "gpt-4o-mini"},
		repo.InterrogationChangesSummary{Live: 3, Temporary: 2, Permanent: 1, NextExpiryAt: at(t, "2026-11-17T10:38:03Z"),
			NextExpiryChangeID: "chg_3f9a1c07b2d44e6a8c51",
			BySource:           []repo.InterrogationSourceChanges{{Source: src, Language: "java", Live: 3}}},
		20)
	sameJSON(t, got, `{
  "queued": 0, "running": 1,
  "running_sources": [{"namespace": "claims-demo", "kind": "Deployment", "name": "claims-intake"}],
  "total": 7, "concluded": 3, "avg_rounds": 1.75, "avg_duration_seconds": 1320,
  "changes": {
    "live": 3, "temporary": 2, "permanent": 1,
    "by_kind": {"custom_instrumentation": 3},
    "next_expiry_at": "2026-11-17T10:38:03Z",
    "next_expiry_change_id": "chg_3f9a1c07b2d44e6a8c51",
    "by_source": [{"source": {"namespace": "claims-demo", "kind": "Deployment", "name": "claims-intake"}, "language": "java", "live": 3, "probes": 3, "max_probes": 20}]
  },
  "model_usage": {"calls": 38, "input_tokens": 388000, "output_tokens": 24000, "model": "gpt-4o-mini", "gateway": "litellm"}
}`)

	// No ended run had started: the averages are absent, not zero.
	empty := FromInterrogationSummary(repo.InterrogationSummary{Total: 1, Ended: 1}, repo.InterrogationChangesSummary{}, 20)
	if empty.AvgRounds != nil || empty.AvgDurationSeconds != nil || len(empty.Changes.ByKind) != 0 || empty.Changes.NextExpiryAt != nil {
		t.Errorf("empty summary %+v", empty)
	}
}
