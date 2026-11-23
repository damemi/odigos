package interrogation

import (
	"context"
	"fmt"
	"slices"
	"time"

	"google.golang.org/adk/v2/session"

	"github.com/odigos-io/odigos/insights/internal/autopilot/loop"
	apschema "github.com/odigos-io/odigos/insights/internal/autopilot/schema"
)

// Event is one item of a run's event log, stored as the API returns it
// (InterrogationEvent; the API calls Seq sequence). A node event of the run's
// session carries a list of them in its custom metadata.
type Event struct {
	Seq      int64          `json:"seq"`
	Type     string         `json:"type"`
	Time     time.Time      `json:"time"`
	Round    int            `json:"round"` // 0 outside a round
	Node     string         `json:"node,omitempty"`
	ChangeID string         `json:"change_id,omitempty"`
	Message  string         `json:"message"`
	Payload  map[string]any `json:"payload"`
}

// Event types.
const (
	EventStarted         = "interrogation.started"
	EventStopRequested   = "interrogation.stop_requested"
	EventExtended        = "interrogation.extended"
	EventEnded           = "interrogation.ended"
	EventRoundStarted    = "round.started"
	EventRoundFinished   = "round.finished"
	EventNodeFinished    = "node.finished"
	EventWaitStarted     = "wait.started"
	EventWaitFinished    = "wait.finished"
	EventEvidence        = "evidence.collected"
	EventModelCalled     = "model.called"
	EventAnswerUpdated   = "answer.updated"
	EventChangeProposed  = "change.proposed"
	EventChangeDenied    = "change.denied"
	EventChangeRequested = "change.requested"
	EventChangeApplied   = "change.applied"
	EventChangeFailed    = "change.failed"
	EventChangeReverted  = "change.reverted"
	EventErrorRaised     = "error.raised"
)

// Keys of a node event's custom metadata: the events it carries and the
// sequence of the last one, which the events read filters on.
const (
	eventsKey  = "interrogation"
	lastSeqKey = "interrogation_seq"
)

const (
	changeKind   = "custom_instrumentation"
	evidenceKind = "call_graph"
	// maxSamples caps the edges an evidence event lists.
	maxSamples = 50
)

// Outcomes of a model call.
const (
	OutcomeOK           = "ok"
	OutcomeInvalidReply = "invalid_reply"
	OutcomeError        = "error"
)

// usage is what one model call cost, as its event reports it.
type usage struct {
	Model string
	In    int64
	Out   int64
}

// Nodes that only route. They are not reported.
const (
	nodeEntry  = "entry"
	nodeClosed = "closed"
)

// routes is the graph: for each node, where each route it can emit leads.
// The workflow's edges and the node a run is reported to be in both come from
// it, so they cannot disagree.
var routes = map[string]map[string]string{
	nodeEntry: {
		StepPrepare: "prepare", StepBaseline: "wait_baseline", StepGate: "gate", StepApply: "apply",
		StepConfirm: "wait_apply", StepObserve: "wait_observe", StepConclude: "conclude", StepClosed: nodeClosed,
	},
	"prepare":       {"ok": "wait_baseline", "conclude": "conclude"},
	"wait_baseline": {"ok": "gate", "conclude": "conclude"},
	"gate":          {"analyze": "gather", "paused": "wait_model", "conclude": "conclude"},
	"wait_model":    {"ok": "gate", "conclude": "conclude"},
	"gather":        {otherwise: "analyze", "conclude": "conclude"},
	"analyze":       {always: "record"},
	"record":        {"retry": "gate", "apply": "apply", "conclude": "conclude"},
	"apply":         {"wait": "wait_apply", "conclude": "conclude"},
	"wait_apply":    {"ok": "confirm", "conclude": "conclude"},
	"confirm":       {"wait": "wait_apply", "observe": "wait_observe", "next": "gate"},
	"wait_observe":  {"ok": "observe", "conclude": "conclude"},
	"observe":       {"next": "gate"},
}

const (
	always    = "*" // the node's one unconditional edge
	otherwise = ""  // the edge taken when the node names no route
)

// reported is whether a node is shown in a run's rounds and events.
func reported(node string) bool {
	return node != "" && node != nodeEntry && node != nodeClosed
}

// invocation is what emit needs of a node's context.
type invocation interface {
	context.Context
	InvocationID() string
}

