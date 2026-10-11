package collectorcutover

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
)

const network = "vpc-0abc"

// fleet builds a network's instrumented workloads: n of them, the first `routed` already exporting
// through the Collector.
func fleet(n, routed int) []Workload {
	out := make([]Workload, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, Workload{Ref: ref(i), Routed: i < routed})
	}
	return out
}

func ref(i int) string { return "svc-" + string(rune('a'+i%26)) + string(rune('0'+i/26)) }

func ready(workloads []Workload) Input {
	return Input{Region: network, Enabled: true, Deployed: true, EverReady: true, Workloads: workloads}
}

func hasCode(problems []collectorstatus.Problem, code string) bool {
	for _, p := range problems {
		if p.Code == code {
			return true
		}
	}
	return false
}

func problemWith(t *testing.T, problems []collectorstatus.Problem, code string) collectorstatus.Problem {
	t.Helper()
	for _, p := range problems {
		if p.Code == code {
			return p
		}
	}
	t.Fatalf("no %s among %v", code, problems)
	return collectorstatus.Problem{}
}

// TestNothingIsRePointedBeforeTheCollectorHasEverBeenReady is the first acceptance criterion, and the
// whole reason this package exists.
//
// rule that selection never follows health is right, and its corollary is that nothing walks a
// rollout back on its own. Re-point a network at a Collector that never becomes ready and every workload
// on it exports into a hole until a human notices — so the first cutover, and only the first, is gated.
func TestNothingIsRePointedBeforeTheCollectorHasEverBeenReady(t *testing.T) {
	in := ready(fleet(5, 0))
	in.EverReady = false

	got := Decide(in)

	if got.Phase != PhaseBlocked {
		t.Fatalf("phase = %q, want blocked", got.Phase)
	}
	if len(got.Admit) != 0 {
		t.Fatalf("admitted %v before the Collector was ever ready", got.Admit)
	}
	problem := problemWith(t, got.Problems, ProblemRolloutBlocked)
	if problem.Severity != collectorstatus.SeverityDegraded {
		t.Errorf("severity = %q, want Degraded: nothing is broken — everything is still exporting "+
			"directly — and something is not working", problem.Severity)
	}
	if !strings.Contains(problem.Message, "exporting straight to the destination") {
		t.Errorf("the message does not say where the telemetry is going: %q", problem.Message)
	}
}

// TestAHealthyThenUnhealthyCollectorRePointsNothingBack. The latch is a latch: once a rollout has
// started, a Collector going unhealthy changes nothing. Re-pointing a fleet on a health flap would
// rewrite every task definition on the network and again on recovery.
//
// Expressed here as the one case that could be mistaken for it — the Collector's readiness record is
// gone (a restored database) while workloads are already on it. They stay.
func TestAHealthyThenUnhealthyCollectorRePointsNothingBack(t *testing.T) {
	in := ready(fleet(5, 5))
	in.EverReady = false

	got := Decide(in)

	if len(got.Restore) != 0 {
		t.Fatalf("restored %v; where a workload points never follows Collector health", got.Restore)
	}
	if got.Phase != PhaseRollingOut {
		t.Errorf("phase = %q, want the rollout to be treated as still in progress", got.Phase)
	}
	problem := problemWith(t, got.Problems, ProblemRolloutBlocked)
	if !strings.Contains(problem.Message, "already exporting through it") {
		t.Errorf("the message does not distinguish this from a rollout that never started: %q", problem.Message)
	}
}

