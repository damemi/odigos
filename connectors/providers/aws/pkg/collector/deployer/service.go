package deployer

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/base/domain/collectoringress"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorownership"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorstatus"
	collectorsettings "github.com/odigos-io/odigos/connectors/providers/aws/pkg/collector/settings"
)

// healthCheckGracePeriod is how long ECS ignores load balancer health for a freshly started task.
//
// Not zero, which is the default and the trap: the Supervisor validates the remote configuration and
// starts the Collector as a child process (`bootstrap_timeout: 30s` in supervisor.yaml), so the health
// port does not answer for several seconds after the task starts. With no grace period the load
// balancer reports the target unhealthy, ECS kills the task, and the service restarts it for ever —
// a crash loop whose cause is a timer, not the Collector.
const healthCheckGracePeriod int32 = 120

// Deployment percentages, set explicitly rather than left to the ECS defaults.
//
// 100/200 means a rolling replacement starts every new task before stopping any old one. For a
// telemetry egress tier that is the difference between a deploy nobody notices and one that drops the
// spans arriving during it — the Collector's own queue does not help when the task holding it is the
// one being replaced.
const (
	minimumHealthyPercent int32 = 100
	maximumPercent        int32 = 200
)

// ServiceClient is the slice of ECS this file uses.
//
// Deliberately absent: DeleteService. Nothing in a converge path may destroy a service — a mode change
// never deletes and teardown is #14, which gates every delete on MayDelete. A converge that
// could delete is one bug away from doing it on a describe that came back empty.
type ServiceClient interface {
	DescribeServices(ctx context.Context, in *ecs.DescribeServicesInput, opts ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	CreateService(ctx context.Context, in *ecs.CreateServiceInput, opts ...func(*ecs.Options)) (*ecs.CreateServiceOutput, error)
	UpdateService(ctx context.Context, in *ecs.UpdateServiceInput, opts ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error)
	TagResource(ctx context.Context, in *ecs.TagResourceInput, opts ...func(*ecs.Options)) (*ecs.TagResourceOutput, error)
}

// ServiceInput is what converging one regional Collector service depends on. HostNetworkID is physical
// placement metadata; the service identity is Region.
type ServiceInput struct {
	HostNetworkID string
	Region        string
	Connector     string
	ConnectorID   string
	Identity      collectorownership.Identity
	Mode          string

	// Cluster is where the service lives, from EnsureCluster.
	ClusterARN string
	// ClusterSupportsSpot decides whether a capacity-provider strategy can be used at all — a strategy
	// naming a provider the cluster has not associated is refused by ECS.
	ClusterSupportsSpot bool
	// TaskDefinitionARN is the revision to run, from EnsureTaskDefinition.
	TaskDefinitionARN string

	// Subnets and SecurityGroupIDs are the awsvpc configuration: placement's chosen subnets and
	// the Collector's own security group plus anything the customer supplied.
	Subnets          []string
	SecurityGroupIDs []string
	AssignPublicIP   bool

	// TargetGroupARNs attach the service to the internal NLB, in data-port order. EMPTY when
	// `endpointOverride` is set: Odigos created no load balancer, so there is nothing to attach to and
	// the customer's own front reaches the tasks directly.
	TargetGroupARNs []string

	// Scaling is the policy, resolved by the caller. Resolved rather than read here so that this
	// converge and EnsureScaling cannot disagree about which control is live — two readings of the mode
	// is how a Collector ends up with a fixed count and an autoscaler at once. In `auto` mode the desired
	// count belongs to Application Auto Scaling and this converge must not touch it, and in a mode
	// neither of them recognises it belongs to nobody: see desiredCountChange.
	Scaling collectorsettings.ResolvedScaling

	// UseFargateSpot is the per-regional-Collector opt-in.
	UseFargateSpot bool

	// Recorded is whether this connector's managed_collectors row already names this service. It is the
	// SECOND half of the deletion gate and the thing that tells "this connector's own service"
	// apart from "a service missing from its state", which is what adoption is for.
	Recorded bool
}

// Service is the converged ECS service for one regional Collector.
type Service struct {
	ARN  string
	Name string

	// Created and Updated are what this pass did. Both false is the steady state.
	Created bool
	Updated bool

	// Adopted, AdoptionReason and InheritsDeleteAuthority record an adoption, which is never silent:
	// "Odigos took its own stack back" and "Odigos now converges a stack it will never remove"
	// look identical in the infrastructure and differ completely at teardown.
	Adopted                 bool
	AdoptionReason          string
	InheritsDeleteAuthority bool

	DesiredCount int32
	RunningCount int32
	Problems     []collectorstatus.Problem
}

// Ready reports whether the regional Collector is deployed.
func (s Service) Ready() bool { return s.ARN != "" && !collectorstatus.HasError(s.Problems) }

// EnsureService converges one regional Collector service. HostNetworkID remains physical placement
// metadata for its awsvpc host and tags; it never selects the service identity.
//
// Every write is gated on collectorownership.MayMutate and every adoption on
// collectorownership.AdoptionOf — computed there and read here, never re-derived from the tags. That is
// not tidiness: a deployer that re-derives the decision is free to disagree with the teardown path in
// #14, and the disagreement is a customer's infrastructure deleted.
//
// The three things this function must never do, each of which the ownership package is what stops:
//
//   - adopt an untagged or unattributed service holding the name Odigos needs. There is no second name
//     to fall back to, so that network's deploy is skipped and the conflict reported by object.
//   - write TagManaged onto anything it did not create. Adoption converges; it never acquires deletion
//     authority, which is what makes flipping manual → automatic over a customer's own stack safe.
//   - delete. Not a mode change, not a name conflict, not a describe that came back empty. DeleteService
//     is not even in the client interface.
func EnsureService(ctx context.Context, api ServiceClient, in ServiceInput) (Service, error) {
	out := Service{Name: ServiceName(in.Region)}
	if problems := modeGuard(in.Mode); len(problems) > 0 {
		out.Problems = problems
		return out, nil
	}
	if in.ClusterARN == "" || in.TaskDefinitionARN == "" {
		out.Problems = []collectorstatus.Problem{{
			Code:     ProblemServiceNotReady,
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("no Collector service is created for %s in %s: it has no cluster or no task "+
				"definition revision to run, and a service created without either would be a permanent object "+
				"describing a Collector that never existed", in.Region, in.HostNetworkID),
		}}
		return out, nil
	}

	desired := desiredService(in)

	existing, err := describeService(ctx, api, in.ClusterARN, out.Name)
	if err != nil {
		return out, err
	}
	if existing == nil {
		created, err := api.CreateService(ctx, createInput(in, desired,
			baseapi.CollectorManagedTags(in.Connector, in.ConnectorID, in.Region, in.HostNetworkID)))
		if err != nil {
			return out, fmt.Errorf("create ECS service %s: %w", out.Name, err)
		}
		if created.Service == nil {
			return out, fmt.Errorf("create ECS service %s returned no service", out.Name)
		}
		out.ARN = aws.ToString(created.Service.ServiceArn)
		out.Created = true
		out.DesiredCount = created.Service.DesiredCount
		log.Info("created collector ECS service",
			"region", in.Region, "network", in.HostNetworkID, "service", out.Name, "revision", in.TaskDefinitionARN)
		return out, nil
	}

	object := collectorownership.Object{
		Ref:      aws.ToString(existing.ServiceArn),
		Tags:     baseapi.ParseCollectorTags(ecsTagMap(existing.Tags)),
		Recorded: in.Recorded,
	}
	if !collectorownership.MayMutate(object, in.Identity, in.Mode) {
		out.Problems = []collectorstatus.Problem{nameConflict(in, object)}
		return out, nil
	}

	adoption := collectorownership.AdoptionOf(object, in.Identity, in.Mode)
	if adoption.Adopt {
		out.Adopted = true
		out.AdoptionReason = adoption.Reason
		out.InheritsDeleteAuthority = adoption.InheritsDeleteAuthority
		out.Problems = append(out.Problems, adoptionProblem(in, adoption))
		if adoption.RestampConnectorID {
			// The IDENTITY tags only. Not CollectorManagedTags — adoption never writes TagManaged
			// (Adoption.AcquiresOwnership is always false), because the tag records who CREATED an
			// object and adopting one does not change that. It is also what keeps the manual →
			// automatic → manual round trip safe: MayDelete's mode gate does that work, not a rewrite.
			if _, err := api.TagResource(ctx, &ecs.TagResourceInput{
				ResourceArn: existing.ServiceArn,
				Tags:        ecsTags(baseapi.CollectorIdentityTags(in.Connector, in.ConnectorID, in.Region, in.HostNetworkID)),
			}); err != nil {
				return out, fmt.Errorf("re-stamp the connector id on adopted service %s: %w", out.Name, err)
			}
			log.Info("adopted a predecessor's collector service and re-stamped its connector id",
				"region", in.Region, "network", in.HostNetworkID, "service", out.Name)
		}
	}

	out.ARN = aws.ToString(existing.ServiceArn)
	out.DesiredCount = existing.DesiredCount
	out.RunningCount = existing.RunningCount

	update, changes := updateFor(in, desired, existing)
	if len(changes) == 0 {
		return out, nil
	}
	if _, err := api.UpdateService(ctx, update); err != nil {
		return out, fmt.Errorf("update ECS service %s: %w", out.Name, err)
	}
	out.Updated = true
	if update.DesiredCount != nil {
		out.DesiredCount = aws.ToInt32(update.DesiredCount)
	}
	log.Info("converged collector ECS service",
		"region", in.Region, "network", in.HostNetworkID, "service", out.Name, "changed", strings.Join(changes, ","))
	return out, nil
}

// serviceSpec is the service shape Odigos decides, resolved once so the create and the diff cannot
// disagree about it.
type serviceSpec struct {
	taskDefinition string
	// desiredCount is the count a CREATE carries — and, in manual mode only, the count a converge
	// writes. In automatic mode it is the scalable target's lower bound, which a converge never writes
	// because manualCount is false.
	desiredCount int32
	// manualCount is whether the desired count is Odigos's to set at all. In `auto` scaling mode it is
	// Application Auto Scaling's, and in an unrecognised mode it is nobody's — see desiredCountChange.
	manualCount      bool
	subnets          []string
	securityGroups   []string
	assignPublicIP   ecstypes.AssignPublicIp
	loadBalancers    []ecstypes.LoadBalancer
	capacityStrategy []ecstypes.CapacityProviderStrategyItem
	// launchType is set only when a strategy cannot be used — a cluster with no capacity providers
	// associated, which is only ever a cluster the customer supplied.
	launchType ecstypes.LaunchType
}

func desiredService(in ServiceInput) serviceSpec {
	assign := ecstypes.AssignPublicIpDisabled
	if in.AssignPublicIP {
		assign = ecstypes.AssignPublicIpEnabled
	}

	spec := serviceSpec{
		taskDefinition: in.TaskDefinitionARN,
		desiredCount:   int32(in.Scaling.StartingCount()),
		manualCount:    in.Scaling.Manual(),
		subnets:        sortedCopy(in.Subnets),
		securityGroups: sortedCopy(in.SecurityGroupIDs),
		assignPublicIP: assign,
		loadBalancers:  loadBalancerAttachments(in.TargetGroupARNs),
	}

	// A capacity-provider STRATEGY rather than a launch type wherever the cluster can serve one, even
	// for plain on-demand Fargate. UpdateService can change a strategy and cannot change a launch type,
	// so a service created with a launch type could never be moved onto Spot without being recreated —
	// and recreating the service is a gap in the network's telemetry.
	provider := capacityProviderFargate
	if in.UseFargateSpot {
		provider = capacityProviderFargateSpot
	}
	if in.ClusterSupportsSpot {
		spec.capacityStrategy = []ecstypes.CapacityProviderStrategyItem{{
			CapacityProvider: aws.String(provider), Weight: 1,
		}}
	} else {
		// Only reachable on a cluster the customer supplied with no providers associated. Odigos does
		// not associate them there (that would replace a set their own services may depend on), so the
		// service runs on the launch type instead. A Spot opt-in in this state is already reported by
		// EnsureCluster.
		spec.launchType = ecstypes.LaunchTypeFargate
	}
	return spec
}

// loadBalancerAttachments maps each data port to its own target group, naming the container the port
// belongs to.
//
// The container name has to match the task definition's, and getting it wrong produces a service that
// will not start with an error about a container that "does not exist" in a definition where it plainly
// does — which is why both come from CollectorContainerName.
func loadBalancerAttachments(targetGroups []string) []ecstypes.LoadBalancer {
	ports := collectoringress.DataPorts()
	out := make([]ecstypes.LoadBalancer, 0, len(targetGroups))
	for i, arn := range targetGroups {
		if i >= len(ports) {
			break
		}
		out = append(out, ecstypes.LoadBalancer{
			TargetGroupArn: aws.String(arn),
			ContainerName:  aws.String(CollectorContainerName),
			ContainerPort:  aws.Int32(int32(ports[i])),
		})
	}
	return out
}

func createInput(in ServiceInput, spec serviceSpec, tags map[string]string) *ecs.CreateServiceInput {
	out := &ecs.CreateServiceInput{
		Cluster:        aws.String(in.ClusterARN),
		ServiceName:    aws.String(ServiceName(in.Region)),
		TaskDefinition: aws.String(spec.taskDefinition),
		DesiredCount:   aws.Int32(spec.desiredCount),
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
				Subnets:        spec.subnets,
				SecurityGroups: spec.securityGroups,
				AssignPublicIp: spec.assignPublicIP,
			},
		},
		LoadBalancers: spec.loadBalancers,
		// ECS rolling update, named explicitly. The other controllers (CODE_DEPLOY, EXTERNAL) cannot be
		// converged by UpdateService at all, so a service created under one would be permanently
		// undeployable by this loop.
		DeploymentController: &ecstypes.DeploymentController{Type: ecstypes.DeploymentControllerTypeEcs},
		DeploymentConfiguration: &ecstypes.DeploymentConfiguration{
			MinimumHealthyPercent: aws.Int32(minimumHealthyPercent),
			MaximumPercent:        aws.Int32(maximumPercent),
			// The deployment circuit breaker is deliberately NOT enabled. Its rollback rewrites
			// service.taskDefinition back to the previous revision, which this converge would read as
			// drift and set forward again — a deployment loop with no exit. Without it a failed
			// deployment leaves the old tasks serving and the new ones visibly failing, which is both
			// safer and diagnosable.
		},
		Tags: ecsTags(tags),
		// Tasks inherit the service's tags, so a task list shows which Collector a task belongs to
		// without a second lookup.
		PropagateTags: ecstypes.PropagateTagsService,
		// No ECS Exec. The image has no shell so a session would be useless, and the process holds the
		// customer's destination credential — the posture is worth declaring rather than inheriting.
		EnableExecuteCommand: false,
	}
	if len(spec.loadBalancers) > 0 {
		out.HealthCheckGracePeriodSeconds = aws.Int32(healthCheckGracePeriod)
	}
	if len(spec.capacityStrategy) > 0 {
		out.CapacityProviderStrategy = spec.capacityStrategy
	} else {
		out.LaunchType = spec.launchType
	}
	return out
}

