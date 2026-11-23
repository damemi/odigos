package loop

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/odigos-io/odigos/insights/internal/autopilot/schema"
	"github.com/odigos-io/odigos/insights/internal/autopilot/store"
)

// What on-demand interrogations (internal/interrogation) use from the loop.
// The loop's own identifiers are not renamed, so this file only exports them.

type (
	CustomProbe         = customProbe
	TransactionCallTree = transactionCallTree
)

// HashEvidence fingerprints application call-graph edges.
func HashEvidence(ev AppEvidence) string { return hashEvidence(ev) }

// BuildFullAnalysisRequest builds the loop's full-graph request for one transaction.
func BuildFullAnalysisRequest(source store.SourceKey, probes []CustomProbe, tx TransactionCallTree, ev AppEvidence) schema.AnalysisRequest {
	return buildFullAnalysisRequest(source, probes, tx, ev)
}

// ProbeTarget is a recommendation's probe identity, as rules and the store key it.
func ProbeTarget(rec schema.Recommendation) string { return probeTarget(rec) }

// AnalysisRevision is the loop's analysis revision for a model alias.
func AnalysisRevision(modelName string) string { return analysisRevision(modelName) }

// Instrumented reports whether the workload still has an InstrumentationConfig.
func (m *Manager) Instrumented(ctx context.Context, source store.SourceKey) (bool, error) {
	if _, err := instrumentationConfigForSource(ctx, m.Client, source); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ModelPausedUntil returns when model calls resume, or zero when not paused.
func (m *Manager) ModelPausedUntil(now time.Time) time.Time {
	return m.retries().pausedUntilTime(now)
}

// NoteModelError records a model failure seen outside the tick. A quota,
// budget or auth failure pauses every model call, the loop's included; any
// other failure is the caller's to retry. It returns when calls resume, or zero.
func (m *Manager) NoteModelError(err error, now time.Time) time.Time {
	if class, retryAfter := classifyModelError(err); class == failureGlobal {
		// recordFailure returns before it touches per-key entries for this
		// class, so the zero key gets no entry.
		m.retries().recordFailure(retryKey{}, "", failureGlobal, retryAfter, now)
	}
	return m.retries().pausedUntilTime(now)
}

// NoteModelSuccess clears the pause backoff after a successful call made
// outside the tick. It does nothing while a pause is in force: that call was
// sent before the pause began.
func (m *Manager) NoteModelSuccess(now time.Time) {
	m.retries().recordSuccess(retryKey{}, now)
}
