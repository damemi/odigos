package observer

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorimage"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorstatus"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/collector/deployer"
)

// ECSReader is the read-only slice of ECS this package uses.
//
// READ-ONLY is the contract, not a description: in manual mode this interface is the only way a
// Collector is reached at all, and Odigos never touches infrastructure the customer owns. Every method
// here is a Describe or a List, and readonly_test.go fails the build's tests if a verb that writes
// appears in this package at all.
type ECSReader interface {
	ListClusters(ctx context.Context, in *ecs.ListClustersInput, opts ...func(*ecs.Options)) (*ecs.ListClustersOutput, error)
	DescribeServices(ctx context.Context, in *ecs.DescribeServicesInput, opts ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	DescribeTaskDefinition(ctx context.Context, in *ecs.DescribeTaskDefinitionInput, opts ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error)
	ListTasks(ctx context.Context, in *ecs.ListTasksInput, opts ...func(*ecs.Options)) (*ecs.ListTasksOutput, error)
	DescribeTasks(ctx context.Context, in *ecs.DescribeTasksInput, opts ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error)
}

// ELBReader is the read-only slice of Elastic Load Balancing.
type ELBReader interface {
	DescribeTargetGroups(ctx context.Context, in *elb.DescribeTargetGroupsInput, opts ...func(*elb.Options)) (*elb.DescribeTargetGroupsOutput, error)
	DescribeTargetHealth(ctx context.Context, in *elb.DescribeTargetHealthInput, opts ...func(*elb.Options)) (*elb.DescribeTargetHealthOutput, error)
	DescribeLoadBalancers(ctx context.Context, in *elb.DescribeLoadBalancersInput, opts ...func(*elb.Options)) (*elb.DescribeLoadBalancersOutput, error)
	DescribeListeners(ctx context.Context, in *elb.DescribeListenersInput, opts ...func(*elb.Options)) (*elb.DescribeListenersOutput, error)
}

// ObserveInput is what one read pass needs to know.
type ObserveInput struct {
	Region    string
	NetworkID string
	// ClusterRef is the customer's required `clusterArn`. A name and an ARN are both accepted by
	// DescribeServices, which is why this is a ref rather than a name.
	ClusterRef string
}

// stoppedTaskSample is how many stopped tasks are described.
//
// Enough to see past a routine replacement to the fault behind it, and small enough that one pass over a
// crash-looping Collector does not describe a hundred tasks that all stopped for the same reason. ECS
// keeps stopped tasks for about an hour, so this is a window and not a history.
const stoppedTaskSample = 10

// Observe reads everything about one network's Collector, and never writes.
//
// A failed read is recorded as a PROBLEM and the pass continues, which is the whole shape of this
// function. The alternative — returning the first error — would mean a throttled DescribeTargetHealth
// cost the service state, the task reasons and the endpoint too, and a Collector would be reported as
// unobservable because one of six calls was unlucky. The severity of each says how much was lost:
// failing to describe the service is Degraded, because nothing after it can be concluded, and everything
// else is Info, because the primary facts still stand.
func Observe(ctx context.Context, ecsAPI ECSReader, elbAPI ELBReader, in ObserveInput) Observation {
	var out Observation
	name := deployer.ServiceName(in.Region)

	service, err := describeService(ctx, ecsAPI, in.ClusterRef, name)
	if err != nil {
		out.Partial = true
		out.Problems = append(out.Problems, collectorstatus.Problem{
			Code:     ProblemObservationFailed,
			Severity: collectorstatus.SeverityDegraded,
			Message: fmt.Sprintf("Odigos could not read the state of the Collector on %s: %v. Nothing below "+
				"is concluded from this pass — the Collector itself may be perfectly healthy",
				in.NetworkID, err),
		})
		return out
	}
	if service == nil {
		return out
	}

	out.ServiceFound = true
	out.Status = aws.ToString(service.Status)
	out.ServiceARN = aws.ToString(service.ServiceArn)
	out.ClusterARN = aws.ToString(service.ClusterArn)
	out.RevisionARN = aws.ToString(service.TaskDefinition)
	out.Tags = ecsTagMap(service.Tags)
	out.DesiredCount = int(service.DesiredCount)
	out.RunningCount = int(service.RunningCount)
	out.PendingCount = int(service.PendingCount)
	out.Rollout, out.RolloutReason = primaryRollout(service.Deployments)
	out.Subnets, out.SecurityGroups = awsvpcConfiguration(service.NetworkConfiguration)

	if out.RevisionARN != "" {
		revision, err := describeRevision(ctx, ecsAPI, out.RevisionARN)
		if err != nil {
			out.Partial = true
			out.Problems = append(out.Problems, readFailed(in.NetworkID, "its task definition", err))
		} else {
			out.Revision = revision
		}
	}

	stopped, err := describeStoppedTasks(ctx, ecsAPI, in.ClusterRef, name)
	if err != nil {
		out.Partial = true
		out.Problems = append(out.Problems, readFailed(in.NetworkID, "its recently stopped tasks", err))
	} else {
		out.StoppedTasks = stopped
	}

	groups, loadBalancer, err := describeIngress(ctx, elbAPI, service.LoadBalancers)
	if err != nil {
		out.Partial = true
		out.Problems = append(out.Problems, readFailed(in.NetworkID, "its load balancer target health", err))
	} else {
		out.TargetGroups, out.LoadBalancer = groups, loadBalancer
	}
	return out
}

// readFailed is the Info-level note for a secondary read that did not come back.
//
// Info rather than Degraded because the primary facts stand: the service, its counts and its status were
// read, so the report is still worth reading and the Collector is not reported as faulty for a read that
// was throttled. What it costs is a specific diagnosis, and saying so is better than a report that looks
// complete.
func readFailed(networkID, what string, err error) collectorstatus.Problem {
	return collectorstatus.Problem{
		Code:     ProblemObservationFailed,
		Severity: collectorstatus.SeverityInfo,
		Message: fmt.Sprintf("Odigos could not read %s for the Collector on %s, so this report is missing "+
			"whatever that would have said: %v", what, networkID, err),
	}
}

// describeService resolves the Collector's service, or nil when there is none.
//
// A service ECS cannot find is a FAILURE in the response rather than an error, and one that was deleted
// lingers as INACTIVE — returned rather than treated as absent, because "being removed" and "never
// existed" have different remedies and only the caller can tell them apart.
func describeService(ctx context.Context, api ECSReader, cluster, name string) (*ecstypes.Service, error) {
	out, err := api.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  aws.String(cluster),
		Services: []string{name},
		// TAGS through the describe rather than a second ListTagsForResource call, which manual mode is
		// not granted: the tags are how a reader is told whether Odigos created this Collector.
		Include: []ecstypes.ServiceField{ecstypes.ServiceFieldTags},
	})
	if err != nil {
		return nil, fmt.Errorf("describe the ECS service %s: %w", name, err)
	}
	for i := range out.Services {
		if aws.ToString(out.Services[i].ServiceName) == name {
			return &out.Services[i], nil
		}
	}
	return nil, nil
}

