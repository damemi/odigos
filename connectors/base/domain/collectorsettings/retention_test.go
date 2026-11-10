package collectorsettings

import (
	"strings"
	"testing"
	"time"
)

func hoursAgo(n int) time.Time {
	return time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC).Add(-time.Duration(n) * time.Hour)
}

func now() time.Time { return time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC) }

// TestAnUnconfiguredNetworkKeepsItsCollectorForEver is the default, and it is the decision the whole
// type exists to defend.
//
// A network's last source being disabled is very often a customer mid-edit. A Collector that vanished on
// the strength of that reading takes with it a load balancer whose DNS name every workload on the
// network was pointed at, so the next enable is a redeployment of the whole network rather than a
// scale-up. Keeping one nobody is using costs money and nothing else.
func TestAnUnconfiguredNetworkKeepsItsCollectorForEver(t *testing.T) {
	retention := Retention{}.Resolve()

	if !retention.Keeps() {
		t.Fatalf("an unconfigured retention acts: %+v", retention)
	}
	for _, idle := range []int{1, 24, 24 * 365} {
		if got := retention.Decide(hoursAgo(idle), now()); got != RetentionKeep {
			t.Errorf("after %dh idle the default policy said %q, want keep", idle, got)
		}
	}
	if len(retention.Corrections) != 0 {
		t.Errorf("the default policy reported corrections: %v", retention.Corrections)
	}
}

// TestTheGracesAreHonouredOnceTheyElapse.
func TestTheGracesAreHonouredOnceTheyElapse(t *testing.T) {
	retention := Retention{IdleScaleToZeroAfterHours: 6, IdleDeleteAfterHours: 48}.Resolve()

	for _, tc := range []struct {
		idleHours int
		want      RetentionAction
	}{
		{0, RetentionKeep},
		{5, RetentionKeep},
		// Inclusive: the grace is "after 6 hours idle", and a boundary that waited for 6h+1ns would make
		// the configured number a lie by an amount nobody can see.
		{6, RetentionScaleToZero},
		{47, RetentionScaleToZero},
		{48, RetentionDelete},
		{500, RetentionDelete},
	} {
		if got := retention.Decide(hoursAgo(tc.idleHours), now()); got != tc.want {
			t.Errorf("after %dh idle: got %q, want %q", tc.idleHours, got, tc.want)
		}
	}
}

// TestAnUnknownIdleStartKeepsTheCollector is the load-bearing case rather than a defensive one.
//
// A zero idleSince is what a pass sees the FIRST time it finds a network idle, before anything has
// recorded when that started. Treating an unknown start as "idle for ever" would delete a Collector on
// the first pass after a connector restart — which is the whole failure the persisted clock exists to
// prevent.
func TestAnUnknownIdleStartKeepsTheCollector(t *testing.T) {
	retention := Retention{IdleScaleToZeroAfterHours: 1, IdleDeleteAfterHours: 1}.Resolve()

	if got := retention.Decide(time.Time{}, now()); got != RetentionKeep {
		t.Fatalf("an unknown idle start decided %q, want keep", got)
	}
}

// TestEveryUnusableValueResolvesTowardsKeeping. Scaling.Resolve corrects towards a working default;
// this corrects towards doing NOTHING, and the asymmetry is deliberate — a guess here is a guess that
// deletes a customer's infrastructure, and there is no value worth inventing.
func TestEveryUnusableValueResolvesTowardsKeeping(t *testing.T) {
	for name, configured := range map[string]Retention{
		"negative delete": {IdleDeleteAfterHours: -1},
		"negative park":   {IdleScaleToZeroAfterHours: -24},
		"both negative":   {IdleScaleToZeroAfterHours: -1, IdleDeleteAfterHours: -1},
		"zero is not now": {IdleScaleToZeroAfterHours: 0, IdleDeleteAfterHours: 0},
	} {
		t.Run(name, func(t *testing.T) {
			retention := configured.Resolve()
			if !retention.Keeps() {
				t.Fatalf("%+v resolved to something that acts: %+v", configured, retention)
			}
			if got := retention.Decide(hoursAgo(10000), now()); got != RetentionKeep {
				t.Errorf("decided %q on a Collector idle for a year", got)
			}
		})
	}
}