// updateFor builds the UpdateService call and names what changed, or returns no changes at all.
//
// A field-by-field diff rather than an unconditional update, because an UpdateService that changes
// nothing is still a service event and, when it touches the task definition or the network
// configuration, a new deployment: every task replaced, and a Collector on ephemeral storage losing its
// queue. Steady state has to be zero writes, not one harmless-looking write.
func updateFor(in ServiceInput, spec serviceSpec, existing *ecstypes.Service) (*ecs.UpdateServiceInput, []string) {
	update := &ecs.UpdateServiceInput{
		Cluster: aws.String(in.ClusterARN),
		Service: existing.ServiceArn,
	}
	var changes []string

	if aws.ToString(existing.TaskDefinition) != spec.taskDefinition {
		update.TaskDefinition = aws.String(spec.taskDefinition)
		changes = append(changes, "task-definition")
	}
	if count, changed := desiredCountChange(spec, existing); changed {
		update.DesiredCount = aws.Int32(count)
		changes = append(changes, "desired-count")
	}
	if networkChanged(spec, existing) {
		update.NetworkConfiguration = &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
				Subnets:        spec.subnets,
				SecurityGroups: spec.securityGroups,
				AssignPublicIp: spec.assignPublicIP,
			},
		}
		changes = append(changes, "network")
	}
	if loadBalancersChanged(spec, existing) {
		update.LoadBalancers = spec.loadBalancers
		changes = append(changes, "load-balancers")
	}
	if strategyChanged(spec, existing) {
		update.CapacityProviderStrategy = spec.capacityStrategy
		changes = append(changes, "capacity-provider")
	}
	return update, changes
}