// TestTheRolloutIsAnnouncedBeforeTheFirstWrite is the "status warns before it starts" criterion.
//
// The pass that first observes readiness admits NOTHING. That is not caution for its own sake: every
// admitted workload is a new deployment revision visible in the customer's own account, and this
// announcement is the only warning they get. Landing it in the same pass as the first write would make
// "before" a matter of statement ordering inside one function.
func TestTheRolloutIsAnnouncedBeforeTheFirstWrite(t *testing.T) {
	in := ready(fleet(30, 0))
	in.EverReady, in.ReadyNow = false, true

	announced := Decide(in)

	if announced.Phase != PhaseAnnounced {
		t.Fatalf("phase = %q, want announced", announced.Phase)
	}
	if len(announced.Admit) != 0 {
		t.Fatalf("the announcing pass admitted %v", announced.Admit)
	}
	problem := problemWith(t, announced.Problems, ProblemRolloutStarting)
	for _, want := range []string{"30 instrumented workload", "new deployment revision", "disabling"} {
		if !strings.Contains(problem.Message, want) {
			t.Errorf("the announcement does not mention %q: %s", want, problem.Message)
		}
	}

	// And the NEXT pass, with the latch recorded, begins.
	in.EverReady = true
	if rolling := Decide(in); len(rolling.Admit) == 0 {
		t.Error("the pass after the announcement admitted nothing, so the rollout would never start")
	}
}

// TestNothingIsAnnouncedWhenThereIsNothingToMove. An announcement on a network with no instrumented
// workloads is a fleet-wide rollout warning about a fleet of zero.
func TestNothingIsAnnouncedWhenThereIsNothingToMove(t *testing.T) {
	for name, in := range map[string]Input{
		"no workloads":     {Region: network, Enabled: true, Deployed: true, ReadyNow: true},
		"already complete": {Region: network, Enabled: true, Deployed: true, ReadyNow: true, Workloads: fleet(3, 3)},
		"not deployed":     {Region: network, Enabled: true, ReadyNow: true, Workloads: fleet(3, 0)},
		"disabled":         {Region: network, Deployed: true, ReadyNow: true, Workloads: fleet(3, 0)},
	} {
		t.Run(name, func(t *testing.T) {
			if got := Decide(in); got.Phase == PhaseAnnounced {
				t.Errorf("announced a rollout with nothing to roll: %+v", got)
			}
		})
	}
}

// TestTheRolloutIsBounded is the second acceptance criterion's first half.
//
// Every admitted workload is a rolling deployment, so a pass that admitted a whole fleet would put a
// customer's entire estate into deployment at once — and if the Collector turns out to be broken in a way
// readiness did not catch, the batch is what has to be walked back.
func TestTheRolloutIsBounded(t *testing.T) {
	got := Decide(ready(fleet(100, 0)))

	if len(got.Admit) != DefaultBatchSize {
		t.Fatalf("admitted %d of 100, want the default batch of %d", len(got.Admit), DefaultBatchSize)
	}
	if got.Phase != PhaseRollingOut {
		t.Errorf("phase = %q", got.Phase)
	}

	in := ready(fleet(100, 0))
	in.BatchSize = 3
	if got := Decide(in); len(got.Admit) != 3 {
		t.Errorf("a configured batch of 3 admitted %d", len(got.Admit))
	}
	// A batch of zero would never finish, so it cannot be turned off.
	in.BatchSize = 0
	if got := Decide(in); len(got.Admit) != DefaultBatchSize {
		t.Errorf("a batch size of zero admitted %d, want the default: a rollout that admitted nothing "+
			"would leave a network half-routed for ever", len(got.Admit))
	}
	in.BatchSize = -5
	if got := Decide(in); len(got.Admit) != DefaultBatchSize {
		t.Errorf("a negative batch size admitted %d, want the default", len(got.Admit))
	}
}

