package deployer

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectoringress"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	collectorsettings "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/settings"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

const (
	testClusterARN = "arn:aws:ecs:eu-west-1:123456789012:cluster/odigos-collector"
	testRevision   = "arn:aws:ecs:eu-west-1:123456789012:task-definition/odigos-collector-vpc-0abc123def456789a:1"
)

var testTargetGroups = []string{
	"arn:aws:elasticloadbalancing:eu-west-1:123456789012:targetgroup/odigos-4317-x/1",
	"arn:aws:elasticloadbalancing:eu-west-1:123456789012:targetgroup/odigos-4318-x/2",
}

func serviceInput() ServiceInput {
	return ServiceInput{
		HostNetworkID:       testVPC,
		Region:              testRegion,
		Connector:           testConnector,
		ConnectorID:         testUID,
		Identity:            testIdentity(),
		Mode:                spec.ModeAutomatic,
		ClusterARN:          testClusterARN,
		ClusterSupportsSpot: true,
		TaskDefinitionARN:   testRevision,
		Subnets:             testSubnets,
		SecurityGroupIDs:    []string{"sg-collector"},
		TargetGroupARNs:     testTargetGroups,
		Scaling:             collectorsettings.Scaling{Mode: collectorsettings.ScalingManual, DesiredCount: 2}.Resolve(),
	}
}

func TestServiceNameIsRegional(t *testing.T) {
	if got, want := ServiceName(testRegion), "odigos-collector-"+testRegion; got != want {
		t.Fatalf("service name = %q, want %q", got, want)
	}
	if ServiceName(testRegion) == ServiceName("us-east-1") {
		t.Fatal("different regions share an ECS service identity")
	}
	// Host VPC placement is deliberately not part of the regional service identity.
	first, second := serviceInput(), serviceInput()
	second.HostNetworkID = "vpc-other-host"
	if ServiceName(first.Region) != ServiceName(second.Region) {
		t.Fatal("same-region service identity changed with host VPC")
	}
}

func TestEnsureServiceCreatesTheCollectorService(t *testing.T) {
	api := newFakeECS()
	got, err := EnsureService(context.Background(), api, serviceInput())
	if err != nil {
		t.Fatalf("EnsureService: %v", err)
	}
	if !got.Ready() || !got.Created {
		t.Fatalf("service = %+v / %v", got, codes(got.Problems))
	}

	svc := api.service(testClusterARN, ServiceName(testRegion))
	if svc == nil {
		t.Fatalf("no service created: %v", api.calls)
	}
	if aws.ToString(svc.TaskDefinition) != testRevision {
		t.Errorf("task definition = %q", aws.ToString(svc.TaskDefinition))
	}
	if svc.DesiredCount != 2 {
		t.Errorf("desired count = %d, want 2", svc.DesiredCount)
	}
	// The awsvpc configuration: placement's subnets, the Collector's own group, and no public address.
	vpc := svc.NetworkConfiguration.AwsvpcConfiguration
	if !reflect.DeepEqual(vpc.Subnets, testSubnets) || !reflect.DeepEqual(vpc.SecurityGroups, []string{"sg-collector"}) {
		t.Errorf("network configuration = %+v", vpc)
	}
	if vpc.AssignPublicIp != ecstypes.AssignPublicIpDisabled {
		t.Errorf("assignPublicIp = %q, want DISABLED", vpc.AssignPublicIp)
	}
	// The rolling controller, because the others cannot be converged by UpdateService at all.
	if svc.DeploymentController == nil || svc.DeploymentController.Type != ecstypes.DeploymentControllerTypeEcs {
		t.Errorf("deployment controller = %+v", svc.DeploymentController)
	}
	// A grace period, because the Supervisor validates and boots the Collector before the health port
	// answers — with none, the load balancer reports the target unhealthy and ECS kills it in a loop.
	if aws.ToInt32(svc.HealthCheckGracePeriodSeconds) == 0 {
		t.Error("no health check grace period; the Collector would be killed before it finished starting")
	}
	if svc.EnableExecuteCommand {
		t.Error("ECS Exec is enabled on a process holding the customer's destination credential")
	}
	// Tagged as Odigos-created — the first half of the deletion gate (§10.4).
	parsed := baseapi.ParseCollectorTags(ecsTagMap(svc.Tags))
	if !parsed.IsCollector() || !parsed.HasOwnershipMarker() || parsed.Region != testRegion || parsed.Network != testVPC {
		t.Errorf("service tags = %v", ecsTagMap(svc.Tags))
	}
}

