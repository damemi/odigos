package collectorsettings

import (
	"fmt"
	"time"
)

// Retention is what happens to a Collector whose network has no enabled sources left.
//
// Both graces default to KEEP, and that default is the decision worth defending. A network's last
// source being disabled is very often a customer mid-edit — disable one source, enable another, look at
// the report — and a Collector that disappeared on the strength of that reading takes with it a load
// balancer whose DNS name every workload on the network was pointed at. Keeping a Collector nobody is
// using costs money and nothing else; deleting one somebody was about to use again costs a
// re-deployment of every instrumented workload on the network.
//
// Configured in HOURS rather than as a duration string, because the granularity is the point: the
// smallest sensible grace is longer than a reconcile interval by a wide margin, and an integer field
// cannot be written as "5m" by somebody who meant "5h".
type Retention struct {
	// IdleScaleToZeroAfterHours parks the Collector — its infrastructure stays and its tasks stop.
	// Zero, absent or negative means keep it running.
	IdleScaleToZeroAfterHours int
	// IdleDeleteAfterHours tears the Collector down, through the same per-object ownership gate a
	// connector deletion uses. Zero, absent or negative means keep it.
	IdleDeleteAfterHours int
}

// RetentionAction is what a pass should do with an idle Collector.
type RetentionAction string

const (
	// RetentionKeep leaves the Collector exactly as it is. What every unconfigured network does for ever.
	RetentionKeep RetentionAction = "keep"
	// RetentionScaleToZero stops the tasks and keeps everything else, so re-enabling a source on the
	// network is a scale-up rather than a redeployment.
	RetentionScaleToZero RetentionAction = "scale-to-zero"
	// RetentionDelete removes what Odigos created. Per object, through collectorownership.MayDelete —
	// idleness decides WHEN teardown runs and never what it is allowed to remove.
	RetentionDelete RetentionAction = "delete"
)

// ResolvedRetention is a Retention whose values are usable, plus what had to be corrected to get there.
type ResolvedRetention struct {
	// ScaleToZeroAfter and DeleteAfter are zero when that half is "keep".
	ScaleToZeroAfter time.Duration
	DeleteAfter      time.Duration
	// Corrections are complete sentences naming the config key and what was used instead, reported for
	// as long as they apply.
	Corrections []string
}

// Resolve turns a configured Retention into one whose values can be acted on.
//
// Corrects rather than refuses, like Scaling.Resolve — with one asymmetry that runs the other way:
// every unusable value resolves towards KEEPING the Collector. A correction that guessed at a grace
// period would be a guess that deletes a customer's infrastructure, and there is no value here worth
// inventing.
func (r Retention) Resolve() ResolvedRetention {
	out := ResolvedRetention{}

	if r.IdleScaleToZeroAfterHours < 0 {
		out.Corrections = append(out.Corrections, fmt.Sprintf(
			"collector.defaults.%s.%s is %d, which is not a duration; this Collector keeps running while idle",
			KeyRetention, KeyIdleScaleToZeroAfterHours, r.IdleScaleToZeroAfterHours))
	} else {
		out.ScaleToZeroAfter = time.Duration(r.IdleScaleToZeroAfterHours) * time.Hour
	}

	if r.IdleDeleteAfterHours < 0 {
		out.Corrections = append(out.Corrections, fmt.Sprintf(
			"collector.defaults.%s.%s is %d, which is not a duration; this Collector is kept when idle",
			KeyRetention, KeyIdleDeleteAfterHours, r.IdleDeleteAfterHours))
	} else {
		out.DeleteAfter = time.Duration(r.IdleDeleteAfterHours) * time.Hour
	}

	// A delete grace no longer than the parking grace is not corrected — the stronger action simply
	// wins, which is what Decide does — but it IS named, because the configuration reads as though the
	// Collector will be parked first and it never will be. Silently honouring the shorter one would
	// leave a customer believing they had a reversible middle step.
	if out.DeleteAfter > 0 && out.ScaleToZeroAfter > 0 && out.DeleteAfter <= out.ScaleToZeroAfter {
		out.Corrections = append(out.Corrections, fmt.Sprintf(
			"collector.defaults.%s.%s (%dh) is not longer than %s (%dh), so this Collector is deleted when "+
				"idle without ever being scaled to zero first",
			KeyRetention, KeyIdleDeleteAfterHours, r.IdleDeleteAfterHours,
			KeyIdleScaleToZeroAfterHours, r.IdleScaleToZeroAfterHours))
	}
	return out
}