// describeRevision reads the task definition's own content.
func describeRevision(ctx context.Context, api ECSReader, arn string) (Revision, error) {
	out, err := api.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{TaskDefinition: aws.String(arn)})
	if err != nil {
		return Revision{}, fmt.Errorf("describe the task definition %s: %w", arn, err)
	}
	if out.TaskDefinition == nil {
		return Revision{}, fmt.Errorf("describe the task definition %s returned nothing", arn)
	}
	td := out.TaskDefinition

	revision := Revision{
		ARN:    aws.ToString(td.TaskDefinitionArn),
		CPU:    aws.ToString(td.Cpu),
		Memory: aws.ToString(td.Memory),
	}
	if td.RuntimePlatform != nil {
		switch td.RuntimePlatform.CpuArchitecture {
		case ecstypes.CPUArchitectureX8664:
			revision.Architecture = string(collectorimage.ArchAMD64)
		case ecstypes.CPUArchitectureArm64:
			revision.Architecture = string(collectorimage.ArchARM64)
		}
	}
	for _, container := range td.ContainerDefinitions {
		if aws.ToString(container.Name) != deployer.CollectorContainerName {
			// A definition with a container Odigos did not put there is a customer's own artifact or a
			// sidecar somebody added. Only the Collector's own container is read: taking the image from
			// whichever container came first would report a sidecar's digest as the Collector's build.
			continue
		}
		revision.Image = aws.ToString(container.Image)
		revision.Digest, revision.Tag = splitImageRef(revision.Image)
		revision.Environment = environmentMap(container.Environment)
		if container.RepositoryCredentials != nil {
			revision.RepositoryCredentialsRef = aws.ToString(container.RepositoryCredentials.CredentialsParameter)
		}
		revision.Secrets = make(map[string]string, len(container.Secrets))
		for _, secret := range container.Secrets {
			revision.Secrets[aws.ToString(secret.Name)] = aws.ToString(secret.ValueFrom)
		}
		if container.LogConfiguration != nil {
			revision.LogGroup = container.LogConfiguration.Options["awslogs-group"]
		}
	}
	return revision, nil
}

