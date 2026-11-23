package interrogation

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/adk/v2/session"

	"github.com/odigos-io/odigos/insights/internal/api/http/apitypes"
	apschema "github.com/odigos-io/odigos/insights/internal/autopilot/schema"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// golden compares got with testdata/name, or rewrites the file with -update.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("update %s: %v", path, err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run: go test ./internal/interrogation -run %s -update)", path, err, t.Name())
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from %s (run with -update to rewrite):\n%s", t.Name(), path, got)
	}
}

type testInvocation struct{ context.Context }

func (testInvocation) InvocationID() string { return "invocation" }

const testRunID = "0b6f6c1e-5d0a-4b53-9f3e-0c1d7a9e4f21"

var testStart = time.Date(2026, 11, 17, 9, 38, 0, 0, time.UTC)

// script drives a run's state the way the graph's nodes do, one node event
// at a time, and keeps every event it emits.
type script struct {
	st     *State
	t      time.Time
	events []*session.Event
}

func (s *script) append(ev *session.Event) {
	s.events = append(s.events, ev)
}

func newScript() *script {
	return &script{
		st: &State{V: StateVersion, ID: testRunID, Step: StepPrepare, Request: repo.InterrogationRequest{
			Source:       repo.InterrogationSource{Namespace: "claims-demo", Kind: "Deployment", Name: "claims-intake"},
			Goal:         "observability",
			Question:     "Why is intake slow?",
			AllowChanges: []string{"custom_instrumentation"},
			Budget:       repo.InterrogationBudget{MaxProbes: 5, TimeLimitSeconds: 3600, MaxRounds: 5},
			Trigger:      repo.InterrogationTrigger{Kind: "manual", Actor: "alice"},
		}},
		t: testStart,
	}
}

// node runs fn as node name one second after the previous node, then emits.
func (s *script) node(name, step, route string, fn func(st *State, t time.Time) []Event) {
	s.t = s.t.Add(time.Second)
	s.st.at = name
	var evs []Event
	if fn != nil {
		evs = fn(s.st, s.t)
	}
	s.append(emit(testInvocation{context.Background()}, s.st, s.t, step, route, evs...))
}

// park parks the run in wait node name, as the graph's waits do.
func (s *script) park(name string, reason WaitReason, until time.Time) {
	s.t = s.t.Add(time.Second)
	s.st.at = name
	s.st.Waits++
	s.st.nodeWaiting(name)
	s.st.Wait = &Wait{Node: name, Reason: reason, Since: s.t, Until: until, InterruptID: name}
	ev := session.NewEvent(testInvocation{context.Background()}, "invocation")
	stamp(ev, s.st, nil)
	s.append(ev)
}

// wake passes the wait the run is parked in.
func (s *script) wake(name, step string, evs func(st *State, t time.Time) []Event) {
	s.st.Wait = nil
	s.node(name, step, "ok", evs)
}

var testProbe = Probe{
	ChangeID: "chg_3f9a1c07b2d44e6a8c51",
	Language: "java",
	Target:   "com.claims.IntakeService.validate",
	Category: "observability",
	Why:      "IntakeService.validate is called from ClaimController.submit.",
	Rec: apschema.Recommendation{
		Category: "observability", Function: "com.claims.IntakeService.validate", Language: "java",
		Probe: apschema.Probe{ClassName: "com.claims.IntakeService", MethodName: "validate"},
		Why:   "IntakeService.validate is called from ClaimController.submit.",
	},
}

