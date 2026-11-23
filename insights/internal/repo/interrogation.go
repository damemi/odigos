package repo

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"
)

// InterrogationRequest is a start request as accepted: validated, with the
// defaults applied. It is stored whole in the run's session and read back by
// the workflow and the API, so its JSON is the wire shape of the start but
// for the transaction ids.
type InterrogationRequest struct {
	Source         InterrogationSource  `json:"source"`
	Goal           string               `json:"goal"`
	Question       string               `json:"question"`
	TransactionIDs TransactionIDs       `json:"transaction_ids"`
	AllowChanges   []string             `json:"allow_changes"`
	Budget         InterrogationBudget  `json:"budget"`
	Trigger        InterrogationTrigger `json:"trigger"`
}

// TransactionID is a transaction id as a run's session stores it: a decimal
// string, because ADK reads session state back into map[string]any and a JSON
// number there is a float64. It still reads a number, as runs stored before
// were written.
type TransactionID int64

func (id TransactionID) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.FormatInt(int64(id), 10))
}

func (id *TransactionID) UnmarshalJSON(b []byte) error {
	// A json.Number takes a number or a string holding one.
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	v, err := n.Int64()
	if err != nil {
		return err
	}
	*id = TransactionID(v)
	return nil
}

// TransactionIDs are transaction ids stored as TransactionID is.
type TransactionIDs []int64

func (ids TransactionIDs) MarshalJSON() ([]byte, error) {
	if ids == nil {
		return []byte("null"), nil
	}
	out := make([]TransactionID, len(ids))
	for i, id := range ids {
		out[i] = TransactionID(id)
	}
	return json.Marshal(out)
}

func (ids *TransactionIDs) UnmarshalJSON(b []byte) error {
	var in []TransactionID
	if err := json.Unmarshal(b, &in); err != nil {
		return err
	}
	if in == nil {
		*ids = nil
		return nil
	}
	*ids = make(TransactionIDs, len(in))
	for i, id := range in {
		(*ids)[i] = int64(id)
	}
	return nil
}

// InterrogationSource is the workload an interrogation looks at.
type InterrogationSource struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
}

// Workload is the source as namespace/Kind/name. A run's session stores it
// under the key the one-open-run-per-workload index reads, so it must be the
// same string for every run of the workload.
func (s InterrogationSource) Workload() string {
	return s.Namespace + "/" + s.Kind + "/" + s.Name
}

// InterrogationBudget bounds what a run may do.
type InterrogationBudget struct {
	MaxProbes        int `json:"max_probes"`
	TimeLimitSeconds int `json:"time_limit_seconds"`
	MaxRounds        int `json:"max_rounds"`
}

// InterrogationTrigger is what started a run.
type InterrogationTrigger struct {
	Kind  string `json:"kind"`
	Actor string `json:"actor"`
}

// Statuses of an interrogation that its sessions give without the workflow's
// state. The workflow's own (running and the terminal ones) are in its state.
const (
	InterrogationQueued   = "queued"
	InterrogationRunning  = "running"
	InterrogationStopping = "stopping"
	InterrogationStopped  = "stopped"
	InterrogationFailed   = "failed"
)

// Reasons an interrogation ended without the workflow recording it.
const (
	InterrogationStoppedByUser = "stopped_by_user"
	InterrogationInterrupted   = "interrupted"
)

// InterrogationStop is a stop as a run's control session records it.
type InterrogationStop struct {
	// Changes is "keep" or "revert".
	Changes string    `json:"changes"`
	Actor   string    `json:"actor"`
	At      time.Time `json:"at"`
}

// InterrogationClosed is an ending the workflow did not record itself: a run
// stopped or given up while queued, or closed by the leader when overdue. It
// is stored under the run session's key "closed".
type InterrogationClosed struct {
	Reason  string    `json:"reason"`
	Error   string    `json:"error"`
	Actor   string    `json:"actor"`
	Changes string    `json:"changes"`
	At      time.Time `json:"at"`
}

// InterrogationControl is the state of a run's control session.
type InterrogationControl struct {
	Stop          *InterrogationStop `json:"stop"`
	ExtendSeconds int                `json:"extend_seconds"`
}

