package placement

import (
	"context"
	"errors"
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorplacement"
	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
	collectorsettings "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/settings"
)

type fakeCollectors struct {
	records []basestore.CollectorRecord
	err     error
}

func (f fakeCollectors) ListManagedCollectors(context.Context) ([]basestore.CollectorRecord, error) {
	return f.records, f.err
}

func recordedOn(ids ...string) fakeCollectors {
	var out []basestore.CollectorRecord
	for _, id := range ids {
		out = append(out, basestore.CollectorRecord{Key: basestore.CollectorKey{Region: "eu-west-1"}, NetworkID: id})
	}
	return fakeCollectors{records: out}
}

func planOver(networks ...string) Plan {
	var out []collectorplacement.Network
	var regions []collectorplacement.Region
	for _, id := range networks {
		ref := collectorplacement.NetworkRef{Region: "eu-west-1", NetworkID: id}
		out = append(out, collectorplacement.Network{
			NetworkRef: ref,
			Role:       collectorplacement.RoleCollector,
		})
		regions = append(regions, collectorplacement.Region{Region: ref.Region, Host: ref})
	}
	return Plan{Networks: collectorplacement.Plan{Networks: out, Regions: regions}}
}

// A host change preserves the region's lifecycle intent, so the old physical host is not a second idle
// Collector. Region identity, rather than the changing host metadata, controls retirement.
func TestARecordedRegionIsNotRetiredWhenItsSelectedHostChanges(t *testing.T) {
	r := &Resolver{Collectors: recordedOn("vpc-idle")}
	plan := planOver("vpc-live")

	plan.Idle = r.idleNetworks(context.Background(), plan)

	if len(plan.Idle) != 0 {
		t.Fatalf("idle = %v, want none while the regional Collector remains placed", plan.Idle)
	}

	// And it reaches the loop: CollectorNetworks is what the coordinator builds its targets from.
	var visited []string
	for _, n := range plan.CollectorNetworks() {
		visited = append(visited, n.NetworkID)
	}
	if len(visited) != 1 || visited[0] != "vpc-live" {
		t.Fatalf("the pass visits %v, want only the region's selected host", visited)
	}
}

func TestConfiguredSourceFreeRegionIsNotIdle(t *testing.T) {
	topo := NewTopology(
		[]string{"eu-west-1"},
		[]VPC{{Region: "eu-west-1", ID: "vpc-host"}},
		subnetsIn("eu-west-1", "vpc-host"),
	)
	plan := Resolve(Input{
		Kind:      collectorKind,
		Topology:  topo,
		Inventory: Inventory{},
		Settings: settings(true, map[string]any{
			"eu-west-1": map[string]any{collectorsettings.KeyEnabled: true},
		}),
	})
	r := &Resolver{Collectors: recordedOn("vpc-host")}
	plan.Idle = r.idleNetworks(context.Background(), plan)
	if len(plan.Idle) != 0 {
		t.Fatalf("configured source-free Collector is idle: %v", plan.Idle)
	}
}

// TestAnIdleNetworkHasNoPlacement, and is not given an empty one.
//
// An empty Placement reads as "the subnets could not be resolved", which is a refusal a customer is told
// to fix with a NAT gateway. An idle network has nothing wrong with it.
func TestAnIdleNetworkHasNoPlacement(t *testing.T) {
	r := &Resolver{Collectors: recordedOn("vpc-idle")}
	plan := planOver()
	plan.Idle = r.idleNetworks(context.Background(), plan)

	if _, ok := plan.Placement("eu-west-1", "vpc-idle"); ok {
		t.Error("an idle network was given a placement decision")
	}
	if len(plan.DeployableNetworks()) != 0 {
		t.Error("an idle network is deployable")
	}
}

// TestAFailedRecordListingTreatsNothingAsIdle. The retention policy is the only thing that reads this
// list, and its actions are to park a Collector and to delete it — neither may ever follow from a failed
// read.
func TestAFailedRecordListingTreatsNothingAsIdle(t *testing.T) {
	r := &Resolver{Collectors: fakeCollectors{err: errors.New("database unreachable")}}

	if idle := r.idleNetworks(context.Background(), planOver("vpc-live")); len(idle) != 0 {
		t.Fatalf("idle = %v, want none from a failed read", idle)
	}
}

