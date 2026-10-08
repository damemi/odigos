package collectorsettings

import (
	"fmt"
	"strings"
)

// Automatic-mode defaults. Deliberately NOT in Defaults(): that tree is merged into the
// effective operational config, and pre-populating bounds there would make every manual-mode region
// read as though somebody had configured autoscaling for it. These apply when a region asks for `auto`
// and names nothing.
const (
	// DefaultMinCount is the same floor manual mode defaults to, for the same reason: one task
	// makes every deployment and every task replacement a gap in a data path with no local queue.
	DefaultMinCount = 2
	// DefaultMaxCount is a bound, not a capacity estimate. Its job is to stop a feedback loop — a
	// Collector under load scaling out, which raises the destination's latency, which raises the queue —
	// from becoming a cost incident before anybody looks. Five times the floor is arbitrary and being
	// arbitrary is fine; being absent is not, because Application Auto Scaling requires a maximum and a
	// connector that had none would have to invent one anyway, silently.
	DefaultMaxCount = 10
	// DefaultTargetCPUPercent and DefaultTargetMemoryPercent are v1 signals. Memory is targeted
	// HIGHER than CPU, which is the counter-intuitive half: the Collector's memory is dominated by
	// memory_limiter's fixed budget, so its utilisation sits high and flat, and a target as low as
	// CPU's would hold the fleet permanently scaled out.
	DefaultTargetCPUPercent    = 70
	DefaultTargetMemoryPercent = 75
)

// ReplicaFloor is the fewest tasks a Collector may be configured to run.
//
// One, not zero, and that difference is the whole point. A Collector scaled to zero is not "off": the
// load balancer still answers, so every workload in the region keeps opening connections to an
// endpoint with nothing behind it, and the telemetry is dropped with no failure anybody is watching.
// Turning a Collector off is `collector.enabled` or the region's own `enabled`, which stops converging
// it and reports that it is off.
const ReplicaFloor = 1

// Target-tracking percentages have to be a real utilisation figure. Zero would ask the autoscaler to
// hold utilisation at nothing, which scales out for ever.
const (
	minTargetPercent = 1
	maxTargetPercent = 100
)

// ResolvedScaling is a Scaling whose every field is usable, plus what had to be corrected to get there.
//
// Resolved once and passed around, rather than each caller reading Scaling for itself: on AWS the
// service converge decides whether to write `desiredCount` and the scaling converge decides whether a
// scalable target exists, and those two answers have to come from the same reading of the mode. Two
// readers is how a Collector ends up with both controls at once.
type ResolvedScaling struct {
	// Mode is the mode as configured, NOT normalised. An unrecognised value is carried through so a
	// report can name it, and Known is the gate that stops anything acting on it.
	Mode string

	// DesiredCount is the fixed replica count, in manual mode.
	DesiredCount int

	// MinCount, MaxCount and the two targets are the automatic-mode policy.
	MinCount            int
	MaxCount            int
	TargetCPUPercent    int
	TargetMemoryPercent int

	// Corrections are the configured values that could not be used, one complete sentence each, naming
	// the config key and what was used instead. Reported for as long as they apply rather than once when
	// applied: a correction is a standing difference between what a customer configured and what is
	// running, and one reported at the moment it happened is invisible to everyone who looks later.
	Corrections []string
}

// Known reports whether the mode is one this connector implements.
//
// An unrecognised mode is neither manual nor automatic, so nothing drives the replica count: no fixed
// count is written and no scalable target is registered or removed. That is deliberate. Guessing
// "manual" for `"automatic"` — the obvious typo — would deregister the scalable target a customer had
// working, which is the one outcome a typo must not be able to cause.
func (r ResolvedScaling) Known() bool { return r.Manual() || r.Automatic() }

// Manual reports whether Odigos holds the replica count itself.
func (r ResolvedScaling) Manual() bool { return r.Mode == ScalingManual }

// Automatic reports whether the platform's autoscaler holds the replica count.
func (r ResolvedScaling) Automatic() bool { return r.Mode == ScalingAuto }

// StartingCount is the replica count a Collector is CREATED with.
//
// The minimum in automatic mode rather than the manual count, because the autoscaler is registered
// after the workload exists: creating at the manual default and then registering bounds of [5, 20]
// starts a Collector at 2 tasks and immediately scales it to 5, which is a deployment nobody asked for
// in the first minute of the Collector's life.
//
// Never below the floor, whatever the mode is, including an unrecognised one — this is the value a
// create call carries, and a create that asked for no tasks would build the endpoint-with-nothing-behind
// it that ReplicaFloor exists to prevent.
func (r ResolvedScaling) StartingCount() int {
	if r.Automatic() {
		return r.MinCount
	}
	return r.DesiredCount
}

