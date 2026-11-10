package placement

import (
	"reflect"
	"strings"
	"testing"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorplacement"
	basestore "github.com/odigos-io/odigos/connectors/base/store"
	collectorsettings "github.com/odigos-io/odigos/connectors/providers/aws/pkg/collector/settings"
	awsstore "github.com/odigos-io/odigos/connectors/providers/aws/pkg/workload/store"
)

const clusterARN = "arn:aws:ecs:eu-west-1:1:cluster/prod"

func enabledSource(kind, externalID string) basestore.SourceRecord {
	return basestore.SourceRecord{Kind: kind, ExternalResourceID: externalID, Enabled: true}
}

// twoZoneTopology is one region, one VPC, two private subnets.
func twoZoneTopology() Topology {
	return NewTopology(
		[]string{"eu-west-1"},
		[]VPC{testVPC()},
		[]Subnet{
			sn("subnet-a", "eu-west-1a", EgressNAT, 100),
			sn("subnet-b", "eu-west-1b", EgressNAT, 100),
		},
	)
}

func refsOf(sources []collectorplacement.Source) []string {
	out := make([]string, 0, len(sources))
	for _, s := range sources {
		out = append(out, s.Ref+"@"+s.Region+"/"+s.NetworkID)
	}
	return out
}

// TestLambdaAttributionIsAFieldRead: VpcConfig carries the VPC id, so no subnet lookup is needed.
func TestLambdaAttributionIsAFieldRead(t *testing.T) {
	inv := Inventory{
		Lambdas: []awsstore.LambdaFunction{
			{Region: "eu-west-1", FunctionARN: "arn:fn:attached", VPCID: "vpc-1", SubnetIDs: []string{"subnet-a"}},
		},
		Sources: []basestore.SourceRecord{enabledSource(awsstore.KindLambda, "arn:fn:attached")},
	}
	sources, problems := Sources(inv, twoZoneTopology())
	if !reflect.DeepEqual(refsOf(sources), []string{"arn:fn:attached@eu-west-1/vpc-1"}) {
		t.Fatalf("sources = %v", refsOf(sources))
	}
	if len(problems) != 0 {
		t.Errorf("unexpected problems: %v", codes(problems))
	}
}

// TestFargateAttributionGoesThroughTheSubnet: awsvpcConfiguration names subnets and never the VPC, so
// this lookup is the only route from a Fargate workload to its network.
func TestFargateAttributionGoesThroughTheSubnet(t *testing.T) {
	inv := Inventory{
		ECSServices: []awsstore.ECSService{
			{Region: "eu-west-1", ClusterARN: clusterARN, ServiceName: "api", SubnetIDs: []string{"subnet-b"}},
		},
		Sources: []basestore.SourceRecord{enabledSource(awsstore.KindECSTask, clusterARN+"/api")},
	}
	sources, problems := Sources(inv, twoZoneTopology())
	if !reflect.DeepEqual(refsOf(sources), []string{clusterARN + "/api@eu-west-1/vpc-1"}) {
		t.Fatalf("sources = %v", refsOf(sources))
	}
	if len(problems) != 0 {
		t.Errorf("unexpected problems: %v", codes(problems))
	}
}

// TestADeletedSubnetIsSkippedAndTriedInOrder. ECS requires every subnet in one awsvpcConfiguration to be
// in the same VPC, so the first that resolves is authoritative; trying the rest covers a service still
// naming a subnet that has since been deleted.
func TestADeletedSubnetIsSkippedAndTriedInOrder(t *testing.T) {
	inv := Inventory{
		ECSServices: []awsstore.ECSService{
			{Region: "eu-west-1", ClusterARN: clusterARN, ServiceName: "api", SubnetIDs: []string{"subnet-gone", "subnet-a"}},
		},
		Sources: []basestore.SourceRecord{enabledSource(awsstore.KindECSTask, clusterARN+"/api")},
	}
	sources, problems := Sources(inv, twoZoneTopology())
	if len(sources) != 1 || sources[0].NetworkID != "vpc-1" {
		t.Fatalf("sources = %v, want the service attributed through its surviving subnet", refsOf(sources))
	}
	if len(problems) != 0 {
		t.Errorf("unexpected problems: %v", codes(problems))
	}
}

