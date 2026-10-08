// Package placement is the AWS half of managed-Collector placement: which VPC each enabled source runs
// on, and which subnets a Collector for that VPC should sit in.
//
// The provider-neutral half — grouping sources by network, regional host selection, and its refusals — is
// base/domain/collectorplacement, and Azure and GCP reuse it. What is here is the
// part that cannot be shared: reading an AWS source's VPC, and choosing subnets across availability
// zones while refusing one a Collector could not reach the destination from.
//
// The stakes are why this is the largest issue in the milestone. Placement is where a wrong default
// produces a Collector nothing can reach: a task in a public subnet with no public address, or in a
// private subnet whose NAT gateway was deleted, comes up healthy from ECS's point of view and exports
// nothing.
package placement

import (
	"sort"
	"strings"
)

// Egress is what an active default route out of a subnet leads to.
//
// Four values rather than the eight gateway kinds a route table can name, because only these
// distinctions change a decision: whether there is a way out at all, whether taking it costs the task a
// public address, and whether Odigos can see what is on the other side.
type Egress string

const (
	// EgressNone: no active default route. Nothing Odigos deploys here can reach the destination or the
	// OpAMP endpoint, so it is not deployed here.
	//
	// It is not proof that the subnet is isolated — interface VPC endpoints reach AWS services with no
	// default route at all, and Odigos does not have DescribeVpcEndpoints in the managed-Collector
	// permission set. That is exactly why a customer who names subnets explicitly is not overruled by
	// this; see SelectSubnets.
	EgressNone Egress = "none"
	// EgressPublic: a default route to an internet gateway.
	//
	// Usable ONLY with a public address on the task's network interface. An awsvpc ENI in a public
	// subnet with no public IPv4 address has no path off the VPC — the internet gateway will not route
	// for it — and this is the trap the AssignPublicIP argument exists to catch: `assignPublicIp`
	// defaults to false, so the most obvious-looking subnet in a default VPC is the one that silently
	// produces a Collector that cannot export.
	EgressPublic Egress = "internet-gateway"
	// EgressNAT: a default route to a NAT gateway or a NAT instance. Private egress to the internet, and
	// the posture prefers.
	EgressNAT Egress = "nat"
	// EgressForwarded: a default route to a transit gateway, a virtual private gateway, a peering
	// connection or a Cloud WAN core network. Traffic leaves the VPC toward something the customer
	// built, and Odigos cannot see whether the far side reaches the destination.
	//
	// Treated as usable, deliberately. A central egress VPC behind a transit gateway is the standard
	// enterprise topology, and refusing it would refuse most large accounts. It is the same
	// assert-don't-infer contract for customer-built network paths: Odigos reports the endpoint it
	// published, and a wrong assertion is visible per workload rather than silent.
	EgressForwarded Egress = "forwarded"
)

// Private reports whether egress happens without a public address on the task.
func (e Egress) Private() bool { return e == EgressNAT || e == EgressForwarded }

// Routes reports whether there is any way out of the subnet at all.
func (e Egress) Routes() bool { return e != EgressNone }

// VPC is one network a Collector could be placed in.
type VPC struct {
	Region string
	ID     string
	// CIDRBlocks are the VPC's IPv4 blocks, primary first as EC2 returns them. Carried because
	// ingress fallback authorizes senders by the network's CIDR when their security groups are not
	// resolvable, and because the fallback's width guard is checked against these.
	CIDRBlocks []string
	IsDefault  bool
}

// Subnet is one subnet, with the two facts that decide whether a Collector can run in it.
type Subnet struct {
	Region           string
	ID               string
	VPCID            string
	AvailabilityZone string
	// AvailableIPs is the subnet's free private IPv4 address count. Zero means ECS cannot attach an
	// awsvpc ENI here at all: the service would be created and never place a task, failing with a
	// message about network interface provisioning that names neither the subnet nor the cause.
	AvailableIPs int32
	// Egress is what the subnet's effective route table says about getting out. Derived from routes,
	// never from MapPublicIpOnLaunch: that attribute governs EC2 instance launches and has no effect on
	// a Fargate awsvpc ENI, whose address comes from the service's own assignPublicIp setting.
	Egress Egress
}

// Usable reports whether a Collector task could run in this subnet, given whether it will be given a
// public address.
func (s Subnet) Usable(assignPublicIP bool) bool {
	if s.AvailableIPs <= 0 {
		return false
	}
	switch s.Egress {
	case EgressNone:
		return false
	case EgressPublic:
		return assignPublicIP
	default:
		return true
	}
}

