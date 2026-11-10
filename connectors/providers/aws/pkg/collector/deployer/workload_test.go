package deployer

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorimage"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectoringress"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	collectorsettings "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/settings"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

func workloadSettings() collectorsettings.Network {
	return collectorsettings.Network{
		Enabled: true,
		CPU:     collectorsettings.DefaultCPU,
		Memory:  collectorsettings.DefaultMemory,
		// The shipped default policy, spelled out rather than left zero: collectorsettings.Read always
		// fills the mode in, and a fixture with an empty one would exercise the unrecognised-mode hold
		// instead of the manual path every one of these tests is about.
		Scaling: collectorsettings.Scaling{
			Mode:         collectorsettings.ScalingManual,
			DesiredCount: collectorsettings.DefaultDesiredCount,
		},
		ClusterRef: ClusterName(),
	}
}

// readyPrerequisites is what the identity half hands over: the customer-owned execution role, a log
// group and two secret refs. The Collector container needs no task role because it calls no AWS APIs.
func readyPrerequisites() Prerequisites {
	return Prerequisites{
		ExecutionRoleARN: "arn:aws:iam::" + testAccount + ":role/OdigosCollectorExecutionRole",
		LogGroup:         LogGroupName(testConnector),
		Delivery: collectorimage.Delivery{
			LicenseSecretRef:    "arn:aws:secretsmanager:" + testRegion + ":" + testAccount + ":secret:license-AbCdEf",
			CredentialSecretRef: "arn:aws:secretsmanager:" + testRegion + ":" + testAccount + ":secret:credential-AbCdEf",
		},
		Credential: Credential{CollectorID: "col_1", CredentialID: "cred_1"},
	}
}

func workloadInput() WorkloadInput {
	return WorkloadInput{
		Key:           ingressKey(),
		HostNetworkID: testVPC,
		Mode:          spec.ModeAutomatic,
		Prerequisites: readyPrerequisites(),
		Settings:      workloadSettings(),
		OpAMPEndpoint: "wss://central.example.com/v1/opamp",
		Pin:           testPin(),
	}
}

func workloadProvisioner(clients *fakeClients) *Provisioner {
	return &Provisioner{Identity: testIdentity(), Provider: "aws", AccountID: testAccount, Clients: clients}
}

func TestEnsureWorkloadUsesTheConfiguredClusterAndBuildsARevision(t *testing.T) {
	clients := newFakeClients()
	got, err := workloadProvisioner(clients).EnsureWorkload(context.Background(), workloadInput())
	if err != nil {
		t.Fatalf("EnsureWorkload: %v", err)
	}
	if !got.Ready() {
		t.Fatalf("not ready: %v", codes(got.Problems))
	}
	if got.Cluster.Name != ClusterName() || !got.Cluster.SupportsSpot() {
		t.Errorf("cluster = %+v", got.Cluster)
	}
	if !got.TaskDefinition.Registered || got.TaskDefinition.Revision != 1 {
		t.Errorf("task definition = %+v", got.TaskDefinition)
	}
	if !strings.Contains(got.TaskDefinition.Image, "@sha256:") {
		t.Errorf("image = %q", got.TaskDefinition.Image)
	}
	if got.TaskDefinition.Architecture != "X86_64" {
		t.Errorf("architecture = %q", got.TaskDefinition.Architecture)
	}
}

// TestTheSteadyStateHasNoWrites — the acceptance criterion, over the whole flow rather than per
// function. Every pass after the first describes and does nothing.
func TestTheSteadyStateHasNoWrites(t *testing.T) {
	clients := newFakeClients()
	p := workloadProvisioner(clients)
	first, err := p.EnsureWorkload(context.Background(), workloadInput())
	if err != nil {
		t.Fatal(err)
	}

	for pass := 0; pass < 5; pass++ {
		clients.ecs.calls = nil
		got, err := p.EnsureWorkload(context.Background(), workloadInput())
		if err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		if !got.Ready() {
			t.Fatalf("pass %d is not ready: %v", pass, codes(got.Problems))
		}
		if got.TaskDefinition.Registered {
			t.Fatalf("pass %d registered a revision", pass)
		}
		if got.TaskDefinition.ARN != first.TaskDefinition.ARN || got.Cluster.ARN != first.Cluster.ARN {
			t.Errorf("pass %d produced different objects: %+v", pass, got)
		}
		for _, call := range clients.ecs.calls {
			if !strings.HasPrefix(call, "Describe") {
				t.Errorf("pass %d called %s", pass, call)
			}
		}
	}
	if clients.ecs.revisionCount(TaskDefinitionFamily(testRegion)) != 1 {
		t.Errorf("the family holds %d revisions after six passes", clients.ecs.revisionCount(TaskDefinitionFamily(testRegion)))
	}
}

