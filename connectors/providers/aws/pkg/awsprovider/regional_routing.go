package awsprovider

import (
	"context"
	"fmt"
	"time"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcutover"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/placement"
	collectorsettings "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/settings"
	awstypes "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/types"
	workloadstore "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
)

// regionalRouteResolver joins the provider-neutral destination decision to the AWS topology snapshot.
// It is deliberately owned by Provider: the base store knows destinations and Collector endpoints,
// placement knows VPC topology, and neither package should acquire the other's responsibility.
type regionalRouteResolver struct{ provider *Provider }

func (r regionalRouteResolver) ResolveFargateRoutes(ctx context.Context, services []workloadstore.ECSService) (map[string]domaindest.Routing, error) {
	return r.resolveFargateRoutes(ctx, services, true)
}

func (r regionalRouteResolver) resolveFargateRoutes(ctx context.Context, services []workloadstore.ECSService, recordAnnouncement bool) (map[string]domaindest.Routing, error) {
	workloads := make([]networkedWorkload, 0, len(services))
	for _, svc := range services {
		workloads = append(workloads, fargateNetworkedWorkload(svc))
	}
	return r.resolveRoutes(ctx, workloads, "Fargate", recordAnnouncement)
}

// fargateNetworkedWorkload is the service as the route resolver sees it. Its failure is the CUTOVER failure,
// not the instrumentation error: that one is cleared by a pass that held the service back from the rollout,
// and it also carries errors that have nothing to do with the Collector — see workloadstore.CutoverFailure.
func fargateNetworkedWorkload(svc workloadstore.ECSService) networkedWorkload {
	return networkedWorkload{Region: svc.Region, Ref: fargateRouteRef(svc),
		Subnets: svc.SubnetIDs, SecurityGroups: svc.SecurityGroupIDs, Instrumented: svc.Instrumented,
		RoutingVia: svc.RoutingVia, Failed: svc.CutoverFailure.Reason, FailedAt: svc.CutoverFailure.At}
}

// ResolveLambdaRoutes resolves all discovered Lambda functions using the same regional Collector route
// and rollout gate as Fargate. A Lambda with no VPC is deliberately represented with an empty network;
// the destination resolver then keeps it on the global OTLP terminal and explains why.
func (r regionalRouteResolver) ResolveLambdaRoutes(ctx context.Context, functions []workloadstore.LambdaFunction) (map[string]domaindest.Routing, error) {
	return r.resolveLambdaRoutes(ctx, functions, true)
}

func (r regionalRouteResolver) resolveLambdaRoutes(ctx context.Context, functions []workloadstore.LambdaFunction, recordAnnouncement bool) (map[string]domaindest.Routing, error) {
	workloads := make([]networkedWorkload, 0, len(functions))
	for _, fn := range functions {
		workloads = append(workloads, lambdaNetworkedWorkload(fn))
	}
	return r.resolveRoutes(ctx, workloads, "Lambda", recordAnnouncement)
}

// lambdaNetworkedWorkload is the function as the route resolver sees it, cutover failure included — without
// it a function that keeps failing is retried as never attempted, ahead of every function that sorts after it.
func lambdaNetworkedWorkload(fn workloadstore.LambdaFunction) networkedWorkload {
	return networkedWorkload{Region: fn.Region, Ref: fn.FunctionARN, Network: fn.VPCID,
		Subnets: fn.SubnetIDs, SecurityGroups: fn.SecurityGroupIDs, Instrumented: fn.Instrumented,
		RoutingVia: fn.RoutingVia, Failed: fn.CutoverFailure.Reason, FailedAt: fn.CutoverFailure.At}
}

type networkedWorkload struct {
	Region, Ref, Network    string
	Subnets, SecurityGroups []string
	Instrumented            bool
	RoutingVia, Failed      string
	FailedAt                time.Time
}

type collectorRouteTarget struct {
	network                 string
	subnets, securityGroups []string
	ports                   []int32
}

