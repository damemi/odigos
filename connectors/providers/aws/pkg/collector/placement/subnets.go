package placement

import (
	"fmt"
	"sort"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
)

// maxAvailabilityZones caps how many zones Odigos spreads a Collector across when it chooses.
//
// Three, because it is the standard multi-AZ posture in every AWS region that has three zones, it
// bounds the load balancer's zone mappings and the cross-zone traffic they carry, and it leaves a spare
// zone for rescheduling above the default two tasks. A customer who wants a different spread names
// subnets explicitly, which is not capped.
const maxAvailabilityZones = 3

// minAvailabilityZones is the spread asks for. Falling short is reported, not refused: one zone
// costs resilience, and this function's refusals are reserved for a Collector that could not export at
// all.
const minAvailabilityZones = 2

// Problem codes. Callers branch on these, never on a message.
const (
	// ProblemNoUsableSubnet: nothing in this VPC can host a Collector that could export. Refused.
	ProblemNoUsableSubnet = "CollectorNoUsableSubnet"
	// ProblemSubnetUnknown: a configured subnetIds entry is not a subnet Odigos found. Refused.
	ProblemSubnetUnknown = "CollectorSubnetUnknown"
	// ProblemSubnetNotInNetwork: a configured subnet belongs to a different VPC. Refused.
	ProblemSubnetNotInNetwork = "CollectorSubnetNotInNetwork"
	// ProblemSubnetFull: a subnet has no free private IPv4 address, so no task can be placed in it.
	ProblemSubnetFull = "CollectorSubnetFull"
	// ProblemSubnetNoEgress: a configured subnet has no active default route.
	ProblemSubnetNoEgress = "CollectorSubnetNoEgress"
	// ProblemSubnetNeedsPublicIP: a configured subnet reaches the internet only through an internet
	// gateway, and the task is not being given a public address.
	ProblemSubnetNeedsPublicIP = "CollectorSubnetNeedsPublicIp"
	// ProblemSubnetPublic: Odigos chose public subnets because no private subnet has egress. This is a
	// customer-visible network posture, not a health fault: assignPublicIp explicitly allows it, and the
	// Collector can be fully ready there.
	ProblemSubnetPublic = "CollectorSubnetPublic"
	// ProblemSingleAvailabilityZone: the Collector sits in fewer zones than asks for.
	ProblemSingleAvailabilityZone = "CollectorSingleAvailabilityZone"
)

// Selection is where one network's Collector is placed.
type Selection struct {
	// SubnetIDs are the subnets the ECS service and the load balancer are created across, sorted.
	SubnetIDs []string
	// Zones are the distinct availability zones those subnets cover, sorted.
	Zones []string
	// Private is whether every chosen subnet reaches the destination without a public address on the
	// task. False means the Collector needs `assignPublicIp`, which the deployer has to honour — a
	// service created in a public subnet without it places tasks that cannot export.
	Private bool
	// Configured is whether these subnets came from the customer's `subnetIds` rather than from
	// Odigos's own choice. It travels because the two carry different authority: a configured subnet is
	// an assertion Odigos does not overrule, and an Odigos-chosen one is a decision it must justify.
	Configured bool
}

// Empty reports whether nothing was selected, which is the shape of a refusal.
func (s Selection) Empty() bool { return len(s.SubnetIDs) == 0 }