// emit builds a node's one event: it records the step a new invocation
// continues from, moves the run to the node route leads to, and carries the
// run's state and evs.
func emit(ctx invocation, st *State, t time.Time, step, route string, evs ...Event) *session.Event {
	if step != "" {
		if step != st.Step {
			st.StepFailures = 0
		}
		st.Step = step
	}
	moved := st.moveNode(route, t)
	if n := len(evs); n > 0 && evs[n-1].Type == EventEnded {
		// A run's last event says how it ended.
		evs = slices.Concat(evs[:n-1], moved, evs[n-1:])
	} else {
		evs = append(evs, moved...)
	}
	ev := session.NewEvent(ctx, ctx.InvocationID())
	stamp(ev, st, evs)
	if route != "" {
		ev.Routes = []string{route}
		ev.Output = route
	}
	return ev
}

// stamp numbers evs after the run's last event and puts them and the run's
// state on ev, so the log and the state are written in one transaction. The
// event that ends the run also clears open, which frees the workload at the
// moment the run is terminal.
func stamp(ev *session.Event, st *State, evs []Event) {
	for i := range evs {
		st.Seq++
		evs[i].Seq = st.Seq
	}
	ev.Actions.StateDelta[runKey] = *st
	if st.Status.Terminal() {
		ev.Actions.StateDelta[openKey] = false
	}
	if len(evs) > 0 {
		ev.CustomMetadata = map[string]any{eventsKey: evs, lastSeqKey: st.Seq}
	}
}

// moveNode moves the run out of the node emitting (st.at) to the node route
// leads to, and returns the node.finished events. A node reached by an edge
// that wrote nothing of ours (record after analyze) is entered and left here.
func (st *State) moveNode(route string, t time.Time) []Event {
	var evs []Event
	from := st.at
	if !reported(from) {
		// entry routes a new invocation: the one before it did not leave its
		// node, because it was cancelled, failed or lost with its leader.
		st.nodeFailed(t)
	}
	if reported(from) && st.Node != from {
		if reported(st.Node) {
			evs = append(evs, st.leaveNode(st.Node, from, t))
		}
		st.enterNode(from, t)
	}
	next := routes[from][route]
	if next == "" && route == otherwise {
		next = routes[from][always]
	}
	if reported(from) {
		evs = append(evs, st.leaveNode(from, nodeName(next), t))
	}
	if !reported(next) {
		st.Node = ""
		return evs
	}
	st.enterNode(next, t)
	return evs
}

// enterNode makes node the run's node and counts a visit in the current
// round. Before the first round nothing is recorded: prepare, which opens it,
// records itself.
func (st *State) enterNode(node string, t time.Time) {
	st.Node = node
	r := st.currentRound()
	if r == nil {
		return
	}
	for i := range r.Nodes {
		if n := &r.Nodes[i]; n.Name == node {
			n.State, n.LeftAt = NodeRunning, nil
			n.Visits++
			return
		}
	}
	r.Nodes = append(r.Nodes, NodeRun{Name: node, State: NodeRunning, Visits: 1, EnteredAt: t})
}

// leaveNode marks node done where the run is in it, in the round it was
// entered in, which the leaving event may already have closed, and returns
// its node.finished event for that round.
func (st *State) leaveNode(node, next string, t time.Time) Event {
	ev := nodeFinished(st, node, next, t)
	if round := st.setNodeState(node, NodeDone, t); round != 0 {
		ev.Round = round
	}
	return ev
}

// nodeWaiting marks node as parked in.
func (st *State) nodeWaiting(node string) {
	if n, _ := st.activeNode(node); n != nil {
		n.State = NodeWaiting
	}
}

// nodeFailed marks the node the run is in as failed, if the run has not left
// it: its last attempt did not finish.
func (st *State) nodeFailed(t time.Time) {
	if n, _ := st.activeNode(st.Node); n != nil {
		n.State, n.LeftAt = NodeFailed, &t
	}
}

// setNodeState sets the state of node where the run is in it and returns the
// round of that entry, 0 when no round is open to record it in.
func (st *State) setNodeState(node, state string, t time.Time) int {
	n, round := st.activeNode(node)
	if n == nil {
		r := st.currentRound()
		if r == nil {
			return 0
		}
		r.Nodes = append(r.Nodes, NodeRun{Name: node, Visits: 1, EnteredAt: t})
		n, round = &r.Nodes[len(r.Nodes)-1], r.Round
	}
	n.State, n.LeftAt = state, &t
	return round
}

