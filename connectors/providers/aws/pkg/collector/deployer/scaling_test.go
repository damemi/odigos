package deployer

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	aastypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorsettings"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

const testServiceARN = "arn:aws:ecs:eu-west-1:123456789012:service/odigos-collector/odigos-collector-eu-west-1"

// testResourceID is Application Auto Scaling's address for the test Collector's service.
var testResourceID = ScalableTargetResourceID(clusterName, testRegion)

// scalingInput is a converged automatic-mode policy: the §8.3 defaults over a service that exists and
// is recorded.
func scalingInput() ScalingInput {
	return ScalingInput{
		HostNetworkID: testVPC,
		Region:        testRegion,
		Connector:     testConnector,
		ConnectorID:   testUID,
		Identity:      testIdentity(),
		Mode:          spec.ModeAutomatic,
		ClusterName:   clusterName,
		ServiceARN:    testServiceARN,
		Scaling: collectorsettings.Scaling{
			Mode: collectorsettings.ScalingAuto, MinCount: 2, MaxCount: 10,
			TargetCPUPercent: 70, TargetMemoryPercent: 75,
		}.Resolve(),
		Recorded: true,
	}
}

// manualInput is the same Collector with a fixed replica count.
func manualInput() ScalingInput {
	in := scalingInput()
	in.Scaling = collectorsettings.Scaling{Mode: collectorsettings.ScalingManual, DesiredCount: 2}.Resolve()
	return in
}

// --- manual mode: a fixed count and no autoscaling resource (§8.2) ---

// TestManualScalingCreatesNoAutoscalingResource: the steady state for every Collector that has never
// been in `auto`. One describe, no writes — a manual-mode Collector must not accumulate an autoscaling
// object it does not use.
func TestManualScalingCreatesNoAutoscalingResource(t *testing.T) {
	api := newFakeAutoScaling()
	got, err := EnsureScaling(context.Background(), api, manualInput())
	if err != nil {
		t.Fatalf("EnsureScaling: %v", err)
	}
	if got.TargetRegistered || got.Registered || got.Deregistered || len(got.PoliciesWritten) > 0 {
		t.Fatalf("manual mode touched an autoscaling resource: %+v", got)
	}
	if writes := api.writes(); len(writes) > 0 {
		t.Fatalf("manual mode wrote %v", writes)
	}
	if len(got.Problems) > 0 {
		t.Errorf("manual mode reported %v", codes(got.Problems))
	}
}

// TestSwitchingToManualDeregistersTheTarget is the "correct mode cleanup" criterion. A switch of modes
// has to CONVERGE rather than layer: leaving the target behind means a fixed desiredCount and an
// autoscaler both moving one replica count, and the autoscaler wins.
func TestSwitchingToManualDeregistersTheTarget(t *testing.T) {
	api := newFakeAutoScaling().withTarget(testResourceID, 2, 10, ourTags(testVPC))
	api.withOdigosPolicies(testResourceID, scalingInput())

	got, err := EnsureScaling(context.Background(), api, manualInput())
	if err != nil {
		t.Fatalf("EnsureScaling: %v", err)
	}
	if !got.Deregistered || got.TargetRegistered {
		t.Fatalf("the target was not deregistered: %+v", got)
	}
	if api.target(testResourceID) != nil {
		t.Error("the scalable target is still registered")
	}
	// Deregistering takes the policies with it, which is why this converge path needs no per-policy
	// delete at all. If that ever stopped being true, the policies would keep scaling a service whose
	// bounds no longer exist.
	if len(api.policies[testResourceID]) > 0 {
		t.Errorf("policies survived the deregistration: %v", api.policies[testResourceID])
	}
	// One destructive call, and nothing else.
	if writes := api.writes(); len(writes) != 1 || !strings.HasPrefix(writes[0], "DeregisterScalableTarget:") {
		t.Errorf("writes = %v", writes)
	}
}

// TestSwitchingToManualLeavesATargetOdigosDidNotRegister. The realistic way one exists is manual (IaC)
// mode: the customer's own template registered a target with their own bounds. Deregistering it on a
// mode change would destroy working infrastructure Odigos never created — and Application Auto Scaling
// would take that template's policies down with it.
func TestSwitchingToManualLeavesATargetOdigosDidNotRegister(t *testing.T) {
	cases := map[string]map[string]string{
		"an untagged target":                    nil,
		"a target tagged for another connector": baseapi.CollectorManagedTags("other", "uid-2", testRegion, testVPC),
		"a collector target naming no connector": {
			baseapi.TagComponent: baseapi.ComponentCollector,
		},
	}
	for name, tags := range cases {
		t.Run(name, func(t *testing.T) {
			api := newFakeAutoScaling().withTarget(testResourceID, 5, 50, tags)

			got, err := EnsureScaling(context.Background(), api, manualInput())
			if err != nil {
				t.Fatalf("EnsureScaling: %v", err)
			}
			if got.Deregistered {
				t.Fatal("deregistered a target Odigos did not register")
			}
			if writes := api.writes(); len(writes) > 0 {
				t.Fatalf("writes = %v", writes)
			}
			if api.target(testResourceID) == nil {
				t.Fatal("the customer's target is gone")
			}
			// Reported, and reported as still governing the count: a fixed desiredCount alongside
			// somebody else's autoscaler is a contradiction only the customer can resolve.
			if !got.TargetRegistered {
				t.Error("the surviving target is not reported as governing the count")
			}
			problem := problemWith(t, got.Problems, ProblemScalingTargetNotOurs)
			if !strings.Contains(problem.Message, "desiredCount") {
				t.Errorf("the message does not say what the conflict is: %q", problem.Message)
			}
			// Degraded, not Error. The Collector is running and something is scaling it, just not
			// Odigos's configuration — an Error would read as "nothing was deployed" and would take
			// ScalingState.Ready down with it.
			if problem.Severity != "Degraded" {
				t.Errorf("severity = %q, want Degraded", problem.Severity)
			}
			if !got.Ready() {
				t.Error("a target Odigos left alone made the scaling state unready")
			}
		})
	}
}

