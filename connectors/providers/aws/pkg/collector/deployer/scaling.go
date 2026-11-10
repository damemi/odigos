package deployer

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	aas "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	aastypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"

	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorownership"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorstatus"
	collectorsettings "github.com/odigos-io/odigos/connectors/providers/aws/pkg/collector/settings"
)

// Cooldowns, in seconds (§8.3). Asymmetric on purpose, and the asymmetry is the design rather than a
// pair of round numbers: a Collector under pressure is DROPPING spans, so it has to be able to scale out
// again a minute later, while one that scaled in too eagerly drops them again on the next spike. Scaling
// in therefore waits five times as long — and, because both policies must agree before capacity is
// removed at all, in practice longer.
const (
	scaleOutCooldown int32 = 60
	scaleInCooldown  int32 = 300
)

// AutoScalingClient is the slice of Application Auto Scaling this file uses.
//
// DeleteScalingPolicy is deliberately absent even though the connector declares it: deregistering a
// scalable target deletes the policies attached to it, and Odigos never puts a policy on a target it did
// not register, so there is no orphaned policy of its own to remove. That leaves this converge path with
// exactly ONE destructive call — the deregistration a switch to manual scaling requires — which is the
// number worth reasoning about.
type AutoScalingClient interface {
	DescribeScalableTargets(ctx context.Context, in *aas.DescribeScalableTargetsInput, opts ...func(*aas.Options)) (*aas.DescribeScalableTargetsOutput, error)
	RegisterScalableTarget(ctx context.Context, in *aas.RegisterScalableTargetInput, opts ...func(*aas.Options)) (*aas.RegisterScalableTargetOutput, error)
	DeregisterScalableTarget(ctx context.Context, in *aas.DeregisterScalableTargetInput, opts ...func(*aas.Options)) (*aas.DeregisterScalableTargetOutput, error)
	DescribeScalingPolicies(ctx context.Context, in *aas.DescribeScalingPoliciesInput, opts ...func(*aas.Options)) (*aas.DescribeScalingPoliciesOutput, error)
	PutScalingPolicy(ctx context.Context, in *aas.PutScalingPolicyInput, opts ...func(*aas.Options)) (*aas.PutScalingPolicyOutput, error)
	ListTagsForResource(ctx context.Context, in *aas.ListTagsForResourceInput, opts ...func(*aas.Options)) (*aas.ListTagsForResourceOutput, error)
}

// ScalingInput is what converging one regional Collector's replica policy depends on.
type ScalingInput struct {
	HostNetworkID string
	Region        string
	Connector     string
	ConnectorID   string
	Identity      collectorownership.Identity

	// Mode is the DEPLOYMENT mode (spec.ModeAutomatic), and the collision of names is worth flagging
	// because getting it wrong inverts an ownership gate: this mode decides whether Odigos may write to
	// the customer's account at all, while Scaling.Mode decides which control drives the replica count.
	// Every ownership gate below takes this one.
	Mode string

	// ClusterName is the cluster's name — see ScalableTargetResourceID for why it cannot be the ARN.
	ClusterName string
	// ServiceARN is the converged service. Empty means there is none yet, and a scalable target cannot
	// be registered on a service that does not exist.
	ServiceARN string

	// Scaling is the policy, already resolved. Resolved by the caller rather than here so the service
	// converge and this one cannot disagree about which control is live — two readings of the mode is how
	// a Collector ends up with a fixed count and an autoscaler at the same time.
	Scaling collectorsettings.ResolvedScaling

	// Recorded is whether managed_collectors names this Collector's service. It is the second half of
	// the deletion gate (§10.4): deregistering a scalable target is a delete, so it needs the ownership
	// tag AND the state record, and losing the database loses the authority rather than the safety.
	Recorded bool
}

// ScalingState is what governs the Collector's replica count after this pass.
type ScalingState struct {
	// ResourceID is Application Auto Scaling's address for the service, or empty when there was no
	// service to address.
	ResourceID string
	// Mode is the scaling mode as configured, whether or not it was usable.
	Mode string

	// TargetRegistered is whether a scalable target now governs the count — including one Odigos did not
	// register and may not remove, because what matters to a reader is that something else is moving it.
	TargetRegistered bool
	// MinCount and MaxCount are the bounds in force, which are Odigos's configured pair only when Odigos
	// owns the target.
	MinCount int
	MaxCount int

	// Registered, Deregistered and PoliciesWritten are what this pass did. All three empty is the steady
	// state, in both modes.
	Registered      bool
	Deregistered    bool
	PoliciesWritten []string

	Problems []collectorstatus.Problem
}

