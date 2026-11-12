package collectorsettings

import (
	"strings"
	"testing"
)

// auto is a well-formed automatic policy, for tests that break one field of it at a time.
func auto() Scaling {
	return Scaling{
		Mode: ScalingAuto, MinCount: 2, MaxCount: 10,
		TargetCPUPercent: 70, TargetMemoryPercent: 75,
	}
}

// TestAWellFormedPolicyIsCorrectedInNoWay: the steady state. A configuration that is already usable has
// to resolve to itself and report nothing — a resolver that "corrects" a valid config puts a standing
// Degraded on a Collector that is doing exactly what it was told.
func TestAWellFormedPolicyIsCorrectedInNoWay(t *testing.T) {
	for name, in := range map[string]Scaling{
		"manual": {Mode: ScalingManual, DesiredCount: 3},
		"auto":   auto(),
		// Deliberately at the floor, and deliberately not corrected: one task is a legitimate choice on
		// a network whose telemetry is not precious. It is reported through SingleTask, not clamped.
		"manual at the floor": {Mode: ScalingManual, DesiredCount: 1},
		"auto at the floor":   {Mode: ScalingAuto, MinCount: 1, MaxCount: 4, TargetCPUPercent: 70, TargetMemoryPercent: 75},
		// min == max is a fixed count expressed as autoscaling. AWS honours it, so Odigos does not
		// second-guess it.
		"auto pinned": {Mode: ScalingAuto, MinCount: 4, MaxCount: 4, TargetCPUPercent: 70, TargetMemoryPercent: 75},
	} {
		t.Run(name, func(t *testing.T) {
			got := in.Resolve()
			if len(got.Corrections) != 0 {
				t.Fatalf("corrections on a valid policy: %v", got.Corrections)
			}
			if !got.Known() {
				t.Fatal("a valid policy resolved to an unknown mode")
			}
		})
	}
}

// TestAnUnrecognisedModeDrivesNothing is the one that matters most here, because the failure it
// prevents is silent and destructive: "automatic" is the obvious typo for "auto", and a resolver that
// treated an unknown mode as manual would deregister the scalable target a customer had working and
// pin the Collector to a fixed count, on a config change that looks like a no-op.
func TestAnUnrecognisedModeDrivesNothing(t *testing.T) {
	for _, mode := range []string{"automatic", "Auto", "AUTO", "on", "manual ", ""} {
		got := Scaling{Mode: mode, DesiredCount: 3, MinCount: 2, MaxCount: 10}.Resolve()
		if mode == "manual " {
			// Whitespace is trimmed: a trailing space in a JSON config value is not a different mode.
			if !got.Manual() {
				t.Errorf("mode %q resolved to %q, want manual after trimming", mode, got.Mode)
			}
			continue
		}
		if got.Known() || got.Manual() || got.Automatic() {
			t.Errorf("mode %q is known/manual/automatic: %+v", mode, got)
		}
		// The raw value survives so a report can name it. A blanked mode would produce a message
		// telling the customer their mode is "" when they can plainly see what they typed.
		if got.Mode != strings.TrimSpace(mode) {
			t.Errorf("mode %q was rewritten to %q", mode, got.Mode)
		}
	}
}

// TestAnUnusableCountNeverStartsAServiceWithNoTasks: StartingCount is the number a create call carries,
// so it has to be usable in every mode — including the unknown one, where nothing else is resolved.
func TestAnUnusableCountNeverStartsAServiceWithNoTasks(t *testing.T) {
	for name, in := range map[string]Scaling{
		"manual with no count":    {Mode: ScalingManual},
		"manual with zero":        {Mode: ScalingManual, DesiredCount: 0},
		"manual with a negative":  {Mode: ScalingManual, DesiredCount: -2},
		"an unknown mode":         {Mode: "automatic"},
		"a zero value altogether": {},
	} {
		t.Run(name, func(t *testing.T) {
			got := in.Resolve()
			if got.StartingCount() < ReplicaFloor {
				t.Fatalf("starting count = %d, below the floor of %d: a service created with no tasks is an "+
					"endpoint that drops every connection", got.StartingCount(), ReplicaFloor)
			}
			if got.DesiredCount != DefaultDesiredCount {
				t.Errorf("desired count = %d, want the documented default %d", got.DesiredCount, DefaultDesiredCount)
			}
		})
	}
}