func (r regionalRouteResolver) resolveRoutes(ctx context.Context, workloads []networkedWorkload, kind string, recordAnnouncement bool) (map[string]domaindest.Routing, error) {
	out := make(map[string]domaindest.Routing, len(workloads))
	if len(workloads) == 0 {
		return out, nil
	}
	p := r.provider
	if p.bstore == nil {
		return nil, fmt.Errorf("regional %s routing requires the connector base store", kind)
	}
	direct, err := p.bstore.OTLPRouting(ctx, "")
	if err != nil {
		return nil, err
	}
	destinations, err := p.bstore.ListDestinations(ctx)
	if err != nil {
		return nil, err
	}
	markedRegions := map[string]bool{}
	markerUpdatedAt := map[string]time.Time{}
	for _, dest := range destinations {
		if !dest.Disabled && domaindest.IsManagedCollectorType(dest.Type) {
			markedRegions[dest.Region] = true
			markerUpdatedAt[dest.Region] = dest.UpdatedAt
		}
	}
	needsRegionalTopology := false
	for _, w := range workloads {
		if markedRegions[w.Region] {
			needsRegionalTopology = true
			break
		}
	}
	if !needsRegionalTopology {
		for _, w := range workloads {
			out[w.Ref] = direct
		}
		return out, nil
	}
	surface := p.collectors()
	if surface == nil {
		for _, w := range workloads {
			out[w.Ref] = direct
		}
		return out, nil
	}
	plan, _, havePlan := surface.resolver.Plan()
	if !havePlan {
		plan, err = surface.resolver.CollectorPlan(ctx)
		if err != nil {
			return nil, fmt.Errorf("resolve Collector topology for %s routing: %w", kind, err)
		}
	}
	records, err := p.bstore.ListManagedCollectors(ctx)
	if err != nil {
		return nil, fmt.Errorf("list regional Collectors for %s routing: %w", kind, err)
	}
	settings := collectorsettings.Read(p.cfgMgr.Effective())
	collectors := make(map[string]collectorRouteTarget, len(records))
	firstReadyAt := make(map[string]*time.Time, len(records))
	for _, rec := range records {
		firstReadyAt[rec.Key.Region] = rec.FirstReadyAt
		groups := append([]string{}, settings.Region(rec.Key.Region).SecurityGroupIDs...)
		if rec.IngressRef != "" {
			groups = append(groups, rec.IngressRef)
		}
		ports := make([]int32, 0, 2)
		if rec.EndpointGRPC != "" {
			ports = append(ports, 4317)
		}
		if rec.EndpointHTTP != "" {
			ports = append(ports, 4318)
		}
		collectors[rec.Key.Region] = collectorRouteTarget{network: rec.NetworkID, subnets: rec.PlacementRefs, securityGroups: groups, ports: ports}
	}
	candidates := make([]collectorRouteCandidate, 0, len(workloads))
	for _, w := range workloads {
		network := w.Network
		if network == "" && len(w.Subnets) > 0 {
			network, _ = fargateServiceNetwork(workloadstore.ECSService{SubnetIDs: w.Subnets}, plan.Topology)
		}
		reachability := domaindest.ReachabilityUnknown
		if network != "" {
			if collector, exists := collectors[w.Region]; exists {
				if plan.Networks.ServedBy(w.Region, network) == collector.network {
					switch plan.Topology.Reachability(w.Region, collector.network, w.Region, network, collector.subnets, w.Subnets) {
					case placement.ReachabilityReachable:
						// VPC routing is accepted only after the actual Collector SG admits this
						// workload's ENI SG or every selected subnet CIDR on each published OTLP port.
						reachability = collectorIngressReachability(plan.Topology, w.Region, collector.network, collector.securityGroups, network, w.Subnets, w.SecurityGroups, collector.ports)
					case placement.ReachabilityUnreachable:
						reachability = domaindest.ReachabilityUnreachable
					}
				} else {
					reachability = domaindest.ReachabilityUnreachable
				}
			}
		}
		routing, routeErr := p.bstore.OTLPRoutingForRegion(ctx, w.Region, network, reachability)
		if routeErr != nil {
			return nil, fmt.Errorf("resolve route for %s: %w", w.Ref, routeErr)
		}
		if routing.Reason == domaindest.ReasonCollectorUnreachable {
			if collector, exists := collectors[w.Region]; exists {
				routing.Note = unreachableCollectorNote(routing, network, collector.network)
			}
		}
		if kind == "Lambda" {
			routing = lambdaRoutingNote(routing)
		}
		out[w.Ref] = routing
		if routing.Via == domaindest.RouteCollector {
			candidates = append(candidates, collectorRouteCandidate{Region: w.Region, Ref: w.Ref, Instrumented: w.Instrumented,
				RoutingVia: w.RoutingVia, Failed: w.Failed, FailedAt: w.FailedAt})
		}
	}
	batchSizes := make(map[string]int, len(candidates))
	batchBlocked := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		if !c.Instrumented {
			continue
		}
		if _, ok := batchSizes[c.Region]; !ok {
			quota := p.collectorBatchQuota(ctx, kind, settings.Region(c.Region).Network.RolloutBatchSize)
			batchSizes[c.Region] = quota
			batchBlocked[c.Region] = quota == 0
		}
	}
	announcing := make(map[string]bool, len(batchSizes))
	if direct.Found {
		for region := range batchSizes {
			readyAt := firstReadyAt[region]
			if readyAt == nil {
				continue
			}
			if recordAnnouncement {
				announcing[region], err = p.bstore.EnsureCollectorCutoverAnnouncement(ctx, region, markerUpdatedAt[region], *readyAt)
				if err != nil {
					return nil, fmt.Errorf("announce Collector cutover in %s: %w", region, err)
				}
			} else {
				announcement, readErr := p.bstore.GetCollectorCutoverAnnouncement(ctx, region)
				if readErr != nil {
					return nil, fmt.Errorf("read Collector cutover announcement in %s: %w", region, readErr)
				}
				if announcement != nil && announcement.Matches(markerUpdatedAt[region], *readyAt) {
					announcing[region] = announcement.Announcing
				} else {
					// A plan query must not write state. Show the route that the first
					// actual reconcile pass will use: direct during its warning window.
					announcing[region] = true
				}
			}
		}
	}
	return selectCollectorRoutes(collectorRouteSelectionInput{Direct: direct, Desired: out, Candidates: candidates,
		BatchSizes: batchSizes, BatchBlocked: batchBlocked, Announcing: announcing}), nil
}