// TestEachDataPortAttachesToItsOwnTargetGroup, naming the container the task definition declares — the
// two disagreeing produces a service that will not start with an error about a container that "does not
// exist" in a definition where it plainly does.
func TestEachDataPortAttachesToItsOwnTargetGroup(t *testing.T) {
	api := newFakeECS()
	if _, err := EnsureService(context.Background(), api, serviceInput()); err != nil {
		t.Fatal(err)
	}
	svc := api.service(testClusterARN, ServiceName(testRegion))
	if len(svc.LoadBalancers) != len(collectoringress.DataPorts()) {
		t.Fatalf("load balancers = %v", svc.LoadBalancers)
	}
	for i, port := range collectoringress.DataPorts() {
		lb := svc.LoadBalancers[i]
		if aws.ToString(lb.TargetGroupArn) != testTargetGroups[i] {
			t.Errorf("port %d attaches to %q, want %q", port, aws.ToString(lb.TargetGroupArn), testTargetGroups[i])
		}
		if aws.ToInt32(lb.ContainerPort) != int32(port) {
			t.Errorf("attachment %d names port %d, want %d", i, aws.ToInt32(lb.ContainerPort), port)
		}
		if aws.ToString(lb.ContainerName) != CollectorContainerName {
			t.Errorf("attachment %d names container %q, want %q", i, aws.ToString(lb.ContainerName), CollectorContainerName)
		}
	}
}

// TestNoTargetGroupsMeansNoAttachment — the endpointOverride case. Odigos created no load balancer, so
// there is nothing to attach to, and a grace period for a health check nothing performs would only
// delay ECS noticing a task that never starts.
func TestNoTargetGroupsMeansNoAttachment(t *testing.T) {
	api := newFakeECS()
	in := serviceInput()
	in.TargetGroupARNs = nil

	got, err := EnsureService(context.Background(), api, in)
	if err != nil || !got.Ready() {
		t.Fatalf("EnsureService: %v / %v", err, codes(got.Problems))
	}
	svc := api.service(testClusterARN, ServiceName(testRegion))
	if len(svc.LoadBalancers) != 0 {
		t.Errorf("load balancers = %v, want none", svc.LoadBalancers)
	}
	if svc.HealthCheckGracePeriodSeconds != nil {
		t.Error("a grace period was set with no load balancer health check to wait for")
	}
}

