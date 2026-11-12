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
	"net/netip"
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
	DNSKnown   bool
	DNSSupport bool
	Region     string
	ID         string
	// CIDRBlocks are the VPC's IPv4 blocks, primary first as EC2 returns them. Carried because
	// ingress fallback authorizes senders by the network's CIDR when their security groups are not
	// resolvable, and because the fallback's width guard is checked against these.
	CIDRBlocks []string
	IsDefault  bool
}

// Peering is an active, direct VPC peering connection. Regions are retained because a peering
// connection can be visible from both sides of the EC2 API, while only same-region connections are
// eligible for the managed Collector ingress path.
type Peering struct {
	ID      string
	VPC1    string
	VPC2    string
	Region1 string
	Region2 string
}

// Route is the effective, active route from a subnet. Only the destination and peering target are
// needed by the placement reachability proof; other route targets remain represented by Egress.
type Route struct {
	DestinationCIDR string
	PeeringID       string
}

// Subnet is one subnet, with the two facts that decide whether a Collector can run in it.
type Subnet struct {
	ACLKnown         bool
	ACLRules         []ACLRule
	Region           string
	ID               string
	VPCID            string
	CIDRBlock        string
	AvailabilityZone string
	// AvailableIPs is the subnet's free private IPv4 address count. Zero means ECS cannot attach an
	// awsvpc ENI here at all: the service would be created and never place a task, failing with a
	// message about network interface provisioning that names neither the subnet nor the cause.
	AvailableIPs int32
	// Egress is what the subnet's effective route table says about getting out. Derived from routes,
	// never from MapPublicIpOnLaunch: that attribute governs EC2 instance launches and has no effect on
	// a Fargate awsvpc ENI, whose address comes from the service's own assignPublicIp setting.
	Egress Egress
	// Routes are the effective active routes for this subnet, including direct peering routes.
	Routes []Route
}

// SecurityGroup is the regional ingress snapshot for one security group. Only inbound rules are retained:
// a Collector route is safe to select only when the Collector's group actually admits a workload group on
// an OTLP port. Group IDs are account-unique, but VPC and region stay attached so a missing or cross-region
// reference cannot accidentally be treated as proof.
type SecurityGroup struct {
	Region      string
	ID          string
	VPCID       string
	Ingress     []SecurityGroupRule
	EgressKnown bool
	Egress      []SecurityGroupRule
}

// SecurityGroupRule is one inbound EC2 security-group permission. PortsKnown distinguishes a malformed or
// partial AWS permission from an intentional all-protocol rule; SourceSecurityGroupIDs are the references
// that can prove a Fargate workload is admitted through the same VPC or an active same-region peering.
type SecurityGroupRule struct {
	Protocol               string
	FromPort               int32
	ToPort                 int32
	PortsKnown             bool
	SourceSecurityGroupIDs []string
	SourceCIDRs            []string
	Incomplete             bool
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
	regions        map[string]bool
	vpcs           map[string]VPC
	subnets        map[string]Subnet
	peerings       map[string]Peering
	securityGroups map[string]SecurityGroup
}

// NewTopology builds a topology for the regions it describes. Exported so tests and the discovery half
// build the same value the same way.
func NewTopology(regions []string, vpcs []VPC, subnets []Subnet) Topology {
	return newTopology(regions, vpcs, subnets, nil, nil)
}

// NewTopologyWithPeerings builds a topology with the active direct peerings discovered for it.
func NewTopologyWithPeerings(regions []string, vpcs []VPC, subnets []Subnet, peerings []Peering) Topology {
	return newTopology(regions, vpcs, subnets, peerings, nil)
}

// NewTopologyWithSecurityGroups builds a topology with the inbound security-group snapshot used by
// Collector ingress reachability checks.
func NewTopologyWithSecurityGroups(regions []string, vpcs []VPC, subnets []Subnet, peerings []Peering, securityGroups []SecurityGroup) Topology {
	return newTopology(regions, vpcs, subnets, peerings, securityGroups)
}

