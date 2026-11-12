package placement

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/ec2"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorplacement"
	basestore "github.com/odigos-io/odigos/connectors/base/store"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/awsclient"
	awsingress "github.com/odigos-io/odigos/connectors/providers/aws/pkg/collector/ingress"
	collectorsettings "github.com/odigos-io/odigos/connectors/providers/aws/pkg/collector/settings"
	awsstore "github.com/odigos-io/odigos/connectors/providers/aws/pkg/workload/store"
)

// Resolver resolves the placement decision ONCE PER PASS and serves it to both the observer and the
// deployer.
//
// One resolution rather than two, because the two halves must agree about which networks exist and
// where each Collector goes: an observer reporting a network the deployer is not converging, or a
// deployer placing a Collector in subnets the observer thinks are wrong, is drift with no cause anybody
// can find. It is also expensive to compute — a topology scan is DescribeVpcs, DescribeSubnets and
// DescribeRouteTables per region — and doing it per network would multiply that by the fleet.
//
// # The pass boundary
//
// CollectorPlan resolves; Plan serves what was last resolved. That split is what gives the deployer a
// per-pass boundary it has no other way to see: `api.CollectorDeployer` is called once per network with
// nothing marking the start of a pass, and the coordinator always builds its target list from
// `CollectorObserver.CollectorNetworks` — which calls CollectorPlan — before converging any of them.
// So the observer refreshes and the deployer reads, and Generation is the pass number.
//
// A deployer that finds NO plan has not been reached through a pass at all, and converging on a guess
// is the one thing it must not do: the plan is where the subnets come from, and a Collector placed in
// subnets nobody chose is one that may have no egress route to the destination.
type Resolver struct {
	// Kind is the collectors type these networks belong to (aws.fargate-collector).
	Kind string
	// Cache builds the per-region EC2 clients the topology scan uses.
	Cache awsclient.ConfigSource
	// Workloads is the discovered inventory, and Sources the customer's instrumentation intent. Sources
	// affect consumers and ingress only; Settings is the Collector lifecycle intent.
	Workloads WorkloadReader
	Sources   SourceReader
	// Collectors is what this connector has already recorded, which is the only way to notice a Collector
	// whose regional lifecycle configuration was disabled or removed — see Plan.Idle.
	Collectors CollectorReader
	// Settings reads the live tree, so an `updateConfig` patch changes placement on the next pass.
	Settings func() collectorsettings.Settings

	mu         sync.Mutex
	plan       *Plan
	generation uint64
}

// WorkloadReader is the discovered inventory placement reads. Both kinds, because both can be routed
// through a Collector and both carry the security groups the ingress tier is built from.
type WorkloadReader interface {
	ListECSServices(ctx context.Context) ([]awsstore.ECSService, error)
	ListLambdaFunctions(ctx context.Context) ([]awsstore.LambdaFunction, error)
}

// SourceReader is the customer's instrumentation intent. Only an ENABLED source's workload is a
// Collector consumer and contributes ingress; sources never create Collector infrastructure.
type SourceReader interface {
	ListSources(ctx context.Context) ([]basestore.SourceRecord, error)
}

// CollectorReader is what this connector already recorded.
type CollectorReader interface {
	ListManagedCollectors(ctx context.Context) ([]basestore.CollectorRecord, error)
}

// CollectorPlan resolves the plan for a new pass.
func (r *Resolver) CollectorPlan(ctx context.Context) (Plan, error) {
	configured := r.Settings()
	selected := make([]string, 0, len(configured.Regions))
	for region := range configured.Regions {
		selected = append(selected, region)
	}
	sort.Strings(selected)
	topology, err := Discover(ctx, r.Cache, selected)
	if err != nil {
		return Plan{}, fmt.Errorf("discovering the account's network topology: %w", err)
	}
	services, err := r.Workloads.ListECSServices(ctx)
	if err != nil {
		return Plan{}, fmt.Errorf("listing discovered ECS services: %w", err)
	}
	functions, err := r.Workloads.ListLambdaFunctions(ctx)
	if err != nil {
		return Plan{}, fmt.Errorf("listing discovered Lambda functions: %w", err)
	}
	sources, err := r.Sources.ListSources(ctx)
	if err != nil {
		return Plan{}, fmt.Errorf("listing the connector's sources: %w", err)
	}

	inventory := Inventory{ECSServices: services, Lambdas: functions, Sources: sources}
	plan := Resolve(Input{
		Kind:      r.Kind,
		Inventory: inventory,
		Topology:  topology,
		Settings:  configured,
	})
	plan.Inventory = inventory
	plan.Topology = topology
	plan.Idle = r.idleNetworks(ctx, plan)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.plan = &plan
	r.generation++
	return plan, nil
}

// Plan returns the plan the last CollectorPlan resolved, and the pass it belongs to.
//
// Never resolves. A caller that gets false has been reached outside a pass and must do nothing — see
// the type comment.
func (r *Resolver) Plan() (Plan, uint64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.plan == nil {
		return Plan{}, 0, false
	}
	return *r.plan, r.generation, true
}

// RefreshSecurityGroups reads back the part of the topology that Collector
// convergence changes. Routes and placement remain those of the current pass;
// only AWS-observed ingress can make a workload eligible for instrumentation.
func (r *Resolver) RefreshSecurityGroups(ctx context.Context) error {
	return r.refreshSecurityGroups(ctx, func(ctx context.Context, region string) ([]SecurityGroup, error) {
		cfg, err := r.Cache.Config(ctx, region)
		if err != nil {
			return nil, err
		}
		return describeSecurityGroups(ctx, ec2.NewFromConfig(cfg), region)
	})
}