// desiredCountChange decides whether the replica count is Odigos's to set on this pass.
//
// In `auto` scaling mode it is NOT: Application Auto Scaling owns desiredCount and moves it in response
// to load, so a converge that set it back would fight the autoscaler on every pass — scaling out
// under load and being scaled straight back in, which is the one moment the extra capacity was needed.
// EnsureScaling registers the scalable target; this stays out of its way.
//
// In a mode NEITHER of them recognises the count is nobody's, and holding is what makes that safe.
// `manual` is the only value that writes, rather than "anything that is not auto": the obvious typo for
// `auto` is `automatic`, and a converge that read an unrecognised mode as manual would pin the Collector
// to the fixed count while the customer's working autoscaler pulled the other way. It is also what stops
// a Scaling nobody filled in from scaling a running Collector down to its zero value.
func desiredCountChange(spec serviceSpec, existing *ecstypes.Service) (int32, bool) {
	if !spec.manualCount {
		return 0, false
	}
	if existing.DesiredCount == spec.desiredCount {
		return 0, false
	}
	return spec.desiredCount, true
}

func networkChanged(spec serviceSpec, existing *ecstypes.Service) bool {
	if existing.NetworkConfiguration == nil || existing.NetworkConfiguration.AwsvpcConfiguration == nil {
		return true
	}
	actual := existing.NetworkConfiguration.AwsvpcConfiguration
	if actual.AssignPublicIp != spec.assignPublicIP {
		// ECS omits the field rather than returning DISABLED on some services, so an empty value is
		// treated as the default it means rather than as a difference.
		if !(actual.AssignPublicIp == "" && spec.assignPublicIP == ecstypes.AssignPublicIpDisabled) {
			return true
		}
	}
	return !sameStrings(actual.Subnets, spec.subnets) || !sameStrings(actual.SecurityGroups, spec.securityGroups)
}