// TestOnlyEnabledSourcesCount. A discovered-but-not-instrumented service sends no telemetry, so building
// an egress tier for it bills the customer for a Collector nothing exports through.
func TestOnlyEnabledSourcesCount(t *testing.T) {
	inv := Inventory{
		Lambdas: []awsstore.LambdaFunction{
			{Region: "eu-west-1", FunctionARN: "arn:fn:on", VPCID: "vpc-1"},
			{Region: "eu-west-1", FunctionARN: "arn:fn:off", VPCID: "vpc-1"},
			{Region: "eu-west-1", FunctionARN: "arn:fn:nosource", VPCID: "vpc-1"},
		},
		Sources: []basestore.SourceRecord{
			enabledSource(awsstore.KindLambda, "arn:fn:on"),
			{Kind: awsstore.KindLambda, ExternalResourceID: "arn:fn:off", Enabled: false},
		},
	}
	sources, _ := Sources(inv, twoZoneTopology())
	if !reflect.DeepEqual(refsOf(sources), []string{"arn:fn:on@eu-west-1/vpc-1"}) {
		t.Fatalf("sources = %v", refsOf(sources))
	}
}

// TestSourcesAreMatchedPerKind. Both kinds are keyed by external id in one table; a Lambda ARN must not
// enable a same-named ECS service or the reverse.
func TestSourcesAreMatchedPerKind(t *testing.T) {
	// The same external id under both kinds, which is what makes this a real collision: an ECS service
	// ref is cluster + "/" + service, so a function ARN spelled that way collides exactly.
	const shared = "shared/id"
	inv := Inventory{
		Lambdas: []awsstore.LambdaFunction{{Region: "eu-west-1", FunctionARN: shared, VPCID: "vpc-1"}},
		ECSServices: []awsstore.ECSService{
			{Region: "eu-west-1", ClusterARN: "shared", ServiceName: "id", SubnetIDs: []string{"subnet-a"}},
		},
	}

	inv.Sources = []basestore.SourceRecord{enabledSource(awsstore.KindLambda, shared)}
	if sources, _ := Sources(inv, twoZoneTopology()); len(sources) != 1 {
		t.Fatalf("a Lambda source produced %v; the ECS service with the same id must not be enabled by it", refsOf(sources))
	}

	inv.Sources = []basestore.SourceRecord{enabledSource(awsstore.KindECSTask, shared)}
	if sources, _ := Sources(inv, twoZoneTopology()); len(sources) != 1 {
		t.Fatalf("an ECS source produced %v; the function with the same id must not be enabled by it", refsOf(sources))
	}
}

// TestLambdaWithNoVpcConfigIsTheNoNetworkCase. It reaches the plan as a source with no network,
// which is how it lands in Plan.Unattached, and the problem is INFO because attaching the function
// costs it default internet egress and is the customer's decision to make.
func TestLambdaWithNoVpcConfigIsTheNoNetworkCase(t *testing.T) {
	inv := Inventory{
		Lambdas: []awsstore.LambdaFunction{
			{Region: "eu-west-1", FunctionARN: "arn:fn:bare"},
			{Region: "eu-west-1", FunctionARN: "arn:fn:attached", VPCID: "vpc-1"},
		},
		Sources: []basestore.SourceRecord{
			enabledSource(awsstore.KindLambda, "arn:fn:bare"),
			enabledSource(awsstore.KindLambda, "arn:fn:attached"),
		},
	}
	sources, problems := Sources(inv, twoZoneTopology())
	if len(sources) != 2 {
		t.Fatalf("sources = %v, want both — the bare one has to reach Plan.Unattached", refsOf(sources))
	}
	var bare collectorplacement.Source
	for _, s := range sources {
		if s.Ref == "arn:fn:bare" {
			bare = s
		}
	}
	if bare.NetworkID != "" {
		t.Errorf("bare function got network %q, want empty", bare.NetworkID)
	}
	p := problemWith(t, problems, ProblemSourceHasNoNetwork)
	if p.Severity != "Info" {
		t.Errorf("severity = %q, want Info: says this is a posture, not a fault", p.Severity)
	}
	// The remediation specifies, and the cost of taking it.
	for _, want := range []string{"Attach", "VPC", "Collector", "NAT gateway", "arn:fn:bare"} {
		if !strings.Contains(p.Message, want) {
			t.Errorf("remediation does not mention %q: %s", want, p.Message)
		}
	}
	if strings.Contains(p.Message, "arn:fn:attached") {
		t.Errorf("remediation names a function that HAS a network: %s", p.Message)
	}
}

