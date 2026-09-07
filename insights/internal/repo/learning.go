package repo

// LearningMode says how a policy's conditions combine into a single promote/
// keep-learning decision. Read by the engine's promotion gate.
type LearningMode string

const (
	// LearningModeAll promotes only when every condition holds (AND).
	LearningModeAll LearningMode = "all"
	// LearningModeAny promotes as soon as one condition holds (OR). This is
	// what lets a rare transaction graduate on its time window alone, since it
	// would never reach an observation count. It is also the default for an
	// empty/unknown mode.
	LearningModeAny LearningMode = "any"
	// LearningModeKOf promotes once at least MinMatches conditions hold.
	LearningModeKOf LearningMode = "k_of"
)

// LearningConditionType names one criterion that can end the learning phase.
// The set is open: the evaluator treats an unknown type as "not satisfied", so
// adding a new type is backward compatible (an old binary just never promotes
// on it rather than crashing).
type LearningConditionType string

const (
	// ConditionMinObservations is met once the class has folded in at least
	// MinObservations normal traces.
	ConditionMinObservations LearningConditionType = "min_observations"
	// ConditionMinDuration is met once MinDurationMinutes have elapsed since
	// the class started learning.
	ConditionMinDuration LearningConditionType = "min_duration"
	// ConditionStableObservations is met once the baseline has stopped growing:
	// at least StableObservations consecutive normal traces added nothing new
	// to it (no new call edge / peer / library / bucket). This is the
	// "learn until it converges" signal, independent of how many traces total
	// were seen.
	ConditionStableObservations LearningConditionType = "stable_observations"
	// ConditionStableDuration is met once the baseline has not grown for at
	// least StableMinutes - the time-based twin of ConditionStableObservations,
	// so a low-traffic transaction can converge on wall-clock quiet rather than
	// a trace count it may never reach.
	ConditionStableDuration LearningConditionType = "stable_duration"
)

// LearningCondition is a single criterion in a LearningPolicy. Only the field
// that matches Type is read; the rest are zero. Stored as JSON in the policy
// row, so it is also the wire shape the API/UI exchange.
type LearningCondition struct {
	Type LearningConditionType `json:"type"`

	// MinObservations is read for ConditionMinObservations.
	MinObservations int64 `json:"minObservations,omitempty"`
	// MinDurationMinutes is read for ConditionMinDuration. Minutes because
	// operators think in hours/days.
	MinDurationMinutes int64 `json:"minDurationMinutes,omitempty"`
	// StableObservations is read for ConditionStableObservations: how many
	// consecutive no-growth traces prove the baseline has settled.
	StableObservations int64 `json:"stableObservations,omitempty"`
	// StableMinutes is read for ConditionStableDuration: how long the baseline
	// must stay unchanged to count as settled.
	StableMinutes int64 `json:"stableMinutes,omitempty"`
}

// LearningPolicy configures a class's learning phase: the conditions it must
// meet before the engine promotes it from learning to production, how they
// combine (Mode), and which transactions it governs (Scope, see scope.go). A
// class has a global default plus optional service/transaction overrides, most
// specific winning. With no conditions it falls back to a count-only default,
// so an unconfigured class still learns rather than promoting on its first trace.
type LearningPolicy struct {
	Class      DeviationClass
	Mode       LearningMode
	Conditions []LearningCondition

	// MinMatches is the K in LearningModeKOf; ignored for the other modes.
	MinMatches int

	// Scope and ScopeKey say which transactions this policy governs. An empty
	// Scope defaults to ScopeGlobal. ScopeKey is "" for global, ServiceScopeKey
	// for a service, and the transaction_id as text for a transaction.
	Scope    PolicyScope
	ScopeKey string
}
