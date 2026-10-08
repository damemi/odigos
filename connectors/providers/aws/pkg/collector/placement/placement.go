package placement

import (
	"fmt"
	"sort"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorplacement"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	collectorsettings "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/settings"
)

// ProblemNetworkUnknown is reported when a network the plan wants a Collector on is not a VPC in the
// topology. Defensive: Resolve only ever names networks the topology supplied, so this fires on a bug
// rather than on a customer's mistake — and it fires loudly instead of placing a Collector using a zero
// VPC, whose refusal messages would name no network at all.
const ProblemNetworkUnknown = "CollectorNetworkUnknown"

// Input is everything the AWS placement decision depends on.
type Input struct {
	// Kind is the collectors type these networks belong to — aws.fargate-collector. It travels into
	// every api.CollectorNetwork because the type is what decides the mode, and therefore whether the
	// network reaches a deployer at all.
	Kind string
	// Inventory is the discovered workloads and the customer's instrumentation intent.
	Inventory Inventory
	// Topology is the account's network shape. Regions it does not describe are skipped rather than
	// judged.
	Topology Topology
	// Settings is the resolved configuration: the feature switch, the per-region overrides, and
	// the `subnetIds` / `assignPublicIp` this package reads for the selected host VPC.
	Settings collectorsettings.Settings
}

// Placement is where one network's Collector goes, and whether it can go there at all.
type Placement struct {
	Region    string
	NetworkID string
	Selection
	// Problems are this network's placement conditions, in a fixed order. A SeverityError here means
	// nothing is deployed for this network — see Deployable.
	Problems []collectorstatus.Problem
}

// Deployable reports whether a Collector should be built for this network.
//
// False is a refusal, not a delay: rule 3 refuses rather than deploying into subnets a Collector
// could not export from, because that failure is invisible — the service is healthy, the tasks are
// running, and no telemetry arrives. The network is still LISTED (see Plan.CollectorNetworks) so the
// refusal is reported against it rather than the network quietly disappearing from the fleet.
func (p Placement) Deployable() bool {
	return !p.Selection.Empty() && !collectorstatus.HasError(p.Problems)
}

// Plan is the AWS placement decision.
type Plan struct {
	// Kind is the collectors type every network in this plan belongs to, carried through from the Input
	// so CollectorNetworks can hand the coordinator complete values.
	Kind string
	// Networks is the provider-neutral decision: which workload networks are served by the regional host,
	// which get none, and why.
	Networks collectorplacement.Plan
	// Placements holds one entry per network Networks builds a Collector for, keyed by
	// NetworkRef.Key(). Always populated for such a network, including when placement was refused —
	// a refused entry has an empty Selection and an error in Problems, which is what makes the refusal
	// reportable rather than silent.
	Placements map[string]Placement
	// Problems are the account-level AWS conditions: source attribution above all — the functions
	// with no network, and any workload whose VPC could not be resolved. Per-host conditions live on the
	// Placement, and provider-neutral host-selection conditions live on Networks.
	Problems []collectorstatus.Problem

	// Inventory and Topology are what the decision was made FROM, carried on the plan so a caller that
	// needs more than the decision does not have to re-read either.
	//
	// The deployer is that caller: converging one network's ingress needs the security groups of every
	// workload routed to it and the network's own CIDR blocks (the fallback's only legal
	// width), and both come out of exactly this inventory and this topology. Re-reading them would mean
	// the rules a pass converges to could be derived from a different reading than the placement they
	// were converged for. Populated by Resolver; empty on a Plan built directly by Resolve, whose
	// callers only want the decision.
	Inventory Inventory
	Topology  Topology

	// Idle are networks with a RECORDED Collector that this plan no longer places one on because its
	// regional lifecycle configuration was disabled or removed. Source count is deliberately irrelevant:
	// a newly created Collector has no marked sources until the customer applies its route destination.
	//
	// They are in the plan for the same reason a network whose placement was refused is: their Collector
	// is still RUNNING. A network dropped from the plan is a network no pass visits, which means nothing
	// observes it, nothing reports it, and the retention policy that would eventually park or remove it
	// never runs — so disabling a configured region would leak its Collector for ever, quietly.
	// Populated by Resolver, which is the only thing here that can read what is recorded.
	Idle []collectorplacement.NetworkRef
}