// splitImageRef separates an image reference into its digest and its tag.
//
// Both, and neither is derived from the other: the digest is the image's identity and a drift check runs
// on it, while the tag is only what a human reads. A reference can carry both — `repo:v1@sha256:…`
// is legal — so this does not treat one as the absence of the other.
func splitImageRef(image string) (digest, tag string) {
	ref := image
	if i := strings.LastIndex(ref, "@"); i >= 0 {
		digest = ref[i+1:]
		ref = ref[:i]
	}
	// The tag is after the LAST colon, and only when that colon is in the final path segment — a
	// registry host may carry a port (`registry.example.com:5000/odigos/collector`), and reading that as
	// a tag would report every private-registry deployment as running a tag called "5000/odigos/collector".
	last := ref
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		last = ref[i+1:]
	}
	if i := strings.LastIndex(last, ":"); i >= 0 {
		tag = last[i+1:]
	}
	return digest, tag
}

// describeStoppedTasks reads the service's recently stopped tasks, newest first.
func describeStoppedTasks(ctx context.Context, api ECSReader, cluster, service string) ([]StoppedTask, error) {
	listed, err := api.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster:       aws.String(cluster),
		ServiceName:   aws.String(service),
		DesiredStatus: ecstypes.DesiredStatusStopped,
		MaxResults:    aws.Int32(stoppedTaskSample),
	})
	if err != nil {
		return nil, fmt.Errorf("list the stopped tasks of %s: %w", service, err)
	}
	if len(listed.TaskArns) == 0 {
		return nil, nil
	}

	described, err := api.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(cluster),
		Tasks:   listed.TaskArns,
	})
	if err != nil {
		return nil, fmt.Errorf("describe the stopped tasks of %s: %w", service, err)
	}

	out := make([]StoppedTask, 0, len(described.Tasks))
	for _, task := range described.Tasks {
		stopped := StoppedTask{
			ARN:       aws.ToString(task.TaskArn),
			StopCode:  string(task.StopCode),
			Reason:    aws.ToString(task.StoppedReason),
			StoppedAt: task.StoppedAt,
		}
		for _, container := range task.Containers {
			if reason := aws.ToString(container.Reason); reason != "" {
				stopped.ContainerReasons = append(stopped.ContainerReasons, reason)
			}
			if container.ExitCode != nil && aws.ToString(container.Name) == deployer.CollectorContainerName {
				stopped.ExitCode = container.ExitCode
			}
		}
		out = append(out, stopped)
	}

	// Newest first. ListTasks does not promise an order, and the diagnosis reports the most recent
	// unexpected stop — over an unordered list that would be an arbitrary one, so a Collector that
	// recovered would keep reporting the failure it recovered from.
	sort.SliceStable(out, func(i, j int) bool {
		switch {
		case out[i].StoppedAt == nil:
			return false
		case out[j].StoppedAt == nil:
			return true
		}
		return out[i].StoppedAt.After(*out[j].StoppedAt)
	})
	return out, nil
}