// TestDeregisteringNeedsBothRecords: a deregistration is a delete, so it takes the ownership tag AND the
// state row, exactly as every other delete in this deployer does (§10.4). Losing the database loses the
// authority rather than the safety — the target is left standing and reported.
func TestDeregisteringNeedsBothRecords(t *testing.T) {
	api := newFakeAutoScaling().withTarget(testResourceID, 2, 10, ourTags(testVPC))
	in := manualInput()
	in.Recorded = false

	got, err := EnsureScaling(context.Background(), api, in)
	if err != nil {
		t.Fatalf("EnsureScaling: %v", err)
	}
	if got.Deregistered {
		t.Fatal("deregistered a target this connector's state does not name")
	}
	problem := problemWith(t, got.Problems, ProblemScalingTargetNotOurs)
	if !strings.Contains(problem.Message, "state") {
		t.Errorf("the message does not say the state record is what is missing: %q", problem.Message)
	}
}

// --- automatic mode: a bounded target and the two policies (§8.3) ---

// TestAutomaticScalingRegistersABoundedTarget.
func TestAutomaticScalingRegistersABoundedTarget(t *testing.T) {
	api := newFakeAutoScaling()
	in := scalingInput()

	got, err := EnsureScaling(context.Background(), api, in)
	if err != nil {
		t.Fatalf("EnsureScaling: %v", err)
	}
	if !got.Registered || !got.TargetRegistered || !got.Ready() {
		t.Fatalf("state = %+v / %v", got, codes(got.Problems))
	}
	target := api.target(testResourceID)
	if target == nil {
		t.Fatalf("no target registered: %v", api.calls)
	}
	if aws.ToInt32(target.MinCapacity) != 2 || aws.ToInt32(target.MaxCapacity) != 10 {
		t.Errorf("bounds = [%d,%d], want [2,10]",
			aws.ToInt32(target.MinCapacity), aws.ToInt32(target.MaxCapacity))
	}
	if target.ScalableDimension != aastypes.ScalableDimensionECSServiceDesiredCount {
		t.Errorf("dimension = %q", target.ScalableDimension)
	}
	// Tagged as Odigos-created on the call that creates it — the first half of the deletion gate, and
	// what makes the switch back to manual able to clean up after itself.
	parsed := baseapi.ParseCollectorTags(api.tags[scalableTargetARN(testResourceID)])
	if !parsed.IsCollector() || !parsed.HasOwnershipMarker() || parsed.Network != testVPC {
		t.Errorf("target tags = %v", api.tags[scalableTargetARN(testResourceID)])
	}
	// Both policies, named after the service so a console listing shows them beside it.
	cpu, memory := ScalingPolicyNames(testRegion)
	for _, name := range []string{cpu, memory} {
		if api.policy(testResourceID, name) == nil {
			t.Errorf("no %s policy: %v", name, api.calls)
		}
	}
	if len(got.PoliciesWritten) != 2 {
		t.Errorf("policies written = %v", got.PoliciesWritten)
	}
}

// TestBothPoliciesCarryTheAsymmetricCooldowns is the cooldown contract. A Collector under pressure is
// dropping spans, so it has to be able to scale out again a minute later; one that scaled in too eagerly
// drops them again on the next spike, so scaling in waits five times as long. The two numbers being the
// same way round is the whole design.
func TestBothPoliciesCarryTheAsymmetricCooldowns(t *testing.T) {
	if scaleOutCooldown >= scaleInCooldown {
		t.Fatalf("scale-out cooldown %ds is not shorter than scale-in %ds: a Collector that scaled in "+
			"eagerly drops data on the next spike", scaleOutCooldown, scaleInCooldown)
	}

	api := newFakeAutoScaling()
	if _, err := EnsureScaling(context.Background(), api, scalingInput()); err != nil {
		t.Fatalf("EnsureScaling: %v", err)
	}

	cpu, memory := ScalingPolicyNames(testRegion)
	if cpu == memory {
		t.Fatal("the two policies share a name, so there is only one")
	}
	want := map[string]struct {
		metric aastypes.MetricType
		target float64
	}{
		cpu:    {aastypes.MetricTypeECSServiceAverageCPUUtilization, 70},
		memory: {aastypes.MetricTypeECSServiceAverageMemoryUtilization, 75},
	}
	for name, expect := range want {
		policy := api.policy(testResourceID, name)
		if policy == nil {
			t.Fatalf("no %s policy", name)
		}
		if policy.PolicyType != aastypes.PolicyTypeTargetTrackingScaling {
			t.Errorf("%s type = %q", name, policy.PolicyType)
		}
		cfg := policy.TargetTrackingScalingPolicyConfiguration
		if cfg == nil || cfg.PredefinedMetricSpecification == nil {
			t.Fatalf("%s has no target-tracking configuration", name)
		}
		if cfg.PredefinedMetricSpecification.PredefinedMetricType != expect.metric {
			t.Errorf("%s metric = %q, want %q", name, cfg.PredefinedMetricSpecification.PredefinedMetricType, expect.metric)
		}
		if aws.ToFloat64(cfg.TargetValue) != expect.target {
			t.Errorf("%s target = %v, want %v", name, aws.ToFloat64(cfg.TargetValue), expect.target)
		}
		if aws.ToInt32(cfg.ScaleOutCooldown) != scaleOutCooldown || aws.ToInt32(cfg.ScaleInCooldown) != scaleInCooldown {
			t.Errorf("%s cooldowns = %d/%d, want %d/%d", name,
				aws.ToInt32(cfg.ScaleOutCooldown), aws.ToInt32(cfg.ScaleInCooldown),
				scaleOutCooldown, scaleInCooldown)
		}
		// Scale-in enabled on BOTH, which is why there are two policies: Application Auto Scaling grants
		// the largest capacity either asks for, so the pair scales out on whichever resource saturates
		// first and scales in only when both agree. Disabled on either one turns that into a fleet that
		// can only grow.
		if aws.ToBool(cfg.DisableScaleIn) {
			t.Errorf("%s has scale-in disabled, so the Collector could never scale back in", name)
		}
	}
}