// TestACapacityProviderStrategyIsUsedWhereverPossible. UpdateService can change a strategy and cannot
// change a launch type, so a service created with a launch type could never be moved onto Spot without
// being recreated — and recreating it is a gap in the network's telemetry.
func TestACapacityProviderStrategyIsUsedWhereverPossible(t *testing.T) {
	t.Run("on demand", func(t *testing.T) {
		api := newFakeECS()
		if _, err := EnsureService(context.Background(), api, serviceInput()); err != nil {
			t.Fatal(err)
		}
		svc := api.service(testClusterARN, ServiceName(testRegion))
		if len(svc.CapacityProviderStrategy) != 1 ||
			aws.ToString(svc.CapacityProviderStrategy[0].CapacityProvider) != "FARGATE" {
			t.Fatalf("strategy = %v", svc.CapacityProviderStrategy)
		}
		if svc.LaunchType != "" {
			t.Errorf("launch type = %q; a launch type cannot later be moved onto Spot", svc.LaunchType)
		}
	})

	t.Run("spot opt-in", func(t *testing.T) {
		api := newFakeECS()
		in := serviceInput()
		in.UseFargateSpot = true
		if _, err := EnsureService(context.Background(), api, in); err != nil {
			t.Fatal(err)
		}
		svc := api.service(testClusterARN, ServiceName(testRegion))
		if aws.ToString(svc.CapacityProviderStrategy[0].CapacityProvider) != "FARGATE_SPOT" {
			t.Fatalf("strategy = %v", svc.CapacityProviderStrategy)
		}
	})

	// A cluster the customer supplied with no providers associated: a strategy naming one would be
	// refused by ECS, so the launch type is the only way to run at all.
	t.Run("a cluster with no providers", func(t *testing.T) {
		api := newFakeECS()
		in := serviceInput()
		in.ClusterSupportsSpot = false
		if _, err := EnsureService(context.Background(), api, in); err != nil {
			t.Fatal(err)
		}
		svc := api.service(testClusterARN, ServiceName(testRegion))
		if svc.LaunchType != ecstypes.LaunchTypeFargate {
			t.Errorf("launch type = %q, want FARGATE", svc.LaunchType)
		}
		if len(svc.CapacityProviderStrategy) != 0 {
			t.Errorf("strategy = %v, want none: ECS would refuse a provider the cluster has not associated", svc.CapacityProviderStrategy)
		}
	})
}

// TestASecondPassWritesNothingToTheService — the idempotency criterion. An UpdateService that touches
// the task definition or the network configuration starts a new deployment: every task replaced, and a
// Collector on ephemeral storage losing its queue.
func TestASecondPassWritesNothingToTheService(t *testing.T) {
	api := newFakeECS()
	in := serviceInput()
	first, err := EnsureService(context.Background(), api, in)
	if err != nil {
		t.Fatal(err)
	}
	in.Recorded = true // what the next pass sees once the ref is recorded

	for pass := 0; pass < 5; pass++ {
		api.calls = nil
		got, err := EnsureService(context.Background(), api, in)
		if err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		if got.Created || got.Updated {
			t.Fatalf("pass %d wrote: created=%v updated=%v", pass, got.Created, got.Updated)
		}
		if got.ARN != first.ARN {
			t.Errorf("pass %d = %q, want %q", pass, got.ARN, first.ARN)
		}
		if got.Adopted {
			t.Errorf("pass %d adopted its own service, which is already tracked", pass)
		}
		for _, call := range api.calls {
			if !strings.HasPrefix(call, "Describe") {
				t.Errorf("pass %d called %s", pass, call)
			}
		}
	}
}

// TestEveryConvergedFieldIsUpdatedAndNamed. Each of these is a configuration change that has to reach
// the running Collector, and each has to be the ONLY thing the update carries — an update that swept in
// unrelated fields would start deployments nobody asked for.
func TestEveryConvergedFieldIsUpdatedAndNamed(t *testing.T) {
	cases := []struct {
		name  string
		apply func(*ServiceInput)
		want  string
	}{
		{"a new revision", func(in *ServiceInput) { in.TaskDefinitionARN += "-2" }, "task-definition"},
		{"a new replica count", func(in *ServiceInput) { in.Scaling.DesiredCount = 4 }, "desired-count"},
		{"different subnets", func(in *ServiceInput) { in.Subnets = []string{"subnet-c"} }, "network"},
		{"a customer's security group", func(in *ServiceInput) { in.SecurityGroupIDs = []string{"sg-theirs"} }, "network"},
		{"a public address", func(in *ServiceInput) { in.AssignPublicIP = true }, "network"},
		{"a rebuilt target group", func(in *ServiceInput) { in.TargetGroupARNs = []string{testTargetGroups[0], "arn:new"} }, "load-balancers"},
		{"the Spot opt-in", func(in *ServiceInput) { in.UseFargateSpot = true }, "capacity-provider"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeECS()
			if _, err := EnsureService(context.Background(), api, serviceInput()); err != nil {
				t.Fatal(err)
			}

			changed := serviceInput()
			changed.Recorded = true
			tc.apply(&changed)
			api.calls = nil

			got, err := EnsureService(context.Background(), api, changed)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Updated {
				t.Fatalf("%s did not reach the service, so the change never takes effect", tc.name)
			}
			var update string
			for _, call := range api.calls {
				if strings.HasPrefix(call, "UpdateService:") {
					update = strings.TrimPrefix(call, "UpdateService:")
				}
			}
			if update != tc.want {
				t.Errorf("the update carried %q, want exactly %q", update, tc.want)
			}

			// And the pass after settles.
			api.calls = nil
			settled, err := EnsureService(context.Background(), api, changed)
			if err != nil {
				t.Fatal(err)
			}
			if settled.Updated {
				t.Errorf("the pass after the change updated again: %v", api.calls)
			}
		})
	}
}

