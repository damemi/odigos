package placement

import (
	"reflect"
	"testing"
)

func TestVPCOfSubnetRefusesWhatItCannotResolve(t *testing.T) {
	orphan := sn("subnet-orphan", "eu-west-1a", EgressNAT, 10)
	orphan.VPCID = ""
	topo := NewTopology([]string{"eu-west-1"}, []VPC{testVPC()}, []Subnet{sn("subnet-a", "eu-west-1a", EgressNAT, 10), orphan})

	if got, ok := topo.VPCOfSubnet("subnet-a"); !ok || got != "vpc-1" {
		t.Errorf("subnet-a resolved to (%q, %v)", got, ok)
	}
	// Unknown, and a known subnet whose VPC id is missing. Both have to report NOT resolved rather than
	// the empty string, or a Fargate service would be grouped onto the "" network — which is the
	// no-network bucket, and the remediation there tells the customer to attach it to a VPC it is in.
	for _, id := range []string{"subnet-nope", "subnet-orphan"} {
		if got, ok := topo.VPCOfSubnet(id); ok {
			t.Errorf("%s resolved to %q, want not resolved", id, got)
		}
	}
}

func TestCollectorSecurityGroupSelfHealth(t *testing.T) {
	topology := NewTopologyWithSecurityGroups([]string{"eu-west-1"},
		[]VPC{{Region: "eu-west-1", ID: "vpc-host"}}, nil, nil,
		[]SecurityGroup{{Region: "eu-west-1", ID: "sg-collector", VPCID: "vpc-host", Ingress: []SecurityGroupRule{{
			Protocol: "tcp", FromPort: 13133, ToPort: 13133, PortsKnown: true,
			SourceSecurityGroupIDs: []string{"sg-collector"},
		}}}})
	if got := topology.CollectorSecurityGroupAllowsSelfHealth("eu-west-1", "vpc-host", "sg-collector"); got != ReachabilityReachable {
		t.Fatalf("self health ingress = %q", got)
	}
	if got := topology.CollectorSecurityGroupAllowsSelfHealth("eu-west-1", "vpc-host", "sg-other"); got != ReachabilityUnknown {
		t.Fatalf("unknown security group = %q", got)
	}
}

func TestSubnetsInIsScopedAndSorted(t *testing.T) {
	other := sn("subnet-z", "eu-west-1a", EgressNAT, 10)
	other.VPCID = "vpc-2"
	topo := NewTopology([]string{"eu-west-1"},
		[]VPC{testVPC(), {Region: "eu-west-1", ID: "vpc-2"}},
		[]Subnet{sn("subnet-b", "eu-west-1b", EgressNAT, 10), sn("subnet-a", "eu-west-1a", EgressNAT, 10), other})

	got := idsOf(topo.SubnetsIn("vpc-1"))
	if want := []string{"subnet-a", "subnet-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("subnets = %v, want %v", got, want)
	}
	if len(topo.SubnetsIn("vpc-missing")) != 0 {
		t.Error("an unknown VPC returned subnets")
	}
}

// TestEgressPredicates pins the two questions the enum exists to answer, because every selection rule
// is written in terms of them rather than of the values.
func TestEgressPredicates(t *testing.T) {
	cases := map[Egress]struct{ private, routes bool }{
		EgressNone:      {false, false},
		EgressPublic:    {false, true},
		EgressNAT:       {true, true},
		EgressForwarded: {true, true},
	}
	for e, want := range cases {
		if e.Private() != want.private || e.Routes() != want.routes {
			t.Errorf("%q: private=%v routes=%v, want %v/%v", e, e.Private(), e.Routes(), want.private, want.routes)
		}
	}
}

// TestUsableRequiresBothAnAddressAndAWayOut, and the public case requires the public address that makes
// the internet gateway route for the task at all.
func TestUsableRequiresBothAnAddressAndAWayOut(t *testing.T) {
	cases := []struct {
		egress       Egress
		freeIPs      int32
		withIP, noIP bool
	}{
		{EgressNAT, 10, true, true},
		{EgressForwarded, 10, true, true},
		{EgressPublic, 10, true, false},
		{EgressNone, 10, false, false},
		{EgressNAT, 0, false, false},
		{EgressPublic, 0, false, false},
	}
	for _, tc := range cases {
		s := sn("subnet-x", "eu-west-1a", tc.egress, tc.freeIPs)
		if got := s.Usable(true); got != tc.withIP {
			t.Errorf("%q/%d usable with a public ip = %v, want %v", tc.egress, tc.freeIPs, got, tc.withIP)
		}
		if got := s.Usable(false); got != tc.noIP {
			t.Errorf("%q/%d usable without a public ip = %v, want %v", tc.egress, tc.freeIPs, got, tc.noIP)
		}
	}
}