// TestASecondScalingPassWritesNothing. Steady state is zero writes, and here that matters twice over:
// RegisterScalableTarget moves the current capacity inside the bounds immediately, so a register on
// every pass would restart tasks whenever the count sat outside them.
func TestASecondScalingPassWritesNothing(t *testing.T) {
	api := newFakeAutoScaling()
	in := scalingInput()
	if _, err := EnsureScaling(context.Background(), api, in); err != nil {
		t.Fatalf("first pass: %v", err)
	}

	for pass := 2; pass <= 5; pass++ {
		api.calls = nil
		got, err := EnsureScaling(context.Background(), api, in)
		if err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		if writes := api.writes(); len(writes) > 0 {
			t.Fatalf("pass %d wrote %v", pass, writes)
		}
		if got.Registered || got.Deregistered || len(got.PoliciesWritten) > 0 {
			t.Fatalf("pass %d reported work: %+v", pass, got)
		}
		if !got.TargetRegistered || got.MinCount != 2 || got.MaxCount != 10 {
			t.Fatalf("pass %d lost the state: %+v", pass, got)
		}
	}
}

// TestDriftedBoundsAreConvergedWithoutStampingOwnership. The bounds are Odigos's to converge on a target
// Odigos registered — but the update must carry NO tags: RegisterScalableTarget is create-or-update, and
// a tag written on the update path would stamp the ownership marker onto whatever it was called on.
func TestDriftedBoundsAreConvergedWithoutStampingOwnership(t *testing.T) {
	api := newFakeAutoScaling().withTarget(testResourceID, 1, 4, ourTags(testVPC))
	api.withOdigosPolicies(testResourceID, scalingInput())

	got, err := EnsureScaling(context.Background(), api, scalingInput())
	if err != nil {
		t.Fatalf("EnsureScaling: %v", err)
	}
	if !got.Registered {
		t.Fatalf("drifted bounds were not converged: %+v", got)
	}
	target := api.target(testResourceID)
	if aws.ToInt32(target.MinCapacity) != 2 || aws.ToInt32(target.MaxCapacity) != 10 {
		t.Errorf("bounds = [%d,%d]", aws.ToInt32(target.MinCapacity), aws.ToInt32(target.MaxCapacity))
	}
	for _, call := range api.calls {
		if strings.HasPrefix(call, "RegisterScalableTarget:") && !strings.HasSuffix(call, "tags=") {
			t.Errorf("the bounds update carried tags: %q", call)
		}
	}
	// And only the bounds: the policies already matched, so nothing was rewritten.
	if len(got.PoliciesWritten) > 0 {
		t.Errorf("policies rewritten alongside the bounds: %v", got.PoliciesWritten)
	}
}

// TestAPolicyEditedByHandIsConverged, field by field. The cooldowns are the ones that matter: they are
// the difference between a Collector that recovers from a spike and one that flaps, and the console is
// exactly where they get changed.
func TestAPolicyEditedByHandIsConverged(t *testing.T) {
	cases := map[string]func(*aastypes.TargetTrackingScalingPolicyConfiguration){
		"a different target":        func(c *aastypes.TargetTrackingScalingPolicyConfiguration) { c.TargetValue = aws.Float64(30) },
		"a shortened scale-in wait": func(c *aastypes.TargetTrackingScalingPolicyConfiguration) { c.ScaleInCooldown = aws.Int32(0) },
		"a lengthened scale-out wait": func(c *aastypes.TargetTrackingScalingPolicyConfiguration) {
			c.ScaleOutCooldown = aws.Int32(900)
		},
		"scale-in disabled": func(c *aastypes.TargetTrackingScalingPolicyConfiguration) { c.DisableScaleIn = aws.Bool(true) },
		"a different metric": func(c *aastypes.TargetTrackingScalingPolicyConfiguration) {
			c.PredefinedMetricSpecification = &aastypes.PredefinedMetricSpecification{
				PredefinedMetricType: aastypes.MetricTypeALBRequestCountPerTarget,
			}
		},
		"no configuration at all": nil,
	}
	cpu, _ := ScalingPolicyNames(testRegion)
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := scalingInput()
			api := newFakeAutoScaling().withTarget(testResourceID, 2, 10, ourTags(testVPC))
			api.withOdigosPolicies(testResourceID, in)

			edited := api.policy(testResourceID, cpu)
			if mutate == nil {
				edited.TargetTrackingScalingPolicyConfiguration = nil
			} else {
				mutate(edited.TargetTrackingScalingPolicyConfiguration)
			}

			got, err := EnsureScaling(context.Background(), api, in)
			if err != nil {
				t.Fatalf("EnsureScaling: %v", err)
			}
			if len(got.PoliciesWritten) != 1 || got.PoliciesWritten[0] != cpu {
				t.Fatalf("policies written = %v, want just %s", got.PoliciesWritten, cpu)
			}
			// Converged back to the contract, not merely rewritten.
			cfg := api.policy(testResourceID, cpu).TargetTrackingScalingPolicyConfiguration
			if aws.ToFloat64(cfg.TargetValue) != 70 ||
				aws.ToInt32(cfg.ScaleOutCooldown) != scaleOutCooldown ||
				aws.ToInt32(cfg.ScaleInCooldown) != scaleInCooldown ||
				aws.ToBool(cfg.DisableScaleIn) ||
				cfg.PredefinedMetricSpecification.PredefinedMetricType != aastypes.MetricTypeECSServiceAverageCPUUtilization {
				t.Errorf("policy after converge = %+v", cfg)
			}
		})
	}
}