// TestOnlyTheLiveFieldsAreReported: "exclusive configuration". A correction against an inert field
// sends a customer to change a value that is doing nothing, and teaches them to ignore the report.
func TestOnlyTheLiveFieldsAreReported(t *testing.T) {
	// Automatic mode with no fixed count: `desiredCount` is Application Auto Scaling's, so its absence
	// is not a fault.
	autoNoCount := Scaling{Mode: ScalingAuto, MinCount: 2, MaxCount: 10, TargetCPUPercent: 70, TargetMemoryPercent: 75}
	if got := autoNoCount.Resolve(); len(got.Corrections) != 0 {
		t.Errorf("automatic mode reported the unused desiredCount: %v", got.Corrections)
	}
	// Manual mode with no bounds and no targets: all four are inert.
	manualNoBounds := Scaling{Mode: ScalingManual, DesiredCount: 2}
	got := manualNoBounds.Resolve()
	if len(got.Corrections) != 0 {
		t.Errorf("manual mode reported unused bounds: %v", got.Corrections)
	}
	// And they are carried through rather than zeroed, so flipping the mode to `auto` resolves what the
	// network already had rather than the defaults.
	kept := Scaling{Mode: ScalingManual, DesiredCount: 2, MinCount: 4, MaxCount: 9}.Resolve()
	if kept.MinCount != 4 || kept.MaxCount != 9 {
		t.Errorf("manual mode zeroed the configured bounds: %+v", kept)
	}
}

// TestEveryUnusableBoundIsCorrectedAndNamed covers the "valid bounds" contract one field at a time. The
// assertion is on both halves: the value in force is usable, AND the customer is told, because a bound
// silently replaced is a Collector that scales differently from its own configuration.
func TestEveryUnusableBoundIsCorrectedAndNamed(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*Scaling)
		wantMin  int
		wantMax  int
		wantSaid string
	}{
		{
			name:    "no lower bound",
			mutate:  func(s *Scaling) { s.MinCount = 0 },
			wantMin: DefaultMinCount, wantMax: 10, wantSaid: "minCount",
		},
		{
			name:    "a negative lower bound",
			mutate:  func(s *Scaling) { s.MinCount = -1 },
			wantMin: DefaultMinCount, wantMax: 10, wantSaid: "minCount",
		},
		{
			name:    "no upper bound",
			mutate:  func(s *Scaling) { s.MaxCount = 0 },
			wantMin: 2, wantMax: DefaultMaxCount, wantSaid: "maxCount",
		},
		{
			name:    "an upper bound below the lower one",
			mutate:  func(s *Scaling) { s.MinCount, s.MaxCount = 6, 3 },
			wantMin: 6, wantMax: 6, wantSaid: "below the lower bound",
		},
		{
			// The default upper bound is not enough for a lower bound above it, so the pair is squared up
			// against the value actually in force rather than against the configured one.
			name:    "a lower bound above the default upper one",
			mutate:  func(s *Scaling) { s.MinCount, s.MaxCount = 40, 0 },
			wantMin: 40, wantMax: 40, wantSaid: "maxCount",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := auto()
			tc.mutate(&in)
			got := in.Resolve()

			if got.MinCount != tc.wantMin || got.MaxCount != tc.wantMax {
				t.Fatalf("bounds = [%d,%d], want [%d,%d]", got.MinCount, got.MaxCount, tc.wantMin, tc.wantMax)
			}
			if got.MinCount < ReplicaFloor || got.MaxCount < got.MinCount {
				t.Fatalf("bounds [%d,%d] are still not usable", got.MinCount, got.MaxCount)
			}
			if !said(got.Corrections, tc.wantSaid) {
				t.Fatalf("no correction naming %q: %v", tc.wantSaid, got.Corrections)
			}
		})
	}
}

// TestAnUnusableTargetFallsBackToTheDefaultRatherThanTheBoundary: clamping 400% to 100% would produce a
// target that never scales out — a working autoscaler doing nothing, which is harder to diagnose than
// one running a documented default.
func TestAnUnusableTargetFallsBackToTheDefaultRatherThanTheBoundary(t *testing.T) {
	for _, percent := range []int{0, -10, 101, 400} {
		in := auto()
		in.TargetCPUPercent, in.TargetMemoryPercent = percent, percent
		got := in.Resolve()

		if got.TargetCPUPercent != DefaultTargetCPUPercent || got.TargetMemoryPercent != DefaultTargetMemoryPercent {
			t.Errorf("targets for %d%% = %d/%d, want the defaults %d/%d",
				percent, got.TargetCPUPercent, got.TargetMemoryPercent,
				DefaultTargetCPUPercent, DefaultTargetMemoryPercent)
		}
		if !said(got.Corrections, "targetCpuPercent") || !said(got.Corrections, "targetMemoryPercent") {
			t.Errorf("%d%% was corrected without naming both keys: %v", percent, got.Corrections)
		}
	}
	// And a usable one is left exactly as configured, in both directions from the defaults.
	in := auto()
	in.TargetCPUPercent, in.TargetMemoryPercent = 1, 100
	if got := in.Resolve(); got.TargetCPUPercent != 1 || got.TargetMemoryPercent != 100 {
		t.Errorf("usable targets were rewritten: %d/%d", got.TargetCPUPercent, got.TargetMemoryPercent)
	}
}

// TestMemoryIsTargetedHigherThanCPU pins the one default that looks like a mistake. The Collector's
// memory is dominated by memory_limiter's fixed budget, so its utilisation sits high and flat; a memory
// target as low as CPU's would hold the whole fleet permanently scaled out.
func TestMemoryIsTargetedHigherThanCPU(t *testing.T) {
	if DefaultTargetMemoryPercent <= DefaultTargetCPUPercent {
		t.Fatalf("memory target %d%% is not above the CPU target %d%%",
			DefaultTargetMemoryPercent, DefaultTargetCPUPercent)
	}
}