// Ready reports whether the replica policy is in force as configured.
func (s ScalingState) Ready() bool { return !collectorstatus.HasError(s.Problems) }

// EnsureScaling converges one regional Collector's replica policy: a fixed count with no autoscaler, or a scalable
// target with the two target-tracking policies §8.3 specifies.
//
// The property the whole file is built around is that the two controls are EXCLUSIVE. Only one thing may
// move a Collector's replica count:
//
//   - `manual`: the service converge writes a fixed desiredCount, and any scalable target Odigos
//     registered is deregistered. A switch of modes converges rather than layers.
//   - `auto`: Application Auto Scaling owns desiredCount and the service converge does not touch it
//     (see desiredCountChange), bounded by [minCount, maxCount].
//   - anything else: NEITHER acts, and the mode is reported. Guessing here is what makes a typo
//     destructive.
//
// A control Odigos cannot remove — a scalable target a customer's own template registered — is reported
// rather than worked around, because a fixed count and somebody else's autoscaler on one service is a
// contradiction only the customer can resolve.
func EnsureScaling(ctx context.Context, api AutoScalingClient, in ScalingInput) (ScalingState, error) {
	out := ScalingState{Mode: in.Scaling.Mode}
	if problems := modeGuard(in.Mode); len(problems) > 0 {
		out.Problems = problems
		return out, nil
	}

	// Reported first and whatever happens next: these are facts about the CONFIGURATION rather than
	// about what could be built, so a customer whose bounds were corrected hears about it even on a pass
	// that then refuses for an unrelated reason.
	out.Problems = append(out.Problems, configProblems(in)...)

	if !in.Scaling.Known() {
		out.Problems = append(out.Problems, unknownModeProblem(in))
		return out, nil
	}
	if in.ClusterName == "" || in.ServiceARN == "" {
		// No service to govern. Whatever stopped it is reported against the service, and this is
		// specifically NOT a reason to deregister anything: a service that failed to converge on this
		// pass is not a service that has stopped existing.
		return out, nil
	}
	out.ResourceID = ScalableTargetResourceID(in.ClusterName, in.Region)

	target, err := describeScalableTarget(ctx, api, out.ResourceID)
	if err != nil {
		return out, err
	}
	object, err := scalableTargetObject(ctx, api, target, in.Recorded)
	if err != nil {
		return out, err
	}

	if in.Scaling.Manual() {
		return removeScaling(ctx, api, in, out, target, object)
	}
	return applyScaling(ctx, api, in, out, target, object)
}

// removeScaling is manual mode: a fixed replica count and no autoscaling resource (§8.2).
//
// The fixed count is the service converge's to write; what this owns is that nothing else is also
// writing it. A target left over from a previous `auto` configuration is deregistered — with its
// policies, which Application Auto Scaling removes along with the target it drops.
func removeScaling(ctx context.Context, api AutoScalingClient, in ScalingInput, out ScalingState,
	target *aastypes.ScalableTarget, object collectorownership.Object) (ScalingState, error) {
	if target == nil {
		// The steady state for every manual-mode Collector that has never been in `auto`: one describe
		// per pass and no writes at all.
		return out, nil
	}

	out.TargetRegistered = true
	out.MinCount = int(aws.ToInt32(target.MinCapacity))
	out.MaxCount = int(aws.ToInt32(target.MaxCapacity))

	// MayDelete, not MayMutate. Deregistering is destruction, so it takes both records — the ownership
	// tag on the target and the state row naming this Collector — exactly as every other delete in this
	// deployer does. A target Odigos merely converged is left standing.
	if !collectorownership.MayDelete(object, in.Identity, in.Mode) {
		out.Problems = append(out.Problems, targetNotOursProblem(in, object, out.MinCount, out.MaxCount))
		return out, nil
	}

	if _, err := api.DeregisterScalableTarget(ctx, &aas.DeregisterScalableTargetInput{
		ServiceNamespace:  aastypes.ServiceNamespaceEcs,
		ResourceId:        aws.String(out.ResourceID),
		ScalableDimension: aastypes.ScalableDimensionECSServiceDesiredCount,
	}); err != nil {
		return out, fmt.Errorf("deregister the scalable target for %s: %w", out.ResourceID, err)
	}
	out.Deregistered = true
	out.TargetRegistered = false
	out.MinCount, out.MaxCount = 0, 0
	log.Info("removed the collector's scalable target for manual scaling",
		"region", in.Region, "network", in.HostNetworkID, "resource", out.ResourceID)
	return out, nil
}

