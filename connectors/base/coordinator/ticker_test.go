package coordinator

import (
	"testing"
	"time"
)

// TestDeferNextTickPostponesThePeriodicPass is the behavioral half of the change, and the reason it is worth a
// timing test rather than a return-value one: the whole point is that a tick which WOULD have fired does not.
//
// Every loop in this package is both ticked and triggered, and the two loops on the discovery interval are
// kicked by each successful discovery cycle — at almost exactly the frequency they already tick at. Without the
// deferral each cycle ran the pass twice, which for observation is a second full round of provider
// control-plane reads for state that had not moved.
func TestDeferNextTickPostponesThePeriodicPass(t *testing.T) {
	const interval = 300 * time.Millisecond
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Two thirds into the period, something triggers the pass out of band. The pass has now run, so the tick
	// that was about to run it is redundant.
	time.Sleep(interval * 2 / 3)
	if got := deferNextTick(ticker, interval, interval); got != interval {
		t.Fatalf("interval = %v, want %v", got, interval)
	}

	// The tick originally due one interval from the start must not arrive: it is now a full interval from the
	// deferral instead. This window deliberately spans that original due time — without the reset the tick
	// lands inside it and this case fails, which is what makes the test discriminate at all.
	select {
	case <-ticker.C:
		t.Fatal("the periodic pass fired anyway; a triggered pass must postpone it by a full interval")
	case <-time.After(interval * 2 / 3):
	}

	// But deferring is not stopping. The tick still comes, and that is what keeps a connector whose discovery
	// is FAILING — runDiscovery returns early without firing any trigger — reconciling, observing and adopting
	// on the timer alone.
	select {
	case <-ticker.C:
	case <-time.After(interval):
		t.Fatal("the periodic pass never fired again; deferring must not stop the loop")
	}
}

// TestDeferNextTickPicksUpAConfigChange pins the second job this shares with resetTicker: a triggered pass is
// as good a moment as a tick to notice that the operator changed the interval, so a loop picks one up without
// waiting for its (possibly very long) next tick.
func TestDeferNextTickPicksUpAConfigChange(t *testing.T) {
	cases := []struct {
		name          string
		current, want time.Duration
		expect        time.Duration
	}{
		{
			name:    "a changed interval takes effect",
			current: time.Minute, want: 30 * time.Second, expect: 30 * time.Second,
		},
		{
			name:    "an unchanged interval is kept",
			current: time.Minute, want: time.Minute, expect: time.Minute,
		},
		{
			// An absent/zero configured interval must never reach Ticker.Reset, which panics on a
			// non-positive duration — the loop keeps the interval already in force.
			name:    "an unset interval keeps the one in force",
			current: time.Minute, want: 0, expect: time.Minute,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ticker := time.NewTicker(tc.current)
			defer ticker.Stop()
			if got := deferNextTick(ticker, tc.current, tc.want); got != tc.expect {
				t.Fatalf("interval = %v, want %v", got, tc.expect)
			}
		})
	}
}
