package repo

import (
	"encoding/json"
	"time"
)

// Integration is one configured third-party backend that receives findings.
// Type names a descriptor in the notify catalog; Config is JSON keyed by that
// descriptor's fields and is opaque to the repo (the notify package validates
// and redacts it). Filters are the backend-independent delivery filters.
type Integration struct {
	ID        string
	Name      string
	Type      string
	Enabled   bool
	Config    json.RawMessage
	Filters   IntegrationFilters
	CreatedAt time.Time
	UpdatedAt time.Time
}

// IntegrationFilters narrow which finding events an integration receives.
type IntegrationFilters struct {
	// MinSeverity is the lowest severity tier delivered ("low" delivers all).
	// Empty means no floor.
	MinSeverity string `json:"min_severity,omitempty"`
	// Kinds restricts delivery to finding kinds ("anomaly", "violation"); empty
	// delivers both.
	Kinds []string `json:"kinds,omitempty"`
	// NotifyOnResolve sends a resolved message to backends that have no native
	// resolve action (fire-and-forget sinks). Ignored by sinks that resolve.
	NotifyOnResolve bool `json:"notify_on_resolve,omitempty"`
}

// DispatchAction is what was last sent to a backend for one finding.
type DispatchAction string

const (
	DispatchFiring   DispatchAction = "firing"
	DispatchResolved DispatchAction = "resolved"
)

// DispatchState is where one (integration, finding) delivery sits.
type DispatchState string

const (
	// DispatchPending was derived but not yet attempted (or manually reset for
	// retry).
	DispatchPending DispatchState = "pending"
	// DispatchSent was delivered; sent_at and the *_at_send fields describe what
	// the backend last received.
	DispatchSent DispatchState = "sent"
	// DispatchFailed had a retryable failure and is scheduled for next_attempt_at.
	DispatchFailed DispatchState = "failed"
	// DispatchDead exhausted its attempts or hit a permanent error. Not retried
	// until the finding changes or an operator resets it.
	DispatchDead DispatchState = "dead"
)

// NotificationDispatch is the ledger row for one (integration, finding): the
// last action sent, the retry schedule, and what the backend last saw. The
// notifier derives new-vs-repeat and resolve-once from it and the retry loop
// drives from it; nothing about delivery state lives in the engine.
type NotificationDispatch struct {
	IntegrationID     string
	FindingKey        string
	LastAction        DispatchAction
	State             DispatchState
	Attempts          int
	NextAttemptAt     time.Time
	LastError         string
	SentAt            time.Time
	OccurrencesAtSend int64
	SeverityAtSend    string
	StatusAtSend      string
	// ExternalID is the id a backend returned on create (ticket key, message
	// ts) and expects back on resolve. Empty for backends that have none.
	ExternalID string
	UpdatedAt  time.Time
}

// DispatchListFilter selects ledger rows. IntegrationID narrows to one
// integration (empty = all, used by the finding-detail "deliveries" block
// together with FindingKeys). State and DueBefore pick retry candidates.
type DispatchListFilter struct {
	IntegrationID string
	FindingKeys   []string
	State         DispatchState
	// DueBefore keeps only rows whose next_attempt_at <= DueBefore. Zero
	// disables the bound.
	DueBefore time.Time
	Limit     int
}

// AttemptOutcome is how one delivery attempt ended.
type AttemptOutcome string

const (
	AttemptOK    AttemptOutcome = "ok"
	AttemptError AttemptOutcome = "error"
	// AttemptDead is a failure that moved the delivery to DispatchDead.
	AttemptDead AttemptOutcome = "dead"
)

// NotificationAttempt is one audit row: one send to one backend, success or
// failure. FindingKey is empty for a batch-level failure that covered several
// findings (EventCount says how many).
type NotificationAttempt struct {
	IntegrationID string
	FindingKey    string
	Action        DispatchAction
	Attempt       int
	Outcome       AttemptOutcome
	HTTPStatus    int
	Error         string
	Duration      time.Duration
	EventCount    int
	AttemptedAt   time.Time
}

// NotificationCursor is the notifier's progress for one integration: the
// newest finding activity timestamp it has processed. Only the elected leader
// writes it.
type NotificationCursor struct {
	IntegrationID string
	Cursor        time.Time
	LastRunAt     time.Time
	Leader        string
}

// IntegrationHealth is the derived (never stored) delivery health of one
// integration, computed from the attempts audit and the ledger.
type IntegrationHealth struct {
	LastSuccessAt       time.Time
	LastFailureAt       time.Time
	LastError           string
	ConsecutiveFailures int
	Pending             int64
	Failed              int64
	Dead                int64
}

// Finding keys name one finding across the ledger, the attempts audit, and the
// payloads sent to backends (idempotency key). Shared by the notifier (writer)
// and the API (reader of a finding's deliveries) so they agree on the format.

// AnomalyFindingKey is the finding key of one anomaly issue.
func AnomalyFindingKey(transactionID int64, signature string) string {
	return "anomaly:" + TransactionScopeKey(transactionID) + ":" + signature
}

// ViolationFindingKey is the finding key of one guardrail violation group.
func ViolationFindingKey(scopeKey string, rule RuleKey, offending string) string {
	return "violation:" + scopeKey + ":" + string(rule) + ":" + offending
}