// TestAnUnresolvableFargateNetworkIsNotFoldedIntoTheNoNetworkCase. Folding them would tell a customer to
// attach a Fargate service to a VPC it is already in — the remediation is Lambda-only, and this
// case is a discovery gap rather than a posture.
func TestAnUnresolvableFargateNetworkIsNotFoldedIntoTheNoNetworkCase(t *testing.T) {
	inv := Inventory{
		ECSServices: []awsstore.ECSService{
			{Region: "eu-west-1", ClusterARN: clusterARN, ServiceName: "ghost", SubnetIDs: []string{"subnet-gone"}},
		},
		Sources: []basestore.SourceRecord{enabledSource(awsstore.KindECSTask, clusterARN+"/ghost")},
	}
	sources, problems := Sources(inv, twoZoneTopology())
	if len(sources) != 0 {
		t.Fatalf("sources = %v, want the unattributable service left out of placement", refsOf(sources))
	}
	if hasCode(problems, ProblemSourceHasNoNetwork) {
		t.Error("a Fargate service was reported with the Lambda remediation")
	}
	p := problemWith(t, problems, ProblemSourceNetworkUnresolved)
	if p.Severity != "Degraded" {
		t.Errorf("severity = %q, want Degraded", p.Severity)
	}
	if !strings.Contains(p.Message, "ghost") {
		t.Errorf("message does not name the service: %s", p.Message)
	}
}

// TestWorkloadsInAnUnscannedRegionAreSkippedSilently. An unlistable region has no plan at all; reporting
// every workload in it would bury the one message that matters, which is the scan failure Discover
// returns.
func TestWorkloadsInAnUnscannedRegionAreSkippedSilently(t *testing.T) {
	inv := Inventory{
		Lambdas: []awsstore.LambdaFunction{
			{Region: "us-east-1", FunctionARN: "arn:fn:elsewhere", VPCID: "vpc-9"},
			{Region: "us-east-1", FunctionARN: "arn:fn:elsewhere-bare"},
		},
		ECSServices: []awsstore.ECSService{
			{Region: "us-east-1", ClusterARN: clusterARN, ServiceName: "api", SubnetIDs: []string{"subnet-z"}},
		},
		Sources: []basestore.SourceRecord{
			enabledSource(awsstore.KindLambda, "arn:fn:elsewhere"),
			enabledSource(awsstore.KindLambda, "arn:fn:elsewhere-bare"),
			enabledSource(awsstore.KindECSTask, clusterARN+"/api"),
		},
	}
	sources, problems := Sources(inv, twoZoneTopology())
	if len(sources) != 0 {
		t.Fatalf("sources = %v, want nothing from an unscanned region", refsOf(sources))
	}
	if len(problems) != 0 {
		t.Fatalf("problems = %v; an unscanned region must not produce per-workload noise", codes(problems))
	}
}

