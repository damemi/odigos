package interrogation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"

	commonlogger "github.com/odigos-io/odigos/common/logger"
	"github.com/odigos-io/odigos/insights/internal/autopilot/loop"
	"github.com/odigos-io/odigos/insights/internal/autopilot/measure"
	apmodel "github.com/odigos-io/odigos/insights/internal/autopilot/model"
	apschema "github.com/odigos-io/odigos/insights/internal/autopilot/schema"
	apstore "github.com/odigos-io/odigos/insights/internal/autopilot/store"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

// The interrogation as an ADK workflow graph. Nodes follow the autopilot
// loop's tick (autopilot/loop) and call its functions; waits park the run.
//
//	START ─▶ entry ─┬─ prepare ─▶ wait_baseline ─▶ gate ─┬─ gather ─▶ analyze ─▶ record ─┬─ apply ─┬─ wait_apply ─▶ confirm ─┬─ wait_observe ─▶ observe ─▶ gate
//	                ├─ (step) ─▶ wait_baseline | gate |  │      ▲                        │         │      ▲                 ├─ wait ─▶ wait_apply
//	                │            apply | wait_apply |    ├─ wait_model ─▶ gate           ├─ retry ─▶ gate                   └─ next ─▶ gate
//	                │            wait_observe            └─ conclude                     └─ conclude  └─ conclude
//	                └─ conclude
//
// Node names are stored in sessions. Renaming or removing one that can be
// waiting or recorded as a step requires bumping GraphVersion.

const (
	// baselineWait gives traffic time to reach the call trees, which every
	// replica syncs into Postgres a few seconds after they change, before the
	// first reading.
	baselineWait = 30 * time.Second
	applyPoll    = 5 * time.Second
	applyTimeout = 60 * time.Second
	// observeMin is the 11 s to the first processed span and the 5 s batcher
	// flush, with a margin. observe then polls until every probe has
	// observeSpans spans or observeTimeout passed.
	observeMin     = 20 * time.Second
	observePoll    = 5 * time.Second
	observeTimeout = 120 * time.Second
	observeSpans   = 20
	// timingsWindow is how far back gather reads the entry timings.
	timingsWindow = 60 * time.Second

	// maxReplyAttempts is how many unusable replies one round tolerates. The
	// loop gives one request the same number (maxPermanentAttempts).
	maxReplyAttempts = 3

	analyzeNode = "analyze"
)

// Deps is what the nodes use outside ADK.
type Deps struct {
	Model model.LLM
	// Loop is *loop.Manager.
	Loop Loop
	// Store is the autopilot store: sources, transactions and call trees.
	Store Evidence
	// Changes is the Insights repo: this run's probe rows.
	Changes Changes
	// Control reads a run's control session as it is now.
	Control       func(ctx context.Context, id string) (Control, error)
	MaxModelCalls int
	Now           func() time.Time
	Log           *commonlogger.OdigosLogger
}

// Loop is the part of *loop.Manager the nodes use.
type Loop interface {
	Pause
	Instrumented(ctx context.Context, source apstore.SourceKey) (bool, error)
	ProbesInEffect(ctx context.Context, source apstore.SourceKey) ([]loop.CustomProbe, error)
	RequestProbe(ctx context.Context, req loop.ProbeRequest) (apstore.RequestedProbe, error)
	ProbeTimings(ctx context.Context, source apstore.SourceKey, since time.Time) (measure.Timings, error)
}

// Evidence is the part of the autopilot store the nodes read. Call trees are
// stored per container, so the workload's containers are found among the
// sources first, as the tick finds them.
type Evidence interface {
	ListSourcesWithTransactions(ctx context.Context) ([]apstore.SourceKey, error)
	ListTransactions(ctx context.Context, source apstore.SourceKey) ([]apstore.Transaction, error)
	GetCallTree(ctx context.Context, source apstore.SourceKey, transactionID string) ([]apstore.CallTreeNode, error)
}

// Changes is the part of the Insights repo the nodes use for this run's
// probe rows.
type Changes interface {
	GetInterrogationChange(ctx context.Context, changeID string) (repo.InterrogationChange, bool, error)
	ListInterrogationChanges(ctx context.Context, interrogationID string) ([]repo.InterrogationChange, error)
	EndInterrogationChange(ctx context.Context, changeID, reason string, now time.Time) error
	RevertInterrogationProbes(ctx context.Context, interrogationID string, now time.Time) error
}