// SelectSubnets chooses the subnets a Collector for one VPC runs in.
//
// Two paths, with deliberately different authority.
//
// When `configured` is empty Odigos chooses, and it is answerable for the choice: it takes only subnets
// that provably work — a free address and an active default route it can follow — spreads them across
// zones, prefers private, and REFUSES the network when nothing qualifies. Refusing is the point.
//
// When the customer named subnets, Odigos validates and reports but does not overrule. It cannot see
// every path out of a subnet: interface VPC endpoints reach AWS services with no default route at all,
// and DescribeVpcEndpoints is not in the managed-Collector permission set. So a named subnet with
// no route Odigos can find is DEGRADED, not refused — the same assert-don't-infer contract applies to
// customer-provided network paths, where a wrong assertion shows up per workload instead of silently. What is
// still refused on this path is what cannot be an assertion: a subnet that does not exist, one in
// another VPC, and a set with no placeable address anywhere in it.
//
// Problems are returned in a fixed order so a status message is stable between passes. A returned
// Selection is empty if and only if some problem is SeverityError.
func SelectSubnets(vpc VPC, subnets []Subnet, configured []string, assignPublicIP bool) (Selection, []collectorstatus.Problem) {
	if len(configured) > 0 {
		return selectConfigured(vpc, subnets, configured, assignPublicIP)
	}
	return selectAutomatic(vpc, subnets, assignPublicIP)
}

// selectAutomatic is the Odigos-chooses path.
func selectAutomatic(vpc VPC, subnets []Subnet, assignPublicIP bool) (Selection, []collectorstatus.Problem) {
	var usable []Subnet
	for _, s := range subnets {
		if s.Usable(assignPublicIP) {
			usable = append(usable, s)
		}
	}
	if len(usable) == 0 {
		return Selection{}, []collectorstatus.Problem{noUsableSubnet(vpc, subnets, assignPublicIP)}
	}

	var problems []collectorstatus.Problem

	// Private over zone spread, and the ordering is a judgment worth stating. Taking a public subnet to
	// reach a second zone would put the telemetry egress tier — the one process holding the destination
	// credential — on a public address to buy resilience. That is a posture change Odigos should not
	// make on the customer's behalf; a missing zone is a resilience cost, it is reported below, and
	// `subnetIds` overrides it. So privacy wins and the narrower spread is what gets reported.
	private := filterSubnets(usable, func(s Subnet) bool { return s.Egress.Private() })
	chosen, public := private, false
	if len(private) == 0 {
		chosen, public = usable, true
	}

	spread := bestPerZone(chosen)
	if len(spread) > maxAvailabilityZones {
		spread = spread[:maxAvailabilityZones]
	}
	sel := Selection{SubnetIDs: idsOf(spread), Zones: zonesOf(spread), Private: allPrivate(spread)}

	if public {
		problems = append(problems, collectorstatus.Problem{
			Code:     ProblemSubnetPublic,
			Severity: collectorstatus.SeverityInfo,
			Message: fmt.Sprintf("no private subnet in %s has egress, so the Collector is placed in public "+
				"subnets (%s) with a public address; give the VPC a NAT gateway, or name private subnets in "+
				"collector.regions.%s.subnetIds, to keep it off the internet",
				vpc.ID, joinIDs(sel.SubnetIDs), vpc.Region),
		})
	}
	if p, ok := zoneSpreadProblem(vpc, sel); ok {
		problems = append(problems, p)
	}
	return sel, problems
}