// unreachableCollectorNote gives the workload's network and the selected Collector host network to the
// operator. ECS clusters can span VPCs, so a same-cluster workload still needs an actual network path.
func unreachableCollectorNote(routing domaindest.Routing, workloadVPC, collectorVPC string) string {
	if workloadVPC == "" || collectorVPC == "" {
		return routing.Note
	}
	if workloadVPC != collectorVPC {
		return fmt.Sprintf("Workload VPC %s cannot reach the regional Collector in VPC %s. Configure active same-region VPC peering and reciprocal routes for the workload and Collector subnets, or place the Collector in the workload VPC. %s",
			workloadVPC, collectorVPC, routing.Note)
	}
	return fmt.Sprintf("Workload VPC %s shares the Collector VPC, but the Collector ingress security group does not admit this workload on every published OTLP port. %s", workloadVPC, routing.Note)
}

// lambdaRoutingNote adds the AWS-specific remediation to the provider-neutral route reason. It is kept
// at the provider edge because VpcConfig, peering, subnet routes, security groups and NAT are AWS facts;
// the shared destination domain must remain cloud-neutral.
func lambdaRoutingNote(r domaindest.Routing) domaindest.Routing {
	if r.Outcome != domaindest.RoutingDirect {
		return r
	}
	switch r.Reason {
	case domaindest.ReasonNoNetwork:
		r.Note = "Defaulting to the global OTLP destination: this Lambda has no VPC configuration, so it cannot reach the Odigos Managed Regional Collector. Odigos will not attach the function to a VPC or change its VpcConfig."
	case domaindest.ReasonCollectorUnreachable:
		r.Note = "Defaulting to the global OTLP destination: this VPC-attached Lambda cannot currently reach the Odigos Managed Regional Collector. Remediate same-region VPC peering, reciprocal routes from every selected subnet, Collector security-group ingress on the OTLP ports, and NAT or other outbound access for the global OTLP fallback."
	}
	return r
}