// scriptedRun is a run of two rounds: the first goes through every node and
// has a failed model call retried, the second ends on the model's done.
func scriptedRun() *script {
	s := newScript()
	reply := func(certainty int, done bool) *Reply {
		r := validReply()
		r.Certainty, r.Done = certainty, done
		return &r
	}
	counts := EvidenceCounts{Transactions: 1, Functions: 57, Edges: 212, NewEdges: 212}
	edges := []apschema.RequestCallEdge{{Caller: "com.claims.ClaimController.submit", Callee: "com.claims.IntakeService.validate"}}

	s.node(nodeEntry, "", StepPrepare, func(st *State, t time.Time) []Event {
		st.Status, st.StartedAt = StatusRunning, t
		st.DeadlineAt = t.Add(time.Hour)
		return nil
	})
	s.node("prepare", StepBaseline, "ok", func(st *State, t time.Time) []Event {
		st.Graph, st.Revision = GraphVersion, Revision("autopilot")
		return []Event{started(st, t), st.openRound(false, t), waitStarted(st, WaitBaseline, t.Add(30*time.Second), t)}
	})
	s.park("wait_baseline", WaitBaseline, s.t.Add(30*time.Second))
	s.t = s.t.Add(30 * time.Second)
	s.wake("wait_baseline", StepGate, func(st *State, t time.Time) []Event {
		return []Event{waitFinished(st, WaitBaseline, WaitSatisfied, t)}
	})
	s.node("gate", "", "analyze", nil)
	gather := func(st *State, t time.Time) []Event {
		st.callSent(t)
		st.Scope = []ScopeTransaction{{TransactionID: 8841, Operation: "POST /claims", Kind: "HTTP", SelectedBy: "auto"}}
		st.currentRound().Observed = &counts
		return []Event{evidenceCollected(st, counts, edges, false, t)}
	}
	s.node("gather", "", "", gather)

	// The model call fails; the supervisor resumes the run, which re-enters
	// at its step.
	s.node(nodeEntry, "", StepGate, func(st *State, t time.Time) []Event {
		evs, _ := applyControl(st, Control{Resume: &Resume{Error: "gateway timeout", ModelCall: true, RetryAfter: 5}}, t)
		return evs
	})
	s.node("gate", "", "analyze", nil)
	s.node("gather", "", "", gather)
	s.t = s.t.Add(20 * time.Second)
	s.node("record", StepApply, "apply", func(st *State, t time.Time) []Event {
		st.answer(reply(30, false), t)
		p := testProbe
		p.Round, p.Status, p.ProposedAt = st.Round, ProbePending, t
		st.Probes = append(st.Probes, p)
		st.currentRound().Proposed = append(st.currentRound().Proposed, p.ChangeID)
		return []Event{modelCalled(st, usage{Model: "gpt-4o-mini", In: 8120, Out: 410}, OutcomeOK, "", t), answerUpdated(st, t), changeProposed(st, &st.Probes[0], t)}
	})
	s.node("apply", StepConfirm, "wait", func(st *State, t time.Time) []Event {
		p := &st.Probes[0]
		p.Status, p.RequestedAt, p.ExpiresAt = ProbeRequested, t, st.DeadlineAt
		st.ApplyStarted = t
		return []Event{changeRequested(st, p, t), waitStarted(st, WaitApply, t.Add(time.Minute), t)}
	})
	s.park("wait_apply", WaitApply, s.t.Add(5*time.Second))
	s.wake("wait_apply", "", nil)
	s.node("confirm", "", "wait", nil)
	s.park("wait_apply", WaitApply, s.t.Add(5*time.Second))
	s.wake("wait_apply", "", nil)
	s.node("confirm", StepObserve, "observe", func(st *State, t time.Time) []Event {
		p := &st.Probes[0]
		p.Status, p.AppliedAt = ProbeApplied, t
		st.currentRound().Applied = append(st.currentRound().Applied, p.ChangeID)
		return []Event{
			changeApplied(st, p, "odig-int-claims-demo-claims-intake", t),
			waitFinished(st, WaitApply, WaitSatisfied, t),
			waitStarted(st, WaitObserve, t.Add(time.Minute), t),
		}
	})
	s.park("wait_observe", WaitObserve, s.t.Add(time.Minute))
	s.t = s.t.Add(time.Minute)
	s.wake("wait_observe", StepObserve, nil)
	s.node("observe", StepGate, "next", func(st *State, t time.Time) []Event {
		after := EvidenceCounts{Transactions: 1, Functions: 58, Edges: 214, NewEdges: 2}
		st.currentRound().ObservedAfter = &after
		return []Event{
			evidenceCollected(st, after, edges, false, t),
			waitFinished(st, WaitObserve, WaitSatisfied, t),
			st.closeRound(RoundObserved, t),
			st.openRound(false, t),
		}
	})
	s.node("gate", "", "analyze", nil)
	s.node("gather", "", "", gather)
	s.t = s.t.Add(20 * time.Second)
	s.node("record", StepConclude, "conclude", func(st *State, t time.Time) []Event {
		st.answer(reply(80, true), t)
		st.end(EndDone, "")
		return []Event{modelCalled(st, usage{Model: "gpt-4o-mini", In: 8300, Out: 380}, OutcomeOK, "", t), answerUpdated(st, t)}
	})
	s.node("conclude", StepClosed, "", func(st *State, t time.Time) []Event {
		evs := []Event{st.closeRound(RoundEnded, t)}
		st.Answer.Final = true
		st.Status, st.EndedAt, st.Wait = st.terminalStatus(), t, nil
		return append(evs, ended(st, t))
	})
	return s
}