// BuildWorkflow builds the interrogation graph. One agent serves every run:
// a run's progress is in its session.
func BuildWorkflow(dp Deps) (agent.Agent, error) {
	now := dp.Now
	if now == nil {
		now = time.Now
	}

	// extendAtDeadline applies the run's extension as the control session
	// holds it now, once the deadline the run knows has passed: one recorded
	// while this invocation ran is not in the control its message carried. A
	// stop is left to the supervisor, which cancels an invocation that does
	// not carry it.
	extendAtDeadline := func(ctx context.Context, st *State, t time.Time) ([]Event, error) {
		if t.Before(st.DeadlineAt) {
			return nil, nil
		}
		ctl, err := dp.Control(ctx, st.ID)
		if err != nil {
			return nil, err
		}
		evs, _ := applyControl(st, Control{ExtendedSeconds: ctl.ExtendedSeconds}, t)
		return evs, nil
	}

	// timings reads what the workload's spans measured since since. A run goes
	// on without them: a failed read is logged and returns none.
	timings := func(ctx context.Context, st *State, since time.Time) measure.Timings {
		tm, err := dp.Loop.ProbeTimings(ctx, workloadSource(st), since)
		if err != nil {
			dp.Log.Warn("failed to read probe timings", "interrogation", st.ID, "error", err)
		}
		return tm
	}

	// entry: a new invocation continues from the step the session recorded.
	entry := node(nodeEntry, func(ctx agent.Context, st *State, ctl Control, _ any) (*session.Event, error) {
		t := now()
		// Closed without the graph (a stop while queued, the leader's overdue
		// close) or picked up by another leader after this one read the run
		// queued: the message came too late and nothing may change.
		if open, _ := ctx.Session().State().Get(openKey); open != true || !st.StartedAt.IsZero() && isStart(ctx.UserContent()) {
			ev := session.NewEvent(ctx, ctx.InvocationID())
			ev.Routes = []string{StepClosed}
			return ev, nil
		}
		if st.Status.Terminal() {
			return emit(ctx, st, t, "", StepClosed), nil
		}
		if st.StartedAt.IsZero() {
			// The first invocation of a queued run: the time limit counts
			// from here, and applyControl sets the deadline.
			st.Status, st.StartedAt = StatusRunning, t
		}
		if st.Graph != 0 && st.Graph != GraphVersion {
			st.end(EndInterrupted, fmt.Sprintf("graph version %d, running %d", st.Graph, GraphVersion))
			return emit(ctx, st, t, StepConclude, StepConclude), nil
		}
		evs, conclude := applyControl(st, ctl, t)
		if conclude {
			// The control reached a parked run by a plain message, not as its
			// wait's answer: the wait ends here.
			if w := st.Wait; w != nil {
				st.Wait = nil
				evs = append(evs, waitFinished(st, w.Reason, WaitStopped, t), st.leaveNode(w.Node, StepConclude, t))
			}
			return emit(ctx, st, t, StepConclude, StepConclude, evs...), nil
		}
		return emit(ctx, st, t, "", st.Step, evs...), nil
	})

	// prepare (tick steps 3, 4): the workload must be instrumented. Opens
	// round 1. Mode is not read: runs are allowed with the loop off.
	prepare := node("prepare", func(ctx agent.Context, st *State, _ Control, _ any) (*session.Event, error) {
		t := now()
		st.Graph, st.Revision = GraphVersion, Revision(apmodel.LiteLLMModelName)
		ok, err := dp.Loop.Instrumented(ctx, workloadSource(st))
		if err != nil {
			return nil, err
		}
		if !ok {
			st.end(EndSourceGone, "")
			return emit(ctx, st, t, StepConclude, StepConclude, started(st, t)), nil
		}
		st.BaselineUntil = t.Add(baselineWait)
		return emit(ctx, st, t, StepBaseline, "ok",
			started(st, t),
			st.openRound(isFinal(st, 1, dp.MaxModelCalls), t),
			waitStarted(st, WaitBaseline, minTime(st.BaselineUntil, st.DeadlineAt), t)), nil
	})

	waitBaseline := park("wait_baseline", StepBaseline, WaitBaseline, true, now, StepGate, func(st *State) time.Time { return st.BaselineUntil })

	// gate (tick step 9a, and the run's limits): decides before paying. No
	// round past max_rounds gets here: the last round is final, and record
	// ends the run after it.
	gate := node("gate", func(ctx agent.Context, st *State, _ Control, _ any) (*session.Event, error) {
		t := now()
		evs, err := extendAtDeadline(ctx, st, t)
		if err != nil {
			return nil, err
		}
		paused := dp.Loop.ModelPausedUntil(t)
		switch {
		case !t.Before(st.DeadlineAt) && !paused.IsZero():
			st.end(EndModelUnavailable, "model gateway paused until "+paused.UTC().Format(time.RFC3339))
		case !t.Before(st.DeadlineAt):
			st.end(EndTimeLimit, "")
		case st.ModelCalls >= dp.MaxModelCalls:
			st.end(EndMaxModelCalls, "")
		case !paused.IsZero():
			st.PausedUntil = paused
			return emit(ctx, st, t, "", "paused", append(evs, waitStarted(st, WaitModelPaused, minTime(paused, st.DeadlineAt), t))...), nil
		default:
			return emit(ctx, st, t, "", "analyze", evs...), nil
		}
		return emit(ctx, st, t, StepConclude, StepConclude, evs...), nil
	})

	waitModel := park("wait_model", StepGate, WaitModelPaused, true, now, StepGate, func(st *State) time.Time { return st.PausedUntil })

	// gather (tick steps 2, 4, 5, 6, 8, 9e): flush tries, read the call graph
	// of each transaction in scope, and build the request with the loop's
	// builder. Its output is the model's user message.
	gather := node("gather", func(ctx agent.Context, st *State, _ Control, _ any) (*session.Event, error) {
		t := now()
		// A probe of this run may have ended because its source is gone.
		evs, err := syncChanges(ctx, dp.Changes, st, t)
		if err != nil {
			return nil, err
		}
		source := workloadSource(st)
		ok, err := dp.Loop.Instrumented(ctx, source)
		if err != nil {
			return nil, err
		}
		if !ok {
			st.end(EndSourceGone, "")
			return emit(ctx, st, t, StepConclude, StepConclude, evs...), nil
		}
		read, err := collectEvidence(ctx, dp.Store, st)
		if err != nil {
			return nil, err
		}
		if len(read) == 0 {
			st.end(EndNoEvidence, "")
			counts := evidenceCounts(nil, st.edgeCount())
			return emit(ctx, st, t, StepConclude, StepConclude, append(evs, evidenceCollected(st, counts, nil, false, t))...), nil
		}
		probes, err := dp.Loop.ProbesInEffect(ctx, source)
		if err != nil {
			return nil, err
		}
		since := t.Add(-timingsWindow)
		if since.Before(st.StartedAt) {
			since = st.StartedAt
		}
		// Requests from before the last probes would count their time as
		// uncovered.
		for _, p := range st.probesIn(st.Round-1, ProbeApplied) {
			if p.AppliedAt.After(since) {
				since = p.AppliedAt
			}
		}
		txs, ev := st.observed(read, probes, timings(ctx, st, since), t.Sub(since), t)
		st.Final = isFinal(st, st.Round, dp.MaxModelCalls)
		st.currentRound().Final = st.Final
		user, err := json.Marshal(buildRequest(st, txs))
		if err != nil {
			return nil, err
		}
		// Counted when sent: a crash before the reply is recorded costs a call.
		st.callSent(t)
		out := emit(ctx, st, t, "", otherwise, append(evs, ev)...)
		out.Output = string(user)
		return out, nil
	})

	// analyze (tick step 10): the model call. The reply comes back as text and
	// record validates it, so a bad reply is a route, not a node failure.
	llm, err := newAnalyzeAgent(dp.Model)
	if err != nil {
		return nil, err
	}
	analyze, err := workflow.NewAgentNode(llm, workflow.NodeConfig{})
	if err != nil {
		return nil, err
	}

	// record (tick step 11): parse and validate the reply as the loop does and
	// note what the model asked for.
	record := node("record", func(ctx agent.Context, st *State, _ Control, in any) (*session.Event, error) {
		t := now()
		raw, _ := in.(string)
		u := lastUsage(ctx)
		st.InputTokens += u.In
		st.OutputTokens += u.Out
		if u.Model != "" {
			st.Model = u.Model
		}
		reply, err := parseReply(raw)
		if err != nil {
			st.ParseFailures++
			called := modelCalled(st, u, OutcomeInvalidReply, err.Error(), t)
			if st.ParseFailures >= maxReplyAttempts {
				st.end(EndError, "model reply unusable: "+err.Error())
				return emit(ctx, st, t, StepConclude, StepConclude, called), nil
			}
			return emit(ctx, st, t, "", "retry", called), nil
		}
		dp.Loop.NoteModelSuccess(t)
		st.ParseFailures = 0
		st.answer(reply, t)
		evs := []Event{modelCalled(st, u, OutcomeOK, "", t), answerUpdated(st, t)}
		if reply.Done {
			st.end(EndDone, "")
			return emit(ctx, st, t, StepConclude, StepConclude, evs...), nil
		}
		// What needs no lookup is denied here. Everything else is decided by
		// loop.Manager.RequestProbe in apply.
		pending, proposed := proposeProbes(st, reply.Recommendations, t)
		evs = append(evs, proposed...)
		if pending == 0 {
			st.end(endWithoutProbes(st, reply.Recommendations, dp.MaxModelCalls), "")
			return emit(ctx, st, t, StepConclude, StepConclude, evs...), nil
		}
		return emit(ctx, st, t, StepApply, "apply", evs...), nil
	})

	// apply (tick steps 11 store, 12): ask the loop for each probe. The loop
	// validates it and stores it as requested by this run; its reconciler
	// writes the rule. Stored before the event says so.
	apply := node("apply", func(ctx agent.Context, st *State, _ Control, _ any) (*session.Event, error) {
		t := now()
		var evs []Event
		for _, p := range st.probesIn(st.Round, ProbePending) {
			row, err := dp.Loop.RequestProbe(ctx, probeRequest(st, p))
			var denied *loop.ProbeDeniedError
			switch {
			case errors.As(err, &denied):
				evs = append(evs, st.deny(p, denied.Reason, denied.Detail, t))
			case err != nil:
				// ErrRunNotOpen among them: the run closed or was stopped with
				// revert meanwhile, which is not the probe's fault.
				return nil, err
			default:
				evs = append(evs, st.requested(p, row.ExpiresAt, t))
			}
		}
		if len(st.probesIn(st.Round, ProbeRequested)) == 0 {
			st.end(endAllDenied(st), "")
			return emit(ctx, st, t, StepConclude, StepConclude, evs...), nil
		}
		st.ApplyStarted, st.WakeAt = t, t.Add(applyPoll)
		evs = append(evs, waitStarted(st, WaitApply, minTime(t.Add(applyTimeout), st.DeadlineAt), t))
		return emit(ctx, st, t, StepConfirm, "wait", evs...), nil
	})

	waitApply := park("wait_apply", StepConfirm, WaitApply, false, now, StepConfirm, func(st *State) time.Time { return st.WakeAt })

	// confirm (tick step 12, observed): the reconciler must report each probe
	// in the loop's rule. A wait past the run's deadline does not park, so the
	// deadline ends the polling too.
	confirm := node("confirm", func(ctx agent.Context, st *State, _ Control, _ any) (*session.Event, error) {
		t := now()
		evs, err := extendAtDeadline(ctx, st, t)
		if err != nil {
			return nil, err
		}
		late := !t.Before(st.ApplyStarted.Add(applyTimeout)) || !t.Before(st.DeadlineAt)
		waiting := 0
		for _, p := range st.probesIn(st.Round, ProbeRequested) {
			ch, found, err := dp.Changes.GetInterrogationChange(ctx, p.ChangeID)
			if err != nil {
				return nil, err
			}
			switch outcome, reason := probeOutcome(ch, found, late, t); outcome {
			case probeInRule:
				evs = append(evs, st.applied(p, ch, loop.RuleObjectName(workloadSource(st)), t))
			case probeEnded:
				evs = append(evs, st.reverted(p, reason, "", t))
			case probeGivenUp:
				if found {
					if err := dp.Changes.EndInterrogationChange(ctx, p.ChangeID, reason, t); err != nil {
						return nil, err
					}
				}
				evs = append(evs, st.failed(p, reason, t))
			default:
				waiting++
			}
		}
		if waiting > 0 {
			st.WakeAt = t.Add(applyPoll)
			return emit(ctx, st, t, "", "wait", evs...), nil
		}
		outcome := WaitSatisfied
		if late {
			outcome = WaitTimedOut
		}
		evs = append(evs, waitFinished(st, WaitApply, outcome, t))
		if len(st.probesIn(st.Round, ProbeApplied)) == 0 {
			evs = append(evs, st.closeRound(RoundNoChangeApplied, t), st.openRound(isFinal(st, st.Round+1, dp.MaxModelCalls), t))
			return emit(ctx, st, t, StepGate, "next", evs...), nil
		}
		st.ObserveStarted, st.WakeAt = t, t.Add(observeMin)
		evs = append(evs, waitStarted(st, WaitObserve, minTime(st.WakeAt, st.DeadlineAt), t))
		return emit(ctx, st, t, StepObserve, "observe", evs...), nil
	})

	waitObserve := park("wait_observe", StepObserve, WaitObserve, false, now, StepObserve, func(st *State) time.Time { return st.WakeAt })

	// observe: traffic ran with the new probes. Wait until each has
	// observeSpans spans, as confirm waits for the rule, then store what they
	// measured, read the call graph again and start the next round. Without
	// timings there is nothing to wait for.
	observe := node("observe", func(ctx agent.Context, st *State, _ Control, _ any) (*session.Event, error) {
		t := now()
		evs, err := extendAtDeadline(ctx, st, t)
		if err != nil {
			return nil, err
		}
		probes := st.probesIn(st.Round, ProbeApplied)
		var since time.Time
		for _, p := range probes {
			if p.AppliedAt.After(since) {
				since = p.AppliedAt
			}
		}
		tm := timings(ctx, st, since)
		window := t.Sub(since)
		measured := make([]Measured, len(probes))
		short := 0
		for i, p := range probes {
			measured[i] = st.measuredProbe(tm, p.Target, window, t)
			if measured[i].Spans < observeSpans {
				short++
			}
		}
		late := !t.Before(st.ObserveStarted.Add(observeTimeout)) || !t.Before(st.DeadlineAt)
		if short > 0 && tm != nil && !late {
			st.WakeAt = t.Add(observePoll)
			return emit(ctx, st, t, "", "wait", evs...), nil
		}
		outcome := WaitSatisfied
		if short > 0 && tm != nil {
			outcome = WaitTimedOut
		}
		if tm != nil {
			for i, p := range probes {
				p.Measured = &measured[i]
				evs = append(evs, changeMeasured(st, p, measured[i], t))
			}
			st.currentRound().Timings = st.entryTimings(tm, window)
		}
		read, err := collectEvidence(ctx, dp.Store, st)
		if err != nil {
			return nil, err
		}
		evs = append(evs, st.observedAfter(read, t), waitFinished(st, WaitObserve, outcome, t), st.closeRound(RoundObserved, t), st.openRound(isFinal(st, st.Round+1, dp.MaxModelCalls), t))
		return emit(ctx, st, t, StepGate, "next", evs...), nil
	})

	// conclude: close the run. Probes stay until they expire unless the stop
	// asked to revert them.
	conclude := node("conclude", func(ctx agent.Context, st *State, _ Control, _ any) (*session.Event, error) {
		t := now()
		evs, err := requestStored(ctx, dp.Changes, st, t)
		if err != nil {
			return nil, err
		}
		if st.Stop != nil && st.Stop.Changes == ChangesRevert {
			if err := dp.Changes.RevertInterrogationProbes(ctx, st.ID, t); err != nil {
				return nil, err
			}
			// From state, not from the rows: a repeat of this node after a
			// failure finds them already reverted.
			evs = append(evs, st.revertProbes(st.Stop.Actor, t)...)
		}
		if r := st.currentRound(); r != nil && r.State == RoundInProgress {
			evs = append(evs, st.closeRound(RoundEnded, t))
		}
		if st.Answer != nil {
			st.Answer.Final = true
		}
		st.Status, st.EndedAt, st.Wait = st.terminalStatus(), t, nil
		return emit(ctx, st, t, StepClosed, "", append(evs, ended(st, t))...), nil
	})

	closed := workflow.NewFunctionNode(nodeClosed, func(agent.Context, any) (string, error) { return StepClosed, nil }, workflow.NodeConfig{})

	nodes := map[string]workflow.Node{
		nodeEntry: entry, "prepare": prepare, "wait_baseline": waitBaseline, "gate": gate, "wait_model": waitModel,
		"gather": gather, analyzeNode: analyze, "record": record, "apply": apply, "wait_apply": waitApply,
		"confirm": confirm, "wait_observe": waitObserve, "observe": observe, "conclude": conclude, nodeClosed: closed,
	}
	edges, err := buildEdges(nodes)
	if err != nil {
		return nil, err
	}
	return workflowagent.New(workflowagent.Config{
		Name:        AppName,
		Description: "Answers a question about one workload from call-graph evidence, adding probes in rounds.",
		Edges:       edges,
	})
}