// TestTheRolloutIsResumableAndNeverDoubleWrites is the second half.
//
// There is no cursor to persist and nothing to resume: "which workloads are not yet routed" is a question
// about what is DEPLOYED. A connector that restarts mid-rollout re-reads it and carries on, and one that
// restarts after a workload was written but before anything recorded it sees that workload as routed and
// does not write it again.
func TestTheRolloutIsResumableAndNeverDoubleWrites(t *testing.T) {
	const total = 25
	workloads := fleet(total, 0)

	var passes int
	for {
		got := Decide(ready(workloads))
		if got.Phase == PhaseComplete {
			break
		}
		passes++
		if passes > total {
			t.Fatal("the rollout did not converge")
		}
		for _, admitted := range got.Admit {
			for i := range workloads {
				if workloads[i].Ref != admitted {
					continue
				}
				if workloads[i].Routed {
					t.Fatalf("%s was admitted twice", admitted)
				}
				workloads[i].Routed = true
			}
		}
	}
	if passes != 3 {
		t.Errorf("took %d passes for %d workloads at a batch of %d", passes, total, DefaultBatchSize)
	}

	// Converged: zero writes, for ever.
	for i := 0; i < 3; i++ {
		got := Decide(ready(workloads))
		if got.Moves() {
			t.Fatalf("a converged network still moves: %+v", got)
		}
		if got.Phase != PhaseComplete {
			t.Errorf("phase = %q, want complete", got.Phase)
		}
	}
}

// TestARestartMidRolloutResumesRatherThanRestarting: the decision is a function of what is deployed, so
// a fresh process with no memory makes the same one.
func TestARestartMidRolloutResumesRatherThanRestarting(t *testing.T) {
	workloads := fleet(25, 12)

	first := Decide(ready(workloads))
	// A brand new process, same facts.
	second := Decide(ready(workloads))

	if strings.Join(first.Admit, ",") != strings.Join(second.Admit, ",") {
		t.Fatalf("two readings of the same state admitted differently:\n%v\n%v", first.Admit, second.Admit)
	}
	for _, admitted := range first.Admit {
		for _, w := range workloads {
			if w.Ref == admitted && w.Routed {
				t.Errorf("%s is already routed and was admitted again", admitted)
			}
		}
	}
	if first.Routed != 12 || first.Pending != 13 {
		t.Errorf("counts = %d routed, %d pending; want the deployed state", first.Routed, first.Pending)
	}
}

// TestTheOrderIsStable. Two passes over the same fleet must take the same workloads, or a restart
// between them would start a second batch before the first finished.
func TestTheOrderIsStable(t *testing.T) {
	shuffled := []Workload{{Ref: "svc-z"}, {Ref: "svc-a"}, {Ref: "svc-m"}}
	in := ready(shuffled)
	in.BatchSize = 2

	got := Decide(in)

	if strings.Join(got.Admit, ",") != "svc-a,svc-m" {
		t.Errorf("admitted %v, want the first two in a stable order", got.Admit)
	}
}

// TestOneFailedWorkloadDoesNotStopTheOthers is the third acceptance criterion.
func TestOneFailedWorkloadDoesNotStopTheOthers(t *testing.T) {
	workloads := fleet(20, 0)
	workloads[7].Failed = "RegisterTaskDefinition denied"

	got := Decide(ready(workloads))

	if got.Phase != PhaseRollingOut {
		t.Fatalf("phase = %q; one workload's failure is not a failed network", got.Phase)
	}
	if len(got.Admit) != DefaultBatchSize {
		t.Fatalf("admitted %d, want a full batch — a failure must not consume another workload's place",
			len(got.Admit))
	}
	// The size alone held when failures could fill the whole batch; what is IN it is the property.
	if got.Admit[0] != workloads[7].Ref {
		t.Errorf("admitted %v, want the failed workload retried first", got.Admit)
	}
	for _, admitted := range got.Admit[1:] {
		if admitted == workloads[7].Ref {
			t.Errorf("the failed workload was admitted twice: %v", got.Admit)
		}
	}
	if got.Failed != 1 {
		t.Errorf("failed = %d, want 1", got.Failed)
	}
	// Reported per workload, naming it and the reason. A rollout that reported this as a failed NETWORK
	// would hide that the other nineteen are fine, and send whoever reads it to the Collector rather
	// than to the one service whose task definition could not be registered.
	problem := problemWith(t, got.Problems, ProblemRolloutWorkloadFailed)
	if problem.Severity != collectorstatus.SeverityDegraded {
		t.Errorf("severity = %q, want Degraded", problem.Severity)
	}
	for _, want := range []string{workloads[7].Ref, "RegisterTaskDefinition denied", "do not hold up the rest"} {
		if !strings.Contains(problem.Message, want) {
			t.Errorf("the message does not contain %q: %s", want, problem.Message)
		}
	}
}