// Topology is the account's network shape as EC2 reported it, across every region scanned.
//
// Account-wide rather than per region because VPC and subnet ids are unique across an account, so a
// lookup needs no region and every value carries the region it was found in. It keeps the source-to-VPC
// mapping a single lookup even though sources arrive from every region at once.
type Topology struct {
	// regions are the regions this topology actually describes.
	//
	// Tracked separately from the values, and it is not bookkeeping. A region Odigos failed to list
	// looks exactly like a region with no VPCs, and the two must not be confused: treating the first as
	// the second reports every source in that region as running on a network Odigos could not find,
	// which is a loud, wrong problem on a healthy account. A region that is absent here is skipped
	// instead, so a listing failure costs that region and nothing else.
	regions map[string]bool
	vpcs    map[string]VPC
	subnets map[string]Subnet
}

// NewTopology builds a topology for the regions it describes. Exported so tests and the discovery half
// build the same value the same way.
func NewTopology(regions []string, vpcs []VPC, subnets []Subnet) Topology {
	t := Topology{
		regions: make(map[string]bool, len(regions)),
		vpcs:    make(map[string]VPC, len(vpcs)),
		subnets: make(map[string]Subnet, len(subnets)),
	}
	for _, r := range regions {
		if r != "" {
			t.regions[r] = true
		}
	}
	for _, v := range vpcs {
		if v.ID != "" {
			t.vpcs[v.ID] = v
		}
	}
	for _, s := range subnets {
		if s.ID != "" {
			t.subnets[s.ID] = s
		}
	}
	return t
}

// Merge returns the union of two topologies, which is how a per-region scan accumulates into one value.
// A duplicate id resolves to other's entry; ids are unique per account, so a duplicate means the same
// object was described twice and either copy is correct.
func (t Topology) Merge(other Topology) Topology {
	out := Topology{
		regions: make(map[string]bool, len(t.regions)+len(other.regions)),
		vpcs:    make(map[string]VPC, len(t.vpcs)+len(other.vpcs)),
		subnets: make(map[string]Subnet, len(t.subnets)+len(other.subnets)),
	}
	for r := range t.regions {
		out.regions[r] = true
	}
	for r := range other.regions {
		out.regions[r] = true
	}
	for id, v := range t.vpcs {
		out.vpcs[id] = v
	}
	for id, v := range other.vpcs {
		out.vpcs[id] = v
	}
	for id, s := range t.subnets {
		out.subnets[id] = s
	}
	for id, s := range other.subnets {
		out.subnets[id] = s
	}
	return out
}

// Scanned reports whether this topology describes a region. False means Odigos has no picture of it —
// either it was never listed or the listing failed — and nothing in it should be judged.
func (t Topology) Scanned(region string) bool { return t.regions[region] }

// Regions returns the regions described, sorted.
func (t Topology) Regions() []string {
	out := make([]string, 0, len(t.regions))
	for r := range t.regions {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// VPC looks up one VPC.
func (t Topology) VPC(id string) (VPC, bool) {
	v, ok := t.vpcs[id]
	return v, ok
}

// Subnet looks up one subnet.
func (t Topology) Subnet(id string) (Subnet, bool) {
	s, ok := t.subnets[id]
	return s, ok
}

// VPCOfSubnet resolves a subnet id to the VPC it belongs to.
//
// This is the whole of source-to-network attribution for Fargate: an ECS service's
// networkConfiguration.awsvpcConfiguration names subnets and never the VPC, so the only way to learn
// which network a Fargate workload runs on is to look one of its subnets up here. Lambda is the easy
// case — its VpcConfig carries the VPC id itself.
func (t Topology) VPCOfSubnet(subnetID string) (string, bool) {
	s, ok := t.subnets[subnetID]
	if !ok || s.VPCID == "" {
		return "", false
	}
	return s.VPCID, true
}

// SubnetsIn returns a VPC's subnets, sorted by id so a selection is deterministic.
func (t Topology) SubnetsIn(vpcID string) []Subnet {
	var out []Subnet
	for _, s := range t.subnets {
		if s.VPCID == vpcID {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// VPCs returns every known VPC, sorted by region then id.
func (t Topology) VPCs() []VPC {
	out := make([]VPC, 0, len(t.vpcs))
	for _, v := range t.vpcs {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Region != out[j].Region {
			return out[i].Region < out[j].Region
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// zonesOf lists the distinct availability zones of a subnet set, sorted.
func zonesOf(subnets []Subnet) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range subnets {
		if s.AvailabilityZone != "" && !seen[s.AvailabilityZone] {
			seen[s.AvailabilityZone] = true
			out = append(out, s.AvailabilityZone)
		}
	}
	sort.Strings(out)
	return out
}

// idsOf lists subnet ids, sorted.
func idsOf(subnets []Subnet) []string {
	out := make([]string, 0, len(subnets))
	for _, s := range subnets {
		out = append(out, s.ID)
	}
	sort.Strings(out)
	return out
}

// joinIDs renders an id list for a status message.
func joinIDs(ids []string) string { return strings.Join(ids, ", ") }