// applyScaling is automatic mode: a scalable target bounded by the configured pair, with the two
// target-tracking policies on it.
//
// The bounds are written only when they differ, so a converged Collector costs one DescribeScalableTargets,
// one ListTagsForResource and one DescribeScalingPolicies per pass and no writes. RegisterScalableTarget
// is not free of consequence: it moves the current capacity inside the new bounds immediately, so a
// register on every pass would restart tasks whenever the count happened to sit outside them.
func applyScaling(ctx context.Context, api AutoScalingClient, in ScalingInput, out ScalingState,
	target *aastypes.ScalableTarget, object collectorownership.Object) (ScalingState, error) {
	if target == nil {
		if _, err := api.RegisterScalableTarget(ctx, &aas.RegisterScalableTargetInput{
			ServiceNamespace:  aastypes.ServiceNamespaceEcs,
			ResourceId:        aws.String(out.ResourceID),
			ScalableDimension: aastypes.ScalableDimensionECSServiceDesiredCount,
			MinCapacity:       aws.Int32(int32(in.Scaling.MinCount)),
			MaxCapacity:       aws.Int32(int32(in.Scaling.MaxCount)),
			// The ownership tag, on the one call in this file that CREATES anything. Nothing else here
			// writes a tag: a target that was already there was not created by Odigos, and TagManaged is
			// what authorizes its deletion (§10.4).
			Tags: baseapi.CollectorManagedTags(in.Connector, in.ConnectorID, in.Region, in.HostNetworkID),
		}); err != nil {
			return out, fmt.Errorf("register a scalable target for %s: %w", out.ResourceID, err)
		}
		out.Registered = true
		log.Info("registered the collector's scalable target",
			"region", in.Region, "network", in.HostNetworkID, "resource", out.ResourceID,
			"min", in.Scaling.MinCount, "max", in.Scaling.MaxCount)
	} else {
		out.TargetRegistered = true
		out.MinCount = int(aws.ToInt32(target.MinCapacity))
		out.MaxCount = int(aws.ToInt32(target.MaxCapacity))

		// A target Odigos did not register is not Odigos's to reconfigure, and refusing here is the
		// deliberate half of the design rather than a missing feature. The realistic way one exists is
		// manual (IaC) mode: the customer's own template registered a target with bounds their capacity
		// planning chose. Flipping that install to automatic mode must not quietly replace working
		// bounds — and Odigos's own policies pushed onto their target would be a third control on one
		// count.
		if !collectorownership.MayMutate(object, in.Identity, in.Mode) {
			out.Problems = append(out.Problems, targetNotOursProblem(in, object, out.MinCount, out.MaxCount))
			return out, nil
		}
		if suspended(target) {
			// Reported, never resumed. Suspending scaling is a deliberate act, usually taken mid-incident
			// to stop the autoscaler moving while somebody works — and a converge loop that undid it
			// every pass would be the worst possible participant in that incident.
			out.Problems = append(out.Problems, suspendedProblem(in))
		}
		if out.MinCount != in.Scaling.MinCount || out.MaxCount != in.Scaling.MaxCount {
			if _, err := api.RegisterScalableTarget(ctx, &aas.RegisterScalableTargetInput{
				ServiceNamespace:  aastypes.ServiceNamespaceEcs,
				ResourceId:        aws.String(out.ResourceID),
				ScalableDimension: aastypes.ScalableDimensionECSServiceDesiredCount,
				MinCapacity:       aws.Int32(int32(in.Scaling.MinCount)),
				MaxCapacity:       aws.Int32(int32(in.Scaling.MaxCount)),
				// No tags on the update path. Register is create-or-update and tagging is documented for
				// the create, so a tag written here would either be ignored or would stamp ownership onto
				// something adoption is not allowed to claim.
			}); err != nil {
				return out, fmt.Errorf("update the scalable target bounds for %s: %w", out.ResourceID, err)
			}
			out.Registered = true
			log.Info("converged the collector's scaling bounds",
				"region", in.Region, "network", in.HostNetworkID, "resource", out.ResourceID,
				"min", in.Scaling.MinCount, "max", in.Scaling.MaxCount)
		}
	}

	out.TargetRegistered = true
	out.MinCount, out.MaxCount = in.Scaling.MinCount, in.Scaling.MaxCount

	written, problems, err := ensureScalingPolicies(ctx, api, in, out.ResourceID)
	if err != nil {
		return out, err
	}
	out.PoliciesWritten = written
	out.Problems = append(out.Problems, problems...)
	return out, nil
}

