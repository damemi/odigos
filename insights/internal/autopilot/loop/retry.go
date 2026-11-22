package loop

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/odigos-io/odigos/insights/internal/autopilot/model"
	"github.com/odigos-io/odigos/insights/internal/autopilot/store"
)

const (
	retryBaseDelay = tickInterval
	retryMaxDelay  = time.Hour
	// maxPermanentAttempts is how many times one exact request may fail
	// deterministically before the loop stops sending it. A new edge or a new
	// revision changes the request and starts over.
	maxPermanentAttempts = 3
)

// failureClass says how a failed analysis is retried.
type failureClass int

const (
	// failureTransient retries the transaction with backoff.
	failureTransient failureClass = iota
	// failurePermanent retries with backoff, up to maxPermanentAttempts for
	// the same request.
	failurePermanent
	// failureGlobal pauses every model call (quota, budget, auth), so
	// requests do not pile up against a provider that refuses all of them.
	failureGlobal
)

func (c failureClass) analysisStatus() string {
	if c == failurePermanent {
		return store.AnalysisFailedPermanent
	}
	return store.AnalysisFailedTransient
}

// classifyModelError maps a CompleteAnalysis error to a failure class and the
// provider's requested wait, if any.
func classifyModelError(err error) (failureClass, time.Duration) {
	var httpErr *model.HTTPError
	if errors.As(err, &httpErr) {
		switch {
		case httpErr.QuotaExceeded():
			return failureGlobal, httpErr.RetryAfter
		case httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden:
			return failureGlobal, httpErr.RetryAfter
		case httpErr.StatusCode == http.StatusRequestTimeout || httpErr.StatusCode == http.StatusConflict:
			return failureTransient, httpErr.RetryAfter
		case httpErr.StatusCode >= 400 && httpErr.StatusCode < 500:
			// Other client errors (context length, bad schema) repeat for the same input.
			return failurePermanent, 0
		}
		return failureTransient, httpErr.RetryAfter
	}
	if errors.Is(err, model.ErrInvalidResponse) {
		return failurePermanent, 0
	}
	return failureTransient, 0
}

// isShutdown reports whether err came from the loop's own context ending,
// which is not the transaction's fault and is not recorded as a failure.
func isShutdown(ctx context.Context, err error) bool {
	return ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
}

// retryKey identifies a source+transaction. It keeps only the workload
// identity because SourceKey carries an attributes map and is not comparable.
type retryKey struct {
	namespace     string
	workloadKind  string
	workloadName  string
	containerName string
	transactionID string
}

func newRetryKey(source store.SourceKey, transactionID string) retryKey {
	return retryKey{
		namespace:     source.Namespace,
		workloadKind:  source.WorkloadKind,
		workloadName:  source.WorkloadName,
		containerName: source.ContainerName,
		transactionID: transactionID,
	}
}

type retryEntry struct {
	failures    int
	nextAttempt time.Time
	// inputHash is the request that last failed permanently, and
	// permanentFailures counts consecutive permanent failures of it.
	inputHash         string
	permanentFailures int
}

// retryTracker holds backoff state for failed analyses. It lives in memory on
// the leader: losing it on restart only means one early retry, which is safe
// because nothing is acknowledged until a call succeeds.
type retryTracker struct {
	mu             sync.Mutex
	entries        map[retryKey]*retryEntry
	pausedUntil    time.Time
	globalFailures int
}

func newRetryTracker() *retryTracker {
	return &retryTracker{entries: map[retryKey]*retryEntry{}}
}

// pausedUntilTime returns when model calls resume, or zero when not paused.
func (t *retryTracker) pausedUntilTime(now time.Time) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	if now.Before(t.pausedUntil) {
		return t.pausedUntil
	}
	return time.Time{}
}

// backingOff reports whether k must wait before its next attempt.
func (t *retryTracker) backingOff(k retryKey, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[k]
	return ok && now.Before(e.nextAttempt)
}

// exhausted reports whether this exact request has already failed
// permanently maxPermanentAttempts times.
func (t *retryTracker) exhausted(k retryKey, inputHash string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[k]
	return ok && e.inputHash == inputHash && e.permanentFailures >= maxPermanentAttempts
}

// recordFailure schedules the next attempt for k, and for failureGlobal also
// pauses every model call. retryAfter, when longer, overrides the backoff.
func (t *retryTracker) recordFailure(k retryKey, inputHash string, class failureClass, retryAfter time.Duration, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if class == failureGlobal {
		t.globalFailures++
		t.pausedUntil = now.Add(max(backoffDelay(t.globalFailures), retryAfter))
		// The transaction itself is not at fault; it runs when the pause ends.
		return
	}
	e, ok := t.entries[k]
	if !ok {
		e = &retryEntry{}
		t.entries[k] = e
	}
	e.failures++
	e.nextAttempt = now.Add(max(backoffDelay(e.failures), retryAfter))
	if class == failurePermanent {
		if e.inputHash != inputHash {
			e.inputHash = inputHash
			e.permanentFailures = 0
		}
		e.permanentFailures++
	}
}

// recordSuccess clears k's backoff and any global pause.
func (t *retryTracker) recordSuccess(k retryKey) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, k)
	t.globalFailures = 0
	t.pausedUntil = time.Time{}
}

// forgetSource drops state for every transaction of a deleted workload.
func (t *retryTracker) forgetSource(source store.SourceKey) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k := range t.entries {
		if k.namespace == source.Namespace &&
			k.workloadKind == source.WorkloadKind &&
			k.workloadName == source.WorkloadName {
			delete(t.entries, k)
		}
	}
}

// backoffDelay doubles from retryBaseDelay per consecutive failure, capped at
// retryMaxDelay.
func backoffDelay(failures int) time.Duration {
	d := retryBaseDelay
	for i := 1; i < failures && d < retryMaxDelay; i++ {
		d *= 2
	}
	return min(d, retryMaxDelay)
}