// buildEdges turns routes into ADK's edges.
func buildEdges(nodes map[string]workflow.Node) ([]workflow.Edge, error) {
	eb := workflow.NewEdgeBuilder().Add(workflow.Start, nodes[nodeEntry])
	for _, from := range slices.Sorted(maps.Keys(routes)) {
		for _, route := range slices.Sorted(maps.Keys(routes[from])) {
			src, dst := nodes[from], nodes[routes[from][route]]
			if src == nil || dst == nil {
				return nil, fmt.Errorf("route %s -%s-> %s names a node the graph does not have", from, route, routes[from][route])
			}
			switch route {
			case always:
				eb = eb.Add(src, dst)
			case otherwise:
				eb = eb.AddRoute(src, dst, workflow.Default)
			default:
				eb = eb.AddRoute(src, dst, workflow.StringRoute(route))
			}
		}
	}
	return eb.Build(), nil
}

// step is a node's work on the run's state. ctl is the control the
// supervisor attached to the invocation's message. It returns the node's one
// event.
type step func(ctx agent.Context, st *State, ctl Control, in any) (*session.Event, error)

// node wraps a step: load the state, run fn, emit its one event.
func node(name string, fn step) workflow.Node {
	return workflow.NewEmittingFunctionNode(name, func(ctx agent.Context, in any, send func(*session.Event) error) (any, error) {
		st, ctl, err := load(ctx.Session())
		if err != nil {
			return nil, err
		}
		st.at = name
		ev, err := fn(ctx, st, ctl, in)
		if err != nil {
			return nil, err
		}
		return nil, send(ev)
	}, workflow.NodeConfig{})
}