// TestATargetOdigosDidNotRegisterIsNotReconfigured is the deliberate half of the design rather than a
// missing feature. A customer's own template registered bounds their capacity planning chose; flipping
// that install to automatic mode must not quietly replace them with Odigos's defaults, and Odigos's own
// policies pushed onto their target would be a third control on one replica count.
func TestATargetOdigosDidNotRegisterIsNotReconfigured(t *testing.T) {
	api := newFakeAutoScaling().withTarget(testResourceID, 5, 50, nil)

	got, err := EnsureScaling(context.Background(), api, scalingInput())
	if err != nil {
		t.Fatalf("EnsureScaling: %v", err)
	}
	if writes := api.writes(); len(writes) > 0 {
		t.Fatalf("wrote to a target Odigos did not register: %v", writes)
	}
	// Not even the policies — the policy IS a write to that target.
	if len(got.PoliciesWritten) > 0 || len(api.policies[testResourceID]) > 0 {
		t.Errorf("policies were pushed onto the customer's target: %v", got.PoliciesWritten)
	}
	target := api.target(testResourceID)
	if aws.ToInt32(target.MinCapacity) != 5 || aws.ToInt32(target.MaxCapacity) != 50 {
		t.Errorf("the customer's bounds were rewritten to [%d,%d]",
			aws.ToInt32(target.MinCapacity), aws.ToInt32(target.MaxCapacity))
	}
	// The bounds actually in force are reported, not the configured ones, because that is the difference
	// the customer has to see.
	if got.MinCount != 5 || got.MaxCount != 50 {
		t.Errorf("reported bounds = [%d,%d], want the ones in force", got.MinCount, got.MaxCount)
	}
	problem := problemWith(t, got.Problems, ProblemScalingTargetNotOurs)
	if !strings.Contains(problem.Message, "5 to 50") || !strings.Contains(problem.Message, "minCount") {
		t.Errorf("the message names neither the bounds in force nor the setting: %q", problem.Message)
	}
	if problem.Severity != "Degraded" || !got.Ready() {
		t.Errorf("severity = %q / ready = %v, want Degraded and ready: the Collector is scaling, "+
			"just not from Odigos's bounds", problem.Severity, got.Ready())
	}
}

// TestATargetWithNoArnCannotBeProvedOurs. The ARN is how the tags are read, so a target without one
// cannot be attributed — and an unattributable object is refused in both directions, which is the safe
// answer for a field an older API version may simply not populate.
func TestATargetWithNoArnCannotBeProvedOurs(t *testing.T) {
	for name, in := range map[string]ScalingInput{"automatic": scalingInput(), "manual": manualInput()} {
		t.Run(name, func(t *testing.T) {
			api := newFakeAutoScaling().withTarget(testResourceID, 2, 10, ourTags(testVPC))
			api.omitTargetARN = true

			got, err := EnsureScaling(context.Background(), api, in)
			if err != nil {
				t.Fatalf("EnsureScaling: %v", err)
			}
			if writes := api.writes(); len(writes) > 0 {
				t.Fatalf("wrote to a target whose ownership could not be read: %v", writes)
			}
			problemWith(t, got.Problems, ProblemScalingTargetNotOurs)
		})
	}
}

// TestAForeignPolicyOnOurTargetIsReported: "no overlapping controls". Application Auto Scaling grants
// the largest capacity ANY policy asks for, so a third policy raises the task count independently of
// what Odigos configured. Reported rather than removed — it is not Odigos's to delete.
func TestAForeignPolicyOnOurTargetIsReported(t *testing.T) {
	in := scalingInput()
	api := newFakeAutoScaling().withTarget(testResourceID, 2, 10, ourTags(testVPC))
	api.withOdigosPolicies(testResourceID, in)
	api.withPolicy(testResourceID, aastypes.ScalingPolicy{
		PolicyName: aws.String("customers-own-burst-policy"),
		PolicyType: aastypes.PolicyTypeTargetTrackingScaling,
		TargetTrackingScalingPolicyConfiguration: &aastypes.TargetTrackingScalingPolicyConfiguration{
			TargetValue: aws.Float64(20),
		},
	})

	got, err := EnsureScaling(context.Background(), api, in)
	if err != nil {
		t.Fatalf("EnsureScaling: %v", err)
	}
	problem := problemWith(t, got.Problems, ProblemScalingPolicyForeign)
	if !strings.Contains(problem.Message, "customers-own-burst-policy") {
		t.Errorf("the message does not name the policy: %q", problem.Message)
	}
	if api.policy(testResourceID, "customers-own-burst-policy") == nil {
		t.Error("the customer's policy was removed")
	}
	// And Odigos's own two are left exactly as they were, so the report costs no writes.
	if writes := api.writes(); len(writes) > 0 {
		t.Errorf("writes = %v", writes)
	}
}

// TestAPolicyNameHeldByAnotherTypeIsReported. Application Auto Scaling will not change an existing
// policy's type, so without this the pass fails identically for ever with an error that names neither
// the policy nor the reason.
func TestAPolicyNameHeldByAnotherTypeIsReported(t *testing.T) {
	cpu, memory := ScalingPolicyNames(testRegion)
	in := scalingInput()
	api := newFakeAutoScaling().withTarget(testResourceID, 2, 10, ourTags(testVPC))
	api.withPolicy(testResourceID, aastypes.ScalingPolicy{
		PolicyName:                     aws.String(cpu),
		PolicyType:                     aastypes.PolicyTypeStepScaling,
		StepScalingPolicyConfiguration: &aastypes.StepScalingPolicyConfiguration{},
	})

	got, err := EnsureScaling(context.Background(), api, in)
	if err != nil {
		t.Fatalf("EnsureScaling: %v", err)
	}
	problem := problemWith(t, got.Problems, ProblemScalingPolicyForeign)
	if !strings.Contains(problem.Message, cpu) || !strings.Contains(problem.Message, "Delete that policy") {
		t.Errorf("the message does not name the policy and the fix: %q", problem.Message)
	}
	// Not overwritten — the API would refuse it — and not deleted either, since this file holds no
	// per-policy delete at all.
	if api.policy(testResourceID, cpu).PolicyType != aastypes.PolicyTypeStepScaling {
		t.Error("the step policy was overwritten")
	}
	// The other policy is still converged: one broken policy does not stop the other.
	if len(got.PoliciesWritten) != 1 || got.PoliciesWritten[0] != memory {
		t.Errorf("policies written = %v, want just %s", got.PoliciesWritten, memory)
	}
}

