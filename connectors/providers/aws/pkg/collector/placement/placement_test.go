package placement

import (
	"reflect"
	"strings"
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorplacement"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
	collectorsettings "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/settings"
	awsstore "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
)

const collectorKind = "aws.fargate-collector"

// settings builds the §5.2 tree through the real reader rather than by struct literal, so these tests
// exercise the same precedence a customer's `updateConfig` patch goes through.
func settings(enabled bool, regions map[string]any) collectorsettings.Settings {
	if regions == nil {
		regions = map[string]any{}
	}
	return collectorsettings.Read(map[string]any{
		collectorsettings.Key: map[string]any{
			collectorsettings.KeyEnabled: enabled,
			collectorsettings.KeyRegions: regions,
		},
	})
}

// subnetsIn builds n private subnets across two zones for a VPC.
func subnetsIn(region, vpcID string) []Subnet {
	return []Subnet{
		{Region: region, ID: vpcID + "-subnet-a", VPCID: vpcID, AvailabilityZone: region + "a", AvailableIPs: 100, Egress: EgressNAT},
		{Region: region, ID: vpcID + "-subnet-b", VPCID: vpcID, AvailabilityZone: region + "b", AvailableIPs: 100, Egress: EgressNAT},
	}
}

func lambdaOn(region, arn, vpcID string) awsstore.LambdaFunction {
	return awsstore.LambdaFunction{Region: region, FunctionARN: arn, VPCID: vpcID}
}

func networkKeys(plan Plan) []string {
	out := make([]string, 0, len(plan.Placements))
	for _, n := range plan.CollectorNetworks() {
		out = append(out, n.Region+"/"+n.NetworkID)
	}
	return out
}

func deployableKeys(plan Plan) []string {
	var out []string
	for _, n := range plan.DeployableNetworks() {
		out = append(out, n.Region+"/"+n.NetworkID)
	}
	return out
}

// TestOneCollectorPerRegion is the regional lifecycle criterion. Multiple workload VPCs in one region
// share one explicitly selected host VPC; the host network remains physical metadata, not identity.
func TestOneCollectorPerRegion(t *testing.T) {
	topo := NewTopology(
		[]string{"eu-west-1", "us-east-1"},
		[]VPC{
			{Region: "eu-west-1", ID: "vpc-a"},
			{Region: "eu-west-1", ID: "vpc-b"},
			{Region: "us-east-1", ID: "vpc-c"},
			{Region: "us-east-1", ID: "vpc-empty"}, // not selected as this configured region's host
		},
		append(append(subnetsIn("eu-west-1", "vpc-a"), subnetsIn("eu-west-1", "vpc-b")...),
			append(subnetsIn("us-east-1", "vpc-c"), subnetsIn("us-east-1", "vpc-empty")...)...),
	)
	plan := Resolve(Input{
		Kind:     collectorKind,
		Topology: topo,
		Settings: settings(true, map[string]any{
			"eu-west-1": map[string]any{collectorsettings.KeyHostNetworkID: "vpc-a"},
			"us-east-1": map[string]any{collectorsettings.KeyHostNetworkID: "vpc-c"},
		}),
		Inventory: Inventory{
			Lambdas: []awsstore.LambdaFunction{
				lambdaOn("eu-west-1", "arn:fn:a", "vpc-a"),
				lambdaOn("eu-west-1", "arn:fn:a2", "vpc-a"),
				lambdaOn("eu-west-1", "arn:fn:b", "vpc-b"),
				lambdaOn("us-east-1", "arn:fn:c", "vpc-c"),
			},
			Sources: []basestore.SourceRecord{
				enabledSource(awsstore.KindLambda, "arn:fn:a"),
				enabledSource(awsstore.KindLambda, "arn:fn:a2"),
				enabledSource(awsstore.KindLambda, "arn:fn:b"),
				enabledSource(awsstore.KindLambda, "arn:fn:c"),
			},
		},
	})

	want := []string{"eu-west-1/vpc-a", "us-east-1/vpc-c"}
	if got := networkKeys(plan); !reflect.DeepEqual(got, want) {
		t.Fatalf("collector networks = %v, want %v", got, want)
	}
	if got := deployableKeys(plan); !reflect.DeepEqual(got, want) {
		t.Fatalf("deployable networks = %v, want %v", got, want)
	}
	for _, n := range plan.CollectorNetworks() {
		if n.Kind != collectorKind {
			t.Errorf("%s/%s carries kind %q, want %q", n.Region, n.NetworkID, n.Kind, collectorKind)
		}
		pl, ok := plan.Placement(n.Region, n.NetworkID)
		if !ok {
			t.Fatalf("%s/%s has no placement", n.Region, n.NetworkID)
		}
		if len(pl.Zones) != 2 {
			t.Errorf("%s is placed in %d zones, want the two available: %v", n.NetworkID, len(pl.Zones), pl.SubnetIDs)
		}
		if !pl.Private {
			t.Errorf("%s is not placed privately", n.NetworkID)
		}
	}
}