func loadBalancersChanged(spec serviceSpec, existing *ecstypes.Service) bool {
	if len(existing.LoadBalancers) != len(spec.loadBalancers) {
		return true
	}
	key := func(lb ecstypes.LoadBalancer) string {
		return fmt.Sprintf("%s/%s/%d", aws.ToString(lb.TargetGroupArn), aws.ToString(lb.ContainerName), aws.ToInt32(lb.ContainerPort))
	}
	have := make([]string, 0, len(existing.LoadBalancers))
	for _, lb := range existing.LoadBalancers {
		have = append(have, key(lb))
	}
	want := make([]string, 0, len(spec.loadBalancers))
	for _, lb := range spec.loadBalancers {
		want = append(want, key(lb))
	}
	return !sameStrings(have, want)
}

// strategyChanged compares the capacity-provider strategy.
//
// A service on a LAUNCH TYPE is left alone even when a strategy is now wanted: UpdateService has no
// field that clears a launch type, so the transition needs the service recreated — and recreating it is
// a gap in the network's telemetry, which is not a price to pay for a cost preference. Only ever
// reachable on a customer's cluster that gained its capacity providers after the service was created.
func strategyChanged(spec serviceSpec, existing *ecstypes.Service) bool {
	if len(spec.capacityStrategy) == 0 || existing.LaunchType != "" {
		return false
	}
	if len(existing.CapacityProviderStrategy) != len(spec.capacityStrategy) {
		return true
	}
	for i := range spec.capacityStrategy {
		if aws.ToString(existing.CapacityProviderStrategy[i].CapacityProvider) !=
			aws.ToString(spec.capacityStrategy[i].CapacityProvider) {
			return true
		}
	}
	return false
}

