package placement

import "testing"

func peeredTopology(hostRoutes, consumerRoutes []Route) Topology {
	return NewTopologyWithPeerings(
		[]string{"eu-west-1"},
		[]VPC{
			{Region: "eu-west-1", ID: "vpc-host", CIDRBlocks: []string{"10.0.0.0/16"}},
			{Region: "eu-west-1", ID: "vpc-consumer", CIDRBlocks: []string{"10.1.0.0/16"}},
		},
		[]Subnet{
			{Region: "eu-west-1", ID: "subnet-host", VPCID: "vpc-host", Routes: hostRoutes},
			{Region: "eu-west-1", ID: "subnet-consumer", VPCID: "vpc-consumer", Routes: consumerRoutes},
		},
		[]Peering{{ID: "pcx-1", VPC1: "vpc-host", VPC2: "vpc-consumer", Region1: "eu-west-1", Region2: "eu-west-1"}},
	)
}

func TestReachabilityRequiresReciprocalPeeringRoutes(t *testing.T) {
	full := peeredTopology(
		[]Route{{DestinationCIDR: "10.1.0.0/16", PeeringID: "pcx-1"}},
		[]Route{{DestinationCIDR: "10.0.0.0/16", PeeringID: "pcx-1"}},
	)
	if got := full.Reachability("eu-west-1", "vpc-host", "eu-west-1", "vpc-consumer", []string{"subnet-host"}, []string{"subnet-consumer"}); got != ReachabilityReachable {
		t.Fatalf("reachability = %q, want reachable", got)
	}

	missing := peeredTopology(
		[]Route{{DestinationCIDR: "10.1.0.0/16", PeeringID: "pcx-1"}}, nil,
	)
	if got := missing.Reachability("eu-west-1", "vpc-host", "eu-west-1", "vpc-consumer", []string{"subnet-host"}, []string{"subnet-consumer"}); got != ReachabilityUnreachable {
		t.Fatalf("missing reciprocal route = %q, want unreachable", got)
	}
}

func TestReachabilityRequiresRoutesFromEverySelectedSubnet(t *testing.T) {
	topo := NewTopologyWithPeerings(
		[]string{"eu-west-1"},
		[]VPC{
			{Region: "eu-west-1", ID: "vpc-host", CIDRBlocks: []string{"10.0.0.0/16"}},
			{Region: "eu-west-1", ID: "vpc-consumer", CIDRBlocks: []string{"10.1.0.0/16"}},
		},
		[]Subnet{
			{ID: "subnet-host-a", VPCID: "vpc-host", Routes: []Route{{DestinationCIDR: "10.1.0.0/16", PeeringID: "pcx-1"}}},
			{ID: "subnet-host-b", VPCID: "vpc-host"},
			{ID: "subnet-consumer", VPCID: "vpc-consumer", Routes: []Route{{DestinationCIDR: "10.0.0.0/16", PeeringID: "pcx-1"}}},
		},
		[]Peering{{ID: "pcx-1", VPC1: "vpc-host", VPC2: "vpc-consumer", Region1: "eu-west-1", Region2: "eu-west-1"}},
	)
	if got := topo.Reachability("eu-west-1", "vpc-host", "eu-west-1", "vpc-consumer",
		[]string{"subnet-host-a", "subnet-host-b"}, []string{"subnet-consumer"}); got != ReachabilityUnreachable {
		t.Fatalf("partially routed host subnets = %q, want unreachable", got)
	}
}

func TestReachabilityRejectsCrossRegionAndOverlappingVPCs(t *testing.T) {
	topo := peeredTopology(
		[]Route{{DestinationCIDR: "10.1.0.0/16", PeeringID: "pcx-1"}},
		[]Route{{DestinationCIDR: "10.0.0.0/16", PeeringID: "pcx-1"}},
	)
	if got := topo.Reachability("eu-west-1", "vpc-host", "us-east-1", "vpc-consumer", []string{"subnet-host"}, []string{"subnet-consumer"}); got != ReachabilityUnknown {
		t.Fatalf("cross-region VPC metadata = %q, want unknown", got)
	}

	overlap := NewTopologyWithPeerings(
		[]string{"eu-west-1"},
		[]VPC{
			{Region: "eu-west-1", ID: "vpc-host", CIDRBlocks: []string{"10.0.0.0/16"}},
			{Region: "eu-west-1", ID: "vpc-consumer", CIDRBlocks: []string{"10.0.0.0/20"}},
		},
		[]Subnet{
			{ID: "subnet-host", VPCID: "vpc-host", Routes: []Route{{DestinationCIDR: "10.0.0.0/20", PeeringID: "pcx-1"}}},
			{ID: "subnet-consumer", VPCID: "vpc-consumer", Routes: []Route{{DestinationCIDR: "10.0.0.0/16", PeeringID: "pcx-1"}}},
		},
		[]Peering{{ID: "pcx-1", VPC1: "vpc-host", VPC2: "vpc-consumer", Region1: "eu-west-1", Region2: "eu-west-1"}},
	)
	if got := overlap.Reachability("eu-west-1", "vpc-host", "eu-west-1", "vpc-consumer", []string{"subnet-host"}, []string{"subnet-consumer"}); got != ReachabilityUnreachable {
		t.Fatalf("overlapping VPC CIDRs = %q, want unreachable", got)
	}
}

func TestReachabilityAllowsSameVPCWithoutPeering(t *testing.T) {
	topo := NewTopology([]string{"eu-west-1"}, []VPC{{Region: "eu-west-1", ID: "vpc-host"}}, []Subnet{{ID: "subnet-host", VPCID: "vpc-host"}})
	if got := topo.Reachability("eu-west-1", "vpc-host", "eu-west-1", "vpc-host", []string{"subnet-host"}, []string{"subnet-host"}); got != ReachabilityReachable {
		t.Fatalf("same VPC reachability = %q, want reachable", got)
	}
}
