// Package collectorcutover decides WHEN a region's workloads are re-pointed at its Collector, and how
// many at a time. Pure — no I/O.
//
// It exists because resolution rule leaves a hole. Endpoint selection changes only when
// configuration changes and never on Collector health, which is right: re-pointing on health would
// rewrite every task definition on a network on a transient failure and again on recovery. But the
// corollary is that nothing walks a rollout back on its own. Re-point a network at a Collector that never
// becomes ready and every workload on it exports into a hole until a human notices — the failure the
// silence-on-health rule buys. An implementer who builds without this has built the ungated version,
// which is worse than no Collector at all: direct export works, and a dead endpoint reached by
// configuration does not.
//
// So the FIRST cutover, and only the first, is gated. After it, health changes re-point nothing, exactly
// as says.
//
// # What this package is not
//
// It is not resolution. `destination.ResolveRouting` answers "where does this workload export", and its
// gate is one bit — has this Collector ever been ready (destination.CollectorTarget.EverReady). This
// package answers "how fast may the fleet move", which is a rate limit on WRITES rather than a change of
// answer, and it has to be separate because the two fail differently: an ungated resolution points
// workloads at a hole, and an unbounded rollout rewrites a customer's whole fleet in one pass.
package collectorcutover

import (
	"fmt"
	"sort"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
)

// DefaultBatchSize is how many workloads a single pass may re-point.
//
// Ten, and the number is a blast radius rather than a throughput target. Every admitted workload is a new
// task-definition revision and a rolling ECS deployment, so a pass that admitted a whole fleet would put
// a customer's entire estate into deployment at once — and if the Collector turns out to be reachable but
// broken in a way readiness did not catch, ten services is what has to be walked back rather than two
// hundred. At one pass per reconcile interval (a minute by default) a hundred workloads take ten minutes,
// which is faster than anyone watching it needs it to be.
const DefaultBatchSize = 10

// Phase is where one region's cutover has got to. Reported, and the input to nothing — the decision
// below is computed from the facts, not from a stored phase, so there is no state machine to get stuck.
type Phase string

const (
	// PhaseDirect: this network's workloads export straight to the destination and nothing is changing.
	// What every network does before a Collector is configured for it, and after one is turned off and
	// its rollback has finished.
	PhaseDirect Phase = "direct"
	// PhaseBlocked: a Collector is configured and has never reported ready, so nothing is re-pointed.
	// The gate.
	PhaseBlocked Phase = "blocked"
	// PhaseAnnounced: it has become ready and this is the pass that noticed. Nothing is re-pointed yet —
	// see Decide, which is where the warning-before-the-write property lives.
	PhaseAnnounced Phase = "announced"
	// PhaseRollingOut: workloads are being re-pointed, a batch per pass.
	PhaseRollingOut Phase = "rolling-out"
	// PhaseComplete: every instrumented workload on the network exports through the Collector.
	PhaseComplete Phase = "complete"
	// PhaseRollingBack: the Collector was disabled or removed and workloads are being restored to direct
	// export.
	PhaseRollingBack Phase = "rolling-back"
)

// Workload is one instrumented workload on the network, as the provider's inventory knows it.
type Workload struct {
	// Ref identifies it for reporting and for the deterministic order — an ECS service ARN, a function
	// ARN. Stable across passes, which is what makes a rollout resumable.
	Ref string
	// Routed is whether it is CURRENTLY exporting through the Collector. Read from what is deployed
	// rather than from what was intended: a workload whose update was written and then rolled back by
	// the platform is not routed, and a rollout that believed its own record would skip it for ever.
	Routed bool
	// Failed is why the last attempt to re-point it did not take, or empty. A failure does not stop the
	// others and does not consume the batch — see Decide.
	Failed string
}