func TestConfiguredCollectorDoesNotNeedMarkedSource(t *testing.T) {
	topo := NewTopology(
		[]string{"eu-west-1"},
		[]VPC{{Region: "eu-west-1", ID: "vpc-host"}},
		subnetsIn("eu-west-1", "vpc-host"),
	)
	plan := Resolve(Input{
		Kind:     collectorKind,
		Topology: topo,
		Settings: settings(true, map[string]any{
			"eu-west-1": map[string]any{collectorsettings.KeyEnabled: true},
		}),
	})
	if got, want := deployableKeys(plan), []string{"eu-west-1/vpc-host"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("deployable source-free Collectors = %v, want %v", got, want)
	}
}

// TestTheFeatureSwitchProducesNothing. Turning managed Collectors on is a rollout (§7.4), so off is the
// default and off means nothing anywhere, whatever a network says.
func TestTheFeatureSwitchProducesNothing(t *testing.T) {
	topo := NewTopology([]string{"eu-west-1"}, []VPC{{Region: "eu-west-1", ID: "vpc-a"}}, subnetsIn("eu-west-1", "vpc-a"))
	inv := Inventory{
		Lambdas: []awsstore.LambdaFunction{lambdaOn("eu-west-1", "arn:fn:a", "vpc-a")},
		Sources: []basestore.SourceRecord{enabledSource(awsstore.KindLambda, "arn:fn:a")},
	}
	plan := Resolve(Input{Kind: collectorKind, Topology: topo, Settings: settings(false, map[string]any{
		"eu-west-1": map[string]any{collectorsettings.KeyEnabled: true, collectorsettings.KeyHostNetworkID: "vpc-a"},
	}), Inventory: inv})
	if got := networkKeys(plan); len(got) != 0 {
		t.Fatalf("collector networks = %v with the feature off", got)
	}
}