// TestAFailedWorkloadIsRetriedFirst. Putting failures last would report one once and then not touch it
// again until every other workload on the network was done — which on a large network is a workload
// silently exporting directly for a very long time.
func TestAFailedWorkloadIsRetriedFirst(t *testing.T) {
	workloads := fleet(50, 0)
	workloads[40].Failed = "throttled"
	in := ready(workloads)
	in.BatchSize = 2

	got := Decide(in)

	if len(got.Admit) == 0 || got.Admit[0] != workloads[40].Ref {
		t.Fatalf("admitted %v, want the failed workload first", got.Admit)
	}
}

// TestAWholeNetworkOfFailuresStillConverges. Every workload failing is the shape of a Collector that is
// reachable and refusing, and it must keep retrying rather than latching into a terminal state — the
// cause is usually transient (a throttle, a quota) and nothing else will retry it.
func TestAWholeNetworkOfFailuresStillConverges(t *testing.T) {
	workloads := fleet(5, 0)
	for i := range workloads {
		workloads[i].Failed = "throttled"
	}

	got := Decide(ready(workloads))

	if len(got.Admit) != 5 {
		t.Fatalf("admitted %d of 5 failed workloads, want all of them retried", len(got.Admit))
	}
	if got.Phase != PhaseRollingOut {
		t.Errorf("phase = %q", got.Phase)
	}
}

// failing builds n workloads whose last attempt failed, named so they sort AHEAD of fleet's refs — the
// arrangement that starved the rest when failures could fill the batch. Each failed a second apart, the
// first longest ago.
func failing(n int) []Workload {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	out := make([]Workload, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, Workload{Ref: fmt.Sprintf("aaa-%02d", i), Failed: "AccessDenied",
			FailedAt: start.Add(time.Duration(i) * time.Second)})
	}
	return out
}

func admittedFrom(admit []string, workloads []Workload) int {
	in := map[string]bool{}
	for _, w := range workloads {
		in[w.Ref] = true
	}
	n := 0
	for _, ref := range admit {
		if in[ref] {
			n++
		}
	}
	return n
}

// TestFailuresCannotStarveTheRest. When failures went first and the batch was cut after them, a region
// with BatchSize failures that never cleared re-admitted the same failures on every pass and never
// reached anything else — a rollout that read as in progress for ever. Failures get at most half.
func TestFailuresCannotStarveTheRest(t *testing.T) {
	stuck := failing(12)
	fresh := fleet(5, 0)

	got := Decide(ready(append(append([]Workload{}, stuck...), fresh...)))

	if len(got.Admit) != DefaultBatchSize {
		t.Fatalf("admitted %d, want a full batch of %d", len(got.Admit), DefaultBatchSize)
	}
	if n := admittedFrom(got.Admit, fresh); n != 5 {
		t.Errorf("admitted %d of 5 never-attempted workloads, want all of them: %v", n, got.Admit)
	}
	if n := admittedFrom(got.Admit, stuck); n != DefaultBatchSize/2 {
		t.Errorf("admitted %d failures, want half the batch: %v", n, got.Admit)
	}
	if got.Admit[0] != stuck[0].Ref {
		t.Errorf("admitted %v, want failures still retried first", got.Admit)
	}
}