// activeNode is the newest entry of node the run has not left, and its round.
func (st *State) activeNode(node string) (*NodeRun, int) {
	for i := len(st.Rounds) - 1; i >= 0; i-- {
		for j := range st.Rounds[i].Nodes {
			if n := &st.Rounds[i].Nodes[j]; n.Name == node && (n.State == NodeRunning || n.State == NodeWaiting) {
				return n, st.Rounds[i].Round
			}
		}
	}
	return nil, 0
}

// nodeName is node as reported, empty for a node that only routes.
func nodeName(node string) string {
	if reported(node) {
		return node
	}
	return ""
}

func newEvent(st *State, typ string, t time.Time, message string, payload map[string]any) Event {
	return Event{Type: typ, Time: t, Round: st.Round, Node: nodeName(st.at), Message: message, Payload: payload}
}

func started(st *State, t time.Time) Event {
	return newEvent(st, EventStarted, t, "Interrogation started", map[string]any{
		"deadline_at":   st.DeadlineAt,
		"max_rounds":    st.Request.Budget.MaxRounds,
		"max_probes":    st.Request.Budget.MaxProbes,
		"allow_changes": nonNil(st.Request.AllowChanges),
		"revision":      st.Revision,
	})
}

func stopRequested(st *State, stop Stop, t time.Time) Event {
	p := map[string]any{"changes": stop.Changes}
	if stop.Actor != "" {
		p["actor"] = stop.Actor
	}
	return newEvent(st, EventStopRequested, t, "Stop requested, changes: "+stop.Changes, p)
}

func extended(st *State, seconds int, t time.Time) Event {
	return newEvent(st, EventExtended, t, fmt.Sprintf("Deadline moved %ds later", seconds), map[string]any{
		"deadline_at": st.DeadlineAt,
		"seconds":     seconds,
	})
}

func ended(st *State, t time.Time) Event {
	p := map[string]any{"status": st.Status, "end_reason": st.EndReason}
	if st.Error != "" {
		p["error"] = st.Error
	}
	if st.Answer != nil {
		p["certainty"] = st.Answer.Certainty
	}
	return newEvent(st, EventEnded, t, fmt.Sprintf("Interrogation %s: %s", st.Status, st.EndReason), p)
}

func roundStarted(st *State, final bool, t time.Time) Event {
	return newEvent(st, EventRoundStarted, t, fmt.Sprintf("Round %d started", st.Round), map[string]any{
		"round": st.Round,
		"final": final,
	})
}

func roundFinished(st *State, round int, outcome string, t time.Time) Event {
	ev := newEvent(st, EventRoundFinished, t, fmt.Sprintf("Round %d finished: %s", round, outcome), map[string]any{
		"round":   round,
		"outcome": outcome,
	})
	ev.Round = round
	return ev
}

func nodeFinished(st *State, node, next string, t time.Time) Event {
	p := map[string]any{"node": node}
	if next != "" {
		p["next"] = next
	}
	return newEvent(st, EventNodeFinished, t, node+" finished", p)
}

func waitStarted(st *State, reason WaitReason, until, t time.Time) Event {
	return newEvent(st, EventWaitStarted, t, "Waiting: "+string(reason), map[string]any{
		"reason":      reason,
		"deadline_at": until,
	})
}

func waitFinished(st *State, reason WaitReason, outcome string, t time.Time) Event {
	return newEvent(st, EventWaitFinished, t, fmt.Sprintf("Wait %s %s", reason, outcome), map[string]any{
		"reason":  reason,
		"outcome": outcome,
	})
}

// evidenceCollected reports one reading of the call graphs in scope, with at
// most maxSamples of its edges.
func evidenceCollected(st *State, counts EvidenceCounts, edges []apschema.RequestCallEdge, truncated bool, t time.Time) Event {
	samples := make([]map[string]any, 0, min(len(edges), maxSamples))
	for _, e := range edges[:min(len(edges), maxSamples)] {
		samples = append(samples, map[string]any{"caller": e.Caller, "callee": e.Callee})
	}
	return newEvent(st, EventEvidence, t, fmt.Sprintf("Read %d call-graph edges in %d transactions", counts.Edges, counts.Transactions), map[string]any{
		"kind":         evidenceKind,
		"transactions": counts.Transactions,
		"functions":    counts.Functions,
		"edges":        counts.Edges,
		"new_edges":    counts.NewEdges,
		"sample_edges": samples,
		"truncated":    truncated,
	})
}