// TestTheAutoscalerOwnsTheReplicaCount. In `auto` mode Application Auto Scaling moves desiredCount in
// response to load (§8.3), so a converge that set it back would fight the autoscaler on every pass —
// scaling out under load and being scaled straight in, at the one moment the capacity was needed.
func TestTheAutoscalerOwnsTheReplicaCount(t *testing.T) {
	api := newFakeECS()
	in := serviceInput()
	in.Scaling = collectorsettings.Scaling{Mode: collectorsettings.ScalingAuto, MinCount: 2, MaxCount: 10, DesiredCount: 2}.Resolve()
	if _, err := EnsureService(context.Background(), api, in); err != nil {
		t.Fatal(err)
	}

	// The autoscaler scales out.
	api.service(testClusterARN, ServiceName(testRegion)).DesiredCount = 7
	in.Recorded = true
	api.calls = nil

	got, err := EnsureService(context.Background(), api, in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Updated {
		t.Fatalf("the converge scaled the Collector back in: %v", api.calls)
	}
	if api.service(testClusterARN, ServiceName(testRegion)).DesiredCount != 7 {
		t.Error("the autoscaler's count was overwritten")
	}

	// In manual mode the count IS Odigos's, and the same drift is corrected.
	manual := serviceInput()
	manual.Recorded = true
	got, err = EnsureService(context.Background(), api, manual)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Updated || api.service(testClusterARN, ServiceName(testRegion)).DesiredCount != 2 {
		t.Error("manual mode did not hold the configured replica count")
	}
}

// --- ownership ---

// TestAnUntaggedServiceIsNeverAdopted — the "no silent untagged adoption" criterion. The name is unique
// per cluster, so there is no second name a customer's documentation would recognise: that network is
// skipped and the conflict reported by object (§10.4).
func TestAnUntaggedServiceIsNeverAdopted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tags   map[string]string
		detail string
	}{
		{"untagged", map[string]string{"team": "platform"}, "no Odigos tags"},
		{"a Collector naming no connector", map[string]string{
			baseapi.TagComponent: baseapi.ComponentCollector,
		}, "names no connector"},
		{"another connector's", baseapi.CollectorManagedTags("staging", "uid-other", testRegion, testVPC), "staging"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeECS().withService(testClusterARN, ServiceName(testRegion), tc.tags, nil)

			got, err := EnsureService(context.Background(), api, serviceInput())
			if err != nil {
				t.Fatal(err)
			}
			if got.ARN != "" || got.Adopted || got.Created || got.Updated {
				t.Fatalf("claimed a service Odigos may not write to: %+v", got)
			}
			if !collectorstatus.HasError(got.Problems) || got.Problems[0].Code != ProblemServiceNameConflict {
				t.Fatalf("problems = %v, want the name conflict", codes(got.Problems))
			}
			if !strings.Contains(got.Problems[0].Message, tc.detail) {
				t.Errorf("the message does not say which case this is: %s", got.Problems[0].Message)
			}
			for _, call := range api.calls {
				if !strings.HasPrefix(call, "Describe") {
					t.Errorf("wrote to a service Odigos may not touch: %v", api.calls)
				}
			}
		})
	}
}

