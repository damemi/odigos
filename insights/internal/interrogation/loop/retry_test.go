package loop

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/odigos-io/odigos/insights/internal/interrogation/db/clickhouse"
	"github.com/odigos-io/odigos/insights/internal/interrogation/model"
)

func TestClassifyModelError(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		class      failureClass
		retryAfter time.Duration
	}{
		{"rate limit", &model.HTTPError{StatusCode: 429, RetryAfter: 30 * time.Second}, failureGlobal, 30 * time.Second},
		{"budget", &model.HTTPError{StatusCode: 400, Body: `{"error":{"type":"budget_exceeded"}}`}, failureGlobal, 0},
		{"auth", fmt.Errorf("wrapped: %w", &model.HTTPError{StatusCode: 401}), failureGlobal, 0},
		{"server", &model.HTTPError{StatusCode: 503}, failureTransient, 0},
		{"timeout status", &model.HTTPError{StatusCode: 408}, failureTransient, 0},
		{"bad request", &model.HTTPError{StatusCode: 400, Body: "context length exceeded"}, failurePermanent, 0},
		{"invalid response", fmt.Errorf("%w: empty content", model.ErrInvalidResponse), failurePermanent, 0},
		{"network", errors.New("dial tcp: connection refused"), failureTransient, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			class, retryAfter := classifyModelError(tc.err)
			if class != tc.class || retryAfter != tc.retryAfter {
				t.Fatalf("got (%v, %v), want (%v, %v)", class, retryAfter, tc.class, tc.retryAfter)
			}
		})
	}
}

func TestIsShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	if isShutdown(ctx, context.Canceled) {
		t.Fatal("live ctx is not shutdown")
	}
	cancel()
	if !isShutdown(ctx, fmt.Errorf("post: %w", context.Canceled)) {
		t.Fatal("cancelled ctx with canceled error is shutdown")
	}
}

var testKey = newRetryKey(
	clickhouse.SourceKey{Namespace: "ns", WorkloadKind: "Deployment", WorkloadName: "app", ContainerName: "c"},
	"tx",
)

func TestRetryTrackerBackoff(t *testing.T) {
	tr := newRetryTracker()
	now := time.Unix(0, 0)
	if tr.backingOff(testKey, now) {
		t.Fatal("no failures yet")
	}
	tr.recordFailure(testKey, "h", failureTransient, 0, now)
	if !tr.backingOff(testKey, now.Add(retryBaseDelay-time.Second)) {
		t.Fatal("should back off within first delay")
	}
	if tr.backingOff(testKey, now.Add(retryBaseDelay)) {
		t.Fatal("first delay elapsed")
	}
	tr.recordFailure(testKey, "h", failureTransient, 0, now)
	if !tr.backingOff(testKey, now.Add(2*retryBaseDelay-time.Second)) {
		t.Fatal("second failure should double the delay")
	}
	tr.recordFailure(testKey, "h", failureTransient, 3*time.Hour, now)
	if !tr.backingOff(testKey, now.Add(2*time.Hour)) {
		t.Fatal("retry-after longer than backoff should win")
	}
	tr.recordSuccess(testKey)
	if tr.backingOff(testKey, now) {
		t.Fatal("success clears backoff")
	}
}

func TestRetryTrackerExhaustedPerInput(t *testing.T) {
	tr := newRetryTracker()
	now := time.Unix(0, 0)
	for i := 0; i < maxPermanentAttempts; i++ {
		if tr.exhausted(testKey, "h1") {
			t.Fatalf("exhausted after %d attempts", i)
		}
		tr.recordFailure(testKey, "h1", failurePermanent, 0, now)
	}
	if !tr.exhausted(testKey, "h1") {
		t.Fatal("same input should be exhausted")
	}
	if tr.exhausted(testKey, "h2") {
		t.Fatal("new input (new edges or revision) must be retried")
	}
	tr.recordFailure(testKey, "h2", failurePermanent, 0, now)
	if tr.exhausted(testKey, "h1") || tr.exhausted(testKey, "h2") {
		t.Fatal("count restarts for the new input")
	}
}

func TestRetryTrackerGlobalPause(t *testing.T) {
	tr := newRetryTracker()
	now := time.Unix(0, 0)
	tr.recordFailure(testKey, "h", failureGlobal, 0, now)
	if tr.pausedUntilTime(now).IsZero() {
		t.Fatal("global failure should pause")
	}
	if tr.backingOff(testKey, now) {
		t.Fatal("global failure should not back off the transaction itself")
	}
	if !tr.pausedUntilTime(now.Add(retryBaseDelay)).IsZero() {
		t.Fatal("pause should end after the first delay")
	}
	tr.recordFailure(testKey, "h", failureGlobal, 0, now)
	if tr.pausedUntilTime(now.Add(2*retryBaseDelay - time.Second)).IsZero() {
		t.Fatal("repeated global failures should double the pause")
	}
	tr.recordSuccess(testKey)
	if !tr.pausedUntilTime(now).IsZero() {
		t.Fatal("success clears the pause")
	}
}

func TestRetryTrackerForgetSource(t *testing.T) {
	tr := newRetryTracker()
	now := time.Unix(0, 0)
	tr.recordFailure(testKey, "h", failureTransient, 0, now)
	other := testKey
	other.workloadName = "other"
	tr.recordFailure(other, "h", failureTransient, 0, now)
	tr.forgetSource(clickhouse.SourceKey{Namespace: "ns", WorkloadKind: "Deployment", WorkloadName: "app", ContainerName: "c"})
	if tr.backingOff(testKey, now) {
		t.Fatal("forgotten source should not back off")
	}
	if !tr.backingOff(other, now) {
		t.Fatal("other source keeps its state")
	}
}

func TestBackoffDelayCaps(t *testing.T) {
	if got := backoffDelay(100); got != retryMaxDelay {
		t.Fatalf("backoffDelay(100) = %v, want %v", got, retryMaxDelay)
	}
}