// park is a wait. It parks the run until min(until, deadline) and records the
// open interrupt in state so the supervisor can answer it. Woken, it applies
// the control that came with the answer: a stop concludes, a later deadline
// parks again. step is the run's step while in this wait. finish says whether
// passing ends the wait (baseline, model pause) or the next node does
// (confirm for the apply wait, observe for its own).
func park(name, step string, reason WaitReason, finish bool, now func() time.Time, then string, until func(*State) time.Time) workflow.Node {
	rerun := true
	return workflow.NewEmittingFunctionNode(name, func(ctx agent.Context, _ any, send func(*session.Event) error) (any, error) {
		st, ctl, err := load(ctx.Session())
		if err != nil {
			return nil, err
		}
		// ADK runs a wait again for any answer to one of its interrupts, also
		// one the run has left behind, as a leader that read the run before
		// another moved it on would send. Such a wake changes nothing.
		if st.Wait != nil && st.Wait.Node != name || st.Wait == nil && st.Step != step {
			return nil, nil
		}
		st.at = name
		t := now()
		var evs []Event
		if st.Wait != nil && st.Wait.Node == name {
			if _, woken := ctx.ResumedInput(st.Wait.InterruptID); woken {
				var conclude bool
				if evs, conclude = applyControl(st, ctl, t); conclude {
					st.Wait = nil
					evs = append(evs, waitFinished(st, reason, WaitStopped, t))
					return nil, send(emit(ctx, st, t, StepConclude, StepConclude, evs...))
				}
			}
		}
		at := minTime(until(st), st.DeadlineAt)
		if !t.Before(at) {
			st.Wait = nil
			if finish {
				outcome := WaitSatisfied
				if t.Before(until(st)) {
					outcome = WaitTimedOut
				}
				evs = append(evs, waitFinished(st, reason, outcome, t))
			}
			return nil, send(emit(ctx, st, t, then, "ok", evs...))
		}
		since := t
		if st.Wait != nil && st.Wait.Node == name {
			since = st.Wait.Since
		}
		st.Waits++
		st.nodeWaiting(name)
		st.Wait = &Wait{Node: name, Reason: reason, Since: since, Until: at, InterruptID: fmt.Sprintf("%s-%d", name, st.Waits)}
		ev := workflow.NewRequestInputEvent(ctx, session.RequestInput{
			InterruptID: st.Wait.InterruptID,
			Message:     "waiting: " + string(reason),
			Payload:     map[string]any{"until": at.UTC().Format(time.RFC3339Nano)},
		})
		stamp(ev, st, evs)
		if err := send(ev); err != nil {
			return nil, err
		}
		return nil, workflow.ErrNodeInterrupted
	}, workflow.NodeConfig{RerunOnResume: &rerun})
}

