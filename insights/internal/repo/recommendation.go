package repo

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// Recommendations are guardrail rules Insights suggests instead of waiting for
// an operator to author them. The lifecycle is generic: a miner produces rows
// of some Kind for some scope, they are shadow-evaluated against live traffic
// while open, and the operator applies (turning one into a real guardrail
// rule), dismisses, or reverts them; a decision sticks across re-mining. A
// recommendation is never enforced on its own.
//
// One kind is implemented today: attribute_correlation, a transaction-scoped
// cross-span "these two values always agree" rule mined from the transaction's
// stored relation samples. Adding a kind means a new RecommendationKind, its
// own Spec shape and identity in RecommendationID, a miner that produces it,
// and an apply branch in the engine; the storage, state machine, live
// counters, API actions, and UI list need no change.

// RelationSampleReason tags the whole traces kept while a transaction learns so
// the miner has enough evidence to find cross-span attribute relations. They
// are separate from ExampleSampleReason because they need a larger cap and a
// shorter retention.
const RelationSampleReason = "relation"

// RecommendationKind is the guardrail rule type a recommendation would create.
type RecommendationKind string

const (
	RecommendationKindAttributeCorrelation RecommendationKind = "attribute_correlation"
)

// RecommendationState is where a recommendation sits in its lifecycle. It is an
// operator decision and survives re-mining: a dismissed pair stays dismissed
// when the miner finds it again.
type RecommendationState string

const (
	// RecommendationOpen awaits a decision. New recommendations start here.
	RecommendationOpen RecommendationState = "open"
	// RecommendationApplied was turned into a guardrail rule.
	RecommendationApplied RecommendationState = "applied"
	// RecommendationDismissed was rejected by the operator.
	RecommendationDismissed RecommendationState = "dismissed"
)

// NormalizeRecommendationState maps an unknown state to open.
func NormalizeRecommendationState(s RecommendationState) RecommendationState {
	switch s {
	case RecommendationApplied, RecommendationDismissed:
		return s
	default:
		return RecommendationOpen
	}
}

// RecommendationExample is one sample trace the relation held on, kept so the
// UI can show concrete values next to the abstract rule.
type RecommendationExample struct {
	TraceID    string    `json:"trace_id"`
	LeftValue  string    `json:"left_value"`
	RightValue string    `json:"right_value"`
	ObservedAt time.Time `json:"observed_at"`
}

// Recommendation is one suggested guardrail rule for one scope. Mining
// statistics (Observed, Held, Distinct, Examples) come from stored samples and
// are rewritten on every mining run. Live counters (LiveHeld, LiveBroken) come
// from shadow evaluation against real traffic and only ever grow. State is the
// operator's decision and is preserved across mining runs.
type Recommendation struct {
	ID   string
	Kind RecommendationKind
	// Scope and ScopeKey name the guardrail the recommendation would extend,
	// with the same meaning as ServiceGuardrail.Scope/ScopeKey.
	Scope    PolicyScope
	ScopeKey string
	// TransactionID is the resolved id for transaction scope, 0 otherwise. It
	// is what joins the transaction identity and what the miner claims.
	TransactionID int64
	State         RecommendationState
	// Stale marks a recommendation the latest mining run no longer produced
	// (the samples aged out or the relation stopped holding). It is kept so an
	// applied rule keeps its provenance, but hidden from the open list.
	Stale bool
	// Rank orders a transaction's open recommendations, 0 = strongest.
	Rank int

	// Spec is the guardrail rule this recommendation would apply.
	Spec CorrelationSpec
	// Transform names how the two values relate: exact, core, digits, contains.
	Transform string
	// Guaranteed by the transport, not business logic (a Kafka message key on
	// producer and consumer): a low-severity integrity tripwire, ranked last.
	Transport bool

	// Observed is how many samples had both values, Held how many satisfied the
	// relation, Distinct how many different left values were seen.
	Observed int
	Held     int
	Distinct int
	// SampleCount is how many relation samples the mining run looked at.
	SampleCount int
	Examples    []RecommendationExample

	LiveHeld   int64
	LiveBroken int64
	LiveSince  time.Time
	LiveLast   time.Time

	MinedAt   time.Time
	CreatedAt time.Time
	UpdatedAt time.Time

	// Transaction identity, joined at read time for filtering and display.
	Service   string
	Namespace string
	Operation string
	TxKind    TransactionKind
}

// NormalizeRecommendationScope resolves the scope pair a row is stored under:
// an unset scope on a row that names a transaction is that transaction's
// scope, anything else goes through NormalizePolicyScope.
func NormalizeRecommendationScope(scope PolicyScope, scopeKey string, transactionID int64) (PolicyScope, string) {
	if scope == "" && transactionID != 0 {
		return ScopeTransaction, TransactionScopeKey(transactionID)
	}
	return NormalizePolicyScope(scope), scopeKey
}

// RecommendationID derives the stable identity of a recommendation from what it
// asserts, so re-mining the same relation updates the row instead of adding a
// duplicate and an operator's decision on it sticks.
func RecommendationID(transactionID int64, kind RecommendationKind, spec CorrelationSpec) string {
	h := sha256.New()
	h.Write([]byte(string(kind)))
	h.Write([]byte{0})
	h.Write([]byte(TransactionScopeKey(transactionID)))
	for _, s := range []string{
		spec.Left.Service, spec.Left.Span, spec.Left.Attr,
		spec.Right.Service, spec.Right.Span, spec.Right.Attr,
		spec.Relation,
	} {
		h.Write([]byte{0})
		h.Write([]byte(s))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// RecommendationFilter narrows ListRecommendations. Zero values mean "any".
// Stale rows are excluded unless IncludeStale is set.
type RecommendationFilter struct {
	Kind          RecommendationKind
	State         RecommendationState
	TransactionID int64
	Namespace     string
	Service       string
	IncludeStale  bool
}

// RecommendationLiveDelta is one flush batch's worth of shadow-evaluation
// outcomes for a recommendation: how many live traces of its scope could be
// checked and whether the rule held.
type RecommendationLiveDelta struct {
	ID            string
	Scope         PolicyScope
	ScopeKey      string
	TransactionID int64
	Held          int64
	Broken        int64
	FirstAt       time.Time
	LastAt        time.Time
}

// RecommendationMiningRun records the outcome of mining one transaction, and
// doubles as the soft claim that keeps replicas from mining the same
// transaction at once. A run with a zero MinedAt is in progress.
type RecommendationMiningRun struct {
	TransactionID int64
	ClaimedAt     time.Time
	MinedAt       time.Time
	SampleCount   int
	Candidates    int
}

// RecommendationMiningCandidate is a promoted transaction whose stored relation
// samples have not been mined yet (or gained samples since).
type RecommendationMiningCandidate struct {
	TransactionID int64
	SampleCount   int
}

// GuardrailOriginRecommendation marks rules created by applying a recommendation.
const GuardrailOriginRecommendation = "recommendation"

// RecommendationShadowReason builds the sample_reason tag under which one
// counter-example trace (the relation broke on live traffic) is kept per
// recommendation, so the UI can show why confidence dropped.
func RecommendationShadowReason(id string) string {
	return "recommendation:" + id
}