// TestARefusedPlacementIsStillListed is the AWS placement refusal criterion. The selected regional host
// stays in CollectorNetworks so the reason is reportable; it is absent from DeployableNetworks.
func TestARefusedPlacementIsStillListed(t *testing.T) {
	isolated := []Subnet{
		{Region: "eu-west-1", ID: "vpc-iso-subnet-a", VPCID: "vpc-iso", AvailabilityZone: "eu-west-1a", AvailableIPs: 100, Egress: EgressNone},
	}
	topo := NewTopology(
		[]string{"eu-west-1"},
		[]VPC{{Region: "eu-west-1", ID: "vpc-ok"}, {Region: "eu-west-1", ID: "vpc-iso"}},
		append(subnetsIn("eu-west-1", "vpc-ok"), isolated...),
	)
	plan := Resolve(Input{
		Kind:     collectorKind,
		Topology: topo,
		Settings: settings(true, map[string]any{
			"eu-west-1": map[string]any{collectorsettings.KeyHostNetworkID: "vpc-iso"},
		}),
		Inventory: Inventory{
			Lambdas: []awsstore.LambdaFunction{
				lambdaOn("eu-west-1", "arn:fn:ok", "vpc-ok"),
				lambdaOn("eu-west-1", "arn:fn:iso", "vpc-iso"),
			},
			Sources: []basestore.SourceRecord{
				enabledSource(awsstore.KindLambda, "arn:fn:ok"),
				enabledSource(awsstore.KindLambda, "arn:fn:iso"),
			},
		},
	})

	if got, want := networkKeys(plan), []string{"eu-west-1/vpc-iso"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("collector networks = %v, want %v — the refused regional host must still be reportable", got, want)
	}
	if got, want := deployableKeys(plan), []string(nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("deployable networks = %v, want %v", got, want)
	}

	iso, _ := plan.Placement("eu-west-1", "vpc-iso")
	if iso.Deployable() {
		t.Fatal("the isolated network reports as deployable")
	}
	if !collectorstatus.HasError(iso.Problems) {
		t.Errorf("the refusal is not an Error: %v", codes(iso.Problems))
	}
	// The refusal has to be reachable from the single list a reporter reads.
	if !hasCode(plan.AllProblems(), ProblemNoUsableSubnet) {
		t.Errorf("AllProblems does not carry the placement refusal: %v", codes(plan.AllProblems()))
	}
}

// TestRegionalHostSurvivesTheAWSLayer. A workload VPC served by the regional host gets no placement of
// its own; only the explicitly selected host receives the AWS subnet decision.
func TestRegionalHostSurvivesTheAWSLayer(t *testing.T) {
	topo := NewTopology(
		[]string{"eu-west-1"},
		[]VPC{{Region: "eu-west-1", ID: "vpc-app"}, {Region: "eu-west-1", ID: "vpc-hub"}},
		append(subnetsIn("eu-west-1", "vpc-app"), subnetsIn("eu-west-1", "vpc-hub")...),
	)
	plan := Resolve(Input{
		Kind:     collectorKind,
		Topology: topo,
		Settings: settings(true, map[string]any{
			"eu-west-1": map[string]any{collectorsettings.KeyHostNetworkID: "vpc-hub"},
		}),
		Inventory: Inventory{
			Lambdas: []awsstore.LambdaFunction{lambdaOn("eu-west-1", "arn:fn:app", "vpc-app")},
			Sources: []basestore.SourceRecord{enabledSource(awsstore.KindLambda, "arn:fn:app")},
		},
	})

	// The hub runs nothing of its own and still gets the regional Collector as the configured host.
	if got, want := networkKeys(plan), []string{"eu-west-1/vpc-hub"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("collector networks = %v, want %v", got, want)
	}
	if _, ok := plan.Placement("eu-west-1", "vpc-app"); ok {
		t.Error("the sharing network got a placement of its own")
	}
	hub, ok := plan.Placement("eu-west-1", "vpc-hub")
	if !ok || !hub.Deployable() {
		t.Fatalf("the share target has no deployable placement: %+v", hub)
	}
	if served := plan.Networks.ServedBy("eu-west-1", "vpc-app"); served != "vpc-hub" {
		t.Errorf("vpc-app is served by %q, want vpc-hub", served)
	}
}