func TestCollectorIngressReachabilityAcceptsSameAndPeeredVPCSecurityGroupReferences(t *testing.T) {
	groups := []SecurityGroup{
		{Region: "eu-west-1", ID: "sg-collector", VPCID: "vpc-host", Ingress: []SecurityGroupRule{{
			Protocol: "tcp", FromPort: 4317, ToPort: 4318, PortsKnown: true, SourceSecurityGroupIDs: []string{"sg-app", "sg-peer"},
		}}},
		{Region: "eu-west-1", ID: "sg-app", VPCID: "vpc-host"},
		{Region: "eu-west-1", ID: "sg-peer", VPCID: "vpc-consumer"},
	}
	topo := NewTopologyWithSecurityGroups(
		[]string{"eu-west-1"},
		[]VPC{
			{Region: "eu-west-1", ID: "vpc-host"},
			{Region: "eu-west-1", ID: "vpc-consumer"},
		}, nil,
		[]Peering{{ID: "pcx-1", VPC1: "vpc-host", VPC2: "vpc-consumer", Region1: "eu-west-1", Region2: "eu-west-1"}},
		groups,
	)
	if got := topo.CollectorIngressReachability("eu-west-1", "vpc-host", "sg-collector", "vpc-host", []string{"sg-app"}); got != ReachabilityReachable {
		t.Fatalf("same-VPC SG admission = %q, want reachable", got)
	}
	if got := topo.CollectorIngressReachability("eu-west-1", "vpc-host", "sg-collector", "vpc-consumer", []string{"sg-peer"}); got != ReachabilityReachable {
		t.Fatalf("peered-VPC SG admission = %q, want reachable", got)
	}
}

func TestCollectorIngressReachabilityIsConservativeForUnknownOrPartialRules(t *testing.T) {
	topo := NewTopologyWithSecurityGroups(
		[]string{"eu-west-1"},
		[]VPC{{Region: "eu-west-1", ID: "vpc-host"}}, nil, nil,
		[]SecurityGroup{
			{Region: "eu-west-1", ID: "sg-collector", VPCID: "vpc-host", Ingress: []SecurityGroupRule{{
				Protocol: "tcp", SourceSecurityGroupIDs: []string{"sg-app"}, Incomplete: true,
			}}},
			{Region: "eu-west-1", ID: "sg-app", VPCID: "vpc-host"},
		},
	)
	for name, sourceGroups := range map[string][]string{
		"missing source group": {"sg-missing"},
		"partial permission":   {"sg-app"},
		"no source groups":     nil,
	} {
		t.Run(name, func(t *testing.T) {
			got := topo.CollectorIngressReachability("eu-west-1", "vpc-host", "sg-collector", "vpc-host", sourceGroups)
			if got != ReachabilityUnknown {
				t.Errorf("admission = %q, want unknown for conservative fallback", got)
			}
		})
	}

	// A known, complete rule on a non-OTLP port is a definitive refusal rather than an unknown snapshot.
	topo = NewTopologyWithSecurityGroups([]string{"eu-west-1"}, []VPC{{Region: "eu-west-1", ID: "vpc-host"}}, nil, nil,
		[]SecurityGroup{
			{Region: "eu-west-1", ID: "sg-collector", VPCID: "vpc-host", Ingress: []SecurityGroupRule{{
				Protocol: "tcp", FromPort: 8080, ToPort: 8080, PortsKnown: true, SourceSecurityGroupIDs: []string{"sg-app"},
			}}},
			{Region: "eu-west-1", ID: "sg-app", VPCID: "vpc-host"},
		})
	if got := topo.CollectorIngressReachability("eu-west-1", "vpc-host", "sg-collector", "vpc-host", []string{"sg-app"}); got != ReachabilityUnreachable {
		t.Fatalf("non-OTLP SG rule = %q, want unreachable", got)
	}
}