// Input is everything the decision depends on.
type Input struct {
	Region string
	// Enabled is whether configuration says this region's workloads use a Collector. NOT whether the
	// Collector is healthy: health never changes where a workload points.
	Enabled bool
	// Deployed is whether the Collector has an endpoint to point at.
	Deployed bool
	// EverReady is the latch as RECORDED: has this region's Collector reported ready on some earlier
	// pass. It is what opens the gate.
	EverReady bool
	// ReadyNow is whether the Collector is reporting ready on THIS pass.
	//
	// The two exist separately for one reason, and it is the whole of "warns before it starts": a pass
	// that finds ReadyNow true and EverReady false is the pass that DISCOVERED the Collector works, and
	// it announces the rollout without moving anything. Only once the latch is recorded does a later pass
	// begin — so the customer sees a fleet-wide rollout announced on the Collector's row at least one
	// pass before the first deployment revision appears in their account.
	//
	// Folded into one decision rather than left as two functions a caller chooses between, because
	// choosing wrongly is silent: calling the rolling one on the announcing pass skips the only warning
	// there is.
	ReadyNow bool
	// Workloads are the instrumented workloads on this network. An uninstrumented one is not part of a
	// rollout: there is nothing deployed to re-point.
	Workloads []Workload
	// BatchSize is how many may be re-pointed this pass. Zero or negative uses DefaultBatchSize; a
	// rollout that admitted nothing would never finish, so this cannot be turned off.
	BatchSize int
}

// Decision is what a pass may do to one region.
type Decision struct {
	Phase Phase
	// Admit are the workload refs that may be re-pointed at the Collector this pass, in the order they
	// will be taken. Empty in every phase but PhaseRollingOut.
	Admit []string
	// Restore are the workload refs that must go back to direct export. Empty except in
	// PhaseRollingBack, and NOT batched — see Decide.
	Restore []string
	// Routed, Pending and Failed count the network's instrumented workloads. Reported on every pass, so
	// a rollout that is stuck on one workload reads as stuck rather than as in progress.
	Routed  int
	Pending int
	Failed  int
	// Problems carry the phase to the Collector's status. See Decide.
	Problems []collectorstatus.Problem
}

// Total is every instrumented workload on the network.
func (d Decision) Total() int { return d.Routed + d.Pending + d.Failed }

// Moves reports whether this pass changes anything.
func (d Decision) Moves() bool { return len(d.Admit) > 0 || len(d.Restore) > 0 }

