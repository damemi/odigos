package deployer

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	aas "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	aastypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
)

// fakeAutoScaling is Application Auto Scaling as this package uses it: one scalable target per
// (namespace, resource id, dimension) and the policies attached to it.
//
// It answers a describe for an unregistered resource id with an EMPTY list rather than an error, which
// is what the real API does and what makes a describe before anything exists safe. A fake that returned
// a not-found error here would let code that mishandled it pass.
type fakeAutoScaling struct {
	targets  map[string]*aastypes.ScalableTarget // by resource id
	tags     map[string]map[string]string        // by scalable target ARN
	policies map[string][]aastypes.ScalingPolicy // by resource id

	calls []string

	registerErr   error
	deregisterErr error
	putPolicyErr  error
	tagsErr       error
	// order, when set, is a call log shared with the other fakes. See sink.
	order *sink
	// omitTargetARN drops ScalableTargetARN from every described target, which is how an API version
	// that does not populate it looks from here.
	omitTargetARN bool
}

func newFakeAutoScaling() *fakeAutoScaling {
	return &fakeAutoScaling{
		targets:  map[string]*aastypes.ScalableTarget{},
		tags:     map[string]map[string]string{},
		policies: map[string][]aastypes.ScalingPolicy{},
	}
}

// scalableTargetARN is the shape Application Auto Scaling gives a target: the resource id, slashes and
// all, appended to the ARN.
func scalableTargetARN(resourceID string) string {
	return "arn:aws:application-autoscaling:" + testRegion + ":" + testAccount + ":scalable-target/" + resourceID
}

// withTarget registers a target the way something other than this pass would have.
func (f *fakeAutoScaling) withTarget(resourceID string, min, max int32, tags map[string]string) *fakeAutoScaling {
	arn := scalableTargetARN(resourceID)
	f.targets[resourceID] = &aastypes.ScalableTarget{
		ServiceNamespace:  aastypes.ServiceNamespaceEcs,
		ResourceId:        aws.String(resourceID),
		ScalableDimension: aastypes.ScalableDimensionECSServiceDesiredCount,
		MinCapacity:       aws.Int32(min),
		MaxCapacity:       aws.Int32(max),
		ScalableTargetARN: aws.String(arn),
	}
	f.tags[arn] = tags
	return f
}

// withSuspendedTarget is a target whose dynamic scaling somebody paused.
func (f *fakeAutoScaling) withSuspendedTarget(resourceID string, min, max int32, tags map[string]string) *fakeAutoScaling {
	f.withTarget(resourceID, min, max, tags)
	f.targets[resourceID].SuspendedState = &aastypes.SuspendedState{
		DynamicScalingInSuspended:  aws.Bool(true),
		DynamicScalingOutSuspended: aws.Bool(true),
	}
	return f
}

// withPolicy attaches a policy to a target, for the drift and foreign-policy cases.
func (f *fakeAutoScaling) withPolicy(resourceID string, policy aastypes.ScalingPolicy) *fakeAutoScaling {
	policy.ResourceId = aws.String(resourceID)
	policy.ServiceNamespace = aastypes.ServiceNamespaceEcs
	policy.ScalableDimension = aastypes.ScalableDimensionECSServiceDesiredCount
	f.policies[resourceID] = append(f.policies[resourceID], policy)
	return f
}

// withOdigosPolicies attaches exactly what a converged pass would have written, so a test can start
// from the steady state rather than from a pass that just built it.
func (f *fakeAutoScaling) withOdigosPolicies(resourceID string, in ScalingInput) *fakeAutoScaling {
	for _, want := range desiredScalingPolicies(in) {
		f.withPolicy(resourceID, aastypes.ScalingPolicy{
			PolicyName:                               aws.String(want.name),
			PolicyType:                               aastypes.PolicyTypeTargetTrackingScaling,
			TargetTrackingScalingPolicyConfiguration: want.configuration(),
		})
	}
	return f
}

func (f *fakeAutoScaling) DescribeScalableTargets(_ context.Context, in *aas.DescribeScalableTargetsInput, _ ...func(*aas.Options)) (*aas.DescribeScalableTargetsOutput, error) {
	f.calls = append(f.calls, fmt.Sprintf("DescribeScalableTargets:%v/%s/%s",
		in.ResourceIds, in.ServiceNamespace, in.ScalableDimension))
	var out aas.DescribeScalableTargetsOutput
	for _, id := range in.ResourceIds {
		target, ok := f.targets[id]
		if !ok {
			continue
		}
		described := *target
		if f.omitTargetARN {
			described.ScalableTargetARN = nil
		}
		out.ScalableTargets = append(out.ScalableTargets, described)
	}
	return &out, nil
}