func (r *Resolver) refreshSecurityGroups(ctx context.Context, read func(context.Context, string) ([]SecurityGroup, error)) error {
	plan, generation, ok := r.Plan()
	if !ok {
		return nil
	}
	groups := make(map[string]SecurityGroup)
	var errs []error
	for _, region := range plan.Topology.Regions() {
		regional, err := read(ctx, region)
		if err != nil {
			// Do not retain a previous allow rule when current admission cannot be
			// verified. Missing evidence keeps routing unknown until a later pass.
			errs = append(errs, fmt.Errorf("refresh security groups in %s: %w", region, err))
			continue
		}
		for _, group := range regional {
			groups[group.ID] = group
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.generation == generation {
		// Publish a new snapshot; observers may still be reading the old maps.
		updated := *r.plan
		updated.Topology.securityGroups = groups
		r.plan = &updated
	}
	return errors.Join(errs...)
}

// idleNetworks are the networks with a recorded Collector that this plan does not place one on.
//
// A read failure yields NONE, which is the safe direction: the retention policy is the only thing that
// acts on this list, and its actions are to park a Collector and to delete it. A failed read must never
// be able to produce either — so a connector that cannot see its own records converges what placement
// gave it and retires nothing.
func (r *Resolver) idleNetworks(ctx context.Context, plan Plan) []collectorplacement.NetworkRef {
	if r.Collectors == nil {
		return nil
	}
	recorded, err := r.Collectors.ListManagedCollectors(ctx)
	if err != nil {
		log.Warn("could not list recorded Collectors; no network is treated as idle this pass", "error", err)
		return nil
	}
	placed := map[string]bool{}
	for _, ref := range plan.Networks.CollectorNetworks() {
		placed[ref.Region] = true
	}

	var idle []collectorplacement.NetworkRef
	for _, rec := range recorded {
		ref := collectorplacement.NetworkRef{Region: rec.Key.Region, NetworkID: rec.NetworkID}
		if placed[ref.Region] {
			continue
		}
		idle = append(idle, ref)
	}
	sort.Slice(idle, func(i, j int) bool { return idle[i].Key() < idle[j].Key() })
	return idle
}

// IngressSources are the workloads routed to one network's Collector, with the security groups their
// telemetry originates from.
//
// The COMPLETE set for that network, because the AWS ingress resolver returns the complete desired rule
// set and the deployer converges to it: a source omitted here has its rule revoked rather than left
// alone, so a partial list silently stops a workload's telemetry being admitted.
//
// A workload whose security groups are empty arrives with an empty slice and is NOT dropped, which is
// the distinction ingress.Source exists to carry: "this sender has no group Odigos could
// resolve" is what holds the network-CIDR fallback back, and dropping it would read as "every sender
// resolved" and narrow the rules onto the ones that did.
func (p Plan) IngressSources(region, networkID string) []awsingress.Source {
	enabled := enabledExternalIDs(p.Inventory.Sources)

	var out []awsingress.Source
	for _, svc := range p.Inventory.ECSServices {
		ref := svc.ClusterARN + "/" + svc.ServiceName
		if svc.Region != region || !enabled[awsstore.KindECSTask][ref] {
			continue
		}
		vpc, ok := networkOfService(svc, p.Topology)
		if !ok {
			continue
		}
		if vpc != networkID {
			// The provider-neutral plan chooses one regional host VPC for every consumer VPC. A
			// consumer is admitted only when the AWS topology proves a direct same-region peering
			// path in both directions; otherwise it must keep its direct export target.
			if p.Networks.ServedBy(region, vpc) != networkID {
				continue
			}
			host, hostOK := p.Placement(region, networkID)
			if !hostOK || !p.Topology.CanReach(region, networkID, svc.Region, vpc, host.SubnetIDs, svc.SubnetIDs) {
				continue
			}
		}
		out = append(out, awsingress.Source{Name: ref, SecurityGroupIDs: svc.SecurityGroupIDs})
	}
	for _, fn := range p.Inventory.Lambdas {
		if fn.Region != region || fn.VPCID == "" || !enabled[awsstore.KindLambda][fn.FunctionARN] {
			continue
		}
		if fn.VPCID != networkID {
			// Lambda functions may use the same regional host through an explicitly validated
			// peering path, just like Fargate services. The ingress tier must not admit a
			// function merely because a marker exists; both placement ownership and the
			// selected subnet route proof are required.
			if p.Networks.ServedBy(region, fn.VPCID) != networkID {
				continue
			}
			host, hostOK := p.Placement(region, networkID)
			if !hostOK || !p.Topology.CanReach(region, networkID, fn.Region, fn.VPCID, host.SubnetIDs, fn.SubnetIDs) {
				continue
			}
		}
		out = append(out, awsingress.Source{Name: fn.FunctionARN, SecurityGroupIDs: fn.SecurityGroupIDs})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// NetworkBlocks are the VPC's own CIDR associations — the only ranges the fallback may name.
//
// From the topology rather than composed, and never a peered range, a gateway route or a block Odigos
// synthesised: the fallback admits a whole network, so the only defensible width is the one the network
// actually is.
func (p Plan) NetworkBlocks(networkID string) []string {
	vpc, ok := p.Topology.VPC(networkID)
	if !ok {
		return nil
	}
	return vpc.CIDRBlocks
}