// Interrogation is one run as its two ADK sessions hold it.
type Interrogation struct {
	ID        string
	CreatedAt time.Time
	// Open is true from the start until the event that ends the run.
	Open    bool
	Request InterrogationRequest
	// Started is whether the workflow has written its state: false while the
	// run is queued.
	Started bool
	// Run is the workflow's state as stored.
	Run json.RawMessage
	// RunStatus, RunEndReason and RunError are how the workflow's state says
	// the run is doing or ended.
	RunStatus    string
	RunEndReason string
	RunError     string
	// Seq is the sequence of the last event the workflow wrote.
	Seq int64
	// Round is the workflow's current round, 0 before the first.
	Round   int
	Closed  *InterrogationClosed
	Control InterrogationControl
	// Changes counts the run's stored changes; denied proposals are only in
	// Run.
	Changes InterrogationChangeCounts
}

// Status derives the run's status, end reason and error from its two
// sessions, by the first rule that matches: a run with no workflow state is
// queued, an open run with a stop recorded is stopping, an open run is
// running, an ending recorded without the workflow comes from Closed, and any
// other ending is the workflow's own.
func (i Interrogation) Status() (status, endReason, errText string) {
	switch {
	case i.Open && !i.Started:
		return InterrogationQueued, "", ""
	case i.Open && i.Control.Stop != nil:
		return InterrogationStopping, "", ""
	case i.Open:
		return InterrogationRunning, "", ""
	case i.Closed != nil && i.Closed.Reason == InterrogationStoppedByUser:
		return InterrogationStopped, InterrogationStoppedByUser, ""
	case i.Closed != nil:
		return InterrogationFailed, i.Closed.Reason, i.Closed.Error
	}
	return i.RunStatus, i.RunEndReason, i.RunError
}

// DisplayID is the run's label: INT- and the first eight hex digits of its
// id in upper case. It is not unique; the id is.
func (i Interrogation) DisplayID() string {
	hex := strings.ReplaceAll(i.ID, "-", "")
	if len(hex) > 8 {
		hex = hex[:8]
	}
	return "INT-" + strings.ToUpper(hex)
}

// OpenInterrogation is an open run as the supervisor reads it on every poll:
// the run session's state and update time, and its control session's state.
type OpenInterrogation struct {
	ID         string
	State      json.RawMessage
	UpdateTime time.Time
	Control    json.RawMessage
}

// InterrogationHolder is the open run that holds a workload.
type InterrogationHolder struct {
	ID        string
	CreatedAt time.Time
	// Started is false while the run is queued.
	Started bool
}

// InterrogationEvent is one item of a run's event log, as the workflow
// stored it.
type InterrogationEvent struct {
	Seq  int64
	JSON json.RawMessage
}

// InterrogationEvents is a page of a run's event log.
type InterrogationEvents struct {
	Items []InterrogationEvent
	// LastSequence is the sequence of the last item, or the one the page was
	// asked to start after when it is empty.
	LastSequence int64
	// Complete is true when the run has ended and no item is left after this
	// page.
	Complete bool
}

// InterrogationSourceStat is a workload with call-graph evidence, as the start
// form's picker lists it.
type InterrogationSourceStat struct {
	Source InterrogationSource
	// Transactions counts the workload's transactions with a call graph.
	Transactions int
	LastSeen     time.Time
	// OpenID is the id of the workload's open run, empty when there is none.
	OpenID string
	// LiveChanges counts the workload's live interrogation changes.
	LiveChanges int
}

// InterrogationSourceTransaction is one transaction of a workload with a call
// graph.
type InterrogationSourceTransaction struct {
	TransactionID int64
	Operation     string
	Kind          string
	Volume        int64
	LastSeen      time.Time
	// Functions counts every function in its call graph, application or not.
	Functions int
}

// InterrogationSummary is the interrogations page's header.
type InterrogationSummary struct {
	// Queued, Running and RunningSources are as of now; Running includes
	// runs that are stopping.
	Queued         int
	Running        int
	RunningSources []InterrogationSource
	// Total and Ended cover every run the server keeps.
	Total int
	Ended int
	// Averaged counts the ended runs that had started; AvgRounds and
	// AvgDurationSeconds are over those and mean nothing when it is zero.
	Averaged           int
	AvgRounds          float64
	AvgDurationSeconds float64
	ModelCalls         int64
	InputTokens        int64
	OutputTokens       int64
	// Model is the model of the run that was updated last.
	Model string
}

// InterrogationChangeCounts counts a run's stored changes by their wire state
// at one time. Live is requested, blocked and active.
type InterrogationChangeCounts struct {
	Stored    int
	Live      int
	Reverting int
	Reverted  int
	Expired   int
	Failed    int
	// NextExpiryAt is the soonest expiry of a live change; zero when there is
	// none.
	NextExpiryAt time.Time
}

