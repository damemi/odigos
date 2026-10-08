package awsprovider

import (
	"context"
	"fmt"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcutover"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/placement"
	collectorsettings "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/settings"
	awstypes "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/types"
	workloadstore "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
)

// fargateRouteResolver joins the provider-neutral destination decision to the AWS topology snapshot.
// It is deliberately owned by Provider: the base store knows destinations and Collector endpoints,
// placement knows VPC topology, and neither package should acquire the other's responsibility.
type fargateRouteResolver struct{ provider *Provider }

func (r fargateRouteResolver) ResolveFargateRoutes(ctx context.Context, services []workloadstore.ECSService) (map[string]domaindest.Routing, error) {
	out := make(map[string]domaindest.Routing, len(services))
	if len(services) == 0 {
		return out, nil
	}
	p := r.provider
	if p.bstore == nil {
		return nil, fmt.Errorf("regional Fargate routing requires the connector base store")
	}

	direct, err := p.bstore.OTLPRouting(ctx, "")
	if err != nil {
		return nil, err
	}
	if direct.Outcome == domaindest.RoutingError {
		for _, svc := range services {
			out[fargateRouteRef(svc)] = direct
		}
		return out, nil
	}
	destinations, err := p.bstore.ListDestinations(ctx)
	if err != nil {
		return nil, err
	}
	markedRegions := map[string]bool{}
	for _, dest := range destinations {
		if domaindest.IsManagedCollectorType(dest.Type) {
			markedRegions[dest.Region] = true
		}
	}
	needsRegionalTopology := false
	for _, svc := range services {
		if markedRegions[svc.Region] {
			needsRegionalTopology = true
			break
		}
	}
	if !needsRegionalTopology {
		for _, svc := range services {
			out[fargateRouteRef(svc)] = direct
		}
		return out, nil
	}

	// A provider without a managed-Collector surface cannot have a valid regional marker. Preserve the
	// direct path so local/test providers and connectors without the capability do not pay for topology.
	surface := p.collectors()
	if surface == nil {
		for _, svc := range services {
			out[fargateRouteRef(svc)] = direct
		}
		return out, nil
	}

	plan, _, havePlan := surface.resolver.Plan()
	if !havePlan {
		plan, err = surface.resolver.CollectorPlan(ctx)
		if err != nil {
			return nil, fmt.Errorf("resolve Collector topology for Fargate routing: %w", err)
		}
	}
	records, err := p.bstore.ListManagedCollectors(ctx)
	if err != nil {
		return nil, fmt.Errorf("list regional Collectors for Fargate routing: %w", err)
	}
	settings := collectorsettings.Read(p.cfgMgr.Effective())
	collectors := make(map[string]struct {
		network        string
		subnets        []string
		securityGroups []string
		ports          []int32
	}, len(records))
	for _, rec := range records {
		securityGroups := append([]string{}, settings.Region(rec.Key.Region).SecurityGroupIDs...)
		if rec.IngressRef != "" {
			securityGroups = append(securityGroups, rec.IngressRef)
		}
		ports := make([]int32, 0, 2)
		if rec.EndpointGRPC != "" {
			ports = append(ports, 4317)
		}
		if rec.EndpointHTTP != "" {
			ports = append(ports, 4318)
		}
		collectors[rec.Key.Region] = struct {
			network        string
			subnets        []string
			securityGroups []string
			ports          []int32
		}{network: rec.NetworkID, subnets: rec.PlacementRefs, securityGroups: securityGroups, ports: ports}
	}

	candidates := make([]fargateRouteCandidate, 0, len(services))
	for _, svc := range services {
		ref := fargateRouteRef(svc)
		network, ok := fargateServiceNetwork(svc, plan.Topology)
		reachability := domaindest.ReachabilityUnknown
		if ok {
			if collector, exists := collectors[svc.Region]; exists {
				// Placement is the authority for which consumer VPCs share this regional host. Peering
				// evidence alone cannot opt a VPC into a Collector the selected regional plan does not serve.
				if plan.Networks.ServedBy(svc.Region, network) != collector.network {
					reachability = domaindest.ReachabilityUnreachable
				} else {
					switch plan.Topology.Reachability(svc.Region, collector.network, svc.Region, network, collector.subnets, svc.SubnetIDs) {
					case placement.ReachabilityReachable:
						// Network reachability is not enough: the Collector's actual security group must
						// already admit at least one of this service's ENI groups on an OTLP port. This
						// makes a failed/lagging ingress converge a workload issue and direct fallback,
						// rather than a task-definition rollout into a black hole.
						switch collectorIngressReachability(plan.Topology, svc.Region, collector.network,
							collector.securityGroups, network, svc.SecurityGroupIDs, collector.ports) {
						case domaindest.ReachabilityReachable:
							reachability = domaindest.ReachabilityReachable
						case domaindest.ReachabilityUnreachable:
							reachability = domaindest.ReachabilityUnreachable
						}
					case placement.ReachabilityUnreachable:
						reachability = domaindest.ReachabilityUnreachable
					}
				}
			}
		}
		routing, routeErr := p.bstore.OTLPRoutingForRegion(ctx, svc.Region, network, reachability)
		if routeErr != nil {
			return nil, fmt.Errorf("resolve route for %s: %w", ref, routeErr)
		}
		out[ref] = routing
		if routing.Via == domaindest.RouteCollector {
			candidates = append(candidates, fargateRouteCandidate{
				Region: svc.Region, Ref: ref, Instrumented: svc.Instrumented,
				RoutingVia: svc.RoutingVia, Failed: svc.InstrumentationError,
			})
		}
	}
	batchSizes := make(map[string]int, len(candidates))
	for _, candidate := range candidates {
		if _, ok := batchSizes[candidate.Region]; !ok {
			batchSizes[candidate.Region] = settings.Region(candidate.Region).Network.RolloutBatchSize
		}
	}
	return selectFargateRoutes(fargateRouteSelectionInput{
		Direct:     direct,
		Desired:    out,
		Candidates: candidates,
		BatchSizes: batchSizes,
	}), nil
}