// apiEvents are the API events an ADK event carries.
func apiEvents(ev *session.Event) []Event {
	evs, _ := ev.CustomMetadata[eventsKey].([]Event)
	return evs
}

func TestNodeTracking(t *testing.T) {
	s := scriptedRun()

	var seq int64
	for i, ev := range s.events {
		run := ev.Actions.StateDelta[runKey].(State)
		for _, e := range apiEvents(ev) {
			if seq++; e.Seq != seq {
				t.Fatalf("ADK event %d has an event with seq %d, want %d", i, e.Seq, seq)
			}
		}
		if got := ev.CustomMetadata[lastSeqKey]; len(apiEvents(ev)) > 0 && got != run.Seq {
			t.Errorf("ADK event %d: %s = %v, want %d", i, lastSeqKey, got, run.Seq)
		}
		_, hasOpen := ev.Actions.StateDelta[openKey]
		if last := i == len(s.events)-1; hasOpen != last {
			t.Errorf("ADK event %d: open in the delta %v, want %v", i, hasOpen, last)
		}
	}
	if s.st.Seq != seq {
		t.Fatalf("state seq %d, events %d", s.st.Seq, seq)
	}
	if open := s.events[len(s.events)-1].Actions.StateDelta[openKey]; open != false {
		t.Fatalf("last event sets open = %v, want false with the terminal run", open)
	}

	// The failed model call left analyze failed until the run entered it
	// again.
	gather, analyze := s.st.Rounds[0].Nodes[3], s.st.Rounds[0].Nodes[4]
	if gather.Name != "gather" || gather.Visits != 2 || analyze.Name != "analyze" || analyze.Visits != 2 || analyze.State != NodeDone {
		t.Fatalf("round 1 nodes 3, 4 = %+v, %+v; want gather and analyze visited twice", gather, analyze)
	}

}

// allEvents builds one event of every type.
func allEvents() []Event {
	st := newScript().st
	t := testStart
	st.Status, st.StartedAt, st.DeadlineAt, st.Revision = StatusRunning, t, t.Add(time.Hour), Revision("autopilot")
	st.at = "gate"
	p := testProbe
	p.DenyReason, p.Detail, p.EndReason, p.ExpiresAt = "already_probed", "in rule odig-int", "not_applied_in_time", st.DeadlineAt
	evs := []Event{
		started(st, t),
		stopRequested(st, Stop{Changes: ChangesRevert, Actor: "alice", At: t}, t),
		extended(st, 600, t),
		st.openRound(true, t),
		nodeFinished(st, "gate", "gather", t),
		nodeFinished(st, "conclude", "", t),
		waitStarted(st, WaitModelPaused, t.Add(time.Minute), t),
		waitFinished(st, WaitModelPaused, WaitStopped, t),
		evidenceCollected(st, EvidenceCounts{Transactions: 1, Functions: 2, Edges: 1, NewEdges: 1}, []apschema.RequestCallEdge{{Caller: "a.B.c", Callee: "a.B.d"}}, true, t),
	}
	st.callSent(t.Add(-time.Second))
	evs = append(evs, modelCalled(st, usage{}, OutcomeError, "gateway timeout", t))
	st.callSent(t.Add(-time.Second))
	evs = append(evs, modelCalled(st, usage{Model: "gpt-4o-mini", In: 10, Out: 5}, OutcomeInvalidReply, "decode reply: invalid character", t))
	st.answer(&Reply{Answer: "a", Certainty: 10}, t)
	evs = append(evs,
		answerUpdated(st, t),
		changeProposed(st, &p, t),
		changeDenied(st, &p, t),
		changeRequested(st, &p, t),
		changeApplied(st, &p, "odig-int-claims-demo-claims-intake", t),
		changeFailed(st, &p, t),
		changeReverted(st, &p, "alice", t),
		errorRaised(st, "analyze", "gateway timeout", 5, t),
		st.closeRound(RoundEnded, t),
	)
	st.end(EndStoppedByUser, "")
	st.Status = st.terminalStatus()
	evs = append(evs, ended(st, t))
	st.Answer = nil
	st.Error = "x"
	return append(evs, ended(st, t))
}

