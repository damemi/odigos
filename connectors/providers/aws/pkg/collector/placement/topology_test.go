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