// lastUsage is what the newest model call of this invocation cost, as the
// analyze agent's event reports it. A call with no usage reported counts
// nothing.
func lastUsage(ctx agent.Context) usage {
	events := ctx.Session().Events()
	for i := events.Len() - 1; i >= 0; i-- {
		ev := events.At(i)
		if ev.InvocationID != ctx.InvocationID() {
			break
		}
		if ev.Author != analyzeNode || ev.UsageMetadata == nil {
			continue
		}
		return usage{
			Model: ev.ModelVersion,
			In:    int64(ev.UsageMetadata.PromptTokenCount),
			Out:   int64(ev.UsageMetadata.CandidatesTokenCount),
		}
	}
	return usage{}
}

// workloadSource is the run's workload as the autopilot store keys it, with
// no container: a run covers all of them.
func workloadSource(st *State) apstore.SourceKey {
	s := st.Request.Source
	return apstore.SourceKey{Namespace: s.Namespace, WorkloadKind: s.Kind, WorkloadName: s.Name}
}

// allowsChanges is whether the run opted in to adding probes.
func allowsChanges(st *State) bool {
	return slices.Contains(st.Request.AllowChanges, changeKind)
}

// isFinal reports whether no probe can be added in round.
func isFinal(st *State, round, maxModelCalls int) bool {
	// The round's call is not sent yet.
	return !allowsChanges(st) || finalReason(st, round, st.ModelCalls+1, maxModelCalls) != ""
}

