package awsprovider

import (
	"context"
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	awstypes "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/types"
)

func testDirectRouting(reason domaindest.Reason) domaindest.Routing {
	routing := domaindest.Routing{
		Target: domaindest.OTLPTarget{Endpoint: "otlp.example:4317", Traces: true},
		Found:  true, Via: domaindest.RouteDirect, Outcome: domaindest.RoutingDirect, Reason: reason,
	}
	routing.Note = routing.Describe()
	return routing
}

func testCollectorRouting(network string) domaindest.Routing {
	return domaindest.Routing{
		Target: domaindest.OTLPTarget{Endpoint: "collector.example:4317", Traces: true},
		Found:  true, Via: domaindest.RouteCollector, Network: network, Outcome: domaindest.RoutingCollector,
	}
}

func TestSelectFargateRoutesAdmitsReadyRegionalCollector(t *testing.T) {
	ref := "cluster/service"
	routes := selectFargateRoutes(fargateRouteSelectionInput{
		Direct:     testDirectRouting(domaindest.ReasonNoCollectorForRegion),
		Desired:    map[string]domaindest.Routing{ref: testCollectorRouting("vpc-host")},
		Candidates: []fargateRouteCandidate{{Region: "eu-west-1", Ref: ref, Instrumented: true}},
		BatchSizes: map[string]int{"eu-west-1": 1},
	})
	if got := routes[ref]; got.Via != domaindest.RouteCollector || got.Network != "vpc-host" {
		t.Fatalf("route = %+v, want Collector route through the ready regional target", got)
	}
}

func TestSelectFargateRoutesKeepsOtherRegionMarkerDirect(t *testing.T) {
	ref := "cluster/service"
	direct := testDirectRouting(domaindest.ReasonNoCollectorForRegion)
	routes := selectFargateRoutes(fargateRouteSelectionInput{Direct: direct, Desired: map[string]domaindest.Routing{ref: direct}})
	if got := routes[ref]; got.Via != domaindest.RouteDirect || got.Reason != domaindest.ReasonNoCollectorForRegion {
		t.Fatalf("route = %+v, want direct because the marker is not for this region", got)
	}
}

func TestSelectFargateRoutesKeepsUnreachableVPCDirectWithIssue(t *testing.T) {
	ref := "cluster/service"
	direct := testDirectRouting(domaindest.ReasonCollectorUnreachable)
	routes := selectFargateRoutes(fargateRouteSelectionInput{
		Direct: direct, Desired: map[string]domaindest.Routing{ref: direct},
	})
	if got := routes[ref]; got.Via != domaindest.RouteDirect || got.Reason != domaindest.ReasonCollectorUnreachable {
		t.Fatalf("route = %+v, want direct with the reachability issue preserved", got)
	}
}

func TestSelectFargateRoutesUsesDirectPeeringDesiredCollectorRoute(t *testing.T) {
	ref := "cluster/peered-service"
	routes := selectFargateRoutes(fargateRouteSelectionInput{
		Direct:     testDirectRouting(domaindest.ReasonCollectorUnreachable),
		Desired:    map[string]domaindest.Routing{ref: testCollectorRouting("vpc-host")},
		Candidates: []fargateRouteCandidate{{Region: "eu-west-1", Ref: ref, Instrumented: true}},
		BatchSizes: map[string]int{"eu-west-1": 1},
	})
	if got := routes[ref]; got.Via != domaindest.RouteCollector {
		t.Fatalf("route = %+v, want Collector route after direct peering reachability was proven", got)
	}
}

func TestSelectFargateRoutesHonorsBatchSizeAndCurrentRouting(t *testing.T) {
	direct := testDirectRouting("")
	desired := map[string]domaindest.Routing{
		"cluster/already": testCollectorRouting("vpc-host"),
		"cluster/first":   testCollectorRouting("vpc-host"),
		"cluster/second":  testCollectorRouting("vpc-host"),
	}
	routes := selectFargateRoutes(fargateRouteSelectionInput{
		Direct: direct, Desired: desired,
		Candidates: []fargateRouteCandidate{
			{Region: "eu-west-1", Ref: "cluster/already", Instrumented: true, RoutingVia: string(domaindest.RouteCollector)},
			{Region: "eu-west-1", Ref: "cluster/second", Instrumented: true},
			{Region: "eu-west-1", Ref: "cluster/first", Instrumented: true},
		},
		BatchSizes: map[string]int{"eu-west-1": 1},
	})
	if routes["cluster/already"].Via != domaindest.RouteCollector {
		t.Fatal("a currently Collector-routed service was changed")
	}
	if routes["cluster/first"].Via != domaindest.RouteCollector {
		t.Fatalf("first admitted service = %+v, want Collector", routes["cluster/first"])
	}
	if routes["cluster/second"].Via != domaindest.RouteDirect || routes["cluster/second"].Reason != domaindest.ReasonCollectorRolloutPending {
		t.Fatalf("second service = %+v, want direct rollout-pending route", routes["cluster/second"])
	}
}

func TestSelectFargateRoutesRestoresDirectWhenCollectorDesiredRouteDisappears(t *testing.T) {
	ref := "cluster/service"
	direct := testDirectRouting(domaindest.ReasonNoCollectorForRegion)
	routes := selectFargateRoutes(fargateRouteSelectionInput{
		Direct: direct, Desired: map[string]domaindest.Routing{ref: direct},
	})
	if got := routes[ref]; got.Via != domaindest.RouteDirect || got.Reason != domaindest.ReasonNoCollectorForRegion {
		t.Fatalf("route = %+v, want direct after marker/Collector removal", got)
	}
}

func TestSelectFargateRoutesDoesNotConsultCurrentCollectorHealth(t *testing.T) {
	ref := "cluster/service"
	routes := selectFargateRoutes(fargateRouteSelectionInput{
		Direct:     testDirectRouting(""),
		Desired:    map[string]domaindest.Routing{ref: testCollectorRouting("vpc-host")},
		Candidates: []fargateRouteCandidate{{Region: "eu-west-1", Ref: ref, Instrumented: true, RoutingVia: string(domaindest.RouteCollector), Failed: "Collector currently unhealthy"}},
		BatchSizes: map[string]int{"eu-west-1": 1},
	})
	if got := routes[ref]; got.Via != domaindest.RouteCollector {
		t.Fatalf("route = %+v, want existing Collector route retained despite transient health", got)
	}
}

func TestAWSProviderRoutingSeamDeclinesLambda(t *testing.T) {
	p := &Provider{}
	routing, handled, err := p.ResolveWorkloadRouting(context.Background(), api.DiscoveredWorkload{
		Kind: awstypes.Lambda, ServiceName: "function", Region: "eu-west-1",
	})
	if err != nil || handled || routing.Found {
		t.Fatalf("Lambda routing seam = (%+v, %v, %v), want the existing global-routing path", routing, handled, err)
	}
}