// scalingPolicy is one target-tracking policy Odigos maintains on a scalable target.
type scalingPolicy struct {
	name   string
	metric aastypes.MetricType
	target float64
}

// desiredScalingPolicies is the pair §8.3 specifies.
//
// CPU and memory are PROXIES for the thing that actually matters — whether the Collector's export queue
// is backing up — chosen for v1 because they need no metric publication, no alarms of Odigos's own and
// no code in the data path. The real signal is the Supervisor's own reported metrics, which the connector
// already receives over OpAMP; that is §8.4 and it removes this target rather than arguing with it.
func desiredScalingPolicies(in ScalingInput) []scalingPolicy {
	cpu, memory := ScalingPolicyNames(in.Region)
	return []scalingPolicy{
		{name: cpu, metric: aastypes.MetricTypeECSServiceAverageCPUUtilization, target: float64(in.Scaling.TargetCPUPercent)},
		{name: memory, metric: aastypes.MetricTypeECSServiceAverageMemoryUtilization, target: float64(in.Scaling.TargetMemoryPercent)},
	}
}

// configuration renders the policy body.
func (p scalingPolicy) configuration() *aastypes.TargetTrackingScalingPolicyConfiguration {
	return &aastypes.TargetTrackingScalingPolicyConfiguration{
		PredefinedMetricSpecification: &aastypes.PredefinedMetricSpecification{PredefinedMetricType: p.metric},
		TargetValue:                   aws.Float64(p.target),
		ScaleOutCooldown:              aws.Int32(scaleOutCooldown),
		ScaleInCooldown:               aws.Int32(scaleInCooldown),
		// Explicitly false rather than left to the API default, because the two policies scaling in
		// together is the entire reason there are two of them: Application Auto Scaling grants the
		// LARGEST capacity either policy asks for, so it scales out on whichever resource saturates first
		// and scales in only when both agree. Scale-in disabled on either one turns that into a fleet
		// that can only ever grow, which is a cost leak with no symptom.
		DisableScaleIn: aws.Bool(false),
	}
}

// matches reports whether a described policy already is this one.
//
// Every field the Put would set is compared, including the two cooldowns: they are the difference
// between a Collector that recovers from a spike and one that flaps, and a policy edited by hand in the
// console is exactly how they drift.
func (p scalingPolicy) matches(have aastypes.ScalingPolicy) bool {
	if have.PolicyType != aastypes.PolicyTypeTargetTrackingScaling {
		return false
	}
	cfg := have.TargetTrackingScalingPolicyConfiguration
	if cfg == nil || cfg.PredefinedMetricSpecification == nil {
		return false
	}
	return cfg.PredefinedMetricSpecification.PredefinedMetricType == p.metric &&
		aws.ToFloat64(cfg.TargetValue) == p.target &&
		aws.ToInt32(cfg.ScaleOutCooldown) == scaleOutCooldown &&
		aws.ToInt32(cfg.ScaleInCooldown) == scaleInCooldown &&
		!aws.ToBool(cfg.DisableScaleIn)
}

