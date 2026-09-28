package destination

import "strings"

// CollectorTarget is one regional managed-Collector routing entry. Region is its identity; Network is
// only the VPC/VNet hosting its ingress. It intentionally carries no health: routing changes when
// configuration changes, not during a transient Collector failure.
type CollectorTarget struct {
	Region       string
	Network      string
	Endpoint     string
	HTTPEndpoint string
	Insecure     bool
}

// Routable reports whether the regional Collector has a published ingress point.
func (c CollectorTarget) Routable() bool {
	return c.Endpoint != "" || c.HTTPEndpoint != ""
}

// Reachability is workload-specific provider evidence that a consumer network can reach the regional
// Collector's host network. Region equality is not evidence. On AWS, Reachable means direct peering (or
// an equivalent customer-owned path) and the relevant routes/ingress have been validated.
type Reachability string

const (
	ReachabilityUnknown     Reachability = "unknown"
	ReachabilityReachable   Reachability = "reachable"
	ReachabilityUnreachable Reachability = "unreachable"
)

type Route string

const (
	RouteDirect    Route = "direct"
	RouteCollector Route = "collector"
)

type RoutingOutcome string

const (
	RoutingDirect    RoutingOutcome = "direct"
	RoutingCollector RoutingOutcome = "collector"
	RoutingError     RoutingOutcome = "error"
	OutcomeDirect    RoutingOutcome = RoutingDirect
	OutcomeCollector RoutingOutcome = RoutingCollector
	OutcomeError     RoutingOutcome = RoutingError
)

type Reason string

const (
	ReasonRouted               Reason = ""
	ReasonNoDestination        Reason = "no-destination"
	ReasonNoNetwork            Reason = "no-network"
	ReasonNoCollectorForRegion Reason = "no-collector-for-region"
	ReasonCollectorNotDeployed Reason = "collector-not-deployed"
	ReasonCollectorUnreachable Reason = "collector-unreachable"
)

// Routing is the complete decision for one workload. Collector routes keep the global terminal's signal
// selection but intentionally omit its headers: the regional Collector, rather than every workload,
// holds the terminal credential.
type Routing struct {
	Target  OTLPTarget
	Headers map[string]string
	Found   bool
	Via     Route
	Network string
	Reason  Reason
	Outcome RoutingOutcome
	Note    string
}

// ResolveRouting is the compatibility path for callers that do not provide a region. Without regional
// context it cannot apply a regional marker safely, so it resolves only the global OTLP terminal.
func ResolveRouting(dests []Destination, _ []CollectorTarget, _ string) Routing {
	return directRouting(dests)
}

// ResolveRoutingForRegion decides where a workload exports. A regional Collector is selected only when
// all of these are true: a marker exists for the workload region, that region has one published
// Collector ingress, and the workload is either in the host network or the provider explicitly proves
// cross-network reachability. Otherwise the global OTLP terminal is used with an explanatory note.
func ResolveRoutingForRegion(dests []Destination, collectors []CollectorTarget, region, network string, reachability Reachability) Routing {
	direct := directRouting(dests)
	if direct.Outcome == RoutingError {
		return direct
	}
	if !hasManagedCollectorMarker(dests, region) {
		return direct
	}
	if strings.TrimSpace(network) == "" {
		return direct.withReason(ReasonNoNetwork)
	}

	collector, ok := findRegionalCollector(collectors, region)
	switch {
	case !ok:
		return direct.withReason(ReasonNoCollectorForRegion)
	case !collector.Routable():
		return direct.withReason(ReasonCollectorNotDeployed)
	case network != collector.Network && reachability != ReachabilityReachable:
		return direct.withReason(ReasonCollectorUnreachable)
	}

	return Routing{
		Target: OTLPTarget{
			Endpoint:     collector.Endpoint,
			HTTPEndpoint: collector.HTTPEndpoint,
			Insecure:     collector.Insecure,
			Traces:       direct.Target.Traces,
			Metrics:      direct.Target.Metrics,
			Logs:         direct.Target.Logs,
		},
		Found:   true,
		Via:     RouteCollector,
		Network: collector.Network,
		Outcome: RoutingCollector,
	}
}

func directRouting(dests []Destination) Routing {
	target, found := ResolveOTLPTarget(dests)
	if !found {
		r := Routing{Reason: ReasonNoDestination, Outcome: RoutingError}
		r.Note = r.Describe()
		return r
	}
	return Routing{
		Target:  target,
		Headers: ResolveOTLPHeaders(dests),
		Found:   true,
		Via:     RouteDirect,
		Outcome: RoutingDirect,
	}
}

func (r Routing) withReason(reason Reason) Routing {
	r.Reason = reason
	r.Note = r.Describe()
	return r
}

func hasManagedCollectorMarker(dests []Destination, region string) bool {
	region = strings.TrimSpace(region)
	if region == "" {
		return false
	}
	for _, d := range dests {
		if isManagedCollectorType(d.Type) && strings.TrimSpace(d.Region) == region {
			return true
		}
	}
	return false
}

func findRegionalCollector(collectors []CollectorTarget, region string) (CollectorTarget, bool) {
	for _, collector := range collectors {
		if collector.Region == region {
			return collector, true
		}
	}
	return CollectorTarget{}, false
}

func ResolveOTLPTargetForNetwork(dests []Destination, collectors []CollectorTarget, network string) (OTLPTarget, bool) {
	routing := ResolveRouting(dests, collectors, network)
	return routing.Target, routing.Found
}

func ResolveOTLPTargetForRegion(dests []Destination, collectors []CollectorTarget, region, network string, reachability Reachability) (OTLPTarget, bool) {
	routing := ResolveRoutingForRegion(dests, collectors, region, network, reachability)
	return routing.Target, routing.Found
}

func ResolveOTLPHeadersForNetwork(dests []Destination, collectors []CollectorTarget, network string) map[string]string {
	return ResolveRouting(dests, collectors, network).Headers
}

func ResolveOTLPHeadersForRegion(dests []Destination, collectors []CollectorTarget, region, network string, reachability Reachability) map[string]string {
	return ResolveRoutingForRegion(dests, collectors, region, network, reachability).Headers
}

// Describe renders the note used by workload reporting. Callers branch on Reason/Outcome, never text.
func (r Routing) Describe() string {
	if r.Via == RouteCollector {
		return "Exporting through the Odigos Managed Regional Collector hosted in network " + r.Network + "."
	}
	switch r.Reason {
	case ReasonNoDestination:
		return "No applicable destination: no global OTLP destination is configured, so this workload cannot export telemetry."
	case ReasonNoNetwork:
		return "Defaulting to the global OTLP destination: this workload is not attached to a VPC and cannot reach the Odigos Managed Regional Collector."
	case ReasonNoCollectorForRegion:
		return "Defaulting to the global OTLP destination: no Odigos Managed Regional Collector is set up in this region."
	case ReasonCollectorNotDeployed:
		return "Defaulting to the global OTLP destination: the Odigos Managed Regional Collector has no ingress endpoint yet."
	case ReasonCollectorUnreachable:
		return "Defaulting to the global OTLP destination: the Odigos Managed Regional Collector is not known to be reachable from this workload's network."
	default:
		return "Exporting directly to the global OTLP destination."
	}
}