func TestSourcesAreSortedByRegionThenRef(t *testing.T) {
	topo := NewTopology(
		[]string{"eu-west-1", "us-east-1"},
		[]VPC{testVPC(), {Region: "us-east-1", ID: "vpc-9"}},
		[]Subnet{sn("subnet-a", "eu-west-1a", EgressNAT, 100)},
	)
	inv := Inventory{
		Lambdas: []awsstore.LambdaFunction{
			{Region: "us-east-1", FunctionARN: "arn:fn:z", VPCID: "vpc-9"},
			{Region: "eu-west-1", FunctionARN: "arn:fn:b", VPCID: "vpc-1"},
			{Region: "us-east-1", FunctionARN: "arn:fn:a", VPCID: "vpc-9"},
		},
		Sources: []basestore.SourceRecord{
			enabledSource(awsstore.KindLambda, "arn:fn:z"),
			enabledSource(awsstore.KindLambda, "arn:fn:b"),
			enabledSource(awsstore.KindLambda, "arn:fn:a"),
		},
	}
	sources, _ := Sources(inv, topo)
	want := []string{"arn:fn:b@eu-west-1/vpc-1", "arn:fn:a@us-east-1/vpc-9", "arn:fn:z@us-east-1/vpc-9"}
	if !reflect.DeepEqual(refsOf(sources), want) {
		t.Fatalf("sources = %v, want %v", refsOf(sources), want)
	}
}

func TestIngressSourcesIncludesOnlyReachableFargateConsumers(t *testing.T) {
	host := VPC{Region: "eu-west-1", ID: "vpc-host", CIDRBlocks: []string{"10.0.0.0/16"}}
	consumer := VPC{Region: "eu-west-1", ID: "vpc-consumer", CIDRBlocks: []string{"10.1.0.0/16"}}
	topo := NewTopologyWithPeerings(
		[]string{"eu-west-1"},
		[]VPC{host, consumer},
		[]Subnet{
			{Region: "eu-west-1", ID: "host-a", VPCID: host.ID, AvailabilityZone: "eu-west-1a", AvailableIPs: 10, Egress: EgressNAT, Routes: []Route{{DestinationCIDR: "10.1.0.0/16", PeeringID: "pcx-1"}}},
			{Region: "eu-west-1", ID: "host-b", VPCID: host.ID, AvailabilityZone: "eu-west-1b", AvailableIPs: 10, Egress: EgressNAT, Routes: []Route{{DestinationCIDR: "10.1.0.0/16", PeeringID: "pcx-1"}}},
			{Region: "eu-west-1", ID: "consumer-a", VPCID: consumer.ID, AvailabilityZone: "eu-west-1a", AvailableIPs: 10, Egress: EgressNAT, Routes: []Route{{DestinationCIDR: "10.0.0.0/16", PeeringID: "pcx-1"}}},
		},
		[]Peering{{ID: "pcx-1", VPC1: host.ID, VPC2: consumer.ID, Region1: "eu-west-1", Region2: "eu-west-1"}},
	)
	ref := clusterARN + "/api"
	plan := Resolve(Input{
		Kind:     collectorKind,
		Topology: topo,
		Settings: settings(true, map[string]any{"eu-west-1": map[string]any{collectorsettings.KeyHostNetworkID: host.ID}}),
		Inventory: Inventory{
			ECSServices: []awsstore.ECSService{{Region: "eu-west-1", ClusterARN: clusterARN, ServiceName: "api", SubnetIDs: []string{"consumer-a"}, SecurityGroupIDs: []string{"sg-api"}}},
			Sources:     []basestore.SourceRecord{enabledSource(awsstore.KindECSTask, ref)},
		},
	})
	plan.Inventory = Inventory{
		ECSServices: []awsstore.ECSService{{Region: "eu-west-1", ClusterARN: clusterARN, ServiceName: "api", SubnetIDs: []string{"consumer-a"}, SecurityGroupIDs: []string{"sg-api"}}},
		Sources:     []basestore.SourceRecord{enabledSource(awsstore.KindECSTask, ref)},
	}
	plan.Topology = topo
	sources := plan.IngressSources("eu-west-1", host.ID)
	if len(sources) != 1 || sources[0].Name != ref {
		t.Fatalf("ingress sources = %+v, want reachable consumer service", sources)
	}

	noRoute := topo
	noRoute.subnets["consumer-a"] = Subnet{Region: "eu-west-1", ID: "consumer-a", VPCID: consumer.ID, Routes: nil}
	plan.Topology = noRoute
	if got := plan.IngressSources("eu-west-1", host.ID); len(got) != 0 {
		t.Fatalf("ingress sources with missing reciprocal route = %+v, want none", got)
	}
}