func collectorIngressReachability(topology placement.Topology, region, collectorVPC string, collectorSecurityGroups []string,
	sourceVPC string, sourceSecurityGroups []string, ports []int32) domaindest.Reachability {
	unknown := false
	for _, collectorSecurityGroup := range collectorSecurityGroups {
		switch topology.CollectorIngressReachabilityForPorts(region, collectorVPC, collectorSecurityGroup, sourceVPC, sourceSecurityGroups, ports) {
		case placement.ReachabilityReachable:
			return domaindest.ReachabilityReachable
		case placement.ReachabilityUnknown:
			unknown = true
		}
	}
	if unknown || len(collectorSecurityGroups) == 0 {
		return domaindest.ReachabilityUnknown
	}
	return domaindest.ReachabilityUnreachable
}

// fargateRouteCandidate is the provider's deployed-state fact for one workload whose desired route is a
// Collector. The desired route has already been resolved from the regional marker, Collector record and
// AWS reachability evidence; this helper only controls rollout writes.
type fargateRouteCandidate struct {
	Region       string
	Ref          string
	Instrumented bool
	RoutingVia   string
	Failed       string
}

type fargateRouteSelectionInput struct {
	Direct     domaindest.Routing
	Desired    map[string]domaindest.Routing
	Candidates []fargateRouteCandidate
	BatchSizes map[string]int
}

// selectFargateRoutes applies readiness-gated, batched first cutover while preserving immediate direct
// rollback. It is pure so regional marker/target decisions and rollout behavior can be tested without a
// database or AWS clients. Current Collector health is intentionally not an input: only the persisted
// first-ready-derived desired route and deployed RoutingVia affect selection.
func selectFargateRoutes(in fargateRouteSelectionInput) map[string]domaindest.Routing {
	out := make(map[string]domaindest.Routing, len(in.Desired))
	for ref, routing := range in.Desired {
		out[ref] = routing
	}
	byRegion := map[string][]fargateRouteCandidate{}
	for _, candidate := range in.Candidates {
		if !candidate.Instrumented {
			continue
		}
		byRegion[candidate.Region] = append(byRegion[candidate.Region], candidate)
	}
	for region, candidates := range byRegion {
		facts := make([]collectorcutover.Workload, 0, len(candidates))
		for _, candidate := range candidates {
			facts = append(facts, collectorcutover.Workload{
				Ref: candidate.Ref, Routed: candidate.RoutingVia == string(domaindest.RouteCollector), Failed: candidate.Failed,
			})
		}
		decision := collectorcutover.Decide(collectorcutover.Input{
			Region: region, Enabled: true, Deployed: true, EverReady: true,
			Workloads: facts, BatchSize: in.BatchSizes[region],
		})
		admitted := make(map[string]bool, len(decision.Admit))
		for _, ref := range decision.Admit {
			admitted[ref] = true
		}
		for _, candidate := range candidates {
			if candidate.RoutingVia == string(domaindest.RouteCollector) || admitted[candidate.Ref] {
				continue
			}
			deferred := in.Direct
			deferred.Reason = domaindest.ReasonCollectorRolloutPending
			deferred.Note = deferred.Describe()
			out[candidate.Ref] = deferred
		}
	}
	return out
}

func fargateRouteRef(svc workloadstore.ECSService) string {
	return svc.ClusterARN + "/" + svc.ServiceName
}

func fargateServiceNetwork(svc workloadstore.ECSService, topology interface {
	VPCOfSubnet(string) (string, bool)
}) (string, bool) {
	for _, subnetID := range svc.SubnetIDs {
		if network, ok := topology.VPCOfSubnet(subnetID); ok {
			return network, true
		}
	}
	return "", false
}

// ResolveWorkloadRouting supplies the read-only instrumentation plan path with the same regional
// Fargate route used by automatic and manual workload reconciliation. Non-Fargate kinds are explicitly
// declined so Lambda and other workload plans continue using the connector-wide OTLP destination.
func (p *Provider) ResolveWorkloadRouting(ctx context.Context, w baseapi.DiscoveredWorkload) (domaindest.Routing, bool, error) {
	if w.Kind != awstypes.FargateTask {
		return domaindest.Routing{}, false, nil
	}
	if p.workloadStdb == nil {
		return domaindest.Routing{}, true, fmt.Errorf("resolve Fargate plan route: workload store is unavailable")
	}
	services, err := p.workloadStdb.ListECSServices(ctx)
	if err != nil {
		return domaindest.Routing{}, true, fmt.Errorf("list ECS services for Fargate plan route: %w", err)
	}
	for _, svc := range services {
		if svc.Region != w.Region {
			continue
		}
		if w.ExternalID != "" && fargateRouteRef(svc) != w.ExternalID {
			continue
		}
		if w.ExternalID == "" && svc.ServiceName != w.ServiceName {
			continue
		}
		routes, err := (fargateRouteResolver{provider: p}).ResolveFargateRoutes(ctx, []workloadstore.ECSService{svc})
		if err != nil {
			return domaindest.Routing{}, true, err
		}
		routing, ok := routes[fargateRouteRef(svc)]
		if !ok {
			return domaindest.Routing{}, true, fmt.Errorf("no route returned for Fargate service %s", fargateRouteRef(svc))
		}
		return routing, true, nil
	}
	return domaindest.Routing{}, true, fmt.Errorf("Fargate service %q was not found in %s", w.ServiceName, w.Region)
}