func (f *fakeAutoScaling) RegisterScalableTarget(_ context.Context, in *aas.RegisterScalableTargetInput, _ ...func(*aas.Options)) (*aas.RegisterScalableTargetOutput, error) {
	id := aws.ToString(in.ResourceId)
	// The tag keys are recorded in the call, because "registered with the ownership tag" and "registered
	// without it" are two different acts and only one of them may happen to a target that already exists.
	var keys []string
	for k := range in.Tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	f.calls = append(f.calls, fmt.Sprintf("RegisterScalableTarget:%s/%d-%d/tags=%s",
		id, aws.ToInt32(in.MinCapacity), aws.ToInt32(in.MaxCapacity), strings.Join(keys, ",")))
	if f.registerErr != nil {
		return nil, f.registerErr
	}
	if existing, ok := f.targets[id]; ok {
		existing.MinCapacity = in.MinCapacity
		existing.MaxCapacity = in.MaxCapacity
		// Tags on an update are NOT merged, matching the API: tagging an existing target is TagResource's
		// job, which this client interface does not have.
		return &aas.RegisterScalableTargetOutput{}, nil
	}
	f.withTarget(id, aws.ToInt32(in.MinCapacity), aws.ToInt32(in.MaxCapacity), in.Tags)
	return &aas.RegisterScalableTargetOutput{
		ScalableTargetARN: aws.String(scalableTargetARN(id)),
	}, nil
}

// DeregisterScalableTarget removes the target AND its policies, which is what the API does — and the
// reason the converge path needs no per-policy delete.
func (f *fakeAutoScaling) DeregisterScalableTarget(_ context.Context, in *aas.DeregisterScalableTargetInput, _ ...func(*aas.Options)) (*aas.DeregisterScalableTargetOutput, error) {
	id := aws.ToString(in.ResourceId)
	f.calls = append(f.calls, "DeregisterScalableTarget:"+id)
	f.order.record("DeregisterScalableTarget:" + id)
	if f.deregisterErr != nil {
		return nil, f.deregisterErr
	}
	if target, ok := f.targets[id]; ok {
		delete(f.tags, aws.ToString(target.ScalableTargetARN))
	}
	delete(f.targets, id)
	delete(f.policies, id)
	return &aas.DeregisterScalableTargetOutput{}, nil
}

func (f *fakeAutoScaling) DescribeScalingPolicies(_ context.Context, in *aas.DescribeScalingPoliciesInput, _ ...func(*aas.Options)) (*aas.DescribeScalingPoliciesOutput, error) {
	id := aws.ToString(in.ResourceId)
	f.calls = append(f.calls, "DescribeScalingPolicies:"+id)
	return &aas.DescribeScalingPoliciesOutput{
		ScalingPolicies: append([]aastypes.ScalingPolicy(nil), f.policies[id]...),
	}, nil
}

func (f *fakeAutoScaling) PutScalingPolicy(_ context.Context, in *aas.PutScalingPolicyInput, _ ...func(*aas.Options)) (*aas.PutScalingPolicyOutput, error) {
	id, name := aws.ToString(in.ResourceId), aws.ToString(in.PolicyName)
	f.calls = append(f.calls, fmt.Sprintf("PutScalingPolicy:%s/%s", id, name))
	if f.putPolicyErr != nil {
		return nil, f.putPolicyErr
	}
	written := aastypes.ScalingPolicy{
		PolicyName:        in.PolicyName,
		PolicyType:        in.PolicyType,
		ResourceId:        in.ResourceId,
		ServiceNamespace:  in.ServiceNamespace,
		ScalableDimension: in.ScalableDimension,
		// Recorded from the request, so a test asserts on what was actually asked for rather than on
		// what the fake decided to remember.
		TargetTrackingScalingPolicyConfiguration: in.TargetTrackingScalingPolicyConfiguration,
	}
	for i, existing := range f.policies[id] {
		if aws.ToString(existing.PolicyName) == name {
			f.policies[id][i] = written
			return &aas.PutScalingPolicyOutput{}, nil
		}
	}
	f.policies[id] = append(f.policies[id], written)
	return &aas.PutScalingPolicyOutput{}, nil
}

func (f *fakeAutoScaling) ListTagsForResource(_ context.Context, in *aas.ListTagsForResourceInput, _ ...func(*aas.Options)) (*aas.ListTagsForResourceOutput, error) {
	arn := aws.ToString(in.ResourceARN)
	f.calls = append(f.calls, "ListTagsForResource:"+arn)
	if f.tagsErr != nil {
		return nil, f.tagsErr
	}
	if arn == "" {
		// What the real API does with no resource ARN, and the reason the caller must not ask: a target
		// whose ARN the describe did not populate has no tags to read, and asking anyway turns a report
		// into a failed pass.
		return nil, fmt.Errorf("ValidationException: 1 validation error detected: value at 'resourceARN' failed to satisfy constraint")
	}
	return &aas.ListTagsForResourceOutput{Tags: f.tags[arn]}, nil
}

// policy returns one policy by name, or nil.
func (f *fakeAutoScaling) policy(resourceID, name string) *aastypes.ScalingPolicy {
	for i := range f.policies[resourceID] {
		if aws.ToString(f.policies[resourceID][i].PolicyName) == name {
			return &f.policies[resourceID][i]
		}
	}
	return nil
}

func (f *fakeAutoScaling) target(resourceID string) *aastypes.ScalableTarget {
	return f.targets[resourceID]
}

// writes are the mutating calls this fake saw, which is what "a second pass writes nothing" asserts on.
func (f *fakeAutoScaling) writes() []string {
	var out []string
	for _, call := range f.calls {
		if strings.HasPrefix(call, "Describe") || strings.HasPrefix(call, "ListTags") {
			continue
		}
		out = append(out, call)
	}
	return out
}
