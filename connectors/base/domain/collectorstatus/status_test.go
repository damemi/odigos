package collectorstatus

import "testing"

// TestStatusForMapsOnlyWhatSeverityPromises: Info and Pending are message-only. A Collector that is
// deploying normally, and saying so, must not be shown as faulty — that is the difference between a
// report somebody reads and one they learn to ignore.
func TestStatusForMapsOnlyWhatSeverityPromises(t *testing.T) {
	cases := []struct {
		name     string
		problems []Problem
		base     Status
		want     Status
	}{
		{name: "no problems keep the base", base: StatusDeploying, want: StatusDeploying},
		{
			name:     "info does not change the status",
			problems: []Problem{{Severity: SeverityInfo}},
			base:     StatusReady, want: StatusReady,
		},
		{
			// Pending means "not usable yet, nothing to fix, clears on its own" — which the base already
			// says better, because the base knows what it is waiting for.
			name:     "pending does not change the status",
			problems: []Problem{{Severity: SeverityPending}},
			base:     StatusAwaitingCustomer, want: StatusAwaitingCustomer,
		},
		{
			name:     "degraded escalates",
			problems: []Problem{{Severity: SeverityInfo}, {Severity: SeverityDegraded}},
			base:     StatusReady, want: StatusDegraded,
		},
		{
			name:     "error wins over degraded whatever the order",
			problems: []Problem{{Severity: SeverityDegraded}, {Severity: SeverityError}},
			base:     StatusReady, want: StatusError,
		},
		{
			name:     "error wins when it comes first too",
			problems: []Problem{{Severity: SeverityError}, {Severity: SeverityDegraded}},
			base:     StatusReady, want: StatusError,
		},
		{
			// Problems only make things worse. A Degraded problem on a Collector that has not been built
			// yet must not report it as running.
			name:     "a problem never improves the base",
			problems: []Problem{{Severity: SeverityDegraded}},
			base:     StatusPending, want: StatusDegraded,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StatusFor(tc.problems, tc.base); got != tc.want {
				t.Errorf("StatusFor = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAwaitingCustomerIsNeverDerived: it means "only the customer can move this forward", which no
// severity expresses. Deriving it from a problem would report a Collector as waiting on a customer who
// has nothing to do.
func TestAwaitingCustomerIsNeverDerived(t *testing.T) {
	every := []Problem{
		{Severity: SeverityInfo}, {Severity: SeverityPending},
		{Severity: SeverityDegraded}, {Severity: SeverityError},
	}
	for _, p := range every {
		if got := StatusFor([]Problem{p}, StatusReady); got == StatusAwaitingCustomer {
			t.Errorf("severity %q derived AwaitingCustomer", p.Severity)
		}
	}
}