// describeService resolves one service in a cluster, or nil.
//
// A service ECS cannot find is a FAILURE rather than an error, and one that was deleted lingers as
// INACTIVE or DRAINING — both treated as absent, because converging a service that is going away would
// mean updating something ECS is in the middle of removing.
func describeService(ctx context.Context, api serviceReader, cluster, name string) (*ecstypes.Service, error) {
	out, err := api.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  aws.String(cluster),
		Services: []string{name},
		Include:  []ecstypes.ServiceField{ecstypes.ServiceFieldTags},
	})
	if err != nil {
		return nil, fmt.Errorf("describe ECS service %s: %w", name, err)
	}
	for i := range out.Services {
		if aws.ToString(out.Services[i].ServiceName) != name {
			continue
		}
		if aws.ToString(out.Services[i].Status) != serviceStatusActive {
			return nil, nil
		}
		return &out.Services[i], nil
	}
	return nil, nil
}

// serviceStatusActive is the only status a service can be converged in.
const serviceStatusActive = "ACTIVE"

// nameConflict is the refusal for a service holding the name this regional Collector needs.
//
// Skipped and reported by object rather than worked around: `odigos-collector-<region>` is unique per
// cluster and there is no second name a customer's documentation would still recognise, so deploying
// alongside is not possible. The message names the class it fell into, because an untagged
// service and one tagged for another connector have different fixes.
func nameConflict(in ServiceInput, object collectorownership.Object) collectorstatus.Problem {
	class := collectorownership.Classify(object, in.Identity, in.Mode)
	detail := "it carries no Odigos tags, so it is not a Collector Odigos deployed"
	switch class.Attribution {
	case collectorownership.AttributionForeign:
		detail = fmt.Sprintf("it is tagged for connector %q, and another connector's Collector is never "+
			"adopted whatever it is named", object.Tags.Connector)
	case collectorownership.AttributionUnattributed:
		detail = "it is tagged as a Collector but names no connector, so Odigos will not claim it"
	}
	return collectorstatus.Problem{
		Code:     ProblemServiceNameConflict,
		Severity: collectorstatus.SeverityError,
		Message: fmt.Sprintf("no Collector is deployed in %s (host %s): the ECS service %q already exists and %s. That "+
			"name is unique per cluster, so Odigos cannot deploy alongside it — remove that service, point "+
			"collector.regions.%s.clusterArn at a different cluster, or leave this region without a Collector",
			in.Region, in.HostNetworkID, ServiceName(in.Region), detail, in.Region),
	}
}