// selectConfigured is the customer-named path.
func selectConfigured(vpc VPC, subnets []Subnet, configured []string, assignPublicIP bool) (Selection, []collectorstatus.Problem) {
	known := make(map[string]Subnet, len(subnets))
	for _, s := range subnets {
		known[s.ID] = s
	}

	// Every problem at once and in a fixed order, rather than the first one found: a customer who listed
	// four subnets and got one complaint per pass would fix them one deploy at a time.
	var unknown, foreign, full []string
	var named []Subnet
	for _, id := range dedup(configured) {
		s, ok := known[id]
		switch {
		case !ok:
			unknown = append(unknown, id)
		case s.VPCID != vpc.ID:
			foreign = append(foreign, id)
		case s.AvailableIPs <= 0:
			full = append(full, id)
		default:
			named = append(named, s)
		}
	}

	var problems []collectorstatus.Problem
	if len(unknown) > 0 {
		problems = append(problems, collectorstatus.Problem{
			Code:     ProblemSubnetUnknown,
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("collector.regions.%s.subnetIds names %s, which Odigos did not find in %s; "+
				"no Collector is deployed until every named subnet exists, because deploying into the rest "+
				"would place it somewhere the configuration did not ask for",
				vpc.Region, joinIDs(unknown), vpc.Region),
		})
	}
	if len(foreign) > 0 {
		problems = append(problems, collectorstatus.Problem{
			Code:     ProblemSubnetNotInNetwork,
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("collector.regions.%s.subnetIds names %s, which belong to another VPC; an "+
				"ECS service cannot span VPCs, so name subnets in %s or move the entry to the VPC that owns them",
				vpc.Region, joinIDs(foreign), vpc.ID),
		})
	}
	if len(full) > 0 {
		problems = append(problems, collectorstatus.Problem{
			Code:     ProblemSubnetFull,
			Severity: collectorstatus.SeverityDegraded,
			Message: fmt.Sprintf("subnet %s in %s has no free IPv4 address, so no Collector task can be placed "+
				"in it and it is skipped; ECS would report this as a network interface failure that names "+
				"neither the subnet nor the cause", joinIDs(full), vpc.ID),
		})
	}
	// A refused entry means the whole configuration is untrustworthy, so nothing is deployed from it. A
	// full subnet is different — it is skipped, and only an empty remainder refuses.
	if len(unknown) > 0 || len(foreign) > 0 {
		return Selection{}, problems
	}
	if len(named) == 0 {
		return Selection{}, append(problems, collectorstatus.Problem{
			Code:     ProblemNoUsableSubnet,
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("no subnet named in collector.regions.%s.subnetIds can host a Collector task; "+
				"free addresses in one of them, or name others", vpc.Region),
		})
	}

	// Reachability findings on named subnets are reported, never acted on. See the function comment: the
	// customer asserted the path, and Odigos cannot see interface VPC endpoints.
	if noRoute := idsOf(filterSubnets(named, func(s Subnet) bool { return s.Egress == EgressNone })); len(noRoute) > 0 {
		problems = append(problems, collectorstatus.Problem{
			Code:     ProblemSubnetNoEgress,
			Severity: collectorstatus.SeverityDegraded,
			Message: fmt.Sprintf("subnet %s in %s has no active default route that Odigos can see; it is used "+
				"because you named it, but if it also has no VPC endpoint reaching the destination and the "+
				"OpAMP endpoint, the Collector will run and export nothing",
				joinIDs(noRoute), vpc.ID),
		})
	}
	if !assignPublicIP {
		needsIP := idsOf(filterSubnets(named, func(s Subnet) bool { return s.Egress == EgressPublic }))
		if len(needsIP) > 0 {
			problems = append(problems, collectorstatus.Problem{
				Code:     ProblemSubnetNeedsPublicIP,
				Severity: collectorstatus.SeverityDegraded,
				Message: fmt.Sprintf("subnet %s in %s reaches the internet only through an internet gateway, "+
					"which does not route for a task with no public address; set "+
					"collector.regions.%s.assignPublicIp to true, or name private subnets",
					joinIDs(needsIP), vpc.ID, vpc.Region),
			})
		}
	}

	sel := Selection{SubnetIDs: idsOf(named), Zones: zonesOf(named), Private: allPrivate(named), Configured: true}
	if p, ok := zoneSpreadProblem(vpc, sel); ok {
		problems = append(problems, p)
	}
	return sel, problems
}