// Keeps reports whether this policy ever acts. True for every network nobody configured.
func (r ResolvedRetention) Keeps() bool {
	return r.ScaleToZeroAfter == 0 && r.DeleteAfter == 0
}

// Decide reports what to do with a Collector that has been idle since idleSince.
//
// The stronger action wins, so a policy whose two graces are in the wrong order deletes rather than
// parking — see the correction in Resolve.
//
// A ZERO idleSince is Keep, and that is the load-bearing case rather than a defensive one: it is what a
// pass sees the first time it finds a network idle, before anything has recorded when that started.
// Treating an unknown start as "idle for ever" would delete a Collector on the first pass after a
// connector restart, which is the whole failure this clock exists to prevent.
func (r ResolvedRetention) Decide(idleSince, now time.Time) RetentionAction {
	if idleSince.IsZero() {
		return RetentionKeep
	}
	idleFor := now.Sub(idleSince)
	if r.DeleteAfter > 0 && idleFor >= r.DeleteAfter {
		return RetentionDelete
	}
	if r.ScaleToZeroAfter > 0 && idleFor >= r.ScaleToZeroAfter {
		return RetentionScaleToZero
	}
	return RetentionKeep
}

// Describe states the policy in the words a status message uses, so the report a customer reads and the
// decision the pass made cannot describe the same configuration differently.
func (r ResolvedRetention) Describe() string {
	switch {
	case r.Keeps():
		return "it is kept as it is for as long as the network has no sources"
	case r.DeleteAfter > 0 && r.ScaleToZeroAfter > 0:
		return fmt.Sprintf("it is scaled to zero after %s idle and deleted after %s",
			r.ScaleToZeroAfter, r.DeleteAfter)
	case r.DeleteAfter > 0:
		return fmt.Sprintf("it is deleted after %s idle", r.DeleteAfter)
	default:
		return fmt.Sprintf("it is scaled to zero after %s idle and then kept", r.ScaleToZeroAfter)
	}
}

// Park is the replica policy of a Collector retention has scaled to zero.
//
// Expressed as a MANUAL count of zero rather than as a flag on the deployer, which is what keeps the
// central invariant intact: exactly one control may move a replica count. Parking through the manual
// path means the same converge that writes `desiredCount: 0` also deregisters the scalable target, so a
// parked Collector cannot be scaled back up by an autoscaler that was still watching a CPU metric it
// will never see again.
//
// This is the one place a Collector is permitted below ReplicaFloor, and the exception is precise
// rather than convenient. ReplicaFloor exists because a Collector at zero replicas still has a load
// balancer answering, so every workload pointed at it keeps opening connections to an endpoint with
// nothing behind it and drops its telemetry with no failure anybody is watching. Parking only ever
// applies to a network with no enabled sources — nothing is pointed at this endpoint, so there is
// nothing to drop. The cost is paid on the way back: a source enabled on a parked network is exporting
// before the Collector's tasks are running again, so the first telemetry after a network wakes up may
// be lost. That is why parking is opt-in and why keeping is the default.
func (r ResolvedScaling) Park() ResolvedScaling {
	out := r
	out.Mode = ScalingManual
	out.DesiredCount = 0
	out.MinCount = 0
	out.MaxCount = 0
	out.Corrections = append(append([]string(nil), r.Corrections...),
		"this Collector's network has no enabled sources, so it is scaled to zero by "+
			KeyRetention+"."+KeyIdleScaleToZeroAfterHours+" rather than by its configured scaling policy")
	return out
}

// Parked reports whether a resolved policy is retention's parked one rather than a configured count.
//
// Read by the scaling report, so a row showing a Collector at zero tasks says why. A configured manual
// count can never be zero — Resolve lifts it to ReplicaFloor — so zero here is unambiguous.
func (r ResolvedScaling) Parked() bool { return r.Manual() && r.DesiredCount == 0 }
