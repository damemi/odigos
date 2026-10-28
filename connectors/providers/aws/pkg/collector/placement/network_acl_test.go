package placement

import "testing"

func TestNetworkACLDenyAndReturnTraffic(t *testing.T) {
	subnet := Subnet{ACLKnown: true, ACLRules: []ACLRule{{Number: 100, Protocol: "6", CIDR: "10.0.0.0/8", From: 4317, To: 4318, Allow: false}, {Number: 200, Protocol: "-1", CIDR: "0.0.0.0/0", From: 0, To: 65535, Allow: true}}}
	if aclAllows(subnet, "10.1.0.0/16", false, 4317, 4317) {
		t.Fatal("earlier deny ignored")
	}
	if !aclAllows(subnet, "10.1.0.0/16", false, 1024, 4000) {
		t.Fatal("allow covering return range was rejected")
	}
	if aclAllows(subnet, "10.1.0.0/16", true, 4317, 4317) {
		t.Fatal("missing egress was allowed")
	}
}
func TestWorkloadEgressRequiresAdmission(t *testing.T) {
	topo := NewTopologyWithSecurityGroups(nil, nil, nil, nil, []SecurityGroup{{ID: "source", EgressKnown: true}})
	if topo.workloadEgressAllowed([]string{"source"}, "collector", []string{"10.0.0.0/16"}, 4317) {
		t.Fatal("egress deny ignored")
	}
	topo.securityGroups["source"] = SecurityGroup{EgressKnown: true, Egress: []SecurityGroupRule{{Protocol: "-1", PortsKnown: true, SourceSecurityGroupIDs: []string{"collector"}}}}
	if !topo.workloadEgressAllowed([]string{"source"}, "collector", nil, 4317) {
		t.Fatal("destination group egress rejected")
	}
}

func TestNetworkACLChecksOnlyPublishedReceiverPorts(t *testing.T) {
	topo := NewTopology(nil, nil, []Subnet{
		{ID: "host", CIDRBlock: "10.0.1.0/24", ACLKnown: true, ACLRules: []ACLRule{
			{Protocol: "6", CIDR: "10.0.2.0/24", From: 4317, To: 4317, Allow: true},
			{Protocol: "6", CIDR: "10.0.2.0/24", Egress: true, From: 1024, To: 65535, Allow: true},
		}},
		{ID: "source", CIDRBlock: "10.0.2.0/24", ACLKnown: true, ACLRules: []ACLRule{
			{Protocol: "6", CIDR: "10.0.1.0/24", Egress: true, From: 4317, To: 4317, Allow: true},
			{Protocol: "6", CIDR: "10.0.1.0/24", From: 1024, To: 65535, Allow: true},
		}},
	})
	if !topo.aclPathAllowed([]string{"host"}, []string{"source"}, []int32{4317}) {
		t.Fatal("gRPC-only path refused")
	}
	if topo.aclPathAllowed([]string{"host"}, []string{"source"}, []int32{4317, 4318}) {
		t.Fatal("unpublished/blocked HTTP path admitted")
	}
}