// TestSuspendedScalingIsReportedNotResumed. Suspending scaling is a deliberate act, usually taken
// mid-incident to stop the autoscaler moving while somebody works — a converge loop that undid it every
// pass would be the worst possible participant in that incident.
func TestSuspendedScalingIsReportedNotResumed(t *testing.T) {
	in := scalingInput()
	api := newFakeAutoScaling().withSuspendedTarget(testResourceID, 2, 10, ourTags(testVPC))
	api.withOdigosPolicies(testResourceID, in)

	got, err := EnsureScaling(context.Background(), api, in)
	if err != nil {
		t.Fatalf("EnsureScaling: %v", err)
	}
	problemWith(t, got.Problems, ProblemScalingSuspended)
	if state := api.target(testResourceID).SuspendedState; state == nil ||
		!aws.ToBool(state.DynamicScalingInSuspended) || !aws.ToBool(state.DynamicScalingOutSuspended) {
		t.Errorf("the suspension was lifted: %+v", state)
	}
	if writes := api.writes(); len(writes) > 0 {
		t.Errorf("writes = %v", writes)
	}
}

// --- the mode gate ---

// TestAnUnrecognisedModeDrivesNeitherControl. "automatic" is one letter from a working `auto`, and a
// converge that read it as manual would deregister the customer's working autoscaler. Nothing is read
// and nothing is written: the Collector keeps running at whatever count it has, and the mode is reported.
func TestAnUnrecognisedModeDrivesNeitherControl(t *testing.T) {
	api := newFakeAutoScaling().withTarget(testResourceID, 2, 10, ourTags(testVPC))
	in := scalingInput()
	in.Scaling = collectorsettings.Scaling{Mode: "automatic", DesiredCount: 2, MinCount: 2, MaxCount: 10}.Resolve()

	got, err := EnsureScaling(context.Background(), api, in)
	if err != nil {
		t.Fatalf("EnsureScaling: %v", err)
	}
	if len(api.calls) > 0 {
		t.Fatalf("an unrecognised mode reached the API: %v", api.calls)
	}
	if api.target(testResourceID) == nil {
		t.Fatal("the target was deregistered on a typo")
	}
	problem := problemWith(t, got.Problems, ProblemScalingModeUnknown)
	if problem.Severity != "Error" {
		t.Errorf("severity = %q, want Error", problem.Severity)
	}
	if !strings.Contains(problem.Message, `"automatic"`) {
		t.Errorf("the message does not quote what was configured: %q", problem.Message)
	}
}

// TestManualDeploymentModeProvisionsNoScaling: in manual (IaC) mode the whole stack is the customer's,
// including its scaling. The coordinator wires no deployer at all for such a type, so reaching this
// means something bypassed that — which is why the guard is a refusal and not a comment.
func TestManualDeploymentModeProvisionsNoScaling(t *testing.T) {
	api := newFakeAutoScaling().withTarget(testResourceID, 2, 10, ourTags(testVPC))
	in := scalingInput()
	in.Mode = "manual"

	got, err := EnsureScaling(context.Background(), api, in)
	if err != nil {
		t.Fatalf("EnsureScaling: %v", err)
	}
	if len(api.calls) > 0 {
		t.Fatalf("manual mode reached the API: %v", api.calls)
	}
	problemWith(t, got.Problems, ProblemManualModeProvisioning)
}

// TestNoServiceMeansNoScalingWrites, and specifically no deregistration: a service that failed to
// converge on this pass is not a service that has stopped existing, and treating it as gone would
// remove a working Collector's autoscaler over a transient ECS error.
func TestNoServiceMeansNoScalingWrites(t *testing.T) {
	for name, mutate := range map[string]func(*ScalingInput){
		"no service yet":  func(in *ScalingInput) { in.ServiceARN = "" },
		"no cluster name": func(in *ScalingInput) { in.ClusterName = "" },
	} {
		t.Run(name, func(t *testing.T) {
			for _, in := range []ScalingInput{scalingInput(), manualInput()} {
				api := newFakeAutoScaling().withTarget(testResourceID, 2, 10, ourTags(testVPC))
				mutate(&in)

				got, err := EnsureScaling(context.Background(), api, in)
				if err != nil {
					t.Fatalf("EnsureScaling: %v", err)
				}
				if len(api.calls) > 0 {
					t.Fatalf("%s reached the API: %v", in.Scaling.Mode, api.calls)
				}
				if got.Deregistered || got.Registered {
					t.Fatalf("%s acted with no service: %+v", in.Scaling.Mode, got)
				}
				if api.target(testResourceID) == nil {
					t.Fatal("the target was deregistered because the service was not ready")
				}
			}
		})
	}
}

// --- bounds and availability ---

// TestCorrectedBoundsAreAppliedAndReported. A network that writes `mode: auto` and nothing else gets
// bounds it never chose, and finding that out from a status message beats finding it out from a bill.
func TestCorrectedBoundsAreAppliedAndReported(t *testing.T) {
	api := newFakeAutoScaling()
	in := scalingInput()
	in.Scaling = collectorsettings.Scaling{Mode: collectorsettings.ScalingAuto}.Resolve()

	got, err := EnsureScaling(context.Background(), api, in)
	if err != nil {
		t.Fatalf("EnsureScaling: %v", err)
	}
	target := api.target(testResourceID)
	if aws.ToInt32(target.MinCapacity) != collectorsettings.DefaultMinCount ||
		aws.ToInt32(target.MaxCapacity) != collectorsettings.DefaultMaxCount {
		t.Errorf("bounds = [%d,%d], want the documented defaults",
			aws.ToInt32(target.MinCapacity), aws.ToInt32(target.MaxCapacity))
	}
	problem := problemWith(t, got.Problems, ProblemScalingBoundsCorrected)
	if !strings.Contains(problem.Message, "minCount") || !strings.Contains(problem.Message, "maxCount") {
		t.Errorf("the message does not name the keys: %q", problem.Message)
	}
	if problem.Severity != "Degraded" {
		t.Errorf("severity = %q, want Degraded", problem.Severity)
	}
	// The targets are corrected too, in the same report rather than a second one.
	if !strings.Contains(problem.Message, "targetCpuPercent") {
		t.Errorf("the utilisation targets were corrected silently: %q", problem.Message)
	}
}