// TestANegativeValueIsNamedRatherThanSilentlyIgnored. The configuration reads as though something
// happens; if it does not, that has to be said.
func TestANegativeValueIsNamedRatherThanSilentlyIgnored(t *testing.T) {
	retention := Retention{IdleDeleteAfterHours: -5}.Resolve()

	if len(retention.Corrections) != 1 {
		t.Fatalf("corrections = %v, want exactly one naming idleDeleteAfterHours", retention.Corrections)
	}
	if !strings.Contains(retention.Corrections[0], KeyIdleDeleteAfterHours) {
		t.Errorf("the correction does not name its config key: %q", retention.Corrections[0])
	}
}

// TestADeleteGraceInsideTheParkGraceIsNamed. The stronger action wins, so the Collector is deleted
// without ever being parked — which is a defensible outcome and NOT what the configuration reads like.
// Silently honouring the shorter one would leave a customer believing they had a reversible middle step.
func TestADeleteGraceInsideTheParkGraceIsNamed(t *testing.T) {
	retention := Retention{IdleScaleToZeroAfterHours: 48, IdleDeleteAfterHours: 6}.Resolve()

	if got := retention.Decide(hoursAgo(7), now()); got != RetentionDelete {
		t.Errorf("after 7h: got %q, want the stronger action", got)
	}
	if len(retention.Corrections) != 1 {
		t.Fatalf("corrections = %v, want one saying the parking never applies", retention.Corrections)
	}
	if !strings.Contains(retention.Corrections[0], "without ever being scaled to zero") {
		t.Errorf("the correction does not say what it costs: %q", retention.Corrections[0])
	}

	// Equal graces are the same story: parking never gets a window of its own.
	equal := Retention{IdleScaleToZeroAfterHours: 6, IdleDeleteAfterHours: 6}.Resolve()
	if len(equal.Corrections) != 1 {
		t.Errorf("equal graces reported %v, want the same correction", equal.Corrections)
	}
}

// TestOneHalfConfiguredIsNotACorrection. Deleting without parking first, or parking and then keeping for
// ever, are both complete policies somebody may have meant.
func TestOneHalfConfiguredIsNotACorrection(t *testing.T) {
	for name, configured := range map[string]Retention{
		"delete only": {IdleDeleteAfterHours: 24},
		"park only":   {IdleScaleToZeroAfterHours: 24},
	} {
		t.Run(name, func(t *testing.T) {
			retention := configured.Resolve()
			if len(retention.Corrections) != 0 {
				t.Errorf("corrections = %v, want none", retention.Corrections)
			}
			if retention.Keeps() {
				t.Error("a configured half resolved to keeping")
			}
		})
	}
}

// TestDescribeSaysWhatWillHappen. The report a customer reads and the decision the pass made come from
// one place, so they cannot describe the same configuration differently.
func TestDescribeSaysWhatWillHappen(t *testing.T) {
	for name, tc := range map[string]struct {
		retention Retention
		want      []string
	}{
		"keeps":  {Retention{}, []string{"kept as it is"}},
		"both":   {Retention{IdleScaleToZeroAfterHours: 6, IdleDeleteAfterHours: 48}, []string{"scaled to zero after 6h", "deleted after 48h"}},
		"park":   {Retention{IdleScaleToZeroAfterHours: 6}, []string{"scaled to zero after 6h", "then kept"}},
		"delete": {Retention{IdleDeleteAfterHours: 48}, []string{"deleted after 48h"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := tc.retention.Resolve().Describe()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("Describe() = %q, want it to contain %q", got, want)
				}
			}
		})
	}
}

// TestParkingIsAManualZeroAndRemovesTheAutoscaler.
//
// Expressed as a manual count of zero rather than as a flag, which is what keeps the central invariant
// intact: exactly one control may move a replica count. Parking through the manual path means the same
// converge that writes `desiredCount: 0` also deregisters the scalable target — so a parked Collector
// cannot be scaled back up by an autoscaler still watching a CPU metric it will never see again.
func TestParkingIsAManualZeroAndRemovesTheAutoscaler(t *testing.T) {
	configured := Scaling{Mode: ScalingAuto, MinCount: 3, MaxCount: 9}.Resolve()
	parked := configured.Park()

	if !parked.Manual() {
		t.Fatalf("a parked policy is %q; only manual mode writes a fixed count and deregisters the target", parked.Mode)
	}
	if parked.DesiredCount != 0 {
		t.Errorf("desired count = %d, want 0", parked.DesiredCount)
	}
	if parked.MinCount != 0 || parked.MaxCount != 0 {
		t.Errorf("a parked policy kept its autoscaling bounds (%d..%d), which would be recorded as the "+
			"bounds in force on a Collector nothing is scaling", parked.MinCount, parked.MaxCount)
	}
	if !parked.Parked() {
		t.Error("a parked policy does not report itself as parked, so a row at zero tasks would have no explanation")
	}
	// The configured policy is not mutated: it is what the network converges back to when a source
	// returns, and a Park that edited it in place would make waking up impossible.
	if configured.MinCount != 3 || !configured.Automatic() {
		t.Errorf("Park mutated the configured policy: %+v", configured)
	}
}