// TestAutomaticModeStartsAtItsLowerBound: the autoscaler is registered after the workload exists, so a
// Collector created at the manual default and then given bounds of [5,20] would scale immediately —
// a deployment nobody asked for in the first minute of its life.
func TestAutomaticModeStartsAtItsLowerBound(t *testing.T) {
	got := Scaling{Mode: ScalingAuto, MinCount: 5, MaxCount: 20, DesiredCount: 2,
		TargetCPUPercent: 70, TargetMemoryPercent: 75}.Resolve()
	if got.StartingCount() != 5 {
		t.Errorf("starting count = %d, want the lower bound 5", got.StartingCount())
	}
	// Manual mode starts at the fixed count, which is the other half of the same rule.
	if fixed := (Scaling{Mode: ScalingManual, DesiredCount: 3}).Resolve(); fixed.StartingCount() != 3 {
		t.Errorf("manual starting count = %d, want 3", fixed.StartingCount())
	}
}

// TestASingleTaskIsReportedInBothModes: one task means every deployment, every task replacement and
// every Spot interruption is a gap in a data path with no local queue. Legitimate, so it is said rather
// than refused — and it has to be said in automatic mode too, where the gap is a scale-in away.
func TestASingleTaskIsReportedInBothModes(t *testing.T) {
	cases := map[string]struct {
		in   Scaling
		want bool
	}{
		"manual on one":       {Scaling{Mode: ScalingManual, DesiredCount: 1}, true},
		"manual on two":       {Scaling{Mode: ScalingManual, DesiredCount: 2}, false},
		"auto down to one":    {Scaling{Mode: ScalingAuto, MinCount: 1, MaxCount: 9}, true},
		"auto down to two":    {Scaling{Mode: ScalingAuto, MinCount: 2, MaxCount: 9}, false},
		"auto with no bounds": {Scaling{Mode: ScalingAuto}, false}, // corrected to the default floor of 2
		// An unknown mode drives nothing, so there is no task count of Odigos's to report on.
		"an unknown mode": {Scaling{Mode: "automatic", DesiredCount: 1}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.in.Resolve().SingleTask(); got != tc.want {
				t.Errorf("SingleTask = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestTheDefaultTreeResolvesWithoutCorrection: whatever Defaults() ships has to be usable as it stands.
// A default that resolves with a correction would put a standing Degraded on every Collector nobody has
// configured.
func TestTheDefaultTreeResolvesWithoutCorrection(t *testing.T) {
	defaults := Read(Defaults()).Defaults.Scaling.Resolve()
	if len(defaults.Corrections) != 0 {
		t.Fatalf("the shipped defaults resolve with corrections: %v", defaults.Corrections)
	}
	if !defaults.Manual() || defaults.DesiredCount != DefaultDesiredCount {
		t.Fatalf("the shipped defaults = %+v", defaults)
	}
	// And flipping only the mode to `auto` — which is all a customer has to write — resolves to the
	// documented policy rather than to zeroes.
	flipped := Scaling{Mode: ScalingAuto, DesiredCount: DefaultDesiredCount}.Resolve()
	if flipped.MinCount != DefaultMinCount || flipped.MaxCount != DefaultMaxCount {
		t.Errorf("bounds after flipping to auto = [%d,%d]", flipped.MinCount, flipped.MaxCount)
	}
	if flipped.TargetCPUPercent != DefaultTargetCPUPercent || flipped.TargetMemoryPercent != DefaultTargetMemoryPercent {
		t.Errorf("targets after flipping to auto = %d/%d", flipped.TargetCPUPercent, flipped.TargetMemoryPercent)
	}
	// It IS reported, though: a customer who wrote only `mode: auto` has bounds they never chose, and
	// finding that out from a status message beats finding it out from a bill.
	if len(flipped.Corrections) == 0 {
		t.Error("bounds Odigos picked for itself were applied silently")
	}
}

// TestResolveDoesNotMutateTheConfiguredPolicy: Scaling is copied out of the settings tree by value, and
// a resolver that wrote back into it would make a second Resolve see corrected input and report nothing.
func TestResolveDoesNotMutateTheConfiguredPolicy(t *testing.T) {
	in := Scaling{Mode: ScalingAuto, MinCount: 0, MaxCount: 0}
	first := in.Resolve()
	if in.MinCount != 0 || in.MaxCount != 0 {
		t.Fatalf("Resolve wrote back into its receiver: %+v", in)
	}
	second := in.Resolve()
	if len(second.Corrections) != len(first.Corrections) {
		t.Errorf("a second Resolve reported %d corrections, the first %d",
			len(second.Corrections), len(first.Corrections))
	}
}

func said(corrections []string, want string) bool {
	for _, c := range corrections {
		if strings.Contains(c, want) {
			return true
		}
	}
	return false
}
