package collectorplacement

import (
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
)

func routingDestinations() []destination.Destination {
	return []destination.Destination{
		{
			Name: "vendor", Type: "otlp",
			Config:  map[string]any{"OTLP_GRPC_ENDPOINT": "otlp.vendor.example.com:4317"},
			Signals: []string{"TRACES"},
		},
		{
			Name: "regional", Type: destination.CatalogTypeManagedCollector, Region: region,
			Signals: []string{"TRACES"},
		},
	}
}

func endpointsFor(plan Plan) map[string]Endpoint {
	out := map[string]Endpoint{}
	for _, host := range plan.CollectorNetworks() {
		out[host.Key()] = Endpoint{
			GRPC:     "odigos-collector-" + host.NetworkID + ".internal:4317",
			Insecure: true,
		}
	}
	return out
}

func TestRoutingTargetsContainOneRegionalCollector(t *testing.T) {
	plan := Resolve(Input{
		Networks: refs("vpc-host", "vpc-app"),
		Sources:  []Source{source("svc", "vpc-app")},
		Settings: regionalSettings("vpc-host"),
	})
	targets := RoutingTargets(plan, endpointsFor(plan))
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want one regional Collector", len(targets))
	}
	if targets[0].Region != region || targets[0].Network != "vpc-host" {
		t.Fatalf("target = %+v, want region %s hosted in vpc-host", targets[0], region)
	}
}

func TestRegionalRoutingUsesHostNetworkWithoutExtraEvidence(t *testing.T) {
	plan := Resolve(Input{
		Networks: refs("vpc-host"), Sources: []Source{source("svc", "vpc-host")}, Settings: regionalSettings("vpc-host"),
	})
	routing := destination.ResolveRoutingForRegion(
		routingDestinations(), RoutingTargets(plan, endpointsFor(plan)), region, "vpc-host", destination.ReachabilityUnknown,
	)
	if routing.Outcome != destination.RoutingCollector || routing.Network != "vpc-host" {
		t.Fatalf("routing = %+v, want the regional Collector", routing)
	}
}

func TestRegionalRoutingRequiresEvidenceAcrossVPCs(t *testing.T) {
	plan := Resolve(Input{
		Networks: refs("vpc-host", "vpc-app"),
		Sources:  []Source{source("svc", "vpc-app")},
		Settings: regionalSettings("vpc-host"),
	})
	targets := RoutingTargets(plan, endpointsFor(plan))

	unknown := destination.ResolveRoutingForRegion(routingDestinations(), targets, region, "vpc-app", destination.ReachabilityUnknown)
	if unknown.Outcome != destination.RoutingDirect || unknown.Reason != destination.ReasonCollectorUnreachable {
		t.Fatalf("unproven route = %+v, want global OTLP fallback", unknown)
	}

	reachable := destination.ResolveRoutingForRegion(
		routingDestinations(), targets, region, "vpc-app", destination.ReachabilityReachable,
	)
	if reachable.Outcome != destination.RoutingCollector || reachable.Network != "vpc-host" {
		t.Fatalf("validated route = %+v, want host Collector", reachable)
	}
}

func TestRegionalRoutingFallsBackUntilEndpointExists(t *testing.T) {
	plan := Resolve(Input{
		Networks: refs("vpc-host"), Sources: []Source{source("svc", "vpc-host")}, Settings: regionalSettings("vpc-host"),
	})
	routing := destination.ResolveRoutingForRegion(
		routingDestinations(), RoutingTargets(plan, nil), region, "vpc-host", destination.ReachabilityUnknown,
	)
	if routing.Outcome != destination.RoutingDirect || routing.Reason != destination.ReasonCollectorNotDeployed {
		t.Fatalf("routing = %+v, want direct fallback while Collector has no ingress", routing)
	}
}