// TestARolloutStuckOnFailuresStillFinishesTheRest runs it to the end: failures that never clear, more of
// them than the batch, and a network behind them that must still all be moved.
func TestARolloutStuckOnFailuresStillFinishesTheRest(t *testing.T) {
	stuck := failing(15)
	fresh := fleet(40, 0)
	workloads := append(append([]Workload{}, stuck...), fresh...)
	now := stuck[len(stuck)-1].FailedAt

	for pass := 1; ; pass++ {
		if pass > len(fresh) {
			t.Fatal("the never-attempted workloads were never all moved")
		}
		got := Decide(ready(workloads))
		if len(got.Admit) > DefaultBatchSize {
			t.Fatalf("pass %d admitted %d, over the batch of %d", pass, len(got.Admit), DefaultBatchSize)
		}
		if got.Pending == 0 {
			break
		}
		if admittedFrom(got.Admit, fresh) == 0 {
			t.Fatalf("pass %d admitted no never-attempted workload while %d were waiting", pass, got.Pending)
		}
		now = now.Add(time.Minute)
		admitted := map[string]bool{}
		for _, ref := range got.Admit {
			admitted[ref] = true
		}
		for i := range workloads {
			switch {
			case !admitted[workloads[i].Ref]:
			case workloads[i].Failed != "":
				workloads[i].FailedAt = now // failed again
			default:
				workloads[i].Routed = true
			}
		}
	}
}

// TestAShareTheRestCannotUseGoesToFailures. The cap protects never-attempted workloads; when there are too
// few of them to fill their half, holding the slots empty would only slow the retries down.
func TestAShareTheRestCannotUseGoesToFailures(t *testing.T) {
	stuck := failing(12)
	fresh := fleet(2, 0)

	got := Decide(ready(append(append([]Workload{}, stuck...), fresh...)))

	if len(got.Admit) != DefaultBatchSize {
		t.Fatalf("admitted %d, want a full batch", len(got.Admit))
	}
	if n := admittedFrom(got.Admit, fresh); n != 2 {
		t.Errorf("admitted %d of 2 never-attempted workloads", n)
	}
}

// TestFailuresTakeTurns. With more failures than a pass retries, retrying the same ones by name every time
// would leave the rest — some of which would now succeed — exporting directly for as long as the first
// ones keep failing. Oldest failure first means the ones not retried this pass go next.
func TestFailuresTakeTurns(t *testing.T) {
	workloads := failing(12)
	// Listed newest first, so the order comes from FailedAt rather than from the input.
	for i, j := 0, len(workloads)-1; i < j; i, j = i+1, j-1 {
		workloads[i], workloads[j] = workloads[j], workloads[i]
	}

	first := Decide(ready(workloads))
	if strings.Join(first.Admit, ",") != strings.Join(refsOf(failing(10)), ",") {
		t.Fatalf("admitted %v, want the ten oldest failures, oldest first", first.Admit)
	}

	retried := map[string]bool{}
	for _, ref := range first.Admit {
		retried[ref] = true
	}
	for i := range workloads {
		if retried[workloads[i].Ref] {
			workloads[i].FailedAt = workloads[i].FailedAt.Add(time.Hour)
		}
	}
	second := Decide(ready(workloads))
	if len(second.Admit) < 2 || second.Admit[0] != "aaa-10" || second.Admit[1] != "aaa-11" {
		t.Errorf("admitted %v, want the two failures the last pass did not retry to go first", second.Admit)
	}
}

func refsOf(workloads []Workload) []string {
	out := make([]string, 0, len(workloads))
	for _, w := range workloads {
		out = append(out, w.Ref)
	}
	return out
}

// TestABatchOfOneStillReachesTheRest. A single slot cannot be shared, and giving it to a failure that
// never clears would stall the network exactly as before, so the never-attempted go first.
func TestABatchOfOneStillReachesTheRest(t *testing.T) {
	in := ready(append(failing(3), fleet(2, 0)...))
	in.BatchSize = 1

	got := Decide(in)
	if len(got.Admit) != 1 || got.Admit[0] != ref(0) {
		t.Fatalf("admitted %v, want the first never-attempted workload", got.Admit)
	}

	// Once everything has been attempted the slot goes back to the failures.
	in = ready(failing(3))
	in.BatchSize = 1
	if got := Decide(in); len(got.Admit) != 1 || got.Admit[0] != "aaa-00" {
		t.Errorf("admitted %v, want the oldest failure", got.Admit)
	}
}