// describeIngress reads the load balancer's verdict on the tasks, reached through the SERVICE's own
// attachments.
//
// Through the service rather than by looking a load balancer up by name, and that is what makes manual
// mode work: the load balancer is the customer's and they may have named it anything, so a name lookup
// would find nothing and publish no endpoint for a Collector that has one. What holds in both modes is
// that the service is attached to the target groups its load balancer forwards to.
func describeIngress(ctx context.Context, api ELBReader, attachments []ecstypes.LoadBalancer) (
	[]TargetGroup, LoadBalancer, error) {
	arns := make([]string, 0, len(attachments))
	ports := make(map[string]int, len(attachments))
	for _, attachment := range attachments {
		arn := aws.ToString(attachment.TargetGroupArn)
		if arn == "" {
			continue
		}
		arns = append(arns, arn)
		ports[arn] = int(aws.ToInt32(attachment.ContainerPort))
	}
	if len(arns) == 0 {
		// No attachment at all, which is the `endpointOverride` shape rather than a fault: Odigos
		// created no load balancer, so there is no target health to read.
		return nil, LoadBalancer{}, nil
	}

	described, err := api.DescribeTargetGroups(ctx, &elb.DescribeTargetGroupsInput{TargetGroupArns: arns})
	if err != nil {
		return nil, LoadBalancer{}, fmt.Errorf("describe the target groups: %w", err)
	}

	groups := make([]TargetGroup, 0, len(described.TargetGroups))
	var loadBalancerARN string
	for _, group := range described.TargetGroups {
		arn := aws.ToString(group.TargetGroupArn)
		port := ports[arn]
		if port == 0 {
			port = int(aws.ToInt32(group.Port))
		}
		if loadBalancerARN == "" && len(group.LoadBalancerArns) > 0 {
			loadBalancerARN = group.LoadBalancerArns[0]
		}

		health, err := api.DescribeTargetHealth(ctx, &elb.DescribeTargetHealthInput{TargetGroupArn: aws.String(arn)})
		if err != nil {
			return nil, LoadBalancer{}, fmt.Errorf("describe the health of target group %s: %w", arn, err)
		}
		groups = append(groups, TargetGroup{
			ARN:             arn,
			Name:            aws.ToString(group.TargetGroupName),
			Port:            port,
			HealthCheckPort: aws.ToString(group.HealthCheckPort),
			Targets:         targetsOf(health.TargetHealthDescriptions),
		})
	}
	// In data-port order, so two passes describe the same Collector in the same order and the recorded
	// message does not change between identical passes.
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Port < groups[j].Port })

	if loadBalancerARN == "" {
		// Target groups that no listener forwards to yet. Real between a crash and the next converge,
		// and not a fault: the groups exist and the load balancer they will belong to does not answer
		// for them yet.
		return groups, LoadBalancer{}, nil
	}
	balancers, err := api.DescribeLoadBalancers(ctx, &elb.DescribeLoadBalancersInput{
		LoadBalancerArns: []string{loadBalancerARN},
	})
	if err != nil {
		return groups, LoadBalancer{}, fmt.Errorf("describe the load balancer %s: %w", loadBalancerARN, err)
	}
	for _, lb := range balancers.LoadBalancers {
		out := LoadBalancer{
			ARN:     aws.ToString(lb.LoadBalancerArn),
			DNSName: aws.ToString(lb.DNSName),
			Scheme:  string(lb.Scheme),
		}
		listeners, err := api.DescribeListeners(ctx, &elb.DescribeListenersInput{LoadBalancerArn: lb.LoadBalancerArn})
		if err != nil {
			return groups, LoadBalancer{}, fmt.Errorf("describe load balancer listeners: %w", err)
		}
		out.ListenerTargets = make(map[int]string, len(listeners.Listeners))
		for _, listener := range listeners.Listeners {
			for _, action := range listener.DefaultActions {
				if action.Type == elbtypes.ActionTypeEnumForward {
					out.ListenerTargets[int(aws.ToInt32(listener.Port))] = aws.ToString(action.TargetGroupArn)
					break
				}
			}
		}
		if lb.State != nil {
			out.State = string(lb.State.Code)
		}
		return groups, out, nil
	}
	return groups, LoadBalancer{}, nil
}

func targetsOf(descriptions []elbtypes.TargetHealthDescription) []Target {
	out := make([]Target, 0, len(descriptions))
	for _, d := range descriptions {
		target := Target{}
		if d.Target != nil {
			target.ID = aws.ToString(d.Target.Id)
			target.Port = int(aws.ToInt32(d.Target.Port))
		}
		if d.TargetHealth != nil {
			target.State = string(d.TargetHealth.State)
			target.Reason = string(d.TargetHealth.Reason)
			target.Description = aws.ToString(d.TargetHealth.Description)
		}
		out = append(out, target)
	}
	return out
}

// primaryRollout returns the PRIMARY deployment's rollout state, which is the one that says whether the
// revision the service wants is actually running.
func primaryRollout(deployments []ecstypes.Deployment) (state, reason string) {
	for _, d := range deployments {
		if aws.ToString(d.Status) != "PRIMARY" {
			continue
		}
		return string(d.RolloutState), aws.ToString(d.RolloutStateReason)
	}
	return "", ""
}

func awsvpcConfiguration(cfg *ecstypes.NetworkConfiguration) (subnets, securityGroups []string) {
	if cfg == nil || cfg.AwsvpcConfiguration == nil {
		return nil, nil
	}
	return cfg.AwsvpcConfiguration.Subnets, cfg.AwsvpcConfiguration.SecurityGroups
}

func ecsTagMap(tags []ecstypes.Tag) map[string]string {
	out := make(map[string]string, len(tags))
	for _, t := range tags {
		out[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return out
}

func environmentMap(pairs []ecstypes.KeyValuePair) map[string]string {
	out := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		out[aws.ToString(pair.Name)] = aws.ToString(pair.Value)
	}
	return out
}