// Decide answers what one pass may do to one region.
//
// # The gate
//
// Nothing is admitted until the Collector has reported ready at least once. That is the whole of "no
// early re-point": a Collector that never becomes ready re-points nothing and says why, so the worst case
// of a broken deploy is a fleet still exporting directly — which is what it was doing before.
//
// Readiness is a LATCH rather than a live check, and the difference is rule. Once the rollout has
// started, a Collector going unhealthy changes nothing: its queue and the load balancer's target health
// are the right tools for a blip, and re-pointing a fleet on a health flap would rewrite every task
// definition twice.
//
// # Warning before the write
//
// The pass that first observes readiness — ReadyNow true, EverReady false — admits NOTHING. It returns
// PhaseAnnounced and a problem saying how many workloads are about to be re-pointed; the caller records
// the latch, and the next pass begins. The customer therefore sees a fleet-wide rollout announced on the
// Collector's row at least one pass before the first task-definition revision — which is the point,
// because that revision is visible in their account and this is the only warning they get.
//
// # Batching and resumability
//
// A pass admits at most BatchSize workloads, chosen from those not yet routed in a stable order. There is
// no rollout cursor to persist and nothing to resume: "which workloads are not yet routed" is a question
// about what is DEPLOYED, so a connector that restarts mid-rollout re-reads it and carries on, and one
// that restarts after a workload was written but before anything recorded it sees the workload as routed
// and does not write it again. Once every workload is routed the answer is the empty set, which is the
// zero-writes steady state.
//
// # Failure isolation
//
// A workload whose last attempt failed is counted, reported and RETRIED — it is admitted again — but it
// never blocks the others and never consumes another workload's place in the batch. A rollout stuck on
// one unwritable service still finishes for the rest of the network, and the failure is reported against
// that workload rather than as a failed network.
//
// # Rollback is not batched
//
// Restoring direct export applies to every routed workload at once. Batching a cutover bounds the blast
// radius of a change that might be wrong; batching a ROLLBACK would leave workloads pointing at a
// Collector somebody has just turned off, dropping telemetry for as many passes as the fleet is large.
// The asymmetry is deliberate: the two directions have opposite risk.
func Decide(in Input) Decision {
	out := Decision{}
	var pending, routed, failed []string
	for _, w := range in.Workloads {
		switch {
		case w.Routed:
			routed = append(routed, w.Ref)
		case w.Failed != "":
			failed = append(failed, w.Ref)
		default:
			pending = append(pending, w.Ref)
		}
	}
	sort.Strings(pending)
	sort.Strings(routed)
	sort.Strings(failed)
	out.Routed, out.Pending, out.Failed = len(routed), len(pending), len(failed)

	// Rollback first, because it is the only branch that can apply while the Collector is gone. A
	// disabled network with workloads still on its Collector is the urgent case in this whole file: they
	// are pointing at something nobody is maintaining any more.
	if !in.Enabled || !in.Deployed {
		if len(routed) == 0 {
			out.Phase = PhaseDirect
			return out
		}
		out.Phase = PhaseRollingBack
		out.Restore = routed
		out.Problems = append(out.Problems, rollingBackProblem(in.Region, len(routed), in.Enabled))
		return out
	}

	if !in.EverReady {
		if in.ReadyNow && len(pending)+len(failed) > 0 {
			// The pass that discovered it works. Announce, move nothing; the caller records the latch and
			// the next pass rolls. See Input.ReadyNow.
			out.Phase = PhaseAnnounced
			out.Problems = append(out.Problems, announceProblem(in, len(pending)+len(failed)))
			return out
		}
		out.Phase = PhaseBlocked
		if len(routed) > 0 {
			// Already rolled out on an earlier pass, and the Collector has since lost the row that
			// recorded its readiness — a restored database, most plausibly. NOT rolled back: rule
			// is that selection never follows health, and these workloads are exporting through a
			// Collector that is by every other measure fine.
			out.Phase = PhaseRollingOut
		}
		out.Problems = append(out.Problems, blockedProblem(in.Region, len(pending)+len(failed), len(routed)))
		return out
	}

	if len(pending) == 0 && len(failed) == 0 {
		out.Phase = PhaseComplete
		if len(routed) > 0 {
			out.Problems = append(out.Problems, completeProblem(in.Region, len(routed)))
		} else {
			out.Phase = PhaseDirect
		}
		return out
	}

	// Failures are retried FIRST, ahead of workloads never attempted. A rollout that put them last would
	// report a failure once and then not touch it again until every other workload on the network was
	// done — which on a large network is a workload silently exporting directly for a very long time.
	admit := append(append([]string{}, failed...), pending...)
	if n := batchSize(in.BatchSize); len(admit) > n {
		admit = admit[:n]
	}
	out.Phase = PhaseRollingOut
	out.Admit = admit
	out.Problems = append(out.Problems, rollingOutProblem(in.Region, out))
	if len(failed) > 0 {
		out.Problems = append(out.Problems, failedProblem(in.Region, in.Workloads))
	}
	return out
}

// announceProblem is the one warning a customer gets before their task definitions start changing.
func announceProblem(in Input, moving int) collectorstatus.Problem {
	return collectorstatus.Problem{
		Code:     ProblemRolloutStarting,
		Severity: collectorstatus.SeverityInfo,
		Message: fmt.Sprintf("the Collector in %s is ready, so the %d instrumented workload(s) in that "+
			"region will be re-pointed at it over the next few reconcile passes, %d at a time. Each one "+
			"gets a new deployment revision, which is a change you will see in your own account; "+
			"disabling this region's Collector restores direct export",
			in.Region, moving, batchSize(in.BatchSize)),
	}
}

func batchSize(configured int) int {
	if configured <= 0 {
		return DefaultBatchSize
	}
	return configured
}
