package interrogation

import (
	"encoding/json"
	"fmt"
	"time"

	"google.golang.org/adk/v2/session"

	"github.com/odigos-io/odigos/insights/internal/api/http/apitypes"
	apschema "github.com/odigos-io/odigos/insights/internal/autopilot/schema"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

const (
	// StateVersion is the version of State this binary writes. A binary
	// reads states up to its own version.
	StateVersion = 1
	// GraphVersion changes when a node that can be waiting or recorded as a
	// step is renamed or removed, or a step changes meaning.
	GraphVersion = 1

	// maxStepFailures ends a run whose invocations failed this many times in
	// a row at one step.
	maxStepFailures = 5
)

// Top-level keys of a run's session state.
const (
	runKey     = "run"
	requestKey = "request"
	openKey    = "open"
	ctlKey     = "ctl"
)

// Status is how the workflow reports a run. Queued and stopping are not the
// workflow's: they come from the run's sessions (repo.Interrogation.Status).
type Status string

const (
	StatusRunning   Status = repo.InterrogationRunning
	StatusCompleted Status = "completed"
	StatusStopped   Status = repo.InterrogationStopped
	StatusFailed    Status = repo.InterrogationFailed
)

// Terminal reports whether the run has ended.
func (s Status) Terminal() bool {
	return s == StatusCompleted || s == StatusStopped || s == StatusFailed
}

// EndReason is why a run ended.
type EndReason string

const (
	EndDone             EndReason = "done"
	EndMaxRounds        EndReason = "max_rounds"
	EndMaxModelCalls    EndReason = "max_model_calls"
	EndTimeLimit        EndReason = "time_limit"
	EndProbeBudget      EndReason = "probe_budget"
	EndChangesDenied    EndReason = "changes_denied"
	EndNoEvidence       EndReason = "no_evidence"
	EndStoppedByUser    EndReason = repo.InterrogationStoppedByUser
	EndSourceGone       EndReason = "source_gone"
	EndModelUnavailable EndReason = "model_unavailable"
	EndInterrupted      EndReason = repo.InterrogationInterrupted
	EndError            EndReason = "error"
)

// status is the status a run that ended for r ends with.
func (r EndReason) status() Status {
	switch r {
	case EndDone, EndMaxRounds, EndMaxModelCalls, EndTimeLimit, EndProbeBudget, EndChangesDenied, EndNoEvidence:
		return StatusCompleted
	case EndStoppedByUser:
		return StatusStopped
	default:
		return StatusFailed
	}
}

// Steps are where a new invocation of a run enters the graph again. A step is
// coarser than a node: a run that died inside gather or analyze re-enters at
// gate.
const (
	StepPrepare  = "prepare"
	StepBaseline = "baseline"
	StepGate     = "gate"
	StepApply    = "apply"
	StepConfirm  = "confirm"
	StepObserve  = "observe"
	StepConclude = "conclude"
	StepClosed   = "closed"
)

// WaitReason is what a parked run waits for.
type WaitReason string

const (
	WaitBaseline    WaitReason = "traffic"
	WaitModelPaused WaitReason = "model_paused"
	WaitApply       WaitReason = "rule_applied"
	WaitObserve     WaitReason = "observe"
)

// How a wait ended.
const (
	WaitSatisfied = "satisfied"
	WaitTimedOut  = "timed_out"
	WaitStopped   = "stopped"
)

// ProbeStatus is what became of a probe the model asked for, as this run
// sees it. The change's own state is derived from its stored row.
type ProbeStatus string

const (
	ProbePending   ProbeStatus = "pending"
	ProbeRequested ProbeStatus = "requested"
	ProbeApplied   ProbeStatus = "applied"
	ProbeDenied    ProbeStatus = "denied"
	ProbeFailed    ProbeStatus = "failed"
	ProbeReverted  ProbeStatus = "reverted"
)

// Round states and how a round ended.
const (
	RoundInProgress = "in_progress"
	RoundCompleted  = "completed"

	RoundObserved        = "observed"
	RoundNoChangeApplied = "no_change_applied"
	RoundEnded           = "ended"
)

// States of a node in a round.
const (
	NodeRunning = "running"
	NodeWaiting = "waiting"
	NodeDone    = "done"
	NodeFailed  = "failed"
)

// State is the workflow's record of a run, stored whole under the session
// state key "run" by every node event.
type State struct {
	V     int    `json:"v"`
	Graph int    `json:"graph"` // 0 until prepare runs
	ID    string `json:"id"`
	// Revision names the prompt, request and model the run uses.
	Revision string `json:"revision"`

	// Request is the session state key "request": filled by load, never
	// written back.
	Request repo.InterrogationRequest `json:"-"`

	Status    Status    `json:"status"`
	EndReason EndReason `json:"end_reason"` // empty while the run goes on
	Error     string    `json:"error"`
	// Step is where a new invocation continues.
	Step string `json:"step"`
	// Node is the node the run is in: running it, or parked in it when Wait
	// is set. Empty before prepare and after conclude.
	Node string `json:"node"`
	// Seq is the sequence of the last event written.
	Seq   int64 `json:"seq"`
	Round int   `json:"round"`

	StartedAt  time.Time `json:"started_at"`
	DeadlineAt time.Time `json:"deadline_at"`
	EndedAt    time.Time `json:"ended_at"`

	Wait  *Wait `json:"wait"`
	Waits int   `json:"waits"` // numbers interrupt ids, which are never reused

	BaselineUntil time.Time `json:"baseline_until"`
	PausedUntil   time.Time `json:"paused_until"`
	ApplyStarted  time.Time `json:"apply_started"`
	WakeAt        time.Time `json:"wake_at"`
	ModelCallAt   time.Time `json:"model_call_at"`

	Transactions []TransactionSummary `json:"transactions"`
	// Previous is Transactions as the round before this one left them: every
	// reading of this round, a retry's too, is compared with it.
	Previous []TransactionSummary `json:"previous"`
	Probes   []Probe              `json:"probes"`
	// Final is whether no probe can be added in the current round.
	Final bool `json:"final"`

	ModelCalls int `json:"model_calls"`
	// RoundModelCalls counts the calls sent in the current round: the attempt
	// a model.called event reports.
	RoundModelCalls int    `json:"round_model_calls"`
	ParseFailures   int    `json:"parse_failures"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
	Model           string `json:"model"` // as the gateway reported it

	// Scope, Rounds and Answer are stored in the API's shape and served as
	// they are, but for Scope's ids.
	Scope  []ScopeTransaction `json:"scope"`
	Rounds []Round            `json:"rounds"`
	Answer *Answer            `json:"answer"` // nil until the first reply

	// StepFailures counts the invocations that failed in a row at Step.
	StepFailures int    `json:"step_failures"`
	LastError    string `json:"last_error"`
	// Stop is the stop this run applied.
	Stop *Stop `json:"stop"`

	// at is the node executing now.
	at string
}

// ScopeTransaction is apitypes.InterrogationScopeTransaction with the id
// stored as a string.
type ScopeTransaction struct {
	TransactionID repo.TransactionID                   `json:"transaction_id"`
	Operation     string                               `json:"operation"`
	Kind          string                               `json:"kind"`
	SelectedBy    apitypes.InterrogationScopeSelection `json:"selected_by"`
}

// Wire types whose generated JSON is exactly what the run stores.
type (
	Answer         = apitypes.InterrogationAnswer
	EvidenceCounts = apitypes.InterrogationEvidenceCounts
	RoundReply     = apitypes.InterrogationRoundReply
)

// Round is one pass of the graph as the run recorded it, in the API's shape
// (InterrogationRound). What has not happened yet is absent.
type Round struct {
	Round         int             `json:"round"`
	State         string          `json:"state"`
	Outcome       string          `json:"outcome,omitempty"`
	Final         bool            `json:"final"`
	StartedAt     time.Time       `json:"started_at"`
	EndedAt       *time.Time      `json:"ended_at,omitempty"`
	Nodes         []NodeRun       `json:"nodes"`
	Observed      *EvidenceCounts `json:"observed,omitempty"`
	Reply         *RoundReply     `json:"reply,omitempty"`
	Proposed      []string        `json:"proposed"`
	Applied       []string        `json:"applied"`
	ObservedAfter *EvidenceCounts `json:"observed_after,omitempty"`
}

// NodeRun is one graph node in one round, in the API's shape
// (InterrogationNode).
type NodeRun struct {
	Name      string     `json:"name"`
	State     string     `json:"state"`
	Visits    int        `json:"visits"`
	EnteredAt time.Time  `json:"entered_at"`
	LeftAt    *time.Time `json:"left_at,omitempty"`
}

// Wait is the interrupt a parked run waits on.
type Wait struct {
	Node        string     `json:"node"`
	Reason      WaitReason `json:"reason"`
	Since       time.Time  `json:"since"`
	Until       time.Time  `json:"until"`
	InterruptID string     `json:"interrupt_id"`
}

// TransactionSummary is a transaction of the scope as the run last read it.
// Call graphs are not kept: the state is rewritten whole on every event.
type TransactionSummary struct {
	ID        string `json:"id"`
	Container string `json:"container"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	// Edges is the count before truncation.
	Edges     int    `json:"edges"`
	Functions int    `json:"functions"`
	Hash      string `json:"hash"`
	Truncated bool   `json:"truncated"`
}

// Probe is one function the model asked for and what became of it. A denied
// proposal exists only here; a requested one also has a row in
// interrogation_requested_probes with the same ChangeID.
type Probe struct {
	ChangeID string `json:"change_id"`
	Language string `json:"language"`
	Target   string `json:"target"`
	Category string `json:"category"`
	Why      string `json:"why"`
	// Rec is the recommendation as the model sent it.
	Rec         apschema.Recommendation `json:"rec"`
	Round       int                     `json:"round"`
	Status      ProbeStatus             `json:"status"`
	DenyReason  string                  `json:"deny_reason"`
	Detail      string                  `json:"detail"`
	EndReason   string                  `json:"end_reason"`
	ProposedAt  time.Time               `json:"proposed_at"`
	RequestedAt time.Time               `json:"requested_at"`
	AppliedAt   time.Time               `json:"applied_at"`
	EndedAt     time.Time               `json:"ended_at"`
	ExpiresAt   time.Time               `json:"expires_at"`
}

// load reads a run's state from its session, and the control the supervisor
// attached to the message that started this invocation. A queued run has no
// state yet and gets a fresh one that starts at prepare. A state a newer
// binary wrote is refused.
func load(sess session.Session) (*State, Control, error) {
	var keys struct {
		Run     json.RawMessage           `json:"run"`
		Request repo.InterrogationRequest `json:"request"`
		Ctl     Control                   `json:"ctl"`
	}
	if err := decodeState(sess, &keys); err != nil {
		return nil, Control{}, fmt.Errorf("load interrogation %s: %w", sess.ID(), err)
	}
	var st *State
	if keys.Run != nil {
		// The version is read first: a newer binary may have changed any field.
		var head struct {
			V int `json:"v"`
		}
		if err := json.Unmarshal(keys.Run, &head); err != nil {
			return nil, Control{}, fmt.Errorf("load interrogation %s: %w", sess.ID(), err)
		}
		if head.V > StateVersion {
			return nil, Control{}, fmt.Errorf("load interrogation %s: state version %d, this server reads %d", sess.ID(), head.V, StateVersion)
		}
		if err := json.Unmarshal(keys.Run, &st); err != nil {
			return nil, Control{}, fmt.Errorf("load interrogation %s: %w", sess.ID(), err)
		}
	}
	if st == nil {
		st = &State{V: StateVersion, ID: sess.ID(), Step: StepPrepare}
	}
	st.Request = keys.Request
	return st, keys.Ctl, nil
}

// end records why the run ends. The first reason recorded stands: a stop
// that arrives while the run is concluding for another reason does not
// change how it ended.
func (st *State) end(reason EndReason, errText string) {
	if st.EndReason != "" {
		return
	}
	st.EndReason, st.Error = reason, errText
}

// terminalStatus is the status the run ends with.
func (st *State) terminalStatus() Status {
	return st.EndReason.status()
}

// currentRound is the run's last round, nil before the first.
func (st *State) currentRound() *Round {
	if len(st.Rounds) == 0 {
		return nil
	}
	return &st.Rounds[len(st.Rounds)-1]
}

// openRound starts the next round and returns its event. final says whether
// no probe can be added in it.
func (st *State) openRound(final bool, t time.Time) Event {
	st.Round++
	st.Previous = st.Transactions
	st.RoundModelCalls = 0
	st.Rounds = append(st.Rounds, Round{
		Round:     st.Round,
		State:     RoundInProgress,
		Final:     final,
		StartedAt: t,
		Nodes:     []NodeRun{},
		Proposed:  []string{},
		Applied:   []string{},
	})
	return roundStarted(st, final, t)
}

// closeRound completes the current round with outcome and returns its event.
func (st *State) closeRound(outcome string, t time.Time) Event {
	r := st.currentRound()
	r.State, r.Outcome, r.EndedAt = RoundCompleted, outcome, &t
	return roundFinished(st, r.Round, outcome, t)
}

// callSent counts a model call when it is sent: a call whose reply is never
// recorded still counts.
func (st *State) callSent(t time.Time) {
	st.ModelCalls++
	st.RoundModelCalls++
	st.ModelCallAt = t
}

// answer records the model's reply as the run's answer and the round's
// reply.
func (st *State) answer(reply *Reply, t time.Time) {
	st.Answer = &Answer{Text: reply.Answer, Certainty: reply.Certainty, Round: st.Round, UpdatedAt: t}
	if r := st.currentRound(); r != nil {
		r.Reply = &RoundReply{Text: reply.Answer, Certainty: reply.Certainty, Done: reply.Done}
	}
}