// TestTheLowerBoundIsNeverZero is the min-availability floor. A Collector scaled to zero is not "off":
// the load balancer still answers, so every workload keeps opening connections to an endpoint with
// nothing behind it and the telemetry is dropped with nothing watching.
func TestTheLowerBoundIsNeverZero(t *testing.T) {
	for _, configured := range []collectorsettings.Scaling{
		{Mode: collectorsettings.ScalingAuto},
		{Mode: collectorsettings.ScalingAuto, MinCount: 0, MaxCount: 20},
		{Mode: collectorsettings.ScalingAuto, MinCount: -5, MaxCount: 20},
	} {
		api := newFakeAutoScaling()
		in := scalingInput()
		in.Scaling = configured.Resolve()

		if _, err := EnsureScaling(context.Background(), api, in); err != nil {
			t.Fatalf("EnsureScaling: %v", err)
		}
		if got := aws.ToInt32(api.target(testResourceID).MinCapacity); got < collectorsettings.ReplicaFloor {
			t.Errorf("registered a lower bound of %d for %+v", got, configured)
		}
	}
}

// TestASingleTaskIsReportedInBothModes: every deployment, task replacement and Spot interruption is then
// a gap in a path with no local queue. Info, because it is a legitimate choice — but never silent.
func TestASingleTaskIsReportedInBothModes(t *testing.T) {
	cases := map[string]collectorsettings.Scaling{
		"a fixed count of one": {Mode: collectorsettings.ScalingManual, DesiredCount: 1},
		"a lower bound of one": {Mode: collectorsettings.ScalingAuto, MinCount: 1, MaxCount: 8,
			TargetCPUPercent: 70, TargetMemoryPercent: 75},
	}
	for name, configured := range cases {
		t.Run(name, func(t *testing.T) {
			api := newFakeAutoScaling()
			in := scalingInput()
			in.Scaling = configured.Resolve()

			got, err := EnsureScaling(context.Background(), api, in)
			if err != nil {
				t.Fatalf("EnsureScaling: %v", err)
			}
			problem := problemWith(t, got.Problems, ProblemScalingSingleTask)
			if problem.Severity != "Info" {
				t.Errorf("severity = %q, want Info", problem.Severity)
			}
		})
	}
	// And two tasks says nothing, in either mode.
	for _, configured := range []collectorsettings.Scaling{
		{Mode: collectorsettings.ScalingManual, DesiredCount: 2},
		{Mode: collectorsettings.ScalingAuto, MinCount: 2, MaxCount: 8, TargetCPUPercent: 70, TargetMemoryPercent: 75},
	} {
		api := newFakeAutoScaling()
		in := scalingInput()
		in.Scaling = configured.Resolve()
		got, err := EnsureScaling(context.Background(), api, in)
		if err != nil {
			t.Fatalf("EnsureScaling: %v", err)
		}
		if hasCode(got.Problems, ProblemScalingSingleTask) {
			t.Errorf("two tasks reported as one: %+v", configured)
		}
	}
}

// --- the resource id ---

// TestTheResourceIdIsBuiltFromTheClusterName. Everything else in this package passes cluster ARNs
// around, and an ARN here is refused with a ValidationException that quotes the resource id back without
// saying which half of it is wrong.
func TestTheResourceIdIsBuiltFromTheClusterName(t *testing.T) {
	id := ScalableTargetResourceID(clusterName, testRegion)
	if id != "service/"+clusterName+"/"+ServiceName(testRegion) {
		t.Fatalf("resource id = %q", id)
	}

	api := newFakeAutoScaling()
	if _, err := EnsureScaling(context.Background(), api, scalingInput()); err != nil {
		t.Fatalf("EnsureScaling: %v", err)
	}
	for _, call := range api.calls {
		if strings.Contains(call, "arn:aws:ecs") {
			t.Fatalf("a cluster ARN reached Application Auto Scaling: %q", call)
		}
	}
	// The service name in the id has to be the same one the service converge creates, or the target
	// governs a service that does not exist.
	if !strings.HasSuffix(api.calls[0], "") || api.target(id) == nil {
		t.Fatalf("no target at %q: %v", id, api.calls)
	}
}

// --- failures ---

// TestAFailedScalingWriteIsAnError, not a problem: a refusal is a decision Odigos made and reports, and
// an API failure is neither. Reporting one as the other would make a broken pass look like a converged
// Collector in a posture worth mentioning.
func TestAFailedScalingWriteIsAnError(t *testing.T) {
	cases := map[string]func(*fakeAutoScaling){
		"the register fails":   func(f *fakeAutoScaling) { f.registerErr = errors.New("throttled") },
		"the policy put fails": func(f *fakeAutoScaling) { f.putPolicyErr = errors.New("throttled") },
		"the tag read fails":   func(f *fakeAutoScaling) { f.tagsErr = errors.New("throttled") },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			api := newFakeAutoScaling()
			if name == "the tag read fails" {
				api.withTarget(testResourceID, 2, 10, ourTags(testVPC))
			}
			breakIt(api)
			if _, err := EnsureScaling(context.Background(), api, scalingInput()); err == nil {
				t.Fatal("no error")
			}
		})
	}
	// And on the manual side, a failed deregistration.
	api := newFakeAutoScaling().withTarget(testResourceID, 2, 10, ourTags(testVPC))
	api.deregisterErr = errors.New("throttled")
	if _, err := EnsureScaling(context.Background(), api, manualInput()); err == nil {
		t.Fatal("a failed deregistration was not an error")
	}
}

// TestNothingHereCanDeleteAPolicy is a source guard. Deregistering a target takes its policies with it,
// and Odigos never puts a policy on a target it did not register — so there is no orphaned policy of its
// own to remove, and this converge path holds exactly ONE destructive call. Adding a second is a
// decision, not a detail.
func TestNothingHereCanDeleteAPolicy(t *testing.T) {
	src, err := os.ReadFile("scaling.go")
	if err != nil {
		t.Fatalf("read scaling.go: %v", err)
	}
	if strings.Contains(string(src), "api.DeleteScalingPolicy(") {
		t.Error("scaling.go deletes a scaling policy; the only delete this path may make is the " +
			"deregistration a switch to manual scaling requires")
	}
	if got := strings.Count(string(src), "api.DeregisterScalableTarget("); got != 1 {
		t.Errorf("%d deregistration call sites, want exactly 1", got)
	}
}

// --- the record ---