// TestAPredecessorsServiceIsAdoptedAndRestamped. A recreated CR gets a new UID; its infrastructure is
// adopted by the stable connector NAME (§10.4) and the id tag re-stamped — and because the stack already
// carried the ownership marker from its original creation, delete authority comes back with it.
func TestAPredecessorsServiceIsAdoptedAndRestamped(t *testing.T) {
	predecessor := baseapi.CollectorManagedTags(testConnector, "uid-previous", testRegion, testVPC)
	api := newFakeECS().withService(testClusterARN, ServiceName(testRegion), predecessor, func(svc *ecstypes.Service) {
		svc.TaskDefinition = aws.String(testRevision)
		svc.DesiredCount = 2
	})

	got, err := EnsureService(context.Background(), api, serviceInput())
	if err != nil {
		t.Fatalf("EnsureService: %v", err)
	}
	if !got.Adopted {
		t.Fatal("a predecessor's service was not adopted")
	}
	if !got.InheritsDeleteAuthority {
		t.Error("delete authority was not inherited from a stack that already carried the ownership marker")
	}
	// Never silent: the adoption reaches the reported problems, because "Odigos took its own stack back"
	// and "Odigos now converges a stack it will never remove" differ completely at teardown.
	if !hasCode(got.Problems, ProblemServiceAdopted) {
		t.Fatalf("problems = %v, want the adoption reported", codes(got.Problems))
	}
	if collectorstatus.HasError(got.Problems) {
		t.Errorf("an adoption was reported as a fault: %v", got.Problems)
	}
	// The id is re-stamped, and the ownership marker is neither added nor removed.
	tags := ecsTagMap(api.service(testClusterARN, ServiceName(testRegion)).Tags)
	if tags[baseapi.TagConnectorID] != testUID {
		t.Errorf("connector id = %q, want %q", tags[baseapi.TagConnectorID], testUID)
	}
	if tags[baseapi.TagManaged] != baseapi.TagManagedTrue {
		t.Errorf("the ownership marker was removed by the re-stamp: %v", tags)
	}
}

// TestManualToAutomaticAdoptsWithoutAcquiringDeleteAuthority — the "safe manual→automatic" criterion,
// and the most consequential assertion in this file. A stack the customer applied from an artifact
// carries TagIaC and no TagManaged; adoption converges it and must NEVER write TagManaged, or Odigos
// would gain the right to delete infrastructure the customer built.
func TestManualToAutomaticAdoptsWithoutAcquiringDeleteAuthority(t *testing.T) {
	artifact := baseapi.CollectorIdentityTags(testConnector, testUID, testRegion, testVPC)
	artifact[baseapi.TagIaC] = "true"
	api := newFakeECS().withService(testClusterARN, ServiceName(testRegion), artifact, func(svc *ecstypes.Service) {
		svc.TaskDefinition = aws.String("arn:their-own-revision")
		svc.DesiredCount = 2
	})

	got, err := EnsureService(context.Background(), api, serviceInput())
	if err != nil {
		t.Fatalf("EnsureService: %v", err)
	}
	if !got.Ready() {
		t.Fatalf("a customer's stack was not converged: %v", codes(got.Problems))
	}
	// Converged: the revision Odigos wants is set, which is exactly what flipping to automatic asks for.
	if !got.Updated {
		t.Error("the adopted stack was not converged onto Odigos's revision")
	}
	// And no delete authority, by either route: not inherited, and not acquired by a tag write.
	if got.InheritsDeleteAuthority {
		t.Fatal("delete authority was inherited from a stack Odigos did not create")
	}
	tags := ecsTagMap(api.service(testClusterARN, ServiceName(testRegion)).Tags)
	if _, present := tags[baseapi.TagManaged]; present {
		t.Fatalf("adoption wrote the ownership marker onto a customer's stack, so teardown could now delete it: %v", tags)
	}
	if tags[baseapi.TagIaC] == "" {
		t.Error("the artifact marker was removed, which is what disqualifies deletion")
	}
	// The reported message has to say which kind of adoption this was.
	adoption := problemWith(t, got.Problems, ProblemServiceAdopted)
	if !strings.Contains(adoption.Message, "never delete") {
		t.Errorf("the message does not say Odigos will not delete it: %s", adoption.Message)
	}
}