// TestEveryServedNetworkIsServedBySomethingPlaceable checks the AWS-side invariant that a selected
// regional host has a placement entry, even when its subnet selection is refused.
func TestEveryServedNetworkIsServedBySomethingPlaceable(t *testing.T) {
	isolatedHub := []Subnet{
		{Region: "eu-west-1", ID: "vpc-hub-subnet-a", VPCID: "vpc-hub", AvailabilityZone: "eu-west-1a", AvailableIPs: 100, Egress: EgressNone},
	}
	topo := NewTopology(
		[]string{"eu-west-1"},
		[]VPC{{Region: "eu-west-1", ID: "vpc-app"}, {Region: "eu-west-1", ID: "vpc-hub"}},
		append(subnetsIn("eu-west-1", "vpc-app"), isolatedHub...),
	)
	plan := Resolve(Input{
		Kind:     collectorKind,
		Topology: topo,
		Settings: settings(true, map[string]any{
			"eu-west-1": map[string]any{collectorsettings.KeyHostNetworkID: "vpc-hub"},
		}),
		Inventory: Inventory{
			Lambdas: []awsstore.LambdaFunction{lambdaOn("eu-west-1", "arn:fn:app", "vpc-app")},
			Sources: []basestore.SourceRecord{enabledSource(awsstore.KindLambda, "arn:fn:app")},
		},
	})

	// The regional placement decision still names the hub. The un-placeable host is visible as such,
	// without creating a second Collector for the consumer VPC.
	for _, n := range plan.Networks.Networks {
		if n.ServedBy == "" {
			continue
		}
		pl, ok := plan.Placement(n.Region, n.ServedBy)
		if !ok {
			t.Fatalf("%s is served by %s, which has no placement entry at all", n.NetworkID, n.ServedBy)
		}
		if !pl.Deployable() && !collectorstatus.HasError(plan.AllProblems()) {
			t.Fatalf("%s is served by an unplaceable network with nothing reported", n.NetworkID)
		}
	}
}

// TestConfiguredSubnetsReachPlacement pins the §5.2 override actually being read: a customer who names
// subnets gets those subnets, not Odigos's choice.
func TestConfiguredSubnetsReachPlacement(t *testing.T) {
	subnets := append(subnetsIn("eu-west-1", "vpc-a"), Subnet{
		Region: "eu-west-1", ID: "vpc-a-subnet-c", VPCID: "vpc-a",
		AvailabilityZone: "eu-west-1c", AvailableIPs: 100, Egress: EgressNAT,
	})
	topo := NewTopology([]string{"eu-west-1"}, []VPC{{Region: "eu-west-1", ID: "vpc-a"}}, subnets)
	plan := Resolve(Input{
		Kind:     collectorKind,
		Topology: topo,
		Settings: settings(true, map[string]any{
			"eu-west-1": map[string]any{
				collectorsettings.KeyHostNetworkID: "vpc-a",
				collectorsettings.KeyProvider: map[string]any{
					collectorsettings.KeySubnetIDs: []any{"vpc-a-subnet-c"},
				},
			},
		}),
		Inventory: Inventory{
			Lambdas: []awsstore.LambdaFunction{lambdaOn("eu-west-1", "arn:fn:a", "vpc-a")},
			Sources: []basestore.SourceRecord{enabledSource(awsstore.KindLambda, "arn:fn:a")},
		},
	})
	pl, ok := plan.Placement("eu-west-1", "vpc-a")
	if !ok {
		t.Fatal("no placement")
	}
	if !reflect.DeepEqual(pl.SubnetIDs, []string{"vpc-a-subnet-c"}) {
		t.Fatalf("subnets = %v, want the configured one only", pl.SubnetIDs)
	}
	if !pl.Configured {
		t.Error("placement is not marked configured")
	}
	// One zone, because that is what was asked for — reported, not overridden.
	if !hasCode(pl.Problems, ProblemSingleAvailabilityZone) {
		t.Errorf("problems = %v, want the single-zone report", codes(pl.Problems))
	}
}