// payloadTypes is the generated type of each event type's payload.
var payloadTypes = map[string]func() any{
	EventStarted:         func() any { return &apitypes.InterrogationEventStartedPayload{} },
	EventStopRequested:   func() any { return &apitypes.InterrogationEventStopRequestedPayload{} },
	EventExtended:        func() any { return &apitypes.InterrogationEventExtendedPayload{} },
	EventEnded:           func() any { return &apitypes.InterrogationEventEndedPayload{} },
	EventRoundStarted:    func() any { return &apitypes.InterrogationEventRoundStartedPayload{} },
	EventRoundFinished:   func() any { return &apitypes.InterrogationEventRoundFinishedPayload{} },
	EventNodeFinished:    func() any { return &apitypes.InterrogationEventNodeFinishedPayload{} },
	EventWaitStarted:     func() any { return &apitypes.InterrogationEventWaitStartedPayload{} },
	EventWaitFinished:    func() any { return &apitypes.InterrogationEventWaitFinishedPayload{} },
	EventEvidence:        func() any { return &apitypes.InterrogationEventEvidencePayload{} },
	EventModelCalled:     func() any { return &apitypes.InterrogationEventModelCalledPayload{} },
	EventAnswerUpdated:   func() any { return &apitypes.InterrogationAnswer{} },
	EventChangeProposed:  func() any { return &apitypes.InterrogationEventChangeProposedPayload{} },
	EventChangeDenied:    func() any { return &apitypes.InterrogationEventChangeDeniedPayload{} },
	EventChangeRequested: func() any { return &apitypes.InterrogationEventChangeRequestedPayload{} },
	EventChangeApplied:   func() any { return &apitypes.InterrogationEventChangeAppliedPayload{} },
	EventChangeFailed:    func() any { return &apitypes.InterrogationEventChangeFailedPayload{} },
	EventChangeReverted:  func() any { return &apitypes.InterrogationEventChangeRevertedPayload{} },
	EventErrorRaised:     func() any { return &apitypes.InterrogationEventErrorRaisedPayload{} },
}

// decodeStrict decodes raw into v and refuses a field v does not have.
func decodeStrict(t *testing.T, what string, raw []byte, v any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Errorf("%s does not decode into %T: %v\n%s", what, v, err, raw)
	}
}

func TestEvents_WireShape(t *testing.T) {
	evs := allEvents()
	for _, ev := range scriptedRun().events {
		evs = append(evs, apiEvents(ev)...)
	}
	seen := map[string]bool{}
	for _, ev := range evs {
		seen[ev.Type] = true
		b, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		// The API renames seq; the rest of an item is served as stored.
		var item map[string]json.RawMessage
		if err := json.Unmarshal(b, &item); err != nil {
			t.Fatal(err)
		}
		item["sequence"] = item["seq"]
		delete(item, "seq")
		wire, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		decodeStrict(t, ev.Type+" event", wire, &apitypes.InterrogationEvent{})

		newPayload, ok := payloadTypes[ev.Type]
		if !ok {
			t.Errorf("event type %q has no payload schema", ev.Type)
			continue
		}
		decodeStrict(t, ev.Type+" payload", item["payload"], newPayload())
	}
	for typ := range payloadTypes {
		if !seen[typ] {
			t.Errorf("no %s event checked", typ)
		}
	}

	st := scriptedRun().st
	for what, v := range map[string]any{"rounds": st.Rounds, "answer": st.Answer} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		switch what {
		case "rounds":
			decodeStrict(t, what, b, &[]apitypes.InterrogationRound{})
		case "answer":
			decodeStrict(t, what, b, &apitypes.InterrogationAnswer{})
		}
	}
}
