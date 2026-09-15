// Package repo defines the database-agnostic interface used to persist
// baselines, observations, anomalies, and policies. It is the swap seam: the
// engine, debug UI, and HTTP handlers depend only on this interface, while
// backends (ClickHouse today) implement it under internal/db/<backend>/.
//
// Domain types are split by area across the package (transaction.go,
// baseline.go, learning.go, policy.go, anomaly.go, guardrail.go,
// observation.go, scope.go); repo.go holds the Repository interface.
package repo

import (
	"context"
	"time"
)

// Repository is the single backend-facing dependency callers hold, implemented
// by the ClickHouse backend and consumed by the engine, debug UI, and HTTP
// handlers. Implementations MUST be safe for concurrent use, since handlers,
// the OTLP receiver, and background workers touch it at once.
type Repository interface {
	// ---------- transactions ----------

	// UpsertTransaction returns the row for (service, namespace, operation,
	// kind), creating it if missing. Idempotent; namespace is part of the
	// identity. Cold path / tests - the engine resolves ids locally and
	// persists via UpsertTransactions on the write-behind flush.
	UpsertTransaction(ctx context.Context, service, namespace, operation string, kind TransactionKind) (Transaction, error)

	// UpsertTransactions batch-inserts transaction identity rows. Idempotent
	// (ReplacingMergeTree). Called by the engine's write-behind flusher so the
	// per-trace path never hits the DB for identity.
	UpsertTransactions(ctx context.Context, txs []Transaction) error

	// ListTransactions returns transactions for internal id→identity lookups.
	// UI listings use ListTransactionStats / ListServiceStats instead.
	ListTransactions(ctx context.Context, limit int) ([]Transaction, error)

	// GetTransactionByID returns one transaction's identity (service, namespace,
	// operation, kind). ok=false when unknown.
	GetTransactionByID(ctx context.Context, transactionID int64) (Transaction, bool, error)

	// RecordActivity folds a batch of activity deltas (volume + last_seen) into
	// storage. Called by the engine's write-behind flusher once per cycle.
	RecordActivity(ctx context.Context, deltas []ActivityDelta) error

	// ListServiceStats returns per-(namespace, service) rollups, most active
	// first. Backs the debug UI transactions page's group-by-service overview.
	ListServiceStats(ctx context.Context, limit int) ([]ServiceStat, error)

	// ListServiceNames returns every distinct service name seen in transactions,
	// sorted A–Z. Cluster-wide caller/callee candidate set for guardrails.
	ListServiceNames(ctx context.Context) ([]string, error)

	// ListTransactionStats returns transactions with volume/last_seen, ordered
	// per filter (default volume desc). Scoped to one service when
	// namespace+service are set, or all transactions when empty.
	ListTransactionStats(ctx context.Context, params TransactionListParams) ([]TransactionStat, error)

	// DeleteTransaction permanently removes a transaction and its related
	// state (baselines, activity, observations, anomalies, txn-scoped policies).
	// Cold path only - operator cleanup of inactive/obsolete transactions.
	DeleteTransaction(ctx context.Context, transactionID int64) error

	// DeleteAllTransactions removes every transaction and related per-txn state.
	// Cold path only - used when identity dimensions change and the operator
	// chooses to reset inventory.
	DeleteAllTransactions(ctx context.Context) error

	// ---------- baselines ----------

	// GetBaselineForTransaction returns every class row for the transaction in
	// one query. Called by the engine on the scoring path, once per trace after
	// a cache miss.
	GetBaselineForTransaction(ctx context.Context, transactionID int64) ([]BaselineClass, error)

	// ListBaselinePromotion returns each transaction's promoted-vs-total class
	// tally in one aggregate query, avoiding an N+1 read. Backs the debug UI
	// overview's promotion metrics.
	ListBaselinePromotion(ctx context.Context) ([]BaselinePromotion, error)

	// ListUnpromotedBaselines returns unpromoted (transaction, class) rows that
	// may satisfy a time-based promotion condition, prefiltered by filter. Called
	// by the promotion sweeper after the in-memory pass; not on the per-trace path.
	ListUnpromotedBaselines(ctx context.Context, filter UnpromotedBaselineFilter) ([]UnpromotedBaselineCandidate, error)

	// UpsertBaselineClass atomically merges an observation into the class's
	// baseline. Cold path only (one-shot seeding and admin/UI writes); the
	// engine uses ApplyBaselineDeltas on the hot path instead.
	UpsertBaselineClass(ctx context.Context, bc BaselineClass) error

	// ApplyBaselineDeltas atomically folds a batch of per-(transaction, class)
	// contributions into storage and returns the authoritative post-merge rows.
	// Called once per cycle by the engine's write-behind flusher, replacing
	// per-trace baseline writes so DB write volume tracks the flush cadence.
	//
	// The contract is per-row and backend-defined: each delta is combined with
	// the freshest stored value via merge (so replicas' set/count contributions
	// are additive, not last-writer-wins), observation_count grows by
	// CountDelta, and promoted is monotonic. Missing rows are seeded from the
	// delta's seeding fields. The ClickHouse backend appends to aggregate tables
	// and reconciles via merge-at-read; a row that failed to apply may be
	// omitted from the result.
	ApplyBaselineDeltas(ctx context.Context, deltas []BaselineDelta, merge BaselineMerger) ([]BaselineClass, error)

	// PromoteBaselineClass flips Promoted=true. Called by the engine's promotion
	// gate once a class meets its observation/age thresholds.
	PromoteBaselineClass(ctx context.Context, transactionID int64, class DeviationClass) error

	// ResetBaselineClass wipes one (transaction, class) baseline (members,
	// counters, meta) so it re-learns from scratch. Cold path only - an operator
	// "re-learn" action.
	ResetBaselineClass(ctx context.Context, transactionID int64, class DeviationClass) error

	// ResetTransactionBaselines wipes every class of a transaction in one shot
	// (the whole-transaction "re-learn"). Cold path only.
	ResetTransactionBaselines(ctx context.Context, transactionID int64) error

	// ---------- learning policies ----------

	// ListLearningPolicies returns every learning policy row (global defaults
	// plus service/transaction overrides). Read by the engine at startup and on
	// reload to build its in-memory promotion-gate snapshot, never per trace.
	ListLearningPolicies(ctx context.Context) ([]LearningPolicy, error)

	// UpsertLearningPolicy inserts or updates one policy, keyed by (Class, Scope,
	// ScopeKey). An empty Scope is stored as ScopeGlobal. Called by the UI editor.
	UpsertLearningPolicy(ctx context.Context, p LearningPolicy) error

	// DeleteLearningPolicy removes one scoped override so the class falls back to
	// a broader policy. Called by the UI editor.
	DeleteLearningPolicy(ctx context.Context, class DeviationClass, scope PolicyScope, scopeKey string) error

	// ---------- observations ----------

	// InsertObservations stores traces as UI/debug examples, all in one insert.
	// Callers pre-decide the sample_reason; the repo does not sample. Called by
	// the engine's background sample writer, which batches so ClickHouse sees a
	// few large inserts instead of one per sampled trace.
	InsertObservations(ctx context.Context, obs []Observation) error

	// ListObservationsByTransaction returns examples for one (transaction,
	// sample_reason), newest first. Metadata only - RawOTLP is empty. Call
	// GetObservationByTrace when the OTLP blob is needed.
	ListObservationsByTransaction(ctx context.Context, transactionID int64, sampleReason string, limit int) ([]Observation, error)

	// GetObservationByTrace returns the stored trace including raw OTLP for a
	// (transaction, trace) pair; ok=false when none was retained (e.g. aged out
	// under the TTL). Backs compare / accept / view-trace paths.
	GetObservationByTrace(ctx context.Context, transactionID int64, traceID string) (Observation, bool, error)

	// GetLatestObservationByReason returns the newest observation with the given
	// sample_reason across all transactions; ok=false when none was retained.
	// Backs guardrail investigate evidence (see GuardrailEvidenceReason).
	GetLatestObservationByReason(ctx context.Context, sampleReason string) (Observation, bool, error)

	// CountObservationsByReason returns how many distinct traces are stored for
	// one transaction, keyed by sample_reason. The engine loads this once per
	// transaction and counts in memory from there, so the per-reason sample caps
	// hold across restarts without a query per candidate sample.
	CountObservationsByReason(ctx context.Context, transactionID int64) (map[string]int, error)

	// ListObservationsWithRaw returns the newest stored traces for one
	// (transaction, sample_reason) including raw OTLP, newest first. Cold path
	// only - the recommendation miner reads a transaction's relation samples in
	// one query instead of one blob fetch per trace.
	ListObservationsWithRaw(ctx context.Context, transactionID int64, sampleReason string, limit int) ([]Observation, error)

	// ---------- guardrail recommendations ----------

	// UpsertRecommendations writes a mining run's rows for one transaction,
	// keyed by recommendation id; re-inserting an id replaces it. The caller
	// carries over State/CreatedAt from the previous rows so decisions survive.
	UpsertRecommendations(ctx context.Context, recs []Recommendation) error

	// ListRecommendations returns recommendations matching filter with live
	// counters and transaction identity joined, best rank first.
	ListRecommendations(ctx context.Context, filter RecommendationFilter) ([]Recommendation, error)

	// GetRecommendation returns one recommendation by id; ok=false when unknown.
	GetRecommendation(ctx context.Context, id string) (Recommendation, bool, error)

	// SetRecommendationState records the operator's decision on one
	// recommendation. Called by apply / dismiss / restore / revert.
	SetRecommendationState(ctx context.Context, id string, state RecommendationState) error

	// AddRecommendationLiveCounts folds a flush batch of shadow-evaluation
	// outcomes into the live counters. Called from the engine's buffered flush,
	// never per trace.
	AddRecommendationLiveCounts(ctx context.Context, deltas []RecommendationLiveDelta) error

	// DeleteRecommendations removes every recommendation (and live counter) of
	// one transaction, or of all transactions when transactionID is 0. Cold path
	// - transaction delete / inventory reset.
	DeleteRecommendations(ctx context.Context, transactionID int64) error

	// ListRecommendationMiningCandidates returns promoted transactions whose
	// relation samples are not yet mined, gained samples since the last run, or
	// whose in-progress claim is older than staleClaim. scoredClasses is how
	// many baseline classes must be promoted for a transaction to count.
	ListRecommendationMiningCandidates(ctx context.Context, scoredClasses int, staleClaim time.Duration, limit int) ([]RecommendationMiningCandidate, error)

	// RecordRecommendationMiningRun writes the claim (zero MinedAt) or the
	// outcome of mining one transaction.
	RecordRecommendationMiningRun(ctx context.Context, run RecommendationMiningRun) error

	// ---------- policies ----------

	// ListEnabledPolicies returns all enabled alert policies across every scope.
	// Backs the read-only API/UI feed; the engine resolves per-transaction
	// policies from its in-memory scope snapshot instead.
	ListEnabledPolicies(ctx context.Context) ([]Policy, error)

	// ListAlertPolicies returns every alert policy row (all scopes, enabled or
	// not), uncached so a UI edit is visible on the next reload. Read by the UI
	// editor and the engine's scope snapshot.
	ListAlertPolicies(ctx context.Context) ([]Policy, error)

	// UpsertAlertPolicy inserts or updates one policy, keyed by (scope, scopeKey),
	// assigning a stable policy_id when ID is zero. Called by the UI editor.
	UpsertAlertPolicy(ctx context.Context, p Policy) error

	// DeleteAlertPolicy removes one scoped override so the scope falls back to a
	// broader policy (service → global). Deleting the global default is a no-op
	// the caller should reject. Called by the UI editor.
	DeleteAlertPolicy(ctx context.Context, scope PolicyScope, scopeKey string) error

	// ---------- service guardrails ----------

	// ListServiceGuardrails returns every declared guardrail (all scopes),
	// uncached so a UI edit is visible on the next reload. Read by the UI editor
	// and the engine's scope snapshot.
	ListServiceGuardrails(ctx context.Context) ([]ServiceGuardrail, error)

	// UpsertServiceGuardrail inserts or updates one guardrail, keyed by (scope,
	// scopeKey); re-inserting the same scope replaces it. Called by the UI editor.
	UpsertServiceGuardrail(ctx context.Context, g ServiceGuardrail) error

	// DeleteServiceGuardrail drops one scoped guardrail so the service is no
	// longer segmented. Called by the UI editor.
	DeleteServiceGuardrail(ctx context.Context, scope PolicyScope, scopeKey string) error

	// GetServiceProfile derives one service's communication summary from the
	// existing D1/D2 baselines (observed callers/callees/egress). Backs the
	// guardrail editor's allowlist seeding; combine with ListServiceNames for
	// open caller/callee candidates.
	GetServiceProfile(ctx context.Context, namespace, service string) (ServiceProfile, error)

	// ---------- anomalies ----------

	// RecordAnomalyOccurrences appends one aggregate increment per occurrence to
	// the anomaly-issue counter. Called from the engine's buffered flush, never
	// per trace, so a repeating deviation costs one insert per flush interval.
	RecordAnomalyOccurrences(ctx context.Context, occs []AnomalyOccurrence) error

	// ListAnomalyIssues returns deduped anomaly findings over the window, one row
	// per (transaction, signature) with Status and occurrence count, worst/most-
	// recent first. Evidence is not loaded - use GetAnomalyIssue for detail.
	ListAnomalyIssues(ctx context.Context, filter FindingListFilter) ([]AnomalyIssue, error)

	// ListAnomalyIssuesByTransaction is ListAnomalyIssues scoped to one
	// transaction via an indexed read. Evidence is not loaded.
	ListAnomalyIssuesByTransaction(ctx context.Context, transactionID int64, filter FindingListFilter) ([]AnomalyIssue, error)

	// GetAnomalyIssue returns one issue by (transaction, signature) including
	// evidence; ok=false when unknown. Backs the investigate / compare view.
	GetAnomalyIssue(ctx context.Context, transactionID int64, signature string) (AnomalyIssue, bool, error)

	// ResolveAnomalyIssue records an operator decision (accept, dismiss, or
	// reopen) from the debug UI. The decision is sticky: the issue moves to the
	// matching status tab and stays there while its count keeps rising.
	ResolveAnomalyIssue(ctx context.Context, transactionID int64, signature string, resolution AnomalyResolution) error

	// DeleteAnomalyIssues removes every anomaly issue row for a transaction.
	// Called by the engine on a baseline re-learn (cold path): the findings were
	// scored against a baseline that no longer exists, so they are dropped rather
	// than left contradicting the fresh baseline. If the behaviour is still
	// anomalous it re-fires against the new baseline as a fresh issue.
	DeleteAnomalyIssues(ctx context.Context, transactionID int64) error

	// ---------- system settings ----------

	GetSystemSettings(ctx context.Context) (SystemSettings, error)
	UpsertSystemSettings(ctx context.Context, s SystemSettings) error
	SetObservationRetentionDays(ctx context.Context, days int) error

	// RecordGuardrailViolations appends one aggregate increment per hit to the
	// guardrail-violation counter. Called from the engine's buffered flush, never
	// per trace, so a steadily-broken edge costs one insert per flush interval.
	RecordGuardrailViolations(ctx context.Context, hits []GuardrailViolationHit) error

	// ListGuardrailViolations returns policy-violation findings over the window,
	// one row per (service, rule, offending item) with Status, worst/most-recent
	// first. Backs the debug UI Findings feed's policy-violations section.
	ListGuardrailViolations(ctx context.Context, filter FindingListFilter) ([]GuardrailViolationGroup, error)

	// GetGuardrailViolation returns one group by (scope_key, rule_key, offending),
	// ok=false when unknown. Backs the investigate detail (evidence trace).
	GetGuardrailViolation(ctx context.Context, scopeKey string, rule RuleKey, offending string) (GuardrailViolationGroup, bool, error)

	// ResolveGuardrailViolation records an operator decision (allow or dismiss)
	// from the debug UI. The decision is sticky: the group moves to the matching
	// status tab and stays there while its count keeps rising.
	ResolveGuardrailViolation(ctx context.Context, scope PolicyScope, scopeKey string, rule RuleKey, offending string, resolution GuardrailResolution) error

	// DeleteGuardrailViolationsForRule removes every violation row for one rule
	// type. Cold path - used when transaction inventory is wiped so stale
	// allowed_transactions findings do not linger in the UI.
	DeleteGuardrailViolationsForRule(ctx context.Context, rule RuleKey) error

	// ---------- service graph (blast radius) ----------

	// UpsertServiceGraphEdges folds a batch of servicegraph edges (shipped from
	// the gateway connector over OTLP) into storage. Idempotent per (edge,
	// collector pod): the store keeps the latest cumulative counts and last-seen
	// time. Called by the engine off the OTLP metrics receiver, never per trace.
	UpsertServiceGraphEdges(ctx context.Context, edges []ServiceGraphEdge) error

	// QueryBlastRadius returns the subgraph around the anchor (namespace,
	// service): the anchor plus every node/edge reachable within depth hops in
	// both directions (callers and callees). depth <= 0 defaults to 1. Backs the
	// UI's per-anomaly Blast Radius panel.
	QueryBlastRadius(ctx context.Context, namespace, service string, depth int) (BlastRadiusSubgraph, error)

	// ---------- storage health ----------

	// GetStorageHealth returns a point-in-time ClickHouse health snapshot for
	// the debug Storage page (connection, disk, memory, running queries, tables).
	GetStorageHealth(ctx context.Context) (StorageHealth, error)

	// ---------- plumbing ----------

	// Ping verifies the backend is reachable. Used by /readyz.
	Ping(ctx context.Context) error

	// Close releases backend resources. Called on graceful shutdown.
	Close() error
}
