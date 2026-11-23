package autopilot

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	commonlogger "github.com/odigos-io/odigos/common/logger"

	"github.com/odigos-io/odigos/insights/internal/interrogation"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

type stubRepo struct{ repo.Repository }

// stubSupervisor runs until ctx ends, or returns at once when quick is set.
type stubSupervisor struct {
	quick   bool
	running *atomic.Int32
}

func (s stubSupervisor) Run(ctx context.Context) {
	s.running.Add(1)
	defer s.running.Add(-1)
	if !s.quick {
		<-ctx.Done()
	}
}

// runInterrogations runs beside the loop in Run's errgroup (autopilot.Run
// itself needs a cluster). Nothing it meets may stop the loop.
func TestAutopilotRun_InterrogationSetupFails_LoopStillRuns(t *testing.T) {
	var running atomic.Int32
	tests := []struct {
		name   string
		apiKey string
		setup  func(context.Context) (supervisor, error)
		// calls is how many setups happen before the loop is checked.
		calls int32
	}{
		{name: "no session store", apiKey: "key", calls: 1,
			setup: interrogationSetup(Options{Repo: stubRepo{}, Sessions: func() *interrogation.Sessions { return nil }}, nil, nil)},
		{name: "failing setup is retried", apiKey: "key", calls: 2,
			setup: func(context.Context) (supervisor, error) { return nil, errors.New("connection refused") }},
		{name: "setup panics", apiKey: "key", calls: 1,
			setup: func(context.Context) (supervisor, error) { panic("nil map") }},
		{name: "supervisor returns early", apiKey: "key", calls: 2,
			setup: func(context.Context) (supervisor, error) { return stubSupervisor{quick: true, running: &running}, nil }},
		{name: "supervisor runs", apiKey: "key", calls: 1,
			setup: func(context.Context) (supervisor, error) { return stubSupervisor{running: &running}, nil }},
		{name: "empty litellm key", apiKey: " ", calls: 0,
			setup: func(context.Context) (supervisor, error) { return stubSupervisor{running: &running}, nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			g, groupCtx := errgroup.WithContext(ctx)

			var calls atomic.Int32
			returned := make(chan error, 1)
			g.Go(func() error {
				err := runInterrogations(groupCtx, commonlogger.LoggerCompat(), tc.apiKey, func(ctx context.Context) (supervisor, error) {
					calls.Add(1)
					return tc.setup(ctx)
				})
				returned <- err
				return err
			})
			var ticks atomic.Int32
			g.Go(func() error {
				ticker := time.NewTicker(time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-groupCtx.Done():
						return nil
					case <-ticker.C:
						ticks.Add(1)
					}
				}
			})

			if !eventually(10*time.Second, func() bool { return calls.Load() >= tc.calls }) {
				t.Fatalf("%d setups, want %d", calls.Load(), tc.calls)
			}
			if tc.calls == 0 {
				select {
				case err := <-returned:
					if err != nil {
						t.Fatalf("returned %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("did not return without a key")
				}
				if calls.Load() != 0 {
					t.Fatal("the supervisor was set up without a key")
				}
			}
			seen := ticks.Load()
			if !eventually(5*time.Second, func() bool { return ticks.Load() > seen }) || groupCtx.Err() != nil {
				t.Fatalf("the loop stopped: %v", groupCtx.Err())
			}

			cancel()
			done := make(chan error, 1)
			go func() { done <- g.Wait() }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("errgroup: %v", err)
				}
			case <-time.After(time.Second):
				// Shorter than the retry backoff: a wait in it ends with ctx.
				t.Fatal("did not return when the context ended")
			}
			if n := running.Load(); n != 0 {
				t.Errorf("%d supervisors still running", n)
			}
		})
	}
}

func eventually(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
	return true
}