func newTopology(regions []string, vpcs []VPC, subnets []Subnet, peerings []Peering, securityGroups []SecurityGroup) Topology {
	t := Topology{
		regions:        make(map[string]bool, len(regions)),
		vpcs:           make(map[string]VPC, len(vpcs)),
		subnets:        make(map[string]Subnet, len(subnets)),
		peerings:       make(map[string]Peering, len(peerings)),
		securityGroups: make(map[string]SecurityGroup, len(securityGroups)),
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
	for _, p := range peerings {
		if p.ID != "" {
			t.peerings[p.ID] = p
		}
	}
	for _, g := range securityGroups {
		if g.ID != "" {
			t.securityGroups[g.ID] = g
		}
	}
	return t
}

// Merge returns the union of two topologies, which is how a per-region scan accumulates into one value.
// A duplicate id resolves to other's entry; ids are unique per account, so a duplicate means the same
// object was described twice and either copy is correct.
func (t Topology) Merge(other Topology) Topology {
	out := Topology{
		regions:        make(map[string]bool, len(t.regions)+len(other.regions)),
		vpcs:           make(map[string]VPC, len(t.vpcs)+len(other.vpcs)),
		subnets:        make(map[string]Subnet, len(t.subnets)+len(other.subnets)),
		peerings:       make(map[string]Peering, len(t.peerings)+len(other.peerings)),
		securityGroups: make(map[string]SecurityGroup, len(t.securityGroups)+len(other.securityGroups)),
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
	for id, p := range t.peerings {
		out.peerings[id] = p
	}
	for id, p := range other.peerings {
		out.peerings[id] = p
	}
	for id, g := range t.securityGroups {
		out.securityGroups[id] = g
	}
	for id, g := range other.securityGroups {
		out.securityGroups[id] = g
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

// Peerings returns active direct peerings, sorted by id.
func (t Topology) Peerings() []Peering {
	out := make([]Peering, 0, len(t.peerings))
	for _, p := range t.peerings {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// SecurityGroup looks up one regional ingress snapshot.
func (t Topology) SecurityGroup(id string) (SecurityGroup, bool) {
	g, ok := t.securityGroups[id]
	return g, ok
}

// SecurityGroups returns every known security-group snapshot, sorted by region then id.
func (t Topology) SecurityGroups() []SecurityGroup {
	out := make([]SecurityGroup, 0, len(t.securityGroups))
	for _, g := range t.securityGroups {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Region != out[j].Region {
			return out[i].Region < out[j].Region
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Reachability is the result of the placement-level path proof.
type Reachability string

const (
	ReachabilityUnknown     Reachability = "unknown"
	ReachabilityUnreachable Reachability = "unreachable"
	ReachabilityReachable   Reachability = "reachable"
)

// Reachability proves whether selected subnets in two VPCs can communicate through an active,
// same-region direct peering, including DNS support and the ordered network ACLs.
// Security-group admission and egress are checked separately.
func (t Topology) Reachability(hostRegion, hostVPC, consumerRegion, consumerVPC string, hostSubnetIDs, consumerSubnetIDs []string) Reachability {
	return t.ReachabilityForPorts(hostRegion, hostVPC, consumerRegion, consumerVPC, hostSubnetIDs, consumerSubnetIDs, []int32{4317, 4318})
}

// ReachabilityForPorts checks only the receivers published by the selected Collector.
func (t Topology) ReachabilityForPorts(hostRegion, hostVPC, consumerRegion, consumerVPC string, hostSubnetIDs, consumerSubnetIDs []string, ports []int32) Reachability {
	if !t.aclPathAllowed(hostSubnetIDs, consumerSubnetIDs, ports) {
		return ReachabilityUnreachable
	}
	host, hostOK := t.vpcs[hostVPC]
	consumer, consumerOK := t.vpcs[consumerVPC]
	if !hostOK || !consumerOK {
		return ReachabilityUnknown
	}
	if (consumer.DNSKnown && !consumer.DNSSupport) || (host.DNSKnown && !host.DNSSupport) {
		return ReachabilityUnreachable
	}
	if hostRegion == "" {
		hostRegion = host.Region
	}
	if consumerRegion == "" {
		consumerRegion = consumer.Region
	}
	if hostRegion == "" || consumerRegion == "" || host.Region != hostRegion || consumer.Region != consumerRegion {
		return ReachabilityUnknown
	}
	if hostVPC == consumerVPC {
		return ReachabilityReachable
	}
	if hostRegion != consumerRegion {
		return ReachabilityUnreachable
	}
	if len(host.CIDRBlocks) == 0 || len(consumer.CIDRBlocks) == 0 {
		return ReachabilityUnknown
	}

	if cidrSetsOverlap(host.CIDRBlocks, consumer.CIDRBlocks) {
		return ReachabilityUnreachable
	}

	var peering Peering
	found := false
	for _, p := range t.peerings {
		if !peeringConnects(p, hostVPC, consumerVPC, hostRegion) {
			continue
		}
		peering, found = p, true
		break
	}
	if !found {
		return ReachabilityUnreachable
	}

	hostSubnets, hostState := t.selectedSubnets(hostVPC, hostSubnetIDs)
	consumerSubnets, consumerState := t.selectedSubnets(consumerVPC, consumerSubnetIDs)
	if hostState != ReachabilityReachable || consumerState != ReachabilityReachable {
		if hostState == ReachabilityUnknown || consumerState == ReachabilityUnknown {
			return ReachabilityUnknown
		}
		return ReachabilityUnreachable
	}
	if !routesCover(hostSubnets, peering.ID, consumer.CIDRBlocks) || !routesCover(consumerSubnets, peering.ID, host.CIDRBlocks) {
		return ReachabilityUnreachable
	}
	return ReachabilityReachable
}

// CanReach is the boolean form used by callers that only need the safe routing decision.
func (t Topology) CanReach(hostRegion, hostVPC, consumerRegion, consumerVPC string, hostSubnetIDs, consumerSubnetIDs []string) bool {
	return t.Reachability(hostRegion, hostVPC, consumerRegion, consumerVPC, hostSubnetIDs, consumerSubnetIDs) == ReachabilityReachable
}

// CollectorIngressReachability proves security-group-reference admission for callers without workload
// subnet IDs. The workload-aware form also proves CIDR admission from every selected subnet.
func (t Topology) CollectorIngressReachability(region, collectorVPC, collectorSecurityGroup, sourceVPC string, sourceSecurityGroups []string) Reachability {
	return t.CollectorIngressReachabilityForPorts(region, collectorVPC, collectorSecurityGroup, sourceVPC, sourceSecurityGroups, []int32{4317, 4318})
}

// CollectorIngressReachabilityForPorts is the protocol-specific form. Every requested receiver port must
// be admitted because different Fargate language agents may select different OTLP transports.
func (t Topology) CollectorIngressReachabilityForPorts(region, collectorVPC, collectorSecurityGroup, sourceVPC string,
	sourceSecurityGroups []string, ports []int32) Reachability {
	return t.CollectorIngressReachabilityForWorkload(region, collectorVPC, collectorSecurityGroup, sourceVPC, nil, sourceSecurityGroups, ports)
}

// CollectorIngressReachabilityForWorkload proves that every published OTLP port admits the workload.
// A source security-group reference admits any of its ENIs; CIDR rules must cover every subnet ECS or
// Lambda may select. Direct same-region peering and reciprocal routes are checked separately.
func (t Topology) CollectorIngressReachabilityForWorkload(region, collectorVPC, collectorSecurityGroup, sourceVPC string,
	sourceSubnetIDs, sourceSecurityGroups []string, ports []int32) Reachability {
	host, hostOK := t.vpcs[collectorVPC]
	consumer, consumerOK := t.vpcs[sourceVPC]
	if !hostOK || !consumerOK || host.Region != region || consumer.Region != region || region == "" {
		return ReachabilityUnknown
	}
	collector, collectorOK := t.securityGroups[collectorSecurityGroup]
	if !collectorOK || collector.ID == "" || collector.VPCID != collectorVPC || collector.Region != region {
		return ReachabilityUnknown
	}
	if collectorVPC != sourceVPC {
		if !hasSameRegionPeering(t.peerings, collectorVPC, sourceVPC, region) {
			return ReachabilityUnreachable
		}
	}
	verifiedSourceGroups := make(map[string]bool, len(sourceSecurityGroups))
	sourceGroupsUnknown := len(sourceSecurityGroups) == 0
	for _, id := range sourceSecurityGroups {
		group, ok := t.securityGroups[id]
		if !ok || group.ID == "" || group.VPCID != sourceVPC || group.Region != region {
			sourceGroupsUnknown = true
			continue
		}
		verifiedSourceGroups[id] = true
	}

	unknown := sourceGroupsUnknown
	for _, port := range ports {
		if !t.workloadEgressAllowed(sourceSecurityGroups, collectorSecurityGroup, host.CIDRBlocks, port) {
			return ReachabilityUnreachable
		}
		admitted := false
		cidrCovered := make(map[string]bool, len(sourceSubnetIDs))
		cidrUnknown := false
		for _, rule := range collector.Ingress {
			if rule.Incomplete {
				unknown = true
			}
			if !securityGroupRuleAllowsPort(rule, port) {
				continue
			}
			for sourceID := range verifiedSourceGroups {
				for _, allowedID := range rule.SourceSecurityGroupIDs {
					if sourceID == allowedID {
						admitted = true
						break
					}
				}
			}
			if admitted {
				break
			}
			if len(rule.SourceCIDRs) == 0 {
				continue
			}
			if len(sourceSubnetIDs) == 0 {
				cidrUnknown = true
				continue
			}
			for _, subnetID := range sourceSubnetIDs {
				subnet, ok := t.subnets[subnetID]
				if !ok || subnet.Region != region || subnet.VPCID != sourceVPC || subnet.CIDRBlock == "" {
					cidrUnknown = true
					continue
				}
				for _, allowed := range rule.SourceCIDRs {
					if cidrContains(allowed, subnet.CIDRBlock) {
						cidrCovered[subnetID] = true
						break
					}
				}
			}
		}
		if !admitted && len(sourceSubnetIDs) > 0 {
			admitted = true
			for _, subnetID := range sourceSubnetIDs {
				if !cidrCovered[subnetID] {
					admitted = false
					break
				}
			}
		}
		if !admitted {
			if unknown || cidrUnknown {
				return ReachabilityUnknown
			}
			return ReachabilityUnreachable
		}
	}
	return ReachabilityReachable
}

func cidrContains(allowed, source string) bool {
	a, err := netip.ParsePrefix(allowed)
	if err != nil {
		return false
	}
	s, err := netip.ParsePrefix(source)
	if err != nil || a.Addr().Is4() != s.Addr().Is4() {
		return false
	}
	return a.Bits() <= s.Bits() && a.Contains(s.Masked().Addr())
}

// CollectorSecurityGroupAllowsOTLP is the boolean form for callers that need a safe admission gate.
func (t Topology) CollectorSecurityGroupAllowsOTLP(region, collectorVPC, collectorSecurityGroup, sourceVPC string, sourceSecurityGroups []string) bool {
	return t.CollectorIngressReachability(region, collectorVPC, collectorSecurityGroup, sourceVPC, sourceSecurityGroups) == ReachabilityReachable
}

// CollectorSecurityGroupAllowsSelfHealth checks the manual artifact's NLB/task
// shared security-group contract for the Collector health endpoint.
func (t Topology) CollectorSecurityGroupAllowsSelfHealth(region, vpcID, securityGroupID string) Reachability {
	return t.CollectorSecurityGroupAllowsSelfPorts(region, vpcID, securityGroupID, []int32{13133})
}

// CollectorSecurityGroupAllowsSelfPorts verifies the NLB-to-target hop.
func (t Topology) CollectorSecurityGroupAllowsSelfPorts(region, vpcID, securityGroupID string, ports []int32) Reachability {
	group, ok := t.securityGroups[securityGroupID]
	if !ok || group.Region != region || group.VPCID != vpcID {
		return ReachabilityUnknown
	}
	for _, port := range ports {
		unknown, admitted := false, false
		for _, rule := range group.Ingress {
			if rule.Incomplete {
				unknown = true
			}
			if !securityGroupRuleAllowsPort(rule, port) {
				continue
			}
			for _, source := range rule.SourceSecurityGroupIDs {
				if source == securityGroupID {
					admitted = true
				}
			}
		}
		if !admitted {
			if unknown {
				return ReachabilityUnknown
			}
			return ReachabilityUnreachable
		}
	}
	return ReachabilityReachable
}

func securityGroupRuleAllowsPort(rule SecurityGroupRule, port int32) bool {
	if rule.Protocol == "-1" {
		return true
	}
	if rule.Protocol != "tcp" || !rule.PortsKnown || rule.FromPort > rule.ToPort {
		return false
	}
	return rule.FromPort <= port && port <= rule.ToPort
}

func hasSameRegionPeering(peerings map[string]Peering, vpc1, vpc2, region string) bool {
	for _, p := range peerings {
		if peeringConnects(p, vpc1, vpc2, region) {
			return true
		}
	}
	return false
}

func (t Topology) selectedSubnets(vpcID string, ids []string) ([]Subnet, Reachability) {
	if len(ids) == 0 {
		out := t.SubnetsIn(vpcID)
		if len(out) == 0 {
			return nil, ReachabilityUnknown
		}
		return out, ReachabilityReachable
	}
	out := make([]Subnet, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		s, ok := t.subnets[id]
		if !ok || s.VPCID != vpcID {
			return nil, ReachabilityUnknown
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, ReachabilityUnknown
	}
	return out, ReachabilityReachable
}

func peeringConnects(p Peering, vpc1, vpc2, region string) bool {
	if !((p.VPC1 == vpc1 && p.VPC2 == vpc2) || (p.VPC1 == vpc2 && p.VPC2 == vpc1)) {
		return false
	}
	return (p.Region1 == "" || p.Region1 == region) && (p.Region2 == "" || p.Region2 == region)
}

func cidrSetsOverlap(a, b []string) bool {
	for _, left := range a {
		lp, err := netip.ParsePrefix(left)
		if err != nil {
			return true
		}
		for _, right := range b {
			rp, err := netip.ParsePrefix(right)
			if err != nil {
				return true
			}
			if lp.Overlaps(rp) {
				return true
			}
		}
	}
	return false
}

func routesCover(subnets []Subnet, peeringID string, targets []string) bool {
	// ECS may place a task in any selected subnet. Every selected subnet therefore needs a route for
	// every CIDR association on the opposite VPC; one good AZ cannot prove the service-wide path.
	for _, subnet := range subnets {
		for _, target := range targets {
			targetPrefix, err := netip.ParsePrefix(target)
			if err != nil {
				return false
			}
			covered := false
			for _, route := range subnet.Routes {
				if route.PeeringID != peeringID {
					continue
				}
				prefix, err := netip.ParsePrefix(route.DestinationCIDR)
				if err == nil && prefix.Bits() <= targetPrefix.Bits() && prefix.Contains(targetPrefix.Addr()) {
					covered = true
					break
				}
			}
			if covered {
				continue
			}
			return false
		}
	}
	return true
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