func TestCollectorIngressReachabilityRequiresEveryPublishedOTLPPort(t *testing.T) {
	topo := NewTopologyWithSecurityGroups(
		[]string{"eu-west-1"},
		[]VPC{{Region: "eu-west-1", ID: "vpc-host"}}, nil, nil,
		[]SecurityGroup{
			{Region: "eu-west-1", ID: "sg-collector", VPCID: "vpc-host", Ingress: []SecurityGroupRule{{
				Protocol: "tcp", FromPort: 4317, ToPort: 4317, PortsKnown: true, SourceSecurityGroupIDs: []string{"sg-app"},
			}}},
			{Region: "eu-west-1", ID: "sg-app", VPCID: "vpc-host"},
		},
	)
	if got := topo.CollectorIngressReachabilityForPorts("eu-west-1", "vpc-host", "sg-collector", "vpc-host", []string{"sg-app"}, []int32{4317}); got != ReachabilityReachable {
		t.Fatalf("gRPC-only admission = %q, want reachable", got)
	}
	if got := topo.CollectorIngressReachabilityForPorts("eu-west-1", "vpc-host", "sg-collector", "vpc-host", []string{"sg-app"}, []int32{4317, 4318}); got != ReachabilityUnreachable {
		t.Fatalf("dual-protocol admission missing HTTP = %q, want unreachable", got)
	}
}

func TestCollectorIngressCIDRRequiresEveryWorkloadSubnetAndPort(t *testing.T) {
	subnets := []Subnet{
		{Region: "eu-west-1", ID: "subnet-a", VPCID: "vpc-workload", CIDRBlock: "10.70.1.0/24"},
		{Region: "eu-west-1", ID: "subnet-b", VPCID: "vpc-workload", CIDRBlock: "10.70.2.0/24"},
	}
	collector := SecurityGroup{Region: "eu-west-1", ID: "sg-collector", VPCID: "vpc-host", Ingress: []SecurityGroupRule{
		{Protocol: "tcp", FromPort: 4317, ToPort: 4317, PortsKnown: true, SourceCIDRs: []string{"10.70.0.0/16"}},
		{Protocol: "tcp", FromPort: 4318, ToPort: 4318, PortsKnown: true, SourceCIDRs: []string{"10.70.0.0/16"}},
	}}
	build := func() Topology {
		return NewTopologyWithSecurityGroups([]string{"eu-west-1"}, []VPC{{Region: "eu-west-1", ID: "vpc-host"}, {Region: "eu-west-1", ID: "vpc-workload"}}, subnets,
			[]Peering{{ID: "pcx-1", VPC1: "vpc-host", VPC2: "vpc-workload", Region1: "eu-west-1", Region2: "eu-west-1"}},
			[]SecurityGroup{collector, {Region: "eu-west-1", ID: "sg-app", VPCID: "vpc-workload"}})
	}
	check := func(want Reachability) {
		t.Helper()
		got := build().CollectorIngressReachabilityForWorkload("eu-west-1", "vpc-host", "sg-collector", "vpc-workload", []string{"subnet-a", "subnet-b"}, []string{"sg-app"}, []int32{4317, 4318})
		if got != want {
			t.Fatalf("CIDR admission = %q, want %q", got, want)
		}
	}
	check(ReachabilityReachable)
	if got := build().CollectorIngressReachabilityForWorkload("eu-west-1", "vpc-host", "sg-collector", "vpc-workload", []string{"subnet-a", "subnet-b"}, nil, []int32{4317, 4318}); got != ReachabilityReachable {
		t.Fatalf("CIDR admission without workload SG IDs = %q, want reachable", got)
	}
	if got := build().CollectorIngressReachabilityForWorkload("eu-west-1", "vpc-host", "sg-collector", "vpc-workload", []string{"subnet-a", "subnet-b"}, []string{"sg-unresolved"}, []int32{4317, 4318}); got != ReachabilityReachable {
		t.Fatalf("CIDR admission with unresolved workload SG = %q, want reachable", got)
	}
	collector.Ingress[1].SourceCIDRs = []string{"10.70.1.0/24"}
	check(ReachabilityUnreachable)
	collector.Ingress[1].SourceCIDRs = []string{"10.70.0.0/16"}
	subnets[1].CIDRBlock = ""
	check(ReachabilityUnknown)
}
