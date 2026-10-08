package collectorcutover

import (
	"fmt"
	"sort"
	"strings"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
)

// Problem codes this package reports. Callers branch on these, never on a message.
const (
	// ProblemRolloutStarting: the Collector has become ready and a fleet-wide re-point is about to
	// begin. Info, and the ONE warning a customer gets before their task definitions start changing —
	// which is why it is emitted a pass before the first write rather than alongside it.
	ProblemRolloutStarting = "CollectorRolloutStarting"
	// ProblemRolloutBlocked: a Collector is configured for this region and has never reported ready, so
	// no workload has been re-pointed. Degraded rather than Error: everything is still exporting
	// directly, which is what it did before the Collector existed — nothing is broken, and something is
	// not working.
	ProblemRolloutBlocked = "CollectorRolloutBlocked"
	// ProblemRolloutInProgress: workloads are being re-pointed. Info.
	ProblemRolloutInProgress = "CollectorRolloutInProgress"
	// ProblemRolloutWorkloadFailed: one workload could not be re-pointed. Degraded, and named per
	// workload: a rollout that reported this as a failed NETWORK would hide that the other ninety-nine
	// are fine, and would send whoever reads it looking at the Collector rather than at the one service
	// whose task definition could not be registered.
	ProblemRolloutWorkloadFailed = "CollectorRolloutWorkloadFailed"
	// ProblemRolloutComplete: every instrumented workload in the region exports through the Collector.
	// Info, and said on every pass — a fleet that silently finished rolling looks identical to one that
	// silently never started.
	ProblemRolloutComplete = "CollectorRolloutComplete"
	// ProblemRollingBack: the Collector was disabled or removed and its workloads are being restored to
	// direct export. Degraded, and it is a state the customer ASKED for — the severity is about the
	// window, not the intent: until each workload's deployment lands it is still pointing at a Collector
	// nothing maintains.
	ProblemRollingBack = "CollectorRolloutRollingBack"
)

// maxNamedWorkloads caps how many refs a message carries. They land in a status column and a report; the
// COUNT is the actionable number and the names are somewhere to start.
const maxNamedWorkloads = 5

func blockedProblem(region string, waiting, routed int) collectorstatus.Problem {
	message := fmt.Sprintf("the Collector in %s has not reported ready, so its %d instrumented workload(s) "+
		"are still exporting straight to the destination. Nothing is re-pointed at a Collector until it "+
		"has been ready at least once — a Collector that never becomes ready would otherwise take a whole "+
		"region's telemetry with it", region, waiting)
	if routed > 0 {
		// The restored-database case. Worth its own sentence, because "blocked" and "blocked with
		// workloads already on it" need different things done about them.
		message = fmt.Sprintf("the Collector in %s has no recorded readiness, and %d workload(s) are "+
			"already exporting through it. They are left alone — where a workload points never follows "+
			"Collector health — and the remaining %d are held back until it reports ready",
			region, routed, waiting)
	}
	return collectorstatus.Problem{
		Code:     ProblemRolloutBlocked,
		Severity: collectorstatus.SeverityDegraded,
		Message:  message,
	}
}

func rollingOutProblem(region string, d Decision) collectorstatus.Problem {
	return collectorstatus.Problem{
		Code:     ProblemRolloutInProgress,
		Severity: collectorstatus.SeverityInfo,
		Message: fmt.Sprintf("re-pointing the workloads in %s at their regional Collector: %d of %d done, %d in this "+
			"pass. Each gets a new deployment revision; the ones not yet moved keep exporting straight to "+
			"the destination in the meantime, so no telemetry is lost while this runs",
			region, d.Routed, d.Total(), len(d.Admit)),
	}
}

func completeProblem(region string, routed int) collectorstatus.Problem {
	return collectorstatus.Problem{
		Code:     ProblemRolloutComplete,
		Severity: collectorstatus.SeverityInfo,
		Message: fmt.Sprintf("all %d instrumented workload(s) in %s export through this Collector. Disabling "+
			"it restores direct export for every one of them", routed, region),
	}
}

func rollingBackProblem(region string, routed int, enabled bool) collectorstatus.Problem {
	why := "its Collector was turned off"
	if enabled {
		why = "its Collector has no endpoint to point at"
	}
	return collectorstatus.Problem{
		Code:     ProblemRollingBack,
		Severity: collectorstatus.SeverityDegraded,
		Message: fmt.Sprintf("%d workload(s) in %s are being restored to direct export because %s. They are "+
			"restored all at once rather than in batches: until each one's deployment lands it is still "+
			"sending to a Collector nothing is maintaining", routed, region, why),
	}
}

func failedProblem(region string, workloads []Workload) collectorstatus.Problem {
	var named []string
	var reason string
	for _, w := range workloads {
		if w.Failed == "" {
			continue
		}
		named = append(named, w.Ref)
		if reason == "" {
			reason = w.Failed
		}
	}
	sort.Strings(named)
	shown := named
	if len(shown) > maxNamedWorkloads {
		shown = shown[:maxNamedWorkloads]
	}
	return collectorstatus.Problem{
		Code:     ProblemRolloutWorkloadFailed,
		Severity: collectorstatus.SeverityDegraded,
		Message: fmt.Sprintf("%d workload(s) in %s could not be re-pointed at the Collector (%s) and are "+
			"still exporting directly: %s. They are retried on the next pass and do not hold up the rest "+
			"of the region", len(named), region, reason, strings.Join(shown, ", ")),
	}
}