func collectorIngressReachability(topology placement.Topology, region, collectorVPC string, collectorSecurityGroups []string,
	sourceVPC string, sourceSubnetIDs, sourceSecurityGroups []string, ports []int32) domaindest.Reachability {
	unknown := false
	for _, collectorSecurityGroup := range collectorSecurityGroups {
		switch topology.CollectorIngressReachabilityForWorkload(region, collectorVPC, collectorSecurityGroup, sourceVPC, sourceSubnetIDs, sourceSecurityGroups, ports) {
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

// collectorRouteCandidate is the provider's deployed-state fact for one workload whose desired route is a
// Collector. The desired route has already been resolved from the regional marker, Collector record and
// AWS reachability evidence; this helper only controls rollout writes.
type collectorRouteCandidate struct {
	Region       string
	Ref          string
	Instrumented bool
	RoutingVia   string
	Failed       string
	FailedAt     time.Time
}

type collectorRouteSelectionInput struct {
	Direct       domaindest.Routing
	Desired      map[string]domaindest.Routing
	Candidates   []collectorRouteCandidate
	BatchSizes   map[string]int
	BatchBlocked map[string]bool
	Announcing   map[string]bool
}

// selectCollectorRoutes applies readiness-gated, batched first cutover while preserving immediate direct
// rollback. It is pure so regional marker/target decisions and rollout behavior can be tested without a
// database or AWS clients. Current Collector health is intentionally not an input: only the persisted
// first-ready-derived desired route and deployed RoutingVia affect selection.
func selectCollectorRoutes(in collectorRouteSelectionInput) map[string]domaindest.Routing {
	out := make(map[string]domaindest.Routing, len(in.Desired))
	for ref, routing := range in.Desired {
		out[ref] = routing
	}
	byRegion := map[string][]collectorRouteCandidate{}
	for _, candidate := range in.Candidates {
		if !candidate.Instrumented {
			continue
		}
		byRegion[candidate.Region] = append(byRegion[candidate.Region], candidate)
	}
	// A ready, reachable regional Collector is a valid route even without a global
	// terminal. In that case there is no direct route for an announcement window or
	// a batched rollout, so use the resolved Collector route immediately. Workloads
	// whose Collector route is unavailable retain their routing error.
	if !in.Direct.Found {
		return out
	}
	for region, candidates := range byRegion {
		facts := make([]collectorcutover.Workload, 0, len(candidates))
		for _, candidate := range candidates {
			facts = append(facts, collectorcutover.Workload{
				Ref: candidate.Ref, Routed: candidate.RoutingVia == string(domaindest.RouteCollector), Failed: candidate.Failed,
				FailedAt: candidate.FailedAt,
			})
		}
		announcing := in.Announcing[region]
		decision := collectorcutover.Decision{}
		if !in.BatchBlocked[region] {
			decision = collectorcutover.Decide(collectorcutover.Input{
				Region: region, Enabled: true, Deployed: true, EverReady: !announcing, ReadyNow: true,
				Workloads: facts, BatchSize: in.BatchSizes[region],
			})
		}
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

// ResolveWorkloadRouting supplies the read-only instrumentation plan path with the same regional route
// used by automatic and manual workload reconciliation. AWS workload kinds are resolved here so a plan
// shown before apply names the same endpoint that reconcile will apply.
func (p *Provider) ResolveWorkloadRouting(ctx context.Context, w baseapi.DiscoveredWorkload) (domaindest.Routing, bool, error) {
	if w.Kind != awstypes.FargateTask && w.Kind != awstypes.Lambda {
		return domaindest.Routing{}, false, nil
	}
	if p.workloadStdb == nil {
		return domaindest.Routing{}, true, fmt.Errorf("resolve AWS workload plan route: workload store is unavailable")
	}
	resolver := regionalRouteResolver{provider: p}
	if w.Kind == awstypes.Lambda {
		functions, err := p.workloadStdb.ListLambdaFunctions(ctx)
		if err != nil {
			return domaindest.Routing{}, true, fmt.Errorf("list Lambda functions for plan route: %w", err)
		}
		var target *workloadstore.LambdaFunction
		for _, fn := range functions {
			if fn.Region == w.Region && ((w.ExternalID != "" && fn.FunctionARN == w.ExternalID) || (w.ExternalID == "" && fn.FunctionName == w.ServiceName)) {
				copy := fn
				target = &copy
				break
			}
		}
		if target == nil {
			return domaindest.Routing{}, true, fmt.Errorf("Lambda function %q was not found in %s", w.ServiceName, w.Region)
		}
		routes, err := resolver.resolveLambdaRoutes(ctx, functions, false)
		if err != nil {
			return domaindest.Routing{}, true, err
		}
		routing, ok := routes[target.FunctionARN]
		if !ok {
			return domaindest.Routing{}, true, fmt.Errorf("no route returned for Lambda function %s", target.FunctionARN)
		}
		return routing, true, nil
	}
	services, err := p.workloadStdb.ListECSServices(ctx)
	if err != nil {
		return domaindest.Routing{}, true, fmt.Errorf("list ECS services for Fargate plan route: %w", err)
	}
	var target *workloadstore.ECSService
	for _, svc := range services {
		if svc.Region == w.Region && ((w.ExternalID != "" && fargateRouteRef(svc) == w.ExternalID) || (w.ExternalID == "" && svc.ServiceName == w.ServiceName)) {
			copy := svc
			target = &copy
			break
		}
	}
	if target == nil {
		return domaindest.Routing{}, true, fmt.Errorf("Fargate service %q was not found in %s", w.ServiceName, w.Region)
	}
	routes, err := resolver.resolveFargateRoutes(ctx, services, false)
	if err != nil {
		return domaindest.Routing{}, true, err
	}
	routing, ok := routes[fargateRouteRef(*target)]
	if !ok {
		return domaindest.Routing{}, true, fmt.Errorf("no route returned for Fargate service %s", fargateRouteRef(*target))
	}
	return routing, true, nil
}