// TestAPredecessorsArtifactStackIsRestampedWithoutGainingDeleteAuthority is the dangerous combination
// the two adoption paths meet in: a stack a customer APPLIED FROM AN ARTIFACT under an earlier
// incarnation of this connector. It is adopted by the name tag and its connector id is re-stamped — and
// that re-stamp is the one write adoption makes, so if it carried the ownership tag along, Odigos would
// silently gain the right to delete infrastructure the customer built.
func TestAPredecessorsArtifactStackIsRestampedWithoutGainingDeleteAuthority(t *testing.T) {
	artifact := baseapi.CollectorIdentityTags(testConnector, "uid-previous", testRegion, testVPC)
	artifact[baseapi.TagIaC] = "true"
	api := newFakeECS().withService(testClusterARN, ServiceName(testRegion), artifact, func(svc *ecstypes.Service) {
		svc.TaskDefinition = aws.String("arn:their-own-revision")
		svc.DesiredCount = 2
	})

	got, err := EnsureService(context.Background(), api, serviceInput())
	if err != nil {
		t.Fatalf("EnsureService: %v", err)
	}
	if !got.Adopted {
		t.Fatal("a predecessor's artifact stack was not adopted")
	}
	if got.InheritsDeleteAuthority {
		t.Fatal("delete authority was inherited from a stack the customer applied")
	}

	tags := ecsTagMap(api.service(testClusterARN, ServiceName(testRegion)).Tags)
	if tags[baseapi.TagConnectorID] != testUID {
		t.Errorf("the connector id was not re-stamped: %v", tags)
	}
	if _, present := tags[baseapi.TagManaged]; present {
		t.Fatalf("the re-stamp wrote the ownership marker onto a customer's stack, so teardown could now delete it: %v", tags)
	}
	if tags[baseapi.TagIaC] == "" {
		t.Error("the artifact marker was removed, which is what disqualifies deletion")
	}
	// And the re-stamp really did happen — otherwise this test would pass by writing nothing at all.
	var restamped bool
	for _, call := range api.calls {
		if strings.HasPrefix(call, "TagResource") {
			restamped = true
		}
	}
	if !restamped {
		t.Errorf("no re-stamp was written: %v", api.calls)
	}
}

// TestOurOwnServiceMissingFromStateIsReRecorded. Database restored empty, or a row lost: the tags are
// authority enough to re-record, and whatever they say is what authority comes back.
func TestOurOwnServiceMissingFromStateIsReRecorded(t *testing.T) {
	api := newFakeECS().withService(testClusterARN, ServiceName(testRegion), ourTags(testVPC), func(svc *ecstypes.Service) {
		svc.TaskDefinition = aws.String(testRevision)
		svc.DesiredCount = 2
	})

	in := serviceInput()
	in.Recorded = false
	got, err := EnsureService(context.Background(), api, in)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Adopted {
		t.Fatal("a service missing from state was not re-recorded")
	}
	if !got.InheritsDeleteAuthority {
		t.Error("delete authority was not restored for a stack carrying Odigos's own ownership marker")
	}
	// No re-stamp: the id already matches, so there is nothing to rewrite.
	for _, call := range api.calls {
		if strings.HasPrefix(call, "TagResource") {
			t.Errorf("re-stamped a service whose connector id already matched: %v", api.calls)
		}
	}
}

// TestManualModeConvergesNothing. The coordinator wires no deployer for a manual-mode type at all, so
// this is belt over braces — and the braces are what §10.5 rests on: a mode change never deletes and
// never re-tags.
func TestManualModeConvergesNothing(t *testing.T) {
	api := newFakeECS().withService(testClusterARN, ServiceName(testRegion), ourTags(testVPC), nil)
	in := serviceInput()
	in.Mode = spec.ModeManual

	got, err := EnsureService(context.Background(), api, in)
	if err != nil {
		t.Fatal(err)
	}
	if got.ARN != "" || got.Created || got.Updated || got.Adopted {
		t.Errorf("converged in manual mode: %+v", got)
	}
	if len(api.calls) != 0 {
		t.Errorf("touched ECS in manual mode: %v", api.calls)
	}
	if len(got.Problems) != 1 || got.Problems[0].Code != ProblemManualModeProvisioning {
		t.Errorf("problems = %v", codes(got.Problems))
	}
}