// modelCalled reports the round's last call, which started at
// st.ModelCallAt.
func modelCalled(st *State, u usage, outcome, errText string, t time.Time) Event {
	p := map[string]any{
		"model":         u.Model,
		"input_tokens":  u.In,
		"output_tokens": u.Out,
		"outcome":       outcome,
		"attempt":       st.RoundModelCalls,
	}
	// A failed call is reported when the supervisor resumes the run, after
	// its backoff, so when the call ended is not known.
	if outcome != OutcomeError && !st.ModelCallAt.IsZero() {
		p["duration_ms"] = t.Sub(st.ModelCallAt).Milliseconds()
	}
	msg := "Model replied"
	if errText != "" {
		p["error"] = errText
		msg = "Model call " + outcome + ": " + errText
	}
	return newEvent(st, EventModelCalled, t, msg, p)
}

// answerUpdated carries the run's answer as its payload.
func answerUpdated(st *State, t time.Time) Event {
	a := st.Answer
	return newEvent(st, EventAnswerUpdated, t, fmt.Sprintf("Answer updated, certainty %d", a.Certainty), map[string]any{
		"text":       a.Text,
		"certainty":  a.Certainty,
		"final":      a.Final,
		"round":      a.Round,
		"updated_at": a.UpdatedAt,
	})
}

func changeEvent(st *State, typ string, p *Probe, t time.Time, message string, payload map[string]any) Event {
	payload["kind"] = changeKind
	payload["target"] = p.Target
	ev := newEvent(st, typ, t, message, payload)
	ev.ChangeID = p.ChangeID
	return ev
}

func changeProposed(st *State, p *Probe, t time.Time) Event {
	return changeEvent(st, EventChangeProposed, p, t, "Proposed a probe on "+p.Target, map[string]any{
		"probe":    probePayload(p),
		"category": p.Category,
		"reason":   p.Why,
	})
}

func changeDenied(st *State, p *Probe, t time.Time) Event {
	return changeEvent(st, EventChangeDenied, p, t, fmt.Sprintf("Denied a probe on %s: %s", p.Target, p.DenyReason), map[string]any{
		"deny_reason": p.DenyReason,
		"detail":      p.Detail,
		"checks":      loop.PassedChecks(loop.DenyReason(p.DenyReason)),
	})
}

func changeRequested(st *State, p *Probe, t time.Time) Event {
	return changeEvent(st, EventChangeRequested, p, t, "Probe on "+p.Target+" passed the checks", map[string]any{
		"checks": loop.PassedChecks(""),
	})
}

func changeApplied(st *State, p *Probe, ruleName string, t time.Time) Event {
	return changeEvent(st, EventChangeApplied, p, t, "Applied a probe on "+p.Target, map[string]any{
		"rule_name":  ruleName,
		"expires_at": p.ExpiresAt,
	})
}

func changeFailed(st *State, p *Probe, t time.Time) Event {
	return changeEvent(st, EventChangeFailed, p, t, fmt.Sprintf("Probe on %s failed: %s", p.Target, p.EndReason), map[string]any{
		"reason": p.EndReason,
	})
}

func changeReverted(st *State, p *Probe, by string, t time.Time) Event {
	payload := map[string]any{"end_reason": p.EndReason}
	if by != "" {
		payload["by"] = by
	}
	return changeEvent(st, EventChangeReverted, p, t, fmt.Sprintf("Probe on %s reverted: %s", p.Target, p.EndReason), payload)
}

// errorRaised reports a failed attempt at node that is retried in retryIn
// seconds.
func errorRaised(st *State, node, errText string, retryIn int, t time.Time) Event {
	return newEvent(st, EventErrorRaised, t, node+" failed: "+errText, map[string]any{
		"node":             node,
		"error":            errText,
		"retry_in_seconds": retryIn,
	})
}

// probePayload is the probe of a recommendation in the API's shape
// (InterrogationProbe): the language and the fields it uses.
func probePayload(p *Probe) map[string]any {
	out := map[string]any{"language": p.Language}
	for k, v := range map[string]string{
		"class_name":           p.Rec.Probe.ClassName,
		"method_name":          p.Rec.Probe.MethodName,
		"package_name":         p.Rec.Probe.PackageName,
		"function_name":        p.Rec.Probe.FunctionName,
		"receiver_name":        p.Rec.Probe.ReceiverName,
		"receiver_method_name": p.Rec.Probe.ReceiverMethodName,
		"signature":            p.Rec.Probe.Signature,
	} {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

// nonNil is s, or an empty slice for nil, so it is written as [] and not
// null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