// TestDisablingTheCollectorRestoresDirectExport is the fourth acceptance criterion.
func TestDisablingTheCollectorRestoresDirectExport(t *testing.T) {
	in := ready(fleet(30, 30))
	in.Enabled = false

	got := Decide(in)

	if got.Phase != PhaseRollingBack {
		t.Fatalf("phase = %q, want rolling back", got.Phase)
	}
	if len(got.Restore) != 30 {
		t.Fatalf("restored %d of 30", len(got.Restore))
	}
	if len(got.Admit) != 0 {
		t.Errorf("a rollback admitted %v", got.Admit)
	}
	problem := problemWith(t, got.Problems, ProblemRollingBack)
	if !strings.Contains(problem.Message, "turned off") {
		t.Errorf("the message does not say why: %s", problem.Message)
	}
}

// TestARollbackIsNotBatched. Batching a cutover bounds the blast radius of a change that might be wrong;
// batching a rollback would leave workloads pointing at a Collector somebody has just turned off,
// dropping telemetry for as many passes as the fleet is large. The two directions have opposite risk.
func TestARollbackIsNotBatched(t *testing.T) {
	in := ready(fleet(500, 500))
	in.Enabled = false
	in.BatchSize = 2

	if got := Decide(in); len(got.Restore) != 500 {
		t.Fatalf("restored %d of 500 with a batch size of 2; a rollback is not batched", len(got.Restore))
	}
}

// TestALostEndpointRollsBackToo. A Collector whose endpoint went away — its load balancer deleted, its
// deploy failed after it had been serving — is as unreachable as one that was turned off, and the
// workloads pointed at it are dropping telemetry either way.
func TestALostEndpointRollsBackToo(t *testing.T) {
	in := ready(fleet(4, 4))
	in.Deployed = false

	got := Decide(in)

	if got.Phase != PhaseRollingBack || len(got.Restore) != 4 {
		t.Fatalf("phase = %q, restored %d", got.Phase, len(got.Restore))
	}
	if problem := problemWith(t, got.Problems, ProblemRollingBack); !strings.Contains(problem.Message, "no endpoint") {
		t.Errorf("the message does not distinguish this from a Collector somebody turned off: %s", problem.Message)
	}
}

// TestARollbackConvergesAndThenGoesQuiet. Once every workload is back on direct export the network is
// PhaseDirect: no writes, and nothing reported, because a network with no Collector is not a condition.
func TestARollbackConvergesAndThenGoesQuiet(t *testing.T) {
	in := ready(fleet(4, 0))
	in.Enabled = false

	got := Decide(in)

	if got.Phase != PhaseDirect {
		t.Fatalf("phase = %q, want direct", got.Phase)
	}
	if got.Moves() {
		t.Errorf("a settled direct-export network still moves: %+v", got)
	}
	if len(got.Problems) != 0 {
		t.Errorf("problems = %v, want silence: exporting directly is what a network does before any "+
			"Collector exists, and reporting it would put a standing finding on every network", got.Problems)
	}
}

// TestAnUnreadyCollectorIsNotARollback. A Collector that has never been ready has no workloads on it, so
// there is nothing to restore — and reporting a rollback would send whoever reads it looking for
// workloads that were never moved.
func TestAnUnreadyCollectorIsNotARollback(t *testing.T) {
	in := ready(fleet(6, 0))
	in.EverReady = false

	got := Decide(in)

	if len(got.Restore) != 0 || hasCode(got.Problems, ProblemRollingBack) {
		t.Errorf("an unready Collector reported a rollback: %+v", got)
	}
}

