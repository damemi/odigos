package deployer

import (
	"context"
	"strings"
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorimage"
	basecollectorsettings "github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorsettings"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	collectorsettings "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/settings"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

func workloadSettings() collectorsettings.Network {
	return collectorsettings.Network{
		Network: basecollectorsettings.Network{
			Enabled: true,
			CPU:     basecollectorsettings.DefaultCPU,
			Memory:  basecollectorsettings.DefaultMemory,
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
// §5.2 setting.
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
