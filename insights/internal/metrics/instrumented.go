package metrics

import (
	"context"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// WrapRepo decorates a Repository so every method records a call count and
// latency observation under its own method label. Wrap once, as close to
// construction as possible, so both engine (hot-path) and UI/API (cold-path)
// callers are measured through the same instance.
func WrapRepo(inner repo.Repository) repo.Repository {
	return &instrumentedRepo{inner: inner}
}

type instrumentedRepo struct {
	inner repo.Repository
}

// observe times fn, then records the call count (ok|error) and duration for
// method. Centralises the bookkeeping so each wrapper method is a one-liner.
func observe(method string, fn func() error) error {
	start := time.Now()
	err := fn()
	status := "ok"
	if err != nil {
		status = "error"
	}
	repoCalls.WithLabelValues(method, status).Inc()
	repoCallDuration.WithLabelValues(method).Observe(time.Since(start).Seconds())
	return err
}

func (m *instrumentedRepo) UpsertTransaction(ctx context.Context, service, namespace, operation string, kind repo.TransactionKind) (repo.Transaction, error) {
	var out repo.Transaction
	err := observe("UpsertTransaction", func() error {
		var e error
		out, e = m.inner.UpsertTransaction(ctx, service, namespace, operation, kind)
		return e
	})
	return out, err
}

func (m *instrumentedRepo) UpsertTransactions(ctx context.Context, txs []repo.Transaction) error {
	return observe("UpsertTransactions", func() error {
		return m.inner.UpsertTransactions(ctx, txs)
	})
}

func (m *instrumentedRepo) ListTransactions(ctx context.Context, limit int) ([]repo.Transaction, error) {
	var out []repo.Transaction
	err := observe("ListTransactions", func() error {
		var e error
		out, e = m.inner.ListTransactions(ctx, limit)
		return e
	})
	return out, err
}

func (m *instrumentedRepo) GetTransactionByID(ctx context.Context, transactionID int64) (repo.Transaction, bool, error) {
	var (
		out repo.Transaction
		ok  bool
	)
	err := observe("GetTransactionByID", func() error {
		var e error
		out, ok, e = m.inner.GetTransactionByID(ctx, transactionID)
		return e
	})
	return out, ok, err
}

func (m *instrumentedRepo) RecordActivity(ctx context.Context, deltas []repo.ActivityDelta) error {
	return observe("RecordActivity", func() error {
		return m.inner.RecordActivity(ctx, deltas)
	})
}

func (m *instrumentedRepo) ListServiceStats(ctx context.Context, limit int) ([]repo.ServiceStat, error) {
	var out []repo.ServiceStat
	err := observe("ListServiceStats", func() error {
		var e error
		out, e = m.inner.ListServiceStats(ctx, limit)
		return e
	})
	return out, err
}

func (m *instrumentedRepo) ListServiceNames(ctx context.Context) ([]string, error) {
	var out []string
	err := observe("ListServiceNames", func() error {
		var e error
		out, e = m.inner.ListServiceNames(ctx)
		return e
	})
	return out, err
}

func (m *instrumentedRepo) ListTransactionStats(ctx context.Context, params repo.TransactionListParams) ([]repo.TransactionStat, error) {
	var out []repo.TransactionStat
	err := observe("ListTransactionStats", func() error {
		var e error
		out, e = m.inner.ListTransactionStats(ctx, params)
		return e
	})
	return out, err
}

func (m *instrumentedRepo) DeleteTransaction(ctx context.Context, transactionID int64) error {
	return observe("DeleteTransaction", func() error {
		return m.inner.DeleteTransaction(ctx, transactionID)
	})
}

func (m *instrumentedRepo) DeleteAllTransactions(ctx context.Context) error {
	return observe("DeleteAllTransactions", func() error {
		return m.inner.DeleteAllTransactions(ctx)
	})
}

func (m *instrumentedRepo) GetBaselineForTransaction(ctx context.Context, transactionID int64) ([]repo.BaselineClass, error) {
	var out []repo.BaselineClass
	err := observe("GetBaselineForTransaction", func() error {
		var e error
		out, e = m.inner.GetBaselineForTransaction(ctx, transactionID)
		return e
	})
	return out, err
}

func (m *instrumentedRepo) ListBaselinePromotion(ctx context.Context) ([]repo.BaselinePromotion, error) {
	var out []repo.BaselinePromotion
	err := observe("ListBaselinePromotion", func() error {
		var e error
		out, e = m.inner.ListBaselinePromotion(ctx)
		return e
	})
	return out, err
}

func (m *instrumentedRepo) ListUnpromotedBaselines(ctx context.Context, filter repo.UnpromotedBaselineFilter) ([]repo.UnpromotedBaselineCandidate, error) {
	var out []repo.UnpromotedBaselineCandidate
	err := observe("ListUnpromotedBaselines", func() error {
		var e error
		out, e = m.inner.ListUnpromotedBaselines(ctx, filter)
		return e
	})
	return out, err
}

func (m *instrumentedRepo) UpsertBaselineClass(ctx context.Context, bc repo.BaselineClass) error {
	return observe("UpsertBaselineClass", func() error {
		return m.inner.UpsertBaselineClass(ctx, bc)
	})
}

func (m *instrumentedRepo) ApplyBaselineDeltas(ctx context.Context, deltas []repo.BaselineDelta, merge repo.BaselineMerger) ([]repo.BaselineClass, error) {
	var out []repo.BaselineClass
	err := observe("ApplyBaselineDeltas", func() error {
		var e error
		out, e = m.inner.ApplyBaselineDeltas(ctx, deltas, merge)
		return e
	})
	return out, err
}

func (m *instrumentedRepo) PromoteBaselineClass(ctx context.Context, transactionID int64, class repo.DeviationClass) error {
	return observe("PromoteBaselineClass", func() error {
		return m.inner.PromoteBaselineClass(ctx, transactionID, class)
	})
}

func (m *instrumentedRepo) ResetBaselineClass(ctx context.Context, transactionID int64, class repo.DeviationClass) error {
	return observe("ResetBaselineClass", func() error {
		return m.inner.ResetBaselineClass(ctx, transactionID, class)
	})
}

func (m *instrumentedRepo) ResetTransactionBaselines(ctx context.Context, transactionID int64) error {
	return observe("ResetTransactionBaselines", func() error {
		return m.inner.ResetTransactionBaselines(ctx, transactionID)
	})
}

func (m *instrumentedRepo) ListLearningPolicies(ctx context.Context) ([]repo.LearningPolicy, error) {
	var out []repo.LearningPolicy
	err := observe("ListLearningPolicies", func() error {
		var e error
		out, e = m.inner.ListLearningPolicies(ctx)
		return e
	})
	return out, err
}

func (m *instrumentedRepo) UpsertLearningPolicy(ctx context.Context, p repo.LearningPolicy) error {
	return observe("UpsertLearningPolicy", func() error {
		return m.inner.UpsertLearningPolicy(ctx, p)
	})
}

func (m *instrumentedRepo) DeleteLearningPolicy(ctx context.Context, class repo.DeviationClass, scope repo.PolicyScope, scopeKey string) error {
	return observe("DeleteLearningPolicy", func() error {
		return m.inner.DeleteLearningPolicy(ctx, class, scope, scopeKey)
	})
}

func (m *instrumentedRepo) InsertObservations(ctx context.Context, obs []repo.Observation) error {
	return observe("InsertObservations", func() error {
		return m.inner.InsertObservations(ctx, obs)
	})
}

func (m *instrumentedRepo) ListObservationsByTransaction(ctx context.Context, transactionID int64, sampleReason string, limit int) ([]repo.Observation, error) {
	var out []repo.Observation
	err := observe("ListObservationsByTransaction", func() error {
		var e error
		out, e = m.inner.ListObservationsByTransaction(ctx, transactionID, sampleReason, limit)
		return e
	})
	return out, err
}

func (m *instrumentedRepo) GetObservationByTrace(ctx context.Context, transactionID int64, traceID string) (repo.Observation, bool, error) {
	var (
		out repo.Observation
		ok  bool
	)
	err := observe("GetObservationByTrace", func() error {
		var e error
		out, ok, e = m.inner.GetObservationByTrace(ctx, transactionID, traceID)
		return e
	})
	return out, ok, err
}

func (m *instrumentedRepo) GetLatestObservationByReason(ctx context.Context, sampleReason string) (repo.Observation, bool, error) {
	var (
		out repo.Observation
		ok  bool
	)
	err := observe("GetLatestObservationByReason", func() error {
		var e error
		out, ok, e = m.inner.GetLatestObservationByReason(ctx, sampleReason)
		return e
	})
	return out, ok, err
}

func (m *instrumentedRepo) CountObservationsByReason(ctx context.Context, transactionID int64) (map[string]int, error) {
	var out map[string]int
	err := observe("CountObservationsByReason", func() error {
		var e error
		out, e = m.inner.CountObservationsByReason(ctx, transactionID)
		return e
	})
	return out, err
}

func (m *instrumentedRepo) ListEnabledPolicies(ctx context.Context) ([]repo.Policy, error) {
	var out []repo.Policy
	err := observe("ListEnabledPolicies", func() error {
		var e error
		out, e = m.inner.ListEnabledPolicies(ctx)
		return e
	})
	return out, err
}

func (m *instrumentedRepo) ListAlertPolicies(ctx context.Context) ([]repo.Policy, error) {
	var out []repo.Policy
	err := observe("ListAlertPolicies", func() error {
		var e error
		out, e = m.inner.ListAlertPolicies(ctx)
		return e
	})
	return out, err
}

func (m *instrumentedRepo) UpsertAlertPolicy(ctx context.Context, p repo.Policy) error {
	return observe("UpsertAlertPolicy", func() error {
		return m.inner.UpsertAlertPolicy(ctx, p)
	})
}

func (m *instrumentedRepo) DeleteAlertPolicy(ctx context.Context, scope repo.PolicyScope, scopeKey string) error {
	return observe("DeleteAlertPolicy", func() error {
		return m.inner.DeleteAlertPolicy(ctx, scope, scopeKey)
	})
}

func (m *instrumentedRepo) ListServiceGuardrails(ctx context.Context) ([]repo.ServiceGuardrail, error) {
	var out []repo.ServiceGuardrail
	err := observe("ListServiceGuardrails", func() error {
		var e error
		out, e = m.inner.ListServiceGuardrails(ctx)
		return e
	})
	return out, err
}

func (m *instrumentedRepo) UpsertServiceGuardrail(ctx context.Context, g repo.ServiceGuardrail) error {
	return observe("UpsertServiceGuardrail", func() error {
		return m.inner.UpsertServiceGuardrail(ctx, g)
	})
}

func (m *instrumentedRepo) DeleteServiceGuardrail(ctx context.Context, scope repo.PolicyScope, scopeKey string) error {
	return observe("DeleteServiceGuardrail", func() error {
		return m.inner.DeleteServiceGuardrail(ctx, scope, scopeKey)
	})
}

func (m *instrumentedRepo) GetServiceProfile(ctx context.Context, namespace, service string) (repo.ServiceProfile, error) {
	var out repo.ServiceProfile
	err := observe("GetServiceProfile", func() error {
		var e error
		out, e = m.inner.GetServiceProfile(ctx, namespace, service)
		return e
	})
	return out, err
}

func (m *instrumentedRepo) RecordAnomalyOccurrences(ctx context.Context, occs []repo.AnomalyOccurrence) error {
	return observe("RecordAnomalyOccurrences", func() error {
		return m.inner.RecordAnomalyOccurrences(ctx, occs)
	})
}

func (m *instrumentedRepo) ListAnomalyIssues(ctx context.Context, filter repo.FindingListFilter) ([]repo.AnomalyIssue, error) {
	var out []repo.AnomalyIssue
	err := observe("ListAnomalyIssues", func() error {
		var e error
		out, e = m.inner.ListAnomalyIssues(ctx, filter)
		return e
	})
	return out, err
}

func (m *instrumentedRepo) ListAnomalyIssuesByTransaction(ctx context.Context, transactionID int64, filter repo.FindingListFilter) ([]repo.AnomalyIssue, error) {
	var out []repo.AnomalyIssue
	err := observe("ListAnomalyIssuesByTransaction", func() error {
		var e error
		out, e = m.inner.ListAnomalyIssuesByTransaction(ctx, transactionID, filter)
		return e
	})
	return out, err
}

func (m *instrumentedRepo) GetAnomalyIssue(ctx context.Context, transactionID int64, signature string) (repo.AnomalyIssue, bool, error) {
	var (
		out repo.AnomalyIssue
		ok  bool
	)
	err := observe("GetAnomalyIssue", func() error {
		var e error
		out, ok, e = m.inner.GetAnomalyIssue(ctx, transactionID, signature)
		return e
	})
	return out, ok, err
}

func (m *instrumentedRepo) ResolveAnomalyIssue(ctx context.Context, transactionID int64, signature string, resolution repo.AnomalyResolution) error {
	return observe("ResolveAnomalyIssue", func() error {
		return m.inner.ResolveAnomalyIssue(ctx, transactionID, signature, resolution)
	})
}

func (m *instrumentedRepo) DeleteAnomalyIssues(ctx context.Context, transactionID int64) error {
	return observe("DeleteAnomalyIssues", func() error {
		return m.inner.DeleteAnomalyIssues(ctx, transactionID)
	})
}

func (m *instrumentedRepo) GetSystemSettings(ctx context.Context) (repo.SystemSettings, error) {
	var out repo.SystemSettings
	err := observe("GetSystemSettings", func() error {
		var e error
		out, e = m.inner.GetSystemSettings(ctx)
		return e
	})
	return out, err
}

func (m *instrumentedRepo) UpsertSystemSettings(ctx context.Context, s repo.SystemSettings) error {
	return observe("UpsertSystemSettings", func() error {
		return m.inner.UpsertSystemSettings(ctx, s)
	})
}

func (m *instrumentedRepo) SetObservationRetentionDays(ctx context.Context, days int) error {
	return observe("SetObservationRetentionDays", func() error {
		return m.inner.SetObservationRetentionDays(ctx, days)
	})
}

func (m *instrumentedRepo) RecordGuardrailViolations(ctx context.Context, hits []repo.GuardrailViolationHit) error {
	return observe("RecordGuardrailViolations", func() error {
		return m.inner.RecordGuardrailViolations(ctx, hits)
	})
}

func (m *instrumentedRepo) ListGuardrailViolations(ctx context.Context, filter repo.FindingListFilter) ([]repo.GuardrailViolationGroup, error) {
	var out []repo.GuardrailViolationGroup
	err := observe("ListGuardrailViolations", func() error {
		var e error
		out, e = m.inner.ListGuardrailViolations(ctx, filter)
		return e
	})
	return out, err
}

func (m *instrumentedRepo) GetGuardrailViolation(ctx context.Context, scopeKey string, rule repo.RuleKey, offending string) (repo.GuardrailViolationGroup, bool, error) {
	var (
		out repo.GuardrailViolationGroup
		ok  bool
	)
	err := observe("GetGuardrailViolation", func() error {
		var e error
		out, ok, e = m.inner.GetGuardrailViolation(ctx, scopeKey, rule, offending)
		return e
	})
	return out, ok, err
}

func (m *instrumentedRepo) ResolveGuardrailViolation(ctx context.Context, scope repo.PolicyScope, scopeKey string, rule repo.RuleKey, offending string, resolution repo.GuardrailResolution) error {
	return observe("ResolveGuardrailViolation", func() error {
		return m.inner.ResolveGuardrailViolation(ctx, scope, scopeKey, rule, offending, resolution)
	})
}

func (m *instrumentedRepo) DeleteGuardrailViolationsForRule(ctx context.Context, rule repo.RuleKey) error {
	return observe("DeleteGuardrailViolationsForRule", func() error {
		return m.inner.DeleteGuardrailViolationsForRule(ctx, rule)
	})
}

func (m *instrumentedRepo) UpsertServiceGraphEdges(ctx context.Context, edges []repo.ServiceGraphEdge) error {
	return observe("UpsertServiceGraphEdges", func() error {
		return m.inner.UpsertServiceGraphEdges(ctx, edges)
	})
}

func (m *instrumentedRepo) QueryBlastRadius(ctx context.Context, namespace, service string, depth int) (repo.BlastRadiusSubgraph, error) {
	var out repo.BlastRadiusSubgraph
	err := observe("QueryBlastRadius", func() error {
		var e error
		out, e = m.inner.QueryBlastRadius(ctx, namespace, service, depth)
		return e
	})
	return out, err
}

func (m *instrumentedRepo) GetStorageHealth(ctx context.Context) (repo.StorageHealth, error) {
	var out repo.StorageHealth
	err := observe("GetStorageHealth", func() error {
		var e error
		out, e = m.inner.GetStorageHealth(ctx)
		return e
	})
	return out, err
}

func (m *instrumentedRepo) Ping(ctx context.Context) error {
	return observe("Ping", func() error {
		return m.inner.Ping(ctx)
	})
}

func (m *instrumentedRepo) Close() error {
	return observe("Close", func() error {
		return m.inner.Close()
	})
}