// TestACompleteRolloutSaysSo, on every pass. A fleet that silently finished rolling looks identical to
// one that silently never started, and the message is where "disabling restores direct export" is stated
// to somebody who was not watching when it began.
func TestACompleteRolloutSaysSo(t *testing.T) {
	got := Decide(ready(fleet(12, 12)))

	if got.Phase != PhaseComplete {
		t.Fatalf("phase = %q", got.Phase)
	}
	problem := problemWith(t, got.Problems, ProblemRolloutComplete)
	if !strings.Contains(problem.Message, "12") || !strings.Contains(problem.Message, "Disabling") {
		t.Errorf("the message does not state the count and the way back: %s", problem.Message)
	}
	if problem.Severity != collectorstatus.SeverityInfo {
		t.Errorf("severity = %q, want Info", problem.Severity)
	}
}

// TestProgressIsReportedWhileItRuns, with the counts a reader needs to tell a running rollout from a
// stuck one.
func TestProgressIsReportedWhileItRuns(t *testing.T) {
	workloads := fleet(40, 15)
	workloads[39].Failed = "throttled"

	got := Decide(ready(workloads))

	if got.Routed != 15 || got.Failed != 1 || got.Pending != 24 {
		t.Fatalf("counts = %d/%d/%d routed/failed/pending", got.Routed, got.Failed, got.Pending)
	}
	if got.Total() != 40 {
		t.Errorf("total = %d, want 40", got.Total())
	}
	problem := problemWith(t, got.Problems, ProblemRolloutInProgress)
	for _, want := range []string{"15 of 40", "keep exporting straight to the destination"} {
		if !strings.Contains(problem.Message, want) {
			t.Errorf("the progress message does not contain %q: %s", want, problem.Message)
		}
	}
}

// TestAnEmptyNetworkIsDirect. No instrumented workloads, so nothing to move and nothing to say.
func TestAnEmptyNetworkIsDirect(t *testing.T) {
	got := Decide(ready(nil))

	if got.Phase != PhaseDirect || got.Moves() || len(got.Problems) != 0 {
		t.Errorf("an empty network produced %+v", got)
	}
}

// TestTheAnnouncementIsNotSkippableByACaller.
//
// There is one entry point, and the announcing pass is a branch inside it rather than a function a
// caller chooses. Choosing wrongly would be silent — calling a "rolling" variant on the pass that
// discovered the Collector works skips the only warning a customer gets — so the choice is not offered.
//
// What this asserts is the consequence: from a cold start, no combination of inputs reaches PhaseRollingOut
// without a PhaseAnnounced pass in front of it.
func TestTheAnnouncementIsNotSkippableByACaller(t *testing.T) {
	in := Input{Region: network, Enabled: true, Deployed: true, Workloads: fleet(9, 0)}

	// Not ready yet: blocked, and nothing said about a rollout starting.
	if got := Decide(in); got.Phase != PhaseBlocked || hasCode(got.Problems, ProblemRolloutStarting) {
		t.Fatalf("a Collector that has never been ready: %+v", got)
	}

	// The pass that finds it ready announces, and moves nothing.
	in.ReadyNow = true
	announced := Decide(in)
	if announced.Phase != PhaseAnnounced || announced.Moves() {
		t.Fatalf("the discovering pass: phase %q, moves %v", announced.Phase, announced.Moves())
	}

	// Only with the latch recorded does anything move.
	in.EverReady = true
	if rolling := Decide(in); rolling.Phase != PhaseRollingOut || len(rolling.Admit) == 0 {
		t.Fatalf("the pass after the announcement: %+v", rolling)
	}
}

// TestReadyNowAloneNeverMovesAnything. The latch is what opens the gate, and it is written by a different
// party on a different plane — the OpAMP session, from the config plane. A decision that acted on this
// pass's observation alone would re-point a fleet on the strength of a single healthy report, which is
// the health-following behaviour exists to prevent.
func TestReadyNowAloneNeverMovesAnything(t *testing.T) {
	in := Input{Region: network, Enabled: true, Deployed: true, ReadyNow: true, Workloads: fleet(9, 0)}

	if got := Decide(in); got.Moves() {
		t.Fatalf("acted on a single ready observation: %+v", got)
	}
}