// TestNoCollectorReaderTreatsNothingAsIdle, which is the shape a provider built without a store has.
func TestNoCollectorReaderTreatsNothingAsIdle(t *testing.T) {
	r := &Resolver{}
	if idle := r.idleNetworks(context.Background(), planOver()); len(idle) != 0 {
		t.Errorf("idle = %v, want none", idle)
	}
}

// TestThePlanIsServedRatherThanReResolved. Resolving costs a topology scan per region, and the observer
// has already paid it this pass — so the deployer reads. The generation is the pass boundary, which
// `api.CollectorDeployer` gives it no other way to see.
func TestThePlanIsServedRatherThanReResolved(t *testing.T) {
	r := &Resolver{}

	if _, _, ok := r.Plan(); ok {
		t.Fatal("a resolver that has never resolved served a plan; converging on one would place a " +
			"Collector in subnets nobody chose")
	}

	first := planOver("vpc-a")
	r.plan, r.generation = &first, 7
	got, generation, ok := r.Plan()
	if !ok || generation != 7 || len(got.Networks.CollectorNetworks()) != 1 {
		t.Fatalf("Plan() = %v, %d, %v", got, generation, ok)
	}
	// Reading again does not advance the pass: a deployer converging ten networks must see one pass.
	if _, second, _ := r.Plan(); second != generation {
		t.Errorf("reading the plan advanced the generation from %d to %d", generation, second)
	}
}

func TestIngressRefreshMakesPeeredWorkloadReachableWithoutAnotherPlacementPass(t *testing.T) {
	topo := peeredTopology(
		[]Route{{DestinationCIDR: "10.1.0.0/16", PeeringID: "pcx-1"}},
		[]Route{{DestinationCIDR: "10.0.0.0/16", PeeringID: "pcx-1"}},
	)
	collector := SecurityGroup{Region: "eu-west-1", ID: "sg-collector", VPCID: "vpc-host"}
	source := SecurityGroup{Region: "eu-west-1", ID: "sg-app", VPCID: "vpc-consumer"}
	topo.securityGroups[collector.ID], topo.securityGroups[source.ID] = collector, source
	initial := Plan{Topology: topo}
	r := &Resolver{plan: &initial, generation: 7}
	admission := func(p Plan) Reachability {
		return p.Topology.CollectorIngressReachability("eu-west-1", "vpc-host", "sg-collector", "vpc-consumer", []string{"sg-app"})
	}
	if admission(initial) != ReachabilityUnreachable {
		t.Fatal("workload was admitted before its ingress rule existed")
	}
	collector.Ingress = []SecurityGroupRule{{
		Protocol: "tcp", FromPort: 4317, ToPort: 4318, PortsKnown: true, SourceSecurityGroupIDs: []string{"sg-app"},
	}}
	err := r.refreshSecurityGroups(context.Background(), func(_ context.Context, region string) ([]SecurityGroup, error) {
		if region != "eu-west-1" {
			t.Fatalf("unexpected region %s", region)
		}
		return []SecurityGroup{collector, source}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, generation, _ := r.Plan()
	if generation != 7 || admission(updated) != ReachabilityReachable {
		t.Fatal("AWS-confirmed ingress did not become usable in the same placement pass")
	}
	if admission(initial) != ReachabilityUnreachable {
		t.Fatal("refresh mutated a snapshot still held by an observer")
	}
	if !updated.Topology.CanReach("eu-west-1", "vpc-host", "eu-west-1", "vpc-consumer", []string{"subnet-host"}, []string{"subnet-consumer"}) {
		t.Fatal("ingress refresh lost the verified peering routes")
	}
	boom := errors.New("AWS read failed")
	if err := r.refreshSecurityGroups(context.Background(), func(context.Context, string) ([]SecurityGroup, error) {
		return nil, boom
	}); !errors.Is(err, boom) {
		t.Fatalf("refresh error = %v", err)
	}
	unverified, _, _ := r.Plan()
	if admission(unverified) != ReachabilityUnknown {
		t.Fatal("failed read retained stale admission instead of unknown reachability")
	}
}
