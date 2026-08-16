package api

import (
	"testing"
	"time"
)

// TestConformanceSince is the drift clock, and it lives in the base because re-stamping on every pass is the
// obvious implementation, looks right, and silently destroys the only thing that makes a drift report
// actionable. Two providers observe on their own schedules with their own change tests; neither may re-derive
// this rule.
func TestConformanceSince(t *testing.T) {
	first := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	later := time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		prev      Conformance
		prevSince time.Time
		next      Conformance
		want      time.Time
	}{
		{
			// The load-bearing case: the same verdict observed again days later keeps its original stamp, which
			// is what makes "drifted for three days" true rather than "drifted right now".
			name: "verdict holds",
			prev: ConformancePartial, prevSince: first, next: ConformancePartial, want: first,
		},
		{
			name: "verdict flips to conformant",
			prev: ConformancePartial, prevSince: first, next: ConformanceConformant, want: later,
		},
		{
			name: "verdict flips to drifted",
			prev: ConformanceConformant, prevSince: first, next: ConformancePartial, want: later,
		},
		{
			// A first observation has nothing to carry forward.
			name: "never observed",
			prev: ConformanceUnknown, prevSince: time.Time{}, next: ConformanceConformant, want: later,
		},
		{
			// Written before observation could date it (or by an older connector): stamped once so it becomes
			// dateable, rather than staying permanently unknown.
			name: "same verdict but no stored stamp",
			prev: ConformancePartial, prevSince: time.Time{}, next: ConformancePartial, want: later,
		},
		{
			// A read failure preserves the previous verdict, so the clock must not move either — the workload
			// has been in this state continuously, whether or not Odigos could look.
			name: "unknown holds after a failed read",
			prev: ConformanceUnknown, prevSince: first, next: ConformanceUnknown, want: first,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ConformanceSince(c.prev, c.prevSince, c.next, later); !got.Equal(c.want) {
				t.Errorf("since = %v, want %v", got, c.want)
			}
		})
	}
}

// TestConformanceSinceNormalizesToUTC: the stamp is rendered into a report as RFC3339 and compared across
// passes, so a local-zone value would read correctly and compare wrong.
func TestConformanceSinceNormalizesToUTC(t *testing.T) {
	local := time.Date(2026, 8, 4, 9, 0, 0, 0, time.FixedZone("PDT", -7*3600))
	got := ConformanceSince(ConformanceConformant, time.Time{}, ConformancePartial, local)
	if got.Location() != time.UTC {
		t.Errorf("since location = %v, want UTC", got.Location())
	}
	if !got.Equal(local) {
		t.Errorf("since = %v, want the same instant as %v", got, local)
	}
}
