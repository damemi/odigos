package repo

import (
	"encoding/json"
	"time"
)

// DeviationClass identifies one independent baseline dimension tracked per
// transaction (call edges, egress, latency, libraries, …). Each class learns,
// is scored, and is promoted on its own; deviations feed the risk layer.
//
// It is a free-form string with opaque JSON values, so the storage layer never
// interprets either and adding a class needs no migration. The scoring engine
// is the source of truth (see internal/engine/doc.go).
type DeviationClass string

const (
	D1CallEdges   DeviationClass = "D1_call_edges"
	D2Egress      DeviationClass = "D2_egress"
	D3Latency     DeviationClass = "D3_latency"
	D4ArgsReturns DeviationClass = "D4_args_returns"
	D5Libraries   DeviationClass = "D5_libraries"
	D6DBAccess    DeviationClass = "D6_db_access"
	D7PayloadSize DeviationClass = "D7_payload_size"
)

var AllDeviationClasses = []DeviationClass{
	D1CallEdges, D2Egress, D3Latency, D4ArgsReturns,
	D5Libraries, D6DBAccess, D7PayloadSize,
}

// BaselineClass holds the learned baseline for one (transaction, deviation
// class). Data is opaque JSON the repo stores byte-for-byte; only the scoring
// engine interprets its per-class shape. DataSchemaVersion lets the engine
// evolve a class's representation without a SQL migration (auto-upgrade or
// re-learn on version mismatch).
type BaselineClass struct {
	TransactionID     int64
	Class             DeviationClass
	Data              json.RawMessage
	DataSchemaVersion int
	ObservationCount  int64
	Promoted          bool

	// Saturated marks a set class whose baseline crossed the per-set member
	// cap. The engine stops learning it, never promotes or scores it, and
	// keeps only the capped subset in Data. Monotonic and max()-aggregated
	// across replicas like Promoted, so once any replica saturates a class
	// every replica adopts that on its next flush or reload. Cleared only by
	// a reset (re-learn).
	Saturated bool

	// LearningStartedAt anchors time-based promotion: a class can graduate once
	// (now - LearningStartedAt) crosses its minimum learning duration. Set at
	// seed time and immutable afterwards (UpsertBaselineClass must not overwrite).
	LearningStartedAt time.Time

	// LastChangedAt is when the baseline last grew and
	// ObservationCountAtLastChange is the ObservationCount at that moment.
	// Together they drive the stability conditions (observations- and time-
	// since-change). Both are max()-aggregated across replicas so the freshest
	// change wins; zero on a baseline that has never grown.
	LastChangedAt                time.Time
	ObservationCountAtLastChange int64
}

// Settled reports whether this class has finished learning for good: either
// it graduated (Promoted) or it saturated and will never graduate. Transaction-
// level roll-ups ("has this transaction finished learning?") use this so one
// runaway class cannot hold the rest of the transaction hostage.
func (b BaselineClass) Settled() bool { return b.Promoted || b.Saturated }

// UnpromotedBaselineFilter narrows ListUnpromotedBaselines to classes that may
// be eligible for time-based promotion. The repo applies a coarse SQL prefilter;
// the engine re-evaluates shouldPromote on each candidate.
type UnpromotedBaselineFilter struct {
	MinDurationEnabled    bool
	MinDurationCutoff     time.Time
	StableDurationEnabled bool
	StableDurationCutoff  time.Time
	Limit                 int
}

// UnpromotedBaselineCandidate is one unpromoted (transaction, class) row with
// the scalar fields needed to evaluate time-based promotion conditions.
type UnpromotedBaselineCandidate struct {
	TransactionID                int64
	Class                        DeviationClass
	ObservationCount             int64
	LearningStartedAt            time.Time
	LastChangedAt                time.Time
	ObservationCountAtLastChange int64
}

// BaselineMerger combines a stored baseline value with an incoming contribution
// for the same (transaction, class). It is the seam that lets the repository
// fold contributions without understanding the per-class JSON shapes: the
// engine owns and supplies it (see ApplyBaselineDeltas), the backend just calls
// it inside its atomic apply.
//
// Per-class semantics (the engine's implementation is authoritative): set
// classes send the full value and MergeData returns the union (idempotent);
// additive classes (D7 histogram) send a since-last-flush delta and MergeData
// adds it (the flusher applies each delta at most once).
type BaselineMerger interface {
	MergeData(class DeviationClass, stored, contribution json.RawMessage) (json.RawMessage, error)
}

// Counter is one additive (bucket, count) pair of a baseline class, e.g. a D7
// payload-size histogram bucket. The ClickHouse backend sums Count across
// contributions for the same (transaction, class, Bucket).
type Counter struct {
	Bucket string
	Count  int64
}

// BaselineCodec translates a class's opaque JSON value to and from the two
// atomic shapes an append-only aggregating store (ClickHouse) can merge itself:
// set members (kept as a union, idempotent) and counters (summed, additive). A
// class uses one shape or the other; classes with no persisted data return
// nothing. The engine owns the JSON shapes and supplies the implementation (see
// engine.NewBaselineCodec), keeping the backend ignorant - like BaselineMerger.
type BaselineCodec interface {
	// Encode splits a class's stored JSON value into its set members and/or
	// additive counters.
	Encode(class DeviationClass, data json.RawMessage) (members []string, counters []Counter, err error)

	// Decode rebuilds a class's JSON value from its merged members and summed
	// counters - the inverse of Encode.
	Decode(class DeviationClass, members []string, counters []Counter) (json.RawMessage, error)
}

// BaselineDelta is one transaction-class's accumulated, unflushed change,
// produced by the engine's in-memory baseline store and drained by its write-
// behind flusher into ApplyBaselineDeltas. It is backend-neutral: it carries
// what changed, not how to persist it.
type BaselineDelta struct {
	TransactionID int64
	Class         DeviationClass

	// Contribution is this replica's data to fold in via
	// BaselineMerger.MergeData. Nil means no data change this cycle (only
	// CountDelta / Promote move). See BaselineMerger for the full-vs-delta rule.
	Contribution json.RawMessage

	// CountDelta is added to the stored observation_count. A delta, never an
	// absolute, since counts are additive across replicas.
	CountDelta int64

	// Promote raises promoted to true; it never lowers it (monotonic).
	Promote bool

	// Saturate raises saturated to true; it never lowers it (monotonic).
	Saturate bool

	// Seeding fields, used only when the row does not exist yet. On an existing
	// row the backend MUST NOT overwrite LearningStartedAt.
	DataSchemaVersion int
	LearningStartedAt time.Time

	// Stability tracking, max()-aggregated (see BaselineClass). Sent as absolute
	// values every flush, not deltas.
	LastChangedAt                time.Time
	ObservationCountAtLastChange int64
}