// TestOnlyRetentionCanReachZero. ReplicaFloor refuses a configured count below one because a Collector
// at zero replicas still has a load balancer answering — every workload pointed at it opens connections
// to an endpoint with nothing behind it and drops telemetry with no failure anybody is watching. That
// hazard is absent only when the network has no senders, which is exactly what idle means.
func TestOnlyRetentionCanReachZero(t *testing.T) {
	// A configured zero is read as UNSET and resolves to the default of two, not to the floor: nobody
	// writing `desiredCount: 0` meant "run no Collector", they left the field out or typed a placeholder.
	configured := Scaling{Mode: ScalingManual, DesiredCount: 0}.Resolve()

	if configured.DesiredCount != DefaultDesiredCount {
		t.Fatalf("a CONFIGURED zero resolved to %d, want the default of %d", configured.DesiredCount, DefaultDesiredCount)
	}
	if lifted := (Scaling{Mode: ScalingManual, DesiredCount: -3}).Resolve(); lifted.DesiredCount < ReplicaFloor {
		t.Fatalf("a negative count resolved to %d, below the floor of %d", lifted.DesiredCount, ReplicaFloor)
	}
	if configured.Parked() {
		t.Error("a configured count reported itself as parked, so a report could not tell the two apart")
	}
	if !configured.Park().Parked() {
		t.Error("retention could not reach zero")
	}
}

// TestParkingNamesItselfAsTheReason. The corrections travel into the status report, and a Collector at
// zero tasks whose configuration says two needs to say which of the two is in force and why.
func TestParkingNamesItselfAsTheReason(t *testing.T) {
	parked := Scaling{Mode: ScalingManual, DesiredCount: 4}.Resolve().Park()

	if len(parked.Corrections) == 0 {
		t.Fatal("a parked policy explains nothing")
	}
	last := parked.Corrections[len(parked.Corrections)-1]
	if !strings.Contains(last, KeyRetention) || !strings.Contains(last, "no enabled sources") {
		t.Errorf("the parking correction does not name the policy that caused it: %q", last)
	}
}

// TestTheRetentionTreeIsReadFromTheConfig, defaults and per-network override both.
func TestTheRetentionTreeIsReadFromTheConfig(t *testing.T) {
	settings := Read(map[string]any{
		Key: map[string]any{
			KeyEnabled: true,
			KeyDefaults: map[string]any{
				KeyRetention: map[string]any{
					KeyIdleScaleToZeroAfterHours: 6,
					KeyIdleDeleteAfterHours:      72,
				},
			},
			KeyRegions: map[string]any{
				"eu-west-1": map[string]any{
					KeyRetention: map[string]any{KeyIdleDeleteAfterHours: 0},
				},
			},
		},
	})

	inherited := settings.Region("us-east-1").Retention
	if inherited.IdleScaleToZeroAfterHours != 6 || inherited.IdleDeleteAfterHours != 72 {
		t.Errorf("a network with no entry did not inherit the defaults: %+v", inherited)
	}
	// An explicit zero on one network means KEEP, and it has to win over an inherited delete grace —
	// "absent is not the same as zero" cuts this way round too.
	override := settings.Region("eu-west-1").Retention
	if override.IdleDeleteAfterHours != 0 {
		t.Errorf("an explicit zero did not override the inherited delete grace: %+v", override)
	}
	if override.IdleScaleToZeroAfterHours != 6 {
		t.Errorf("overriding one half dropped the other: %+v", override)
	}
	if override.Resolve().DeleteAfter != 0 {
		t.Error("the overriding network still deletes")
	}
}

// TestRetentionIsNotInTheDefaultsTree. Defaults() is merged into the effective config, so writing zeros
// there would make every network read as though somebody had configured a retention policy for it — and
// the whole point is that an unconfigured network has none.
func TestRetentionIsNotInTheDefaultsTree(t *testing.T) {
	tree, _ := Defaults()[Key].(map[string]any)
	defaults, _ := tree[KeyDefaults].(map[string]any)
	if _, present := defaults[KeyRetention]; present {
		t.Error("Defaults() pre-populates the retention tree")
	}
}