func TestIngressSourcesIncludesOnlyReachableLambdaConsumers(t *testing.T) {
	host := VPC{Region: "eu-west-1", ID: "vpc-host", CIDRBlocks: []string{"10.0.0.0/16"}}
	consumer := VPC{Region: "eu-west-1", ID: "vpc-consumer", CIDRBlocks: []string{"10.1.0.0/16"}}
	topo := NewTopologyWithPeerings(
		[]string{"eu-west-1"}, []VPC{host, consumer}, []Subnet{
			{Region: "eu-west-1", ID: "host-a", VPCID: host.ID, AvailabilityZone: "eu-west-1a", AvailableIPs: 10, Egress: EgressNAT, Routes: []Route{{DestinationCIDR: "10.1.0.0/16", PeeringID: "pcx-1"}}},
			{Region: "eu-west-1", ID: "consumer-a", VPCID: consumer.ID, AvailabilityZone: "eu-west-1a", AvailableIPs: 10, Egress: EgressNAT, Routes: []Route{{DestinationCIDR: "10.0.0.0/16", PeeringID: "pcx-1"}}},
		}, []Peering{{ID: "pcx-1", VPC1: host.ID, VPC2: consumer.ID, Region1: "eu-west-1", Region2: "eu-west-1"}},
	)
	arn := "arn:aws:lambda:eu-west-1:1:function:api"
	inv := Inventory{
		Lambdas: []awsstore.LambdaFunction{{Region: "eu-west-1", FunctionARN: arn, VPCID: consumer.ID, SubnetIDs: []string{"consumer-a"}, SecurityGroupIDs: []string{"sg-api"}}},
		Sources: []basestore.SourceRecord{enabledSource(awsstore.KindLambda, arn)},
	}
	plan := Resolve(Input{Kind: collectorKind, Topology: topo, Settings: settings(true, map[string]any{"eu-west-1": map[string]any{collectorsettings.KeyHostNetworkID: host.ID}}), Inventory: inv})
	plan.Inventory, plan.Topology = inv, topo
	if got := plan.IngressSources("eu-west-1", host.ID); len(got) != 1 || got[0].Name != arn {
		t.Fatalf("ingress sources = %+v, want reachable consumer Lambda", got)
	}
	noRoute := topo
	noRoute.subnets["consumer-a"] = Subnet{Region: "eu-west-1", ID: "consumer-a", VPCID: consumer.ID}
	plan.Topology = noRoute
	if got := plan.IngressSources("eu-west-1", host.ID); len(got) != 0 {
		t.Fatalf("ingress sources with missing reciprocal route = %+v, want none", got)
	}
}

// TestNetworksListsEveryVPC ensures host selection can validate any configured VPC, not only source VPCs.
func TestNetworksListsEveryVPC(t *testing.T) {
	topo := NewTopology(
		[]string{"eu-west-1"},
		[]VPC{testVPC(), {Region: "eu-west-1", ID: "vpc-hub"}},
		nil,
	)
	got := Networks(topo)
	want := []collectorplacement.NetworkRef{
		{Region: "eu-west-1", NetworkID: "vpc-1"},
		{Region: "eu-west-1", NetworkID: "vpc-hub"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("networks = %v, want %v", got, want)
	}
}