// adoptionProblem states an adoption. Info, not a fault — nothing is wrong — but never silent, because
// what it means at TEARDOWN depends entirely on which kind it was.
func adoptionProblem(in ServiceInput, adoption collectorownership.Adoption) collectorstatus.Problem {
	authority := "Odigos will converge it and will never delete it: the ownership tag records who created " +
		"an object, adoption does not write one, and deletion requires it"
	if adoption.InheritsDeleteAuthority {
		authority = "it already carries Odigos's ownership tag from when Odigos created it, so teardown may " +
			"remove it again"
	}
	return collectorstatus.Problem{
		Code:     ProblemServiceAdopted,
		Severity: collectorstatus.SeverityInfo,
		Message: fmt.Sprintf("Odigos adopted the existing regional Collector service in %s (host %s) — %s. %s",
			in.Region, in.HostNetworkID, adoption.Reason, authority),
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := sortedCopy(a), sortedCopy(b)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// ParkInput is what scaling one idle Collector to zero depends on.
type ParkInput struct {
	Region        string
	HostNetworkID string
	Identity      collectorownership.Identity
	Mode          string
	ClusterARN    string
	// Recorded is whether managed_collectors names this service. Half the mutation record, and read for
	// the same reason the converge reads it: a service this connector has no record of is not one it
	// should be moving the replica count of.
	Recorded bool
}

// ParkService sets an idle Collector's desired count to zero, and CREATES NOTHING.
//
// The retention policy's reversible halfde: a network whose last enabled source was removed keeps
// its cluster, its load balancer, its target groups, its security group, its secrets and its task
// definition, and stops paying for tasks. Bringing it back is a scale-up rather than a redeployment,
// which is the whole reason this exists instead of tearing down at the first grace period — a
// redeployment moves the load balancer's DNS name, and every instrumented workload on the network is
// pointed at it.
//
// # Why this is not EnsureService with a zero count
//
// EnsureService creates a service it cannot find. An idle network has no placement decision behind it —
// no subnets, no ingress, no resolved image — so there is nothing to create one FROM, and a service
// created here would be a Collector nobody asked for, running no tasks, in subnets nobody chose. This
// finds or does nothing.
//
// # Why zero is allowed here and nowhere else
//
// collectorsettings.ReplicaFloor refuses a configured count below one, because a Collector at zero
// replicas still has a load balancer answering: every workload pointed at it keeps opening connections
// to an endpoint with nothing behind it and drops its telemetry with no failure anybody is watching.
// That hazard is precisely absent here — idle MEANS no enabled sources on the network, so nothing is
// pointed at this endpoint and there is nothing to drop. The cost is paid on the way back: a source
// enabled on a parked network is exporting before the Collector's tasks are running again, so the first
// telemetry after a network wakes up may be lost. That is why parking is opt-in and why keeping is what
// an unconfigured network does.
func ParkService(ctx context.Context, api ServiceClient, in ParkInput) (Service, []collectorstatus.Problem, error) {
	out := Service{Name: ServiceName(in.Region)}
	if problems := modeGuard(in.Mode); len(problems) > 0 {
		return out, problems, nil
	}
	if in.ClusterARN == "" {
		return out, nil, nil
	}

	existing, err := describeService(ctx, api, in.ClusterARN, out.Name)
	if err != nil {
		return out, nil, err
	}
	if existing == nil {
		// Already gone, or never built. Both are "nothing to park", and neither is a fault: a retention
		// policy that reported a missing service would put a standing finding on every network whose
		// Collector somebody removed by hand.
		return out, nil, nil
	}

	object := collectorownership.Object{
		Ref:      aws.ToString(existing.ServiceArn),
		Tags:     baseapi.ParseCollectorTags(ecsTagMap(existing.Tags)),
		Recorded: in.Recorded,
	}
	if !collectorownership.MayMutate(object, in.Identity, in.Mode) {
		// The same gate the converge uses, and the same refusal. Scaling a service Odigos may not write
		// to would be moving the replica count of a customer's own workload — which is a more surprising
		// thing to do to somebody than declining to save them money.
		return out, []collectorstatus.Problem{nameConflictForPark(in, object)}, nil
	}

	out.ARN = aws.ToString(existing.ServiceArn)
	out.DesiredCount = existing.DesiredCount
	out.RunningCount = existing.RunningCount
	if existing.DesiredCount == 0 {
		// Already parked. Returned without a write, which is what makes the steady state of an idle
		// network zero AWS writes per pass rather than one — the same property the converge path has.
		return out, nil, nil
	}

	if _, err := api.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster: aws.String(in.ClusterARN),
		// The ARN rather than the name. Both are accepted, and the ARN is what the converge path passes:
		// one spelling means a test that exercises one exercises the other.
		Service:      aws.String(out.ARN),
		DesiredCount: aws.Int32(0),
	}); err != nil {
		return out, nil, fmt.Errorf("scale the idle ECS service %s to zero: %w", out.Name, err)
	}
	out.Updated = true
	out.DesiredCount = 0
	log.Info("scaled an idle collector service to zero",
		"region", in.Region, "network", in.HostNetworkID, "service", out.Name, "was", existing.DesiredCount)
	return out, nil, nil
}

// nameConflictForPark is the refusal for a service the retention policy may not touch.
func nameConflictForPark(in ParkInput, object collectorownership.Object) collectorstatus.Problem {
	return collectorstatus.Problem{
		Code:     ProblemServiceNameConflict,
		Severity: collectorstatus.SeverityDegraded,
		Message: fmt.Sprintf("the Collector service %q in region %s (host network %s) has no enabled sources left, and Odigos "+
			"will not scale it down: it is not a service this connector may write to. Scale or remove it "+
			"yourself if it is no longer needed", ServiceName(in.Region), in.Region, in.HostNetworkID),
	}
}