// ensureScalingPolicies converges the two policies and reports any third control sharing the target.
func ensureScalingPolicies(ctx context.Context, api AutoScalingClient, in ScalingInput, resourceID string) (
	written []string, problems []collectorstatus.Problem, err error) {
	// One page. A scalable target is unique per (namespace, resource, dimension) and Application Auto
	// Scaling permits at most 50 policies on one, which is also this call's page size — so a second page
	// cannot exist, and paginating would be code no account can reach.
	out, err := api.DescribeScalingPolicies(ctx, &aas.DescribeScalingPoliciesInput{
		ServiceNamespace:  aastypes.ServiceNamespaceEcs,
		ResourceId:        aws.String(resourceID),
		ScalableDimension: aastypes.ScalableDimensionECSServiceDesiredCount,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("describe the scaling policies on %s: %w", resourceID, err)
	}
	have := make(map[string]aastypes.ScalingPolicy, len(out.ScalingPolicies))
	for _, policy := range out.ScalingPolicies {
		have[aws.ToString(policy.PolicyName)] = policy
	}

	ours := map[string]bool{}
	for _, want := range desiredScalingPolicies(in) {
		ours[want.name] = true
		existing, found := have[want.name]
		if found && want.matches(existing) {
			continue
		}
		if found && existing.PolicyType != aastypes.PolicyTypeTargetTrackingScaling {
			// Application Auto Scaling will not change an existing policy's type, so this cannot be
			// converged — and without saying so it fails identically on every pass with an error that
			// names neither the policy nor the reason.
			problems = append(problems, policyTypeProblem(in, existing))
			continue
		}
		if _, err := api.PutScalingPolicy(ctx, &aas.PutScalingPolicyInput{
			PolicyName:                               aws.String(want.name),
			ServiceNamespace:                         aastypes.ServiceNamespaceEcs,
			ResourceId:                               aws.String(resourceID),
			ScalableDimension:                        aastypes.ScalableDimensionECSServiceDesiredCount,
			PolicyType:                               aastypes.PolicyTypeTargetTrackingScaling,
			TargetTrackingScalingPolicyConfiguration: want.configuration(),
		}); err != nil {
			return written, problems, fmt.Errorf("put the %s scaling policy on %s: %w", want.name, resourceID, err)
		}
		written = append(written, want.name)
		log.Info("converged a collector scaling policy",
			"region", in.Region, "network", in.HostNetworkID, "policy", want.name, "target", want.target)
	}

	// Anything else on the target is a control Odigos did not write. Reported rather than removed: it is
	// not Odigos's, and two policies moving one replica count is something the customer has to decide
	// about.
	var foreign []string
	for _, name := range sortedPolicyNames(have) {
		if !ours[name] {
			foreign = append(foreign, name)
		}
	}
	if len(foreign) > 0 {
		problems = append(problems, foreignPolicyProblem(in, foreign))
	}
	return written, problems, nil
}

// describeScalableTarget resolves the target on one resource id, or nil.
//
// A resource id nobody has registered comes back as an EMPTY list rather than an error, which is why
// there is no not-found to match on here — and why a describe of a service that does not exist yet is
// harmless.
func describeScalableTarget(ctx context.Context, api scalableTargetReader, resourceID string) (*aastypes.ScalableTarget, error) {
	out, err := api.DescribeScalableTargets(ctx, &aas.DescribeScalableTargetsInput{
		ServiceNamespace:  aastypes.ServiceNamespaceEcs,
		ResourceIds:       []string{resourceID},
		ScalableDimension: aastypes.ScalableDimensionECSServiceDesiredCount,
	})
	if err != nil {
		return nil, fmt.Errorf("describe the scalable target for %s: %w", resourceID, err)
	}
	for i := range out.ScalableTargets {
		if aws.ToString(out.ScalableTargets[i].ResourceId) == resourceID {
			return &out.ScalableTargets[i], nil
		}
	}
	return nil, nil
}

// scalableTargetObject reads an existing target's ownership standing.
//
// From the tags on the TARGET, not on the service, and the distinction is the whole safety property
// here: Odigos owning the service does not mean Odigos registered the target. Application Auto Scaling
// returns no tags on a describe, so this is a second call — the same shape as the ELBv2 objects, whose
// tags DescribeLoadBalancers does not return either.
func scalableTargetObject(ctx context.Context, api scalableTargetReader, target *aastypes.ScalableTarget, recorded bool) (
	collectorownership.Object, error) {
	if target == nil {
		return collectorownership.Object{}, nil
	}
	arn := aws.ToString(target.ScalableTargetARN)
	object := collectorownership.Object{Ref: arn, Recorded: recorded}
	if arn == "" {
		// No ARN means no tags can be read, so ownership cannot be proved. Left unattributed, which
		// refuses both the write and the delete — the safe direction for a field an older API version
		// may simply not populate.
		return object, nil
	}
	tags, err := api.ListTagsForResource(ctx, &aas.ListTagsForResourceInput{ResourceARN: aws.String(arn)})
	if err != nil {
		return object, fmt.Errorf("read the tags on scalable target %s: %w", arn, err)
	}
	object.Tags = baseapi.ParseCollectorTags(tags.Tags)
	return object, nil
}

// suspended reports whether dynamic scaling is paused on a target.
//
// Only the two DYNAMIC flags. Scheduled scaling being suspended says nothing about the target-tracking
// policies Odigos maintains, and reporting it would be a standing Degraded about a feature Odigos does
// not use.
func suspended(target *aastypes.ScalableTarget) bool {
	if target.SuspendedState == nil {
		return false
	}
	return aws.ToBool(target.SuspendedState.DynamicScalingInSuspended) ||
		aws.ToBool(target.SuspendedState.DynamicScalingOutSuspended)
}

// configProblems reports what the resolved policy could not honour, in either mode.
func configProblems(in ScalingInput) []collectorstatus.Problem {
	var problems []collectorstatus.Problem
	if corrections := in.Scaling.Corrections; len(corrections) > 0 {
		problems = append(problems, collectorstatus.Problem{
			Code:     ProblemScalingBoundsCorrected,
			Severity: collectorstatus.SeverityDegraded,
			Message: fmt.Sprintf("the Collector in %s (host %s) is not scaling exactly as configured: %s",
				in.Region, in.HostNetworkID, strings.Join(corrections, "; ")),
		})
	}
	if in.Scaling.SingleTask() {
		detail := fmt.Sprintf("scaling.desiredCount is %d", in.Scaling.DesiredCount)
		if in.Scaling.Automatic() {
			detail = fmt.Sprintf("scaling.minCount is %d, so the autoscaler may reduce it to one",
				in.Scaling.MinCount)
		}
		problems = append(problems, collectorstatus.Problem{
			Code:     ProblemScalingSingleTask,
			Severity: collectorstatus.SeverityInfo,
			Message: fmt.Sprintf("the Collector in %s (host %s) can run on a single task (%s). Every deployment, task "+
				"replacement and Spot interruption is then a gap in a path with no local queue, so the spans "+
				"arriving during one are dropped. Two tasks across two availability zones is the default for "+
				"that reason", in.Region, in.HostNetworkID, detail),
		})
	}
	return problems
}

// unknownModeProblem is the refusal for a scaling mode this connector does not implement.
//
// An Error, and nothing is written: neither the fixed count nor the scalable target. The typo this
// exists for is "automatic" — one letter from a working `auto` and, if it were read as manual, a config
// edit that silently deregistered a working autoscaler and pinned the Collector to a fixed count.
func unknownModeProblem(in ScalingInput) collectorstatus.Problem {
	return collectorstatus.Problem{
		Code:     ProblemScalingModeUnknown,
		Severity: collectorstatus.SeverityError,
		Message: fmt.Sprintf("the Collector in %s (host %s) has scaling.mode %q, which is neither %q nor %q, so Odigos "+
			"is driving its replica count in neither way: the fixed count is not applied and no autoscaling "+
			"target is created or removed. The Collector keeps running at whatever count it has. Set "+
			"collector.regions.%s.scaling.mode to %q or %q",
			in.Region, in.HostNetworkID, in.Scaling.Mode, collectorsettings.ScalingManual, collectorsettings.ScalingAuto,
			in.Region, collectorsettings.ScalingManual, collectorsettings.ScalingAuto),
	}
}

// targetNotOursProblem is the report for a scalable target Odigos may not write to or may not remove.
//
// One code, two consequences, because the object and the cause are the same and only the mode differs:
// in `auto` the configured bounds are not in force, and in `manual` something other than Odigos is
// still moving the count. Both are Degraded rather than Error — the Collector is running and scaling,
// just not from Odigos's configuration — and both name the fix, which only the customer can apply.
func targetNotOursProblem(in ScalingInput, object collectorownership.Object, minCount, maxCount int) collectorstatus.Problem {
	class := collectorownership.Classify(object, in.Identity, in.Mode)
	cause := "it carries no Odigos ownership tag, so Odigos did not create it — an autoscaling target " +
		"applied by your own template or added by hand"
	switch {
	case class.Attribution == collectorownership.AttributionForeign:
		cause = fmt.Sprintf("it is tagged for connector %q, so another connector registered it", object.Tags.Connector)
	case class.Attribution == collectorownership.AttributionUnattributed:
		cause = "it is tagged as a Collector object but names no connector, so Odigos will not claim it"
	case class.Origin == collectorownership.OriginOdigos && !object.Recorded:
		cause = "Odigos created it but no longer has it in state, so it cannot prove the target is its " +
			"to remove. Re-enabling this region in automatic mode re-records it"
	}

	consequence := fmt.Sprintf("scaling.minCount and scaling.maxCount (%d to %d) are not in force — the "+
		"bounds on the existing target are %d to %d. Deregister it to let Odigos manage scaling",
		in.Scaling.MinCount, in.Scaling.MaxCount, minCount, maxCount)
	if in.Scaling.Manual() {
		consequence = "the fixed scaling.desiredCount is applied, but that target can move the count away " +
			"from it again. Deregister it, or set scaling.mode to \"auto\" so Odigos manages the bounds itself"
	}
	return collectorstatus.Problem{
		Code:     ProblemScalingTargetNotOurs,
		Severity: collectorstatus.SeverityDegraded,
		Message: fmt.Sprintf("the Collector in %s (host %s) already has an Application Auto Scaling target on %s and "+
			"Odigos left it alone: %s. %s",
			in.Region, in.HostNetworkID, ScalableTargetResourceID(in.ClusterName, in.Region), cause, consequence),
	}
}

// suspendedProblem reports a target whose dynamic scaling is paused.
func suspendedProblem(in ScalingInput) collectorstatus.Problem {
	return collectorstatus.Problem{
		Code:     ProblemScalingSuspended,
		Severity: collectorstatus.SeverityDegraded,
		Message: fmt.Sprintf("automatic scaling is configured for the Collector in %s (host %s) but suspended on its "+
			"scalable target, so the policies exist and do nothing. Odigos does not resume it — a suspension "+
			"is usually deliberate — so resume it in Application Auto Scaling when the reason has passed",
			in.Region, in.HostNetworkID),
	}
}

// foreignPolicyProblem reports scaling policies sharing the target that Odigos did not write.
func foreignPolicyProblem(in ScalingInput, names []string) collectorstatus.Problem {
	return collectorstatus.Problem{
		Code:     ProblemScalingPolicyForeign,
		Severity: collectorstatus.SeverityDegraded,
		Message: fmt.Sprintf("the Collector in %s (host %s) has scaling policies Odigos did not create on its scalable "+
			"target (%s), alongside the two it maintains. Application Auto Scaling grants the largest capacity "+
			"any policy asks for, so those raise the Collector's task count independently of "+
			"scaling.targetCpuPercent and scaling.targetMemoryPercent. Odigos leaves them alone because they "+
			"are not its to remove", in.Region, in.HostNetworkID, strings.Join(names, ", ")),
	}
}

// policyTypeProblem reports one of Odigos's two policy names held by a policy of another type.
func policyTypeProblem(in ScalingInput, existing aastypes.ScalingPolicy) collectorstatus.Problem {
	return collectorstatus.Problem{
		Code:     ProblemScalingPolicyForeign,
		Severity: collectorstatus.SeverityDegraded,
		Message: fmt.Sprintf("the scaling policy %q on the Collector in %s (host %s) is a %s policy, and Application "+
			"Auto Scaling cannot change an existing policy's type, so Odigos cannot converge it into the "+
			"target-tracking policy it maintains. Delete that policy and it will be recreated",
			aws.ToString(existing.PolicyName), in.Region, in.HostNetworkID, existing.PolicyType),
	}
}

func sortedPolicyNames(policies map[string]aastypes.ScalingPolicy) []string {
	names := make([]string, 0, len(policies))
	for name := range policies {
		names = append(names, name)
	}
	return sortedCopy(names)
}
