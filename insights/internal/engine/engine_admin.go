package engine

import (
	"context"
	"encoding/json"
	"fmt"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

// AssessAnomalyIssue recomputes an issue's risk breakdown from its stored
// evidence and the transaction's currently-resolved alert policy, returning the
// assessment and the policy's fire threshold. Nothing is persisted: the
// investigate view calls this so the score/signals reflect the live policy
// (weights, enricher lists) rather than a stale snapshot. When no enabled policy
// resolves (e.g. it was disabled after the issue fired) it falls back to catalog
// defaults so the breakdown still renders.
func (e *Engine) AssessAnomalyIssue(_ context.Context, issue repo.AnomalyIssue) (risk.Assessment, int, error) {
	var evidence map[string]json.RawMessage
	if len(issue.Evidence) > 0 {
		if err := json.Unmarshal(issue.Evidence, &evidence); err != nil {
			return risk.Assessment{}, 0, fmt.Errorf("decode issue evidence: %w", err)
		}
	}
	tx := repo.Transaction{
		ID: issue.TransactionID, Service: issue.Service,
		Namespace: issue.Namespace, Operation: issue.Operation, Kind: issue.Kind,
	}
	policy, ok := e.resolveAlertPolicy(tx)
	threshold := repo.DefaultFireAtScore
	if ok {
		threshold = policy.FireAtScore
	}
	return e.assessRisk(issue.TriggeredClasses, evidence, policy), threshold, nil
}

// Admin actions are rare, operator-driven, cold-path controls. Each evicts the
// affected transaction from the in-memory store so the next trace reloads the
// changed baseline instead of a stale cached copy.

// ResetBaselineClass wipes one class so it re-learns from scratch.
func (e *Engine) ResetBaselineClass(ctx context.Context, transactionID int64, class repo.DeviationClass) error {
	// Evict, delete, evict again: the second eviction drops anything a concurrent
	// trace reloaded and re-flushed while the delete was in flight, which would
	// otherwise resurrect the reset baseline.
	e.store.removeEntry(transactionID)
	if err := e.repo.ResetBaselineClass(ctx, transactionID, class); err != nil {
		return err
	}
	e.store.removeEntry(transactionID)
	return nil
}

// PromoteBaselineClass flips one class to promoted (operator force-promote) and,
// when every scored class of the transaction is promoted, may auto-enable the
// service's allowed_transactions guardrail.
func (e *Engine) PromoteBaselineClass(ctx context.Context, transactionID int64, class repo.DeviationClass) error {
	if err := e.repo.PromoteBaselineClass(ctx, transactionID, class); err != nil {
		return err
	}
	e.store.removeEntry(transactionID)

	tx, ok, err := e.repo.GetTransactionByID(ctx, transactionID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	rows, err := e.repo.GetBaselineForTransaction(ctx, transactionID)
	if err != nil {
		return err
	}
	promoted := map[repo.DeviationClass]bool{}
	for _, row := range rows {
		promoted[row.Class] = row.Promoted
	}
	// The class we just wrote may not be visible in ClickHouse yet.
	promoted[class] = true
	for _, sc := range ScoredClasses() {
		if !promoted[sc] {
			return nil
		}
	}
	return e.maybeAutoEnableTransactionGuardrail(ctx, tx.Namespace, tx.Service, transactionID)
}

// ResetTransactionBaselines wipes every class of a transaction.
func (e *Engine) ResetTransactionBaselines(ctx context.Context, transactionID int64) error {
	e.store.removeEntry(transactionID)
	if err := e.repo.ResetTransactionBaselines(ctx, transactionID); err != nil {
		return err
	}
	e.store.removeEntry(transactionID)
	// Drop findings scored against the now-wiped baseline: they'd otherwise
	// contradict the fresh baseline in the feed. Best-effort - if the behaviour
	// is still anomalous it re-fires against the new baseline as a fresh issue.
	if err := e.repo.DeleteAnomalyIssues(ctx, transactionID); err != nil {
		e.warn("delete anomaly issues on re-learn", err)
	}
	return nil
}

// DeleteTransaction permanently removes a transaction and related state, and
// evicts it from the in-memory store so a concurrent trace cannot resurrect it
// mid-delete. Reloads alert/learning snapshots so txn-scoped overrides are gone.
func (e *Engine) DeleteTransaction(ctx context.Context, transactionID int64) error {
	e.store.removeEntry(transactionID)
	if err := e.repo.DeleteTransaction(ctx, transactionID); err != nil {
		return err
	}
	e.store.removeEntry(transactionID)
	if err := e.ReloadLearningConfig(ctx); err != nil {
		e.warn("reload learning config after delete transaction", err)
	}
	if err := e.ReloadAlertConfig(ctx); err != nil {
		e.warn("reload alert config after delete transaction", err)
	}
	return nil
}

// DeleteAllTransactions wipes every transaction (DB + in-memory store) and
// reloads txn-scoped policy snapshots. Clears allowed_transactions guardrail
// rules so stale identities cannot flag relearned traffic. Cold path only.
func (e *Engine) DeleteAllTransactions(ctx context.Context) error {
	e.store.clearAll()
	if err := e.repo.DeleteAllTransactions(ctx); err != nil {
		return err
	}
	e.store.clearAll()
	e.anomalies.clearSamples()
	if err := e.resetInventoryGuardrails(ctx); err != nil {
		return err
	}
	if err := e.ReloadLearningConfig(ctx); err != nil {
		e.warn("reload learning config after delete all transactions", err)
	}
	if err := e.ReloadAlertConfig(ctx); err != nil {
		e.warn("reload alert config after delete all transactions", err)
	}
	return nil
}

// AcceptAnomalyIssue folds the issue's offending trace into the baseline so its
// once-novel behaviour becomes normal and stops alerting. Only the classes that
// triggered the issue are widened, and they stay promoted (not sent back to
// learning). The issue is then marked accepted (sticky). The trace folded in is
// the newest stored sample for the signature - LastTraceID may point at a firing
// the throttled sampler never captured, so we take what is actually on disk.
func (e *Engine) AcceptAnomalyIssue(ctx context.Context, transactionID int64, signature string) error {
	issue, ok, err := e.repo.GetAnomalyIssue(ctx, transactionID, signature)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("anomaly issue %d/%s not found", transactionID, signature)
	}

	samples, err := e.repo.ListObservationsByTransaction(ctx, transactionID, repo.AnomalySampleReason(signature), 1)
	if err != nil {
		return err
	}
	if len(samples) == 0 {
		return fmt.Errorf("anomaly issue %d/%s: no stored sample trace, cannot accept", transactionID, signature)
	}
	full, ok, err := e.repo.GetObservationByTrace(ctx, transactionID, samples[0].TraceID)
	if err != nil {
		return err
	}
	if !ok || len(full.RawOTLP) == 0 {
		return fmt.Errorf("anomaly issue %d/%s: no stored sample trace, cannot accept", transactionID, signature)
	}
	t, err := traceFromOTLP(full.RawOTLP, full.TraceID)
	if err != nil {
		return err
	}

	// Each class's contribution is the trace folded into an empty value, the
	// per-class shape ApplyBaselineDeltas expects.
	fresh, err := e.repo.GetBaselineForTransaction(ctx, transactionID)
	if err != nil {
		return err
	}
	freshByClass := make(map[repo.DeviationClass]repo.BaselineClass, len(fresh))
	for _, b := range fresh {
		freshByClass[b.Class] = b
	}

	now := e.clock()
	var deltas []repo.BaselineDelta
	for _, class := range issue.TriggeredClasses {
		sc := e.scorerForClass(class)
		if sc == nil {
			continue
		}
		contribution, mErr := sc.Merge(t, json.RawMessage(`{}`))
		if mErr != nil {
			e.warn(fmt.Sprintf("accept anomaly: merge %s", class), mErr)
			continue
		}
		cur := freshByClass[class]
		deltas = append(deltas, repo.BaselineDelta{
			TransactionID:                transactionID,
			Class:                        class,
			Contribution:                 contribution,
			CountDelta:                   1,
			Promote:                      true,
			DataSchemaVersion:            cur.DataSchemaVersion,
			LearningStartedAt:            cur.LearningStartedAt,
			LastChangedAt:                now,
			ObservationCountAtLastChange: cur.ObservationCount + 1,
		})
	}

	if len(deltas) > 0 {
		if _, err := e.repo.ApplyBaselineDeltas(ctx, deltas, e.store.merger); err != nil {
			return err
		}
		e.store.removeEntry(transactionID)
	}

	return e.repo.ResolveAnomalyIssue(ctx, transactionID, signature, repo.AnomalyResolveAccept)
}

// DismissAnomalyIssue closes an issue without widening the baseline. The decision
// is sticky: the issue leaves the open tab but its count keeps rising if the
// pattern still fires, so a known-but-unacted deviation stays visible.
func (e *Engine) DismissAnomalyIssue(ctx context.Context, transactionID int64, signature string) error {
	return e.repo.ResolveAnomalyIssue(ctx, transactionID, signature, repo.AnomalyResolveDismiss)
}

// ReopenAnomalyIssue moves a dismissed issue back to open for re-triage.
func (e *Engine) ReopenAnomalyIssue(ctx context.Context, transactionID int64, signature string) error {
	return e.repo.ResolveAnomalyIssue(ctx, transactionID, signature, repo.AnomalyResolveReopen)
}

// Reload* refresh an in-memory policy snapshot from the DB so a UI edit takes
// effect immediately instead of waiting for the periodic reloader.

func (e *Engine) ReloadLearningConfig(ctx context.Context) error {
	if e.learning == nil {
		return nil
	}
	return e.learning.Reload(ctx, e.repo)
}

func (e *Engine) ReloadAlertConfig(ctx context.Context) error {
	if e.alerts == nil {
		return nil
	}
	return e.alerts.Reload(ctx, e.repo)
}

// ReloadGuardrailConfig is nil-safe: an engine with no guardrail snapshot is a no-op.
func (e *Engine) ReloadGuardrailConfig(ctx context.Context) error {
	return e.guardrails.reload(ctx, e.repo)
}

func (e *Engine) ReloadSystemConfig(ctx context.Context) error {
	if e.system == nil {
		e.system = newSystemConfig(repo.DefaultSystemSettings())
	}
	prev := e.system.Get()
	if err := e.system.Reload(ctx, e.repo); err != nil {
		return err
	}
	next := e.system.Get()
	e.applyCapacity(next)
	// Drop resident baselines when inventory epoch advances (wipe) or identity
	// dimensions change so other replicas do not keep learning/flushing stale
	// transaction ids after a settings change.
	epochAdvanced := next.TransactionInventoryEpoch != prev.TransactionInventoryEpoch
	dimsChanged := !repo.IdentityDimensionsEqual(prev.TransactionIdentityDimensions, next.TransactionIdentityDimensions)
	if epochAdvanced || dimsChanged {
		e.store.clearAll()
		e.anomalies.clearSamples()
	}
	// Epoch bump means another replica wiped transactions and cleared the
	// allowed_transactions rules. Pick the cleared rules up now instead of
	// waiting for the guardrail reloader's own tick, so this replica stops
	// policing a relearning service sooner. Correctness does not depend on the
	// timing: the identity fingerprint keeps a stale allowlist from firing (see
	// forbiddenTransactions) and the inventory epoch keeps stale counts from
	// being written (see guardrailBuffer.drain).
	if epochAdvanced {
		if err := e.ReloadGuardrailConfig(ctx); err != nil {
			e.warn("reload guardrail config after inventory epoch change", err)
		}
	}
	return nil
}

func (e *Engine) SystemSettings() repo.SystemSettings {
	if e.system == nil {
		return repo.DefaultSystemSettings()
	}
	return e.system.Get()
}

// traceFromOTLP rebuilds the engine's Trace for traceID from stored OTLP bytes.
func traceFromOTLP(raw []byte, traceID string) (*Trace, error) {
	var td tracepb.TracesData
	if err := proto.Unmarshal(raw, &td); err != nil {
		return nil, fmt.Errorf("decode stored trace: %w", err)
	}
	for _, t := range decodeTraceTrees(&td) {
		if t.TraceID == traceID {
			return t, nil
		}
	}
	return nil, fmt.Errorf("stored trace %s has no rootable spans", traceID)
}