// TestObservedScalingRecordsWhatIsInForce. One assembler because RecordCollectorScaling writes the mode,
// both counts, both bounds and the task size together — three callers each filling their own half would
// null the others'.
func TestObservedScalingRecordsWhatIsInForce(t *testing.T) {
	settings := collectorsettings.Network{
		CPU: "1024", Memory: "2048",
		Scaling: collectorsettings.Scaling{Mode: collectorsettings.ScalingAuto, MinCount: 2, MaxCount: 10},
	}
	service := Service{ARN: testServiceARN, DesiredCount: 3, RunningCount: 2}
	got := ObservedScaling(settings, service, ScalingState{TargetRegistered: true, MinCount: 2, MaxCount: 10})

	if got.Mode != collectorsettings.ScalingAuto || got.CPU != "1024" || got.Memory != "2048" {
		t.Errorf("observation = %+v", got)
	}
	if got.DesiredCount == nil || *got.DesiredCount != 3 || got.RunningCount == nil || *got.RunningCount != 2 {
		t.Errorf("counts = %v/%v", got.DesiredCount, got.RunningCount)
	}
	if got.MinCount == nil || *got.MinCount != 2 || got.MaxCount == nil || *got.MaxCount != 10 {
		t.Errorf("bounds = %v/%v", got.MinCount, got.MaxCount)
	}

	// A mode this connector does not implement records NO mode, rather than writing a typo into a column
	// documented as manual | auto. Nothing is driving the count, and a null says exactly that.
	settings.Scaling.Mode = "automatic"
	if unknown := ObservedScaling(settings, service, ScalingState{}); unknown.Mode != "" {
		t.Errorf("recorded an unimplemented mode as %q", unknown.Mode)
	}

	// Bounds only while a target exists, so a row carrying bounds is a row where something autoscales.
	settings.Scaling.Mode = collectorsettings.ScalingManual
	manual := ObservedScaling(settings, service, ScalingState{})
	if manual.MinCount != nil || manual.MaxCount != nil {
		t.Errorf("manual mode recorded bounds: %v/%v", manual.MinCount, manual.MaxCount)
	}

	// Counts only once there is a service. Nil is "no service yet"; a recorded zero is a service running
	// no tasks, which is a different and much more interesting state.
	none := ObservedScaling(settings, Service{}, ScalingState{})
	if none.DesiredCount != nil || none.RunningCount != nil {
		t.Errorf("recorded counts with no service: %v/%v", none.DesiredCount, none.RunningCount)
	}
	// And the two pointers are not the same address, which is what would make a caller's edit to one
	// silently change the other.
	if got.MinCount == got.MaxCount {
		t.Error("the bounds share one pointer")
	}
}

// --- the two controls together ---

// TestOnlyOneControlEverMovesTheReplicaCount is the "no overlapping controls" criterion, asserted across
// both halves rather than inside either. Each half is individually correct and the pair is what matters:
// a fixed desiredCount and a scalable target on one ECS service fight each other for ever, and the
// autoscaler wins every round.
//
// The drift is set up deliberately — the service is left at a count neither mode asked for — because
// that is the only state in which the question "who writes it?" has an observable answer.
func TestOnlyOneControlEverMovesTheReplicaCount(t *testing.T) {
	cases := []struct {
		name           string
		scaling        collectorsettings.Scaling
		wantCountWrite bool
		wantTarget     bool
	}{
		{
			name:           "manual holds the count itself",
			scaling:        collectorsettings.Scaling{Mode: collectorsettings.ScalingManual, DesiredCount: 2},
			wantCountWrite: true, wantTarget: false,
		},
		{
			name: "automatic hands it to the autoscaler",
			scaling: collectorsettings.Scaling{Mode: collectorsettings.ScalingAuto, MinCount: 2, MaxCount: 10,
				TargetCPUPercent: 70, TargetMemoryPercent: 75},
			wantCountWrite: false, wantTarget: true,
		},
		{
			// Neither, which is the case a guess would get wrong in the most expensive direction.
			name:           "an unrecognised mode leaves it to nobody",
			scaling:        collectorsettings.Scaling{Mode: "automatic", DesiredCount: 2, MinCount: 2, MaxCount: 10},
			wantCountWrite: false, wantTarget: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clients := newFakeClients()
			p := workloadProvisioner(clients)
			in := serviceStateInput(clients, t)
			in.Settings.Scaling = tc.scaling

			// A first pass to create the service, then drift its count so both controls have something to
			// disagree about.
			if _, err := p.EnsureCollectorService(context.Background(), in); err != nil {
				t.Fatalf("create: %v", err)
			}
			clients.ecs.service(in.Workload.Cluster.ARN, ServiceName(testRegion)).DesiredCount = 7
			in.Recorded = true

			clients.ecs.calls = nil
			service, err := p.EnsureCollectorService(context.Background(), in)
			if err != nil {
				t.Fatalf("converge: %v", err)
			}
			scaling, err := p.EnsureCollectorScaling(context.Background(), ScalingStateInput{
				Key: in.Key, HostNetworkID: in.HostNetworkID, Mode: in.Mode, Workload: in.Workload, Service: service,
				Settings: in.Settings, Recorded: true,
			})
			if err != nil {
				t.Fatalf("scaling: %v", err)
			}

			wroteCount := false
			for _, call := range clients.ecs.calls {
				if strings.Contains(call, "desired-count") {
					wroteCount = true
				}
			}
			if wroteCount != tc.wantCountWrite {
				t.Errorf("wrote desiredCount = %v, want %v (calls %v)", wroteCount, tc.wantCountWrite, clients.ecs.calls)
			}
			if got := clients.scaling.target(testResourceID) != nil; got != tc.wantTarget {
				t.Errorf("scalable target exists = %v, want %v (calls %v)", got, tc.wantTarget, clients.scaling.calls)
			}
			if wroteCount && scaling.TargetRegistered {
				t.Fatal("a fixed count and a scalable target are both driving one service")
			}
		})
	}
}