// TestTheNoNetworkRemediationTravelsWithThePlan — the fourth acceptance criterion. A bare Lambda has to
// reach both Plan.Unattached (so routing leaves it exporting directly) and the reported problems (so the
// customer is told the one thing they could change).
func TestTheNoNetworkRemediationTravelsWithThePlan(t *testing.T) {
	topo := NewTopology([]string{"eu-west-1"}, []VPC{{Region: "eu-west-1", ID: "vpc-a"}}, subnetsIn("eu-west-1", "vpc-a"))
	plan := Resolve(Input{
		Kind:     collectorKind,
		Topology: topo,
		Settings: settings(true, map[string]any{
			"eu-west-1": map[string]any{collectorsettings.KeyEnabled: true},
		}),
		Inventory: Inventory{
			Lambdas: []awsstore.LambdaFunction{
				lambdaOn("eu-west-1", "arn:fn:attached", "vpc-a"),
				lambdaOn("eu-west-1", "arn:fn:bare", ""),
			},
			Sources: []basestore.SourceRecord{
				enabledSource(awsstore.KindLambda, "arn:fn:attached"),
				enabledSource(awsstore.KindLambda, "arn:fn:bare"),
			},
		},
	})
	if !reflect.DeepEqual(plan.Networks.Unattached, []string{"arn:fn:bare"}) {
		t.Fatalf("unattached = %v, want the bare function", plan.Networks.Unattached)
	}
	p := problemWith(t, plan.AllProblems(), ProblemSourceHasNoNetwork)
	if !strings.Contains(p.Message, "arn:fn:bare") {
		t.Errorf("message = %s", p.Message)
	}
	// It must not stop the network that does have a Collector.
	if got, want := deployableKeys(plan), []string{"eu-west-1/vpc-a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("deployable networks = %v, want %v", got, want)
	}
}

// TestAllProblemsCollectsFromSourceAndPlacement. Problems from source attribution and the selected
// regional host placement are both retained in the aggregate list.
func TestAllProblemsCollectsFromSourceAndPlacement(t *testing.T) {
	isolated := []Subnet{
		{Region: "eu-west-1", ID: "vpc-iso-subnet-a", VPCID: "vpc-iso", AvailabilityZone: "eu-west-1a", AvailableIPs: 100, Egress: EgressNone},
	}
	topo := NewTopology(
		[]string{"eu-west-1"},
		[]VPC{{Region: "eu-west-1", ID: "vpc-iso"}, {Region: "eu-west-1", ID: "vpc-cycle"}},
		append(isolated, subnetsIn("eu-west-1", "vpc-cycle")...),
	)
	plan := Resolve(Input{
		Kind:     collectorKind,
		Topology: topo,
		Settings: settings(true, map[string]any{
			"eu-west-1": map[string]any{collectorsettings.KeyHostNetworkID: "vpc-iso"},
		}),
		Inventory: Inventory{
			Lambdas: []awsstore.LambdaFunction{
				lambdaOn("eu-west-1", "arn:fn:iso", "vpc-iso"),
				lambdaOn("eu-west-1", "arn:fn:cycle", "vpc-cycle"),
				lambdaOn("eu-west-1", "arn:fn:bare", ""),
			},
			Sources: []basestore.SourceRecord{
				enabledSource(awsstore.KindLambda, "arn:fn:iso"),
				enabledSource(awsstore.KindLambda, "arn:fn:cycle"),
				enabledSource(awsstore.KindLambda, "arn:fn:bare"),
			},
		},
	})
	all := codes(plan.AllProblems())
	want := []string{ProblemSourceHasNoNetwork, ProblemNoUsableSubnet}
	if !reflect.DeepEqual(all, want) {
		t.Fatalf("AllProblems = %v, want %v (source attribution, then regional host placement)", all, want)
	}
}

// TestAnUnknownNetworkIsRefusedRatherThanPlacedFromAZeroVPC. Defensive: Resolve only names networks the
// topology supplied, so this is a bug guard — and it guards the shape of the bug, which is a refusal
// message that names no network at all.
func TestAnUnknownNetworkIsRefusedRatherThanPlacedFromAZeroVPC(t *testing.T) {
	pl := place(collectorplacement.NetworkRef{Region: "eu-west-1", NetworkID: "vpc-ghost"}, Input{
		Topology: NewTopology([]string{"eu-west-1"}, nil, nil),
		Settings: settings(true, nil),
	})
	if pl.Deployable() {
		t.Fatal("a network with no VPC in the topology reports as deployable")
	}
	p := problemWith(t, pl.Problems, ProblemNetworkUnknown)
	if !strings.Contains(p.Message, "vpc-ghost") {
		t.Errorf("message does not name the network: %s", p.Message)
	}
}