// InterrogationChangesSummary counts the changes live in the cluster. Live is
// split into Temporary and Permanent; the next expiry is a temporary one's.
type InterrogationChangesSummary struct {
	Live               int
	Temporary          int
	Permanent          int
	NextExpiryAt       time.Time
	NextExpiryChangeID string
	BySource           []InterrogationSourceChanges
}

// InterrogationSourceChanges counts the live changes of one workload.
type InterrogationSourceChanges struct {
	Source   InterrogationSource
	Language string
	Live     int
}

// Desired states of a change, as stored.
const (
	ChangeApproved = "approved"
	ChangeKept     = "kept"
	ChangeReverted = "reverted"
)

// Wire states of a change, derived from its row.
const (
	ChangeRequested = "requested"
	ChangeBlocked   = "blocked"
	ChangeActive    = "active"
	ChangeReverting = "reverting"
	ChangeExpired   = "expired"
	ChangeFailed    = "failed"
)

// Lifetimes of a change.
const (
	ChangeTemporary = "temporary"
	ChangePermanent = "permanent"
)

// Actions a person may take on a change, and the end reasons of the two that
// end one.
const (
	ChangeKeep   = "keep"
	ChangeRevert = "revert"
	ChangeRemove = "remove"
)

// ChangeNotAppliedInTime is the end reason of a change the reconciler did not
// place in time.
const ChangeNotAppliedInTime = "not_applied_in_time"

// ChangeFailureReasons are the end reasons of a change that never took
// effect: not placed in time, or given up on with the block reason the
// reconciler recorded.
var ChangeFailureReasons = []string{ChangeNotAppliedInTime, "rule_disabled", "rule_not_managed", "workload_cap_reached"}

// InterrogationChange is a probe an interrogation requested: a row of
// interrogation_requested_probes.
type InterrogationChange struct {
	ChangeID        string
	InterrogationID string
	Round           int
	FirstSeen       time.Time
	Source          InterrogationSource
	Language        string
	Target          string
	Category        string
	// Why is the model's reason for the probe, as it sent it.
	Why string

	ClassName          string
	MethodName         string
	PackageName        string
	FunctionName       string
	ReceiverName       string
	ReceiverMethodName string
	Signature          string

	// Desired is the stored state: ChangeApproved, ChangeKept or
	// ChangeReverted.
	Desired   string
	ExpiresAt time.Time
	KeptBy    string
	KeptAt    time.Time
	EndReason string

	// What the reconciler observed.
	InRule      bool
	AppliedAt   time.Time
	RemovedAt   time.Time
	BlockReason string

	UpdatedAt time.Time

	// Interrogation is the change's run without its workflow state and change
	// counts; nil once the run is gone. Reads of one run's changes leave it
	// nil.
	Interrogation *Interrogation
}

// Live reports whether the change should be in the workload's rule at now.
func (c InterrogationChange) Live(now time.Time) bool {
	return c.Desired == ChangeKept || (c.Desired == ChangeApproved && c.ExpiresAt.After(now))
}

// State derives the change's wire state at now from its row. A stored state
// this binary does not know is returned as it is.
func (c InterrogationChange) State(now time.Time) string {
	live := c.Live(now)
	switch {
	case live && c.InRule:
		return ChangeActive
	case live && c.BlockReason != "":
		return ChangeBlocked
	case live:
		return ChangeRequested
	case c.InRule:
		return ChangeReverting
	case c.Desired == ChangeReverted && slices.Contains(ChangeFailureReasons, c.EndReason):
		return ChangeFailed
	case c.Desired == ChangeReverted:
		return ChangeReverted
	case c.Desired == ChangeApproved:
		return ChangeExpired
	}
	return c.Desired
}

// Lifetime is ChangePermanent for a change a person kept, also after it was
// removed, and ChangeTemporary otherwise.
func (c InterrogationChange) Lifetime() string {
	if c.Desired == ChangeKept || c.Desired == ChangeReverted && c.EndReason == ChangeRemove {
		return ChangePermanent
	}
	return ChangeTemporary
}

// Actions lists what a person may do with the change at now.
func (c InterrogationChange) Actions(now time.Time) []string {
	switch {
	case c.Desired == ChangeKept:
		return []string{ChangeRemove}
	case c.Live(now):
		return []string{ChangeKeep, ChangeRevert}
	}
	return []string{}
}
