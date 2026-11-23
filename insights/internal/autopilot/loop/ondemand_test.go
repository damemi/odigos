package loop

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/odigos-io/odigos/insights/internal/autopilot/model"
)

func TestManager_OnDemandPauseDefersTick(t *testing.T) {
	m := &Manager{}
	// analyzeTransaction reads the clock, so the pause has to start now.
	t0 := time.Now()

	until := m.NoteModelError(&model.HTTPError{StatusCode: http.StatusTooManyRequests}, t0)
	if want := t0.Add(retryBaseDelay); !until.Equal(want) {
		t.Fatalf("paused until %v, want %v", until, want)
	}
	// The tick's per-transaction gate sees the pause before it reads the store
	// or calls the model.
	outcome, _, err := m.analyzeTransaction(context.Background(), checkout, nil, transactionCallTree{}, AppEvidence{})
	if err != nil || outcome != analyzeDeferredPaused {
		t.Fatalf("analyzeTransaction = %v, %v; want deferred while paused", outcome, err)
	}

	// A second failure doubles the pause; a success after it resets the backoff,
	// so the next failure pauses for the base delay again.
	m.NoteModelError(&model.HTTPError{StatusCode: http.StatusUnauthorized}, t0)
	if got := m.ModelPausedUntil(t0); !got.Equal(t0.Add(2 * retryBaseDelay)) {
		t.Fatalf("second failure paused until %v, want %v", got, t0.Add(2*retryBaseDelay))
	}
	later := t0.Add(3 * retryBaseDelay)
	m.NoteModelSuccess(later)
	if got := m.ModelPausedUntil(later); !got.IsZero() {
		t.Fatalf("paused until %v after success, want not paused", got)
	}
	if got := m.NoteModelError(&model.HTTPError{StatusCode: http.StatusTooManyRequests}, later); !got.Equal(later.Add(retryBaseDelay)) {
		t.Fatalf("failure after success paused until %v, want %v", got, later.Add(retryBaseDelay))
	}
}

func TestNoteModelSuccess_WhilePaused(t *testing.T) {
	m := &Manager{}
	// The tick reads the wall clock, so the pause has to cover it.
	t0 := time.Now()
	until := m.NoteModelError(&model.HTTPError{StatusCode: http.StatusTooManyRequests, RetryAfter: 5 * time.Minute}, t0)

	m.NoteModelSuccess(t0.Add(time.Second))
	if got := m.ModelPausedUntil(t0.Add(time.Second)); !got.Equal(until) {
		t.Fatalf("paused until %v after a success during the pause, want %v", got, until)
	}
}

func TestNoteModelError_NotGlobal(t *testing.T) {
	for _, err := range []error{
		&model.HTTPError{StatusCode: http.StatusInternalServerError},
		&model.HTTPError{StatusCode: http.StatusBadRequest, Body: "context length"},
		model.ErrInvalidResponse,
		errors.New("connection reset"),
	} {
		m := &Manager{}
		now := time.Now()
		if got := m.NoteModelError(err, now); !got.IsZero() {
			t.Errorf("%v: paused until %v, want not paused", err, got)
		}
		if n := len(m.retries().entries); n != 0 {
			t.Errorf("%v: %d retry entries recorded, want none", err, n)
		}
	}
}

func TestLoopSuccess_KeepsInterrogationPause(t *testing.T) {
	m := &Manager{}
	t0 := time.Now()
	// An interrogation's 429 arrives while a loop call sent before it is in flight.
	until := m.NoteModelError(&model.HTTPError{StatusCode: http.StatusTooManyRequests}, t0)

	m.retries().recordSuccess(newRetryKey(checkout, "1"), t0.Add(time.Second))
	if got := m.ModelPausedUntil(t0.Add(time.Second)); !got.Equal(until) {
		t.Fatalf("paused until %v after the loop's success, want %v", got, until)
	}
}