// TestUnreadyPrerequisitesRegisterNothing. A revision names the execution role, the log group and the two
// secrets, so one registered before they exist could never start a task — and a revision is permanent,
// unlike the failure that produced it.
func TestUnreadyPrerequisitesRegisterNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(*Prerequisites)
	}{
		{"no execution role", func(p *Prerequisites) { p.ExecutionRoleARN = "" }},
		{"no log group", func(p *Prerequisites) { p.LogGroup = "" }},
		{"no licence secret", func(p *Prerequisites) { p.Delivery.LicenseSecretRef = "" }},
		{"no credential", func(p *Prerequisites) { p.Credential = Credential{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clients := newFakeClients()
			in := workloadInput()
			tc.apply(&in.Prerequisites)

			got, err := workloadProvisioner(clients).EnsureWorkload(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			if got.Ready() {
				t.Fatal("reported ready over incomplete prerequisites")
			}
			if !collectorstatus.HasError(got.Problems) {
				t.Fatalf("problems = %v, want an error", codes(got.Problems))
			}
			if len(clients.ecs.calls) != 0 {
				t.Errorf("touched ECS with incomplete prerequisites: %v", clients.ecs.calls)
			}
		})
	}
}

// TestTheSpotOptInReachesTheCluster — the "Spot opt-in" acceptance criterion, end to end from the
// setting.
func TestTheSpotOptInReachesTheCluster(t *testing.T) {
	// The pre-provisioned test cluster carries both providers, so the opt-in is served.
	clients := newFakeClients()
	in := workloadInput()
	in.Settings.UseFargateSpot = true

	got, err := workloadProvisioner(clients).EnsureWorkload(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Ready() {
		t.Fatalf("not ready: %v", codes(got.Problems))
	}
	if !got.Cluster.SupportsSpot() {
		t.Error("the configured cluster cannot serve the opt-in")
	}
	if len(got.Problems) > 0 {
		t.Errorf("problems = %v, want none: the opt-in is served", codes(got.Problems))
	}

	// On a cluster that cannot serve it, the Collector still runs and the saving is what is reported
	// lost — refusing would take a working Collector down over a cost preference.
	clients = newFakeClients()
	clients.ecs.clusters[ClusterName()].CapacityProviders = []string{"FARGATE"}
	got, err = workloadProvisioner(clients).EnsureWorkload(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Ready() {
		t.Fatalf("a Spot opt-in on a cluster that cannot serve it took the Collector down: %v", codes(got.Problems))
	}
	if !hasCode(got.Problems, ProblemSpotUnavailable) {
		t.Errorf("problems = %v, want the opt-in reported unavailable", codes(got.Problems))
	}
}

// TestACustomerClusterServesTheRevision, and is neither created nor tagged.
func TestACustomerClusterServesTheRevision(t *testing.T) {
	clients := newFakeClients()
	clients.ecs.withCluster("their-cluster", nil, []string{"FARGATE", "FARGATE_SPOT"})
	in := workloadInput()
	in.Settings.ClusterRef = "their-cluster"

	got, err := workloadProvisioner(clients).EnsureWorkload(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Ready() {
		t.Fatalf("not ready: %v", codes(got.Problems))
	}
	if got.Cluster.Name != "their-cluster" || !got.Cluster.CustomerNamed {
		t.Fatalf("cluster = %+v", got.Cluster)
	}
	// The revision is still Odigos's, in Odigos's own family — only the cluster is theirs.
	if !got.TaskDefinition.Registered {
		t.Error("no revision was registered into a customer's cluster")
	}
	for _, call := range clients.ecs.calls {
		if !strings.HasPrefix(call, "Describe") && !strings.HasPrefix(call, "RegisterTaskDefinition") {
			t.Errorf("unexpected cluster mutation while one was named: %v", clients.ecs.calls)
		}
	}
}

// TestAMissingClusterStopsBeforeTheRevision. A revision is permanent and a family accumulates them, so
// registering one for a network that has nowhere to run it is a lasting cost for a transient failure.
func TestAMissingClusterStopsBeforeTheRevision(t *testing.T) {
	clients := newFakeClients()
	in := workloadInput()
	in.Settings.ClusterRef = "typo"

	got, err := workloadProvisioner(clients).EnsureWorkload(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ready() {
		t.Fatal("reported ready with no cluster")
	}
	if !hasCode(got.Problems, ProblemClusterMissing) {
		t.Fatalf("problems = %v", codes(got.Problems))
	}
	for _, call := range clients.ecs.calls {
		if strings.HasPrefix(call, "Register") {
			t.Errorf("registered a revision for a network with no cluster: %v", clients.ecs.calls)
		}
	}
}

func TestManualModeBuildsNoWorkload(t *testing.T) {
	clients := newFakeClients()
	in := workloadInput()
	in.Mode = spec.ModeManual

	got, err := workloadProvisioner(clients).EnsureWorkload(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ready() {
		t.Fatal("reported ready in manual mode")
	}
	if len(clients.ecs.calls) != 0 {
		t.Errorf("touched ECS in manual mode: %v", clients.ecs.calls)
	}
	if len(got.Problems) != 1 || got.Problems[0].Code != ProblemManualModeProvisioning {
		t.Errorf("problems = %v", codes(got.Problems))
	}
}

// --- the service converge over the whole stack ---

func readyIngress() Ingress {
	return Ingress{
		SecurityGroupID: "sg-collector",
		LoadBalancerARN: "arn:aws:elasticloadbalancing:eu-west-1:123456789012:loadbalancer/net/odigos-col-x/1",
		TargetGroupARNs: testTargetGroups,
		Transport:       collectoringress.Transport{GRPCEndpoint: "odigos-col-x.elb.eu-west-1.amazonaws.com:4317"},
		EndpointSource:  EndpointSourceLoadBalancer,
		Tier:            collectoringress.TierNetworkIdentities,
	}
}

func serviceStateInput(clients *fakeClients, t *testing.T) ServiceStateInput {
	t.Helper()
	workload, err := workloadProvisioner(clients).EnsureWorkload(context.Background(), workloadInput())
	if err != nil {
		t.Fatalf("EnsureWorkload: %v", err)
	}
	if !workload.Ready() {
		t.Fatalf("workload not ready: %v", codes(workload.Problems))
	}
	return ServiceStateInput{
		Key:           ingressKey(),
		HostNetworkID: testVPC,
		Mode:          spec.ModeAutomatic,
		Workload:      workload,
		Ingress:       readyIngress(),
		Subnets:       testSubnets,
		Settings:      workloadSettings(),
	}
}

func TestEnsureCollectorServiceRunsTheWholeStack(t *testing.T) {
	clients := newFakeClients()
	in := serviceStateInput(clients, t)

	got, err := workloadProvisioner(clients).EnsureCollectorService(context.Background(), in)
	if err != nil {
		t.Fatalf("EnsureCollectorService: %v", err)
	}
	if !got.Ready() || !got.Created {
		t.Fatalf("service = %+v / %v", got, codes(got.Problems))
	}
	svc := clients.ecs.service(in.Workload.Cluster.ARN, ServiceName(testRegion))
	if svc == nil {
		t.Fatalf("no service: %v", clients.ecs.calls)
	}
	// It runs the revision the workload converge registered, in the cluster it converged, attached to
	// the target groups the ingress converge built, in the security group the ingress converge wrote the
	// rules onto.
	if aws.ToString(svc.TaskDefinition) != in.Workload.TaskDefinition.ARN {
		t.Errorf("task definition = %q", aws.ToString(svc.TaskDefinition))
	}
	if len(svc.LoadBalancers) != 2 {
		t.Errorf("load balancers = %v", svc.LoadBalancers)
	}
	groups := svc.NetworkConfiguration.AwsvpcConfiguration.SecurityGroups
	if len(groups) != 1 || groups[0] != "sg-collector" {
		t.Errorf("security groups = %v, want the group the ingress rules were written onto", groups)
	}
}

// TestACustomerSecurityGroupReplacesOdigosOwn. documents securityGroupIds as "supply your own
// instead of the created one", so a union would mean the rules Odigos converges still applied to a
// customer who deliberately took that over.
func TestACustomerSecurityGroupReplacesOdigosOwn(t *testing.T) {
	clients := newFakeClients()
	in := serviceStateInput(clients, t)
	in.Settings.SecurityGroupIDs = []string{"sg-theirs", "sg-theirs-2"}

	if _, err := workloadProvisioner(clients).EnsureCollectorService(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	groups := clients.ecs.service(in.Workload.Cluster.ARN, ServiceName(testRegion)).NetworkConfiguration.AwsvpcConfiguration.SecurityGroups
	if !reflect.DeepEqual(groups, []string{"sg-theirs", "sg-theirs-2"}) {
		t.Fatalf("security groups = %v, want only the customer's", groups)
	}
	if containsStringValue(groups, "sg-collector") {
		t.Error("Odigos's own group was kept alongside the customer's")
	}
}

// TestAnUnreadyHalfCreatesNoService. A service is a permanent object — it describes a Collector for as
// long as it exists — so it must not be created around a transient failure in one of the halves it
// names.
func TestAnUnreadyHalfCreatesNoService(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(*ServiceStateInput)
	}{
		{"no cluster", func(in *ServiceStateInput) { in.Workload.Cluster = Cluster{} }},
		{"no revision", func(in *ServiceStateInput) { in.Workload.TaskDefinition = TaskDefinition{} }},
		{"no security group", func(in *ServiceStateInput) { in.Ingress.SecurityGroupID = "" }},
		{"no endpoint", func(in *ServiceStateInput) { in.Ingress.Transport = collectoringress.Transport{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clients := newFakeClients()
			in := serviceStateInput(clients, t)
			tc.apply(&in)
			clients.ecs.calls = nil

			got, err := workloadProvisioner(clients).EnsureCollectorService(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			if got.Ready() {
				t.Fatal("reported ready over an unready half")
			}
			if !collectorstatus.HasError(got.Problems) {
				t.Fatalf("problems = %v, want an error", codes(got.Problems))
			}
			for _, call := range clients.ecs.calls {
				if strings.HasPrefix(call, "Create") {
					t.Errorf("created something: %v", clients.ecs.calls)
				}
			}
		})
	}
}

// TestTheWholeStackSettles: five passes over cluster, revision and service issue only describes.
func TestTheWholeStackSettles(t *testing.T) {
	clients := newFakeClients()
	p := workloadProvisioner(clients)
	in := serviceStateInput(clients, t)
	first, err := p.EnsureCollectorService(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	in.Recorded = true

	for pass := 0; pass < 5; pass++ {
		clients.ecs.calls = nil
		workload, err := p.EnsureWorkload(context.Background(), workloadInput())
		if err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		in.Workload = workload

		got, err := p.EnsureCollectorService(context.Background(), in)
		if err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		if got.Created || got.Updated || got.Adopted {
			t.Fatalf("pass %d wrote: %+v", pass, got)
		}
		if got.ARN != first.ARN {
			t.Errorf("pass %d = %q, want %q", pass, got.ARN, first.ARN)
		}
		for _, call := range clients.ecs.calls {
			if !strings.HasPrefix(call, "Describe") {
				t.Errorf("pass %d called %s", pass, call)
			}
		}
	}
}

// TestInfrastructureRecordCarriesEverySevenRefs. A ref that is not recorded is an object teardown may
// never delete, and the accessor writes all seven together — so one assembler, because there is
// one writer.
func TestInfrastructureRecordCarriesEverySevenRefs(t *testing.T) {
	clients := newFakeClients()
	p := workloadProvisioner(clients)
	in := serviceStateInput(clients, t)
	service, err := p.EnsureCollectorService(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}

	record := InfrastructureRecord(ingressKey(), in.Workload, in.Ingress, service, testSubnets)
	if record.Key != ingressKey() {
		t.Errorf("key = %+v", record.Key)
	}
	for name, ref := range map[string]string{
		"workload":       record.WorkloadRef,
		"revision":       record.RevisionRef,
		"cluster":        record.RuntimeRef,
		"load balancer":  record.FrontendRef,
		"security group": record.IngressRef,
	} {
		if ref == "" {
			t.Errorf("the %s ref is empty, so teardown could never delete that object", name)
		}
	}
	if len(record.BackendRefs) != 2 || len(record.PlacementRefs) != 2 {
		t.Errorf("target groups = %v, subnets = %v", record.BackendRefs, record.PlacementRefs)
	}
	if record.WorkloadRef != service.ARN || record.RevisionRef != in.Workload.TaskDefinition.ARN {
		t.Error("the record does not name what was actually converged")
	}
}

func TestManualModeCreatesNoService(t *testing.T) {
	clients := newFakeClients()
	in := serviceStateInput(clients, t)
	in.Mode = spec.ModeManual
	clients.ecs.calls = nil

	got, err := workloadProvisioner(clients).EnsureCollectorService(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ready() || len(clients.ecs.calls) != 0 {
		t.Errorf("converged in manual mode: %+v / %v", got, clients.ecs.calls)
	}
}