// SingleTask reports whether the resolved policy permits the Collector to run on one task.
//
// Worth reporting in both modes and for the same reason: with one task, every deployment, every task
// replacement and every Spot interruption is a gap in a data path that has no local queue to cover it.
// It is a legitimate choice on a network whose telemetry is not precious, so it is said
// once per pass and never refused.
func (r ResolvedScaling) SingleTask() bool {
	if r.Automatic() {
		return r.MinCount <= 1
	}
	return r.Manual() && r.DesiredCount <= 1
}

// Resolve turns a configured Scaling into one every field of which can be used.
//
// Corrects rather than refuses, which is the same choice Read makes and for the same reason: a
// Collector whose scaling configuration is rejected still has to run at SOME replica count, so
// refusing does not avoid picking a number — it only avoids saying which number was picked. What it
// must never do is correct silently, which is what Corrections is for.
//
// Only the fields the mode actually uses are checked. `desiredCount` in automatic mode and `maxCount`
// in manual mode are inert, so reporting them would send a customer to fix a value that is doing
// nothing — and "exclusive configuration" means exactly that one set of fields is live at a time.
func (s Scaling) Resolve() ResolvedScaling {
	out := ResolvedScaling{
		Mode:                strings.TrimSpace(s.Mode),
		DesiredCount:        s.DesiredCount,
		MinCount:            s.MinCount,
		MaxCount:            s.MaxCount,
		TargetCPUPercent:    s.TargetCPUPercent,
		TargetMemoryPercent: s.TargetMemoryPercent,
	}

	// The fixed count is clamped whatever the mode says, because StartingCount uses it for every mode
	// that is not automatic — an unrecognised one included. Only manual mode reports it: in automatic
	// mode the value is inert and a customer sent to fix it would be fixing nothing.
	if out.DesiredCount < ReplicaFloor {
		if out.Manual() {
			out.Corrections = append(out.Corrections, fmt.Sprintf(
				"scaling.desiredCount is %d, which is below %d, so %d is used instead: a service with no tasks "+
					"is not a Collector that is off, it is an endpoint that accepts and drops every connection "+
					"the workloads pointed at it open. Set collector.regions.<region>.enabled to false to turn a "+
					"Collector off",
				s.DesiredCount, ReplicaFloor, DefaultDesiredCount))
		}
		out.DesiredCount = DefaultDesiredCount
	}

	if !out.Automatic() {
		// The bounds are inert, so they are neither corrected nor reported. They are left as configured
		// rather than zeroed, so a network that flips to `auto` resolves the values it already had.
		return out
	}

	if out.MinCount < ReplicaFloor {
		out.Corrections = append(out.Corrections, fmt.Sprintf(
			"scaling.minCount is %d, which is below %d, so %d is used instead: a lower bound of zero lets the "+
				"autoscaler remove the last task, and a Collector with no tasks drops the telemetry of every "+
				"workload still pointed at its endpoint",
			s.MinCount, ReplicaFloor, DefaultMinCount))
		out.MinCount = DefaultMinCount
	}
	if out.MaxCount < ReplicaFloor {
		out.Corrections = append(out.Corrections, fmt.Sprintf(
			"scaling.maxCount is not set to a usable value (%d), so %d is used: automatic scaling needs an "+
				"upper bound, and a Collector that scales out on its own destination's latency can reach a "+
				"surprising number of tasks before anybody looks",
			s.MaxCount, DefaultMaxCount))
		out.MaxCount = DefaultMaxCount
	}
	if out.MaxCount < out.MinCount {
		// After both floors, so a max that only looks too small because the min was raised is reported
		// against the value actually in force rather than against the one that was configured.
		out.Corrections = append(out.Corrections, fmt.Sprintf(
			"scaling.maxCount (%d) is below the lower bound in force (%d), so the two are set equal and this "+
				"Collector will not scale: raise maxCount above minCount to give the autoscaler room",
			out.MaxCount, out.MinCount))
		out.MaxCount = out.MinCount
	}

	out.TargetCPUPercent = resolveTargetPercent(
		"scaling.targetCpuPercent", out.TargetCPUPercent, DefaultTargetCPUPercent, &out.Corrections)
	out.TargetMemoryPercent = resolveTargetPercent(
		"scaling.targetMemoryPercent", out.TargetMemoryPercent, DefaultTargetMemoryPercent, &out.Corrections)
	return out
}

// resolveTargetPercent clamps one target-tracking percentage, falling back to the default rather than
// to the nearest legal value.
//
// The default rather than the boundary because an out-of-range percentage is a value nobody meant: 0 is
// "unset" arriving as a number, and 400 is a typo. Clamping 400 to 100 would produce a target that
// never scales out — a working autoscaler that does nothing, which is worse to diagnose than one
// running a documented default.
func resolveTargetPercent(key string, configured, def int, corrections *[]string) int {
	if configured >= minTargetPercent && configured <= maxTargetPercent {
		return configured
	}
	*corrections = append(*corrections, fmt.Sprintf(
		"%s is %d, which is not a utilisation percentage between %d and %d, so %d%% is used instead",
		key, configured, minTargetPercent, maxTargetPercent, def))
	return def
}