// TestSwitchingModesConvergesRatherThanLayers walks the round trip a customer actually makes: manual to
// auto and back. The target has to appear and then be gone, and the fixed count has to be Odigos's again
// afterwards — a mode switch that only ever adds leaves whichever control was configured first in charge
// for ever.
func TestSwitchingModesConvergesRatherThanLayers(t *testing.T) {
	clients := newFakeClients()
	p := workloadProvisioner(clients)
	in := serviceStateInput(clients, t)

	if _, err := p.EnsureCollectorService(context.Background(), in); err != nil {
		t.Fatalf("create: %v", err)
	}
	in.Recorded = true

	converge := func(t *testing.T, scaling collectorsettings.Scaling) ScalingState {
		t.Helper()
		in.Settings.Scaling = scaling
		svc, err := p.EnsureCollectorService(context.Background(), in)
		if err != nil {
			t.Fatalf("service: %v", err)
		}
		state, err := p.EnsureCollectorScaling(context.Background(), ScalingStateInput{
			Key: in.Key, HostNetworkID: in.HostNetworkID, Mode: in.Mode, Workload: in.Workload, Service: svc,
			Settings: in.Settings, Recorded: true,
		})
		if err != nil {
			t.Fatalf("scaling: %v", err)
		}
		return state
	}

	toAuto := converge(t, collectorsettings.Scaling{Mode: collectorsettings.ScalingAuto, MinCount: 3, MaxCount: 12,
		TargetCPUPercent: 70, TargetMemoryPercent: 75})
	if !toAuto.Registered || clients.scaling.target(testResourceID) == nil {
		t.Fatalf("switching to auto registered nothing: %+v", toAuto)
	}
	if len(clients.scaling.policies[testResourceID]) != 2 {
		t.Fatalf("policies = %v", clients.scaling.policies[testResourceID])
	}

	backToManual := converge(t, collectorsettings.Scaling{Mode: collectorsettings.ScalingManual, DesiredCount: 2})
	if !backToManual.Deregistered || clients.scaling.target(testResourceID) != nil {
		t.Fatalf("switching back to manual left the target: %+v", backToManual)
	}
	if len(clients.scaling.policies[testResourceID]) != 0 {
		t.Errorf("policies survived: %v", clients.scaling.policies[testResourceID])
	}
	// And Odigos holds the count again, which is the half that would silently not come back if the
	// service converge decided on anything other than the mode being exactly manual.
	clients.ecs.service(in.Workload.Cluster.ARN, ServiceName(testRegion)).DesiredCount = 9
	clients.ecs.calls = nil
	if _, err := p.EnsureCollectorService(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if svc := clients.ecs.service(in.Workload.Cluster.ARN, ServiceName(testRegion)); svc.DesiredCount != 2 {
		t.Errorf("desired count = %d after returning to manual, want the fixed 2 (calls %v)",
			svc.DesiredCount, clients.ecs.calls)
	}

	// A second switch to auto has to work too: the deregistration must not have left anything behind
	// that a re-register would trip over.
	again := converge(t, collectorsettings.Scaling{Mode: collectorsettings.ScalingAuto, MinCount: 3, MaxCount: 12,
		TargetCPUPercent: 70, TargetMemoryPercent: 75})
	if !again.Registered || len(again.PoliciesWritten) != 2 {
		t.Fatalf("the second switch to auto = %+v", again)
	}
}

// TestEnsureCollectorScalingAddressesTheRightService: the provisioner is what joins the cluster and the
// service to Application Auto Scaling's composed resource id, and it is the only place that can get
// the cluster NAME versus its ARN wrong.
func TestEnsureCollectorScalingAddressesTheRightService(t *testing.T) {
	clients := newFakeClients()
	p := workloadProvisioner(clients)
	in := serviceStateInput(clients, t)
	in.Settings.Scaling = collectorsettings.Scaling{Mode: collectorsettings.ScalingAuto, MinCount: 2, MaxCount: 10,
		TargetCPUPercent: 70, TargetMemoryPercent: 75}
	service, err := p.EnsureCollectorService(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}

	got, err := p.EnsureCollectorScaling(context.Background(), ScalingStateInput{
		Key: in.Key, HostNetworkID: in.HostNetworkID, Mode: in.Mode, Workload: in.Workload, Service: service,
		Settings: in.Settings, Recorded: true,
	})
	if err != nil {
		t.Fatalf("EnsureCollectorScaling: %v", err)
	}
	want := "service/" + in.Workload.Cluster.Name + "/" + ServiceName(testRegion)
	if got.ResourceID != want {
		t.Fatalf("resource id = %q, want %q", got.ResourceID, want)
	}
	if clients.scaling.target(want) == nil {
		t.Fatalf("no target at %q: %v", want, clients.scaling.calls)
	}
	// The cluster is addressed by name, and the ECS service the id names is the one that exists.
	if strings.Contains(got.ResourceID, in.Workload.Cluster.ARN) {
		t.Error("the resource id carries the cluster ARN")
	}
	if clients.ecs.service(in.Workload.Cluster.ARN, ServiceName(testRegion)) == nil {
		t.Error("the resource id names a service that does not exist")
	}

	// A customer's own cluster is addressed by ITS name, which is not the one Odigos would compose.
	clients = newFakeClients()
	clients.ecs.withCluster("their-cluster", nil, []string{"FARGATE", "FARGATE_SPOT"})
	p = workloadProvisioner(clients)
	in = serviceStateInput(clients, t)
	in.Settings.ClusterRef = "their-cluster"
	workload, err := p.EnsureWorkload(context.Background(), func() WorkloadInput {
		w := workloadInput()
		w.Settings.ClusterRef = "their-cluster"
		return w
	}())
	if err != nil {
		t.Fatal(err)
	}
	in.Workload = workload
	in.Settings.Scaling = collectorsettings.Scaling{Mode: collectorsettings.ScalingAuto, MinCount: 2, MaxCount: 10,
		TargetCPUPercent: 70, TargetMemoryPercent: 75}
	service, err = p.EnsureCollectorService(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	got, err = p.EnsureCollectorScaling(context.Background(), ScalingStateInput{
		Key: in.Key, HostNetworkID: in.HostNetworkID, Mode: in.Mode, Workload: workload, Service: service,
		Settings: in.Settings, Recorded: true,
	})
	if err != nil {
		t.Fatalf("EnsureCollectorScaling on a customer cluster: %v", err)
	}
	if got.ResourceID != "service/their-cluster/"+ServiceName(testRegion) {
		t.Errorf("resource id on a customer cluster = %q", got.ResourceID)
	}
}