// finalReason is the limit that leaves no room for a probe after round,
// whose model call is the calls-th: the probe budget, the rounds or the
// model calls. Empty when there is room.
func finalReason(st *State, round, calls, maxModelCalls int) EndReason {
	switch {
	case probeBudgetLeft(st) == 0:
		return EndProbeBudget
	case round >= st.Request.Budget.MaxRounds:
		return EndMaxRounds
	case calls >= maxModelCalls:
		return EndMaxModelCalls
	}
	return ""
}

// endWithoutProbes is why a run whose model asked for nothing it could add
// ends: the limit that made the round final; changes_denied when every probe
// it asked for was one the run already had, as the model is not done; else
// the model is done.
func endWithoutProbes(st *State, recs []apschema.Recommendation, maxModelCalls int) EndReason {
	if r := finalReason(st, st.Round, st.ModelCalls, maxModelCalls); r != "" {
		return r
	}
	if len(recs) > 0 && len(st.currentRound().Proposed) == 0 {
		return EndChangesDenied
	}
	return EndDone
}

// endAllDenied is why a run ends whose probes of the round were all denied:
// the run's budget when that refused every one, else the denials.
func endAllDenied(st *State) EndReason {
	denied := st.probesIn(st.Round, ProbeDenied)
	if len(denied) > 0 && !slices.ContainsFunc(denied, func(p *Probe) bool { return p.DenyReason != string(loop.DenyRunBudget) }) {
		return EndProbeBudget
	}
	return EndChangesDenied
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}