// Resolve decides which VPCs get a Collector and where each one is placed.
//
// Two halves, in the order they depend on each other. First the provider-neutral decision uses explicit
// regional lifecycle intent, groups enabled sources as consumers, and selects the regional host — which
// is shared with Azure and GCP and is the only thing that may decide WHICH regions have a Collector.
// Then the AWS-specific half:
// for each selected host VPC, which subnets the regional Collector sits in.
//
// The split matters because the two fail differently. A refused regional host selection means the
// customer's configuration is incomplete and the answer is to select a host VPC. A refused AWS
// placement means that host cannot run a Collector that could export, and the answer is a NAT gateway
// or a setting. Merging them into one "no Collector here" would send every customer to the wrong fix.
func Resolve(in Input) Plan {
	sources, sourceProblems := Sources(in.Inventory, in.Topology)

	plan := Plan{
		Kind: in.Kind,
		Networks: collectorplacement.Resolve(collectorplacement.Input{
			Networks: Networks(in.Topology),
			Sources:  sources,
			Settings: in.Settings.Base(),
		}),
		Placements: map[string]Placement{},
		Problems:   sourceProblems,
	}

	for _, ref := range plan.Networks.CollectorNetworks() {
		plan.Placements[ref.Key()] = place(ref, in)
	}
	return plan
}

// place resolves one network's subnets.
func place(ref collectorplacement.NetworkRef, in Input) Placement {
	out := Placement{Region: ref.Region, NetworkID: ref.NetworkID}

	vpc, ok := in.Topology.VPC(ref.NetworkID)
	if !ok {
		out.Problems = []collectorstatus.Problem{{
			Code:     ProblemNetworkUnknown,
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("no Collector is deployed for network %s in %s: it is not a VPC Odigos "+
				"found, so its subnets cannot be resolved", ref.NetworkID, ref.Region),
		}}
		return out
	}

	// Subnet and task-network settings are regional. NetworkID is only the selected physical host VPC;
	// it is not a settings identity.
	cfg := in.Settings.Region(ref.Region)
	out.Selection, out.Problems = SelectSubnets(vpc, in.Topology.SubnetsIn(vpc.ID), cfg.SubnetIDs, cfg.AssignPublicIP)
	return out
}

// CollectorNetworks lists one physical host network per regional Collector, in the shape
// api.CollectorObserver.CollectorNetworks returns.
//
// Every regional host the plan builds for, INCLUDING one whose placement was refused. A refusal has to
// be reported against a Collector row, and a host dropped from this list has no row to report it on.
// Whether a regional Collector is actually built is Placement.Deployable, checked by the deployer.
func (p Plan) CollectorNetworks() []baseapi.CollectorNetwork {
	refs := p.Networks.CollectorNetworks()
	out := make([]baseapi.CollectorNetwork, 0, len(refs)+len(p.Idle))
	for _, ref := range refs {
		out = append(out, baseapi.CollectorNetwork{Region: ref.Region, NetworkID: ref.NetworkID, Kind: p.Kind})
	}
	// Then the idle ones, which placement does not build for and something still has to visit. Appended
	// rather than merged into Placements: an idle network has no placement decision, and inventing an
	// empty one would make it read as a network whose subnets could not be resolved.
	for _, ref := range p.Idle {
		out = append(out, baseapi.CollectorNetwork{Region: ref.Region, NetworkID: ref.NetworkID, Kind: p.Kind})
	}
	return out
}

// IsIdle reports whether this host network has a recorded regional Collector the plan no longer places.
//
// What separates "converge this Collector towards its configuration" from "decide how long to keep it".
// A regional Collector cannot be both idle and placed — Resolver only ever adds what placement left out.
func (p Plan) IsIdle(region, networkID string) bool {
	for _, ref := range p.Idle {
		if ref.Region == region && ref.NetworkID == networkID {
			return true
		}
	}
	return false
}

// DeployableNetworks lists only the regional host networks placement could actually resolve subnets for.
func (p Plan) DeployableNetworks() []baseapi.CollectorNetwork {
	var out []baseapi.CollectorNetwork
	for _, n := range p.CollectorNetworks() {
		if pl, ok := p.Placement(n.Region, n.NetworkID); ok && pl.Deployable() {
			out = append(out, n)
		}
	}
	return out
}

// Placement returns one regional host VPC's subnet decision.
func (p Plan) Placement(region, networkID string) (Placement, bool) {
	pl, ok := p.Placements[collectorplacement.NetworkRef{Region: region, NetworkID: networkID}.Key()]
	return pl, ok
}

// AllProblems is every condition the plan produced, in a stable order: the account-level AWS ones, then
// the provider-neutral host-selection conditions, then each regional host placement condition.
//
// A single list because a caller reporting a plan must not have to know that problems arrive from three
// places, and forgetting one of the three is how a refusal becomes silent.
func (p Plan) AllProblems() []collectorstatus.Problem {
	out := make([]collectorstatus.Problem, 0, len(p.Problems)+len(p.Networks.Problems))
	out = append(out, p.Problems...)
	out = append(out, p.Networks.Problems...)
	keys := make([]string, 0, len(p.Placements))
	for k := range p.Placements {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, p.Placements[k].Problems...)
	}
	return out
}