// noUsableSubnet is the refusal, and it says which of the three causes applied rather than one sentence
// covering all of them: "no usable subnet" sends a customer looking in the wrong place, and the three
// fixes — a NAT gateway, assignPublicIp, freeing addresses — have nothing in common.
func noUsableSubnet(vpc VPC, subnets []Subnet, assignPublicIP bool) collectorstatus.Problem {
	problem := func(msg string) collectorstatus.Problem {
		return collectorstatus.Problem{Code: ProblemNoUsableSubnet, Severity: collectorstatus.SeverityError, Message: msg}
	}
	if len(subnets) == 0 {
		return problem(fmt.Sprintf("no Collector is deployed in %s: Odigos found no subnets in it, so there is "+
			"nowhere to place a task; create subnets, or name existing ones in collector.regions.%s.subnetIds",
			vpc.ID, vpc.Region))
	}
	// Reported in the order a customer can act on: the one-setting fix first, then the one that needs
	// infrastructure, then capacity.
	if !assignPublicIP {
		if public := idsOf(filterSubnets(subnets, func(s Subnet) bool {
			return s.Egress == EgressPublic && s.AvailableIPs > 0
		})); len(public) > 0 {
			return problem(fmt.Sprintf("no Collector is deployed in %s: the only subnets that could hold a task "+
				"(%s) reach the internet through an internet gateway, which does not route for a task with no "+
				"public address. Set collector.regions.%s.assignPublicIp to true to use them, or give the VPC "+
				"a NAT gateway and private subnets. Deploying anyway would produce a Collector that runs and "+
				"exports nothing", vpc.ID, joinIDs(public), vpc.Region))
		}
	}
	if routed := filterSubnets(subnets, func(s Subnet) bool { return s.Egress.Routes() }); len(routed) == 0 {
		return problem(fmt.Sprintf("no Collector is deployed in %s: none of its %d subnets has an active default "+
			"route, so a Collector there could reach neither the destination nor the OpAMP endpoint. Add a NAT "+
			"gateway (or a transit gateway path) and its route, or name subnets you know are reachable in "+
			"collector.regions.%s.subnetIds", vpc.ID, len(subnets), vpc.Region))
	}
	return problem(fmt.Sprintf("no Collector is deployed in %s: every subnet with egress is out of free IPv4 "+
		"addresses, so ECS could not attach a task network interface. Free addresses, or add a subnet",
		vpc.ID))
}

// zoneSpreadProblem reports a Collector placed in fewer zones than asks for.
func zoneSpreadProblem(vpc VPC, sel Selection) (collectorstatus.Problem, bool) {
	if len(sel.Zones) >= minAvailabilityZones {
		return collectorstatus.Problem{}, false
	}
	detail := "no other zone in the VPC has a subnet a Collector could export from"
	if sel.Configured {
		detail = fmt.Sprintf("collector.regions.%s.subnetIds names subnets in one zone only", vpc.Region)
	}
	return collectorstatus.Problem{
		Code:     ProblemSingleAvailabilityZone,
		Severity: collectorstatus.SeverityDegraded,
		Message: fmt.Sprintf("the Collector for %s is placed in a single availability zone (%s): %s. It works, "+
			"and a zone failure takes the whole network's telemetry egress with it",
			vpc.ID, joinIDs(sel.Zones), detail),
	}, true
}

// bestPerZone picks one subnet per availability zone and returns them sorted by zone.
//
// The pick inside a zone is the subnet with the most free addresses, so a Collector scaling up does not
// land in the tightest subnet in the VPC, with the id as the tie-break so two passes over the same
// account choose the same subnets — a changed selection is a new task definition revision and a rolling
// replacement, which is not something a re-read of unchanged infrastructure should cause.
func bestPerZone(subnets []Subnet) []Subnet {
	byZone := map[string]Subnet{}
	for _, s := range subnets {
		cur, seen := byZone[s.AvailabilityZone]
		if !seen || s.AvailableIPs > cur.AvailableIPs || (s.AvailableIPs == cur.AvailableIPs && s.ID < cur.ID) {
			byZone[s.AvailabilityZone] = s
		}
	}
	out := make([]Subnet, 0, len(byZone))
	for _, s := range byZone {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AvailabilityZone < out[j].AvailabilityZone })
	return out
}

func filterSubnets(subnets []Subnet, keep func(Subnet) bool) []Subnet {
	var out []Subnet
	for _, s := range subnets {
		if keep(s) {
			out = append(out, s)
		}
	}
	return out
}

func allPrivate(subnets []Subnet) bool {
	for _, s := range subnets {
		if !s.Egress.Private() {
			return false
		}
	}
	return len(subnets) > 0
}

func dedup(ids []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