// TestNothingHereCanDelete. Not a mode change, not a name conflict, not a describe that came back
// empty. The interface is the guarantee: a converge that COULD delete is one bug away from doing it.
func TestNothingHereCanDelete(t *testing.T) {
	typ := reflect.TypeOf((*ServiceClient)(nil)).Elem()
	for i := 0; i < typ.NumMethod(); i++ {
		if name := typ.Method(i).Name; strings.HasPrefix(name, "Delete") {
			t.Errorf("ServiceClient declares %s; nothing in a converge path may destroy a service", name)
		}
	}
	source, err := packageSourceFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	// A CALL, not a mention: the file names DeleteService in a comment saying why it is absent, and a
	// guard that tripped on its own rationale would be deleted rather than fixed.
	for _, forbidden := range []string{"api.DeleteService(", "ForceNewDeployment: true"} {
		if strings.Contains(source, forbidden) {
			t.Errorf("service.go contains %q", forbidden)
		}
	}
}

// TestAServiceBeingDeletedIsTreatedAsAbsent. A deleted service lingers as INACTIVE or DRAINING, and
// updating one ECS is in the middle of removing fails in a way that reads as a permission problem.
func TestAServiceBeingDeletedIsTreatedAsAbsent(t *testing.T) {
	for _, status := range []string{"INACTIVE", "DRAINING"} {
		t.Run(status, func(t *testing.T) {
			api := newFakeECS().withService(testClusterARN, ServiceName(testRegion), ourTags(testVPC), func(svc *ecstypes.Service) {
				svc.Status = aws.String(status)
			})
			// The fake's create would collide on the key, so this asserts the DECISION: a non-active
			// service is not converged, and the pass tries to create instead.
			got, err := EnsureService(context.Background(), api, serviceInput())
			if err != nil {
				t.Fatal(err)
			}
			if got.Updated || got.Adopted {
				t.Errorf("converged a service in %s: %+v", status, got)
			}
			var created bool
			for _, call := range api.calls {
				if strings.HasPrefix(call, "CreateService") {
					created = true
				}
			}
			if !created {
				t.Errorf("calls = %v, want a create attempt", api.calls)
			}
		})
	}
}

func TestNoClusterOrRevisionCreatesNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(*ServiceInput)
	}{
		{"no cluster", func(in *ServiceInput) { in.ClusterARN = "" }},
		{"no revision", func(in *ServiceInput) { in.TaskDefinitionARN = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeECS()
			in := serviceInput()
			tc.apply(&in)

			got, err := EnsureService(context.Background(), api, in)
			if err != nil {
				t.Fatal(err)
			}
			if got.ARN != "" || len(api.calls) != 0 {
				t.Errorf("created a service with nothing to run: %+v / %v", got, api.calls)
			}
			if !collectorstatus.HasError(got.Problems) || got.Problems[0].Code != ProblemServiceNotReady {
				t.Fatalf("problems = %v", codes(got.Problems))
			}
		})
	}
}

func TestAFailedWriteIsAnError(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		api := newFakeECS()
		api.createServiceErr = errors.New("throttled")
		if _, err := EnsureService(context.Background(), api, serviceInput()); err == nil {
			t.Fatal("no error")
		}
	})
	t.Run("update", func(t *testing.T) {
		api := newFakeECS()
		if _, err := EnsureService(context.Background(), api, serviceInput()); err != nil {
			t.Fatal(err)
		}
		api.updateServiceErr = errors.New("throttled")
		changed := serviceInput()
		changed.Recorded = true
		changed.TaskDefinitionARN += "-2"
		if _, err := EnsureService(context.Background(), api, changed); err == nil {
			t.Fatal("no error")
		}
	})
}
