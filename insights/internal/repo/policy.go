package repo

// SignalSource identifies a risk signal in a policy's weight map and the risk
// catalog. It is either a deviation-class id (e.g. "D6_db_access") or an
// enricher-signal id (e.g. "d6_destructive_op"); the risk catalog is the source
// of truth for valid values.
type SignalSource string

// EnricherListKey identifies an enricher's configurable keyword list (e.g.
// "sensitive_tables"). It keys Policy.EnricherLists and the editor's list fields.
type EnricherListKey string

// DefaultFireAtScore is the threshold seeded when a policy is created without an
// explicit one. 20 is the "low" band floor, so any single meaningful deviation
// fires; operators raise it for fewer alerts or lower it toward 0 to fire on
// any signal.
const DefaultFireAtScore = 20

// Policy decides whether a trace's deviations are worth an anomaly: the engine
// hands the deviated classes to the risk layer for a 0-100 score, and the policy
// fires when that score reaches FireAtScore. A single high-risk class can fire
// while several low-risk ones don't, which is the point of scoring.
type Policy struct {
	ID      int64
	Name    string
	Enabled bool

	// FireAtScore is the risk-score threshold (0-100) at or above which the
	// policy fires, taken literally: 0 is a valid "fire on any signal". A policy
	// created without an explicit value is seeded with DefaultFireAtScore.
	FireAtScore int

	// SignalWeights overrides the risk catalog's default weight per signal,
	// keyed by source (a deviation class or an enricher signal, so one map tunes
	// both). Sparse - absent signals keep their catalog default; 0 mutes a signal.
	SignalWeights map[SignalSource]int

	// EnricherLists holds this scope's overrides for enricher keyword lists,
	// keyed by list key (see risk.EnricherList.Key). Sparse - an absent key uses
	// the enricher's built-in default, a present list replaces it for this scope.
	EnricherLists map[EnricherListKey][]string

	// Scope and ScopeKey say which transactions this policy governs (see
	// scope.go): a global default plus optional service/transaction overrides,
	// most specific winning. An empty Scope defaults to global; ScopeKey is ""
	// for global, ServiceScopeKey for a service, the transaction_id text otherwise.
	Scope    PolicyScope
	ScopeKey string
}
