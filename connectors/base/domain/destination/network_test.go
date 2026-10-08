package destination

import (
	"reflect"
	"strings"
	"testing"
)

const directGRPC = "otlp.vendor.example.com:4317"

func vendorDestination(signals ...string) Destination {
	if len(signals) == 0 {
		signals = []string{"TRACES", "METRICS", "LOGS"}
	}
	return Destination{
		Name: "vendor", Type: "otlp", Signals: signals,
		Config: map[string]any{
			"OTLP_GRPC_ENDPOINT": directGRPC,
			"OTLP_GRPC_HEADERS":  `[{"key":"authorization","value":"Bearer vendor-token"}]`,
		},
	}
}

func marker(region string) Destination {
	return Destination{Name: "regional", Type: CatalogTypeManagedCollector, Region: region, Signals: []string{"TRACES"}}
}

func regionalCollector(region, hostNetwork string) CollectorTarget {
	return CollectorTarget{
		Region: region, Network: hostNetwork,
		Endpoint: "collector." + region + ".internal:4317", Insecure: true, EverReady: true,
	}
}

func TestRegionalRoutingContract(t *testing.T) {
	const region = "eu-west-1"
	dests := []Destination{vendorDestination(), marker(region)}
	collector := regionalCollector(region, "vpc-host")

	tests := []struct {
		name         string
		destinations []Destination
		collectors   []CollectorTarget
		region       string
		network      string
		reachability Reachability
		wantOutcome  RoutingOutcome
		wantReason   Reason
		wantEndpoint string
	}{
		{"host VPC", dests, []CollectorTarget{collector}, region, "vpc-host", ReachabilityUnknown, RoutingCollector, ReasonRouted, collector.Endpoint},
		{"peered VPC", dests, []CollectorTarget{collector}, region, "vpc-app", ReachabilityReachable, RoutingCollector, ReasonRouted, collector.Endpoint},
		{"unproven VPC", dests, []CollectorTarget{collector}, region, "vpc-app", ReachabilityUnknown, RoutingDirect, ReasonCollectorUnreachable, directGRPC},
		{"non-VPC Lambda", dests, []CollectorTarget{collector}, region, "", ReachabilityUnreachable, RoutingDirect, ReasonNoNetwork, directGRPC},
		{"no regional marker", []Destination{vendorDestination()}, []CollectorTarget{collector}, region, "vpc-host", ReachabilityUnknown, RoutingDirect, ReasonRouted, directGRPC},
		{"marker for another region", dests, []CollectorTarget{collector}, "us-east-1", "vpc-host", ReachabilityUnknown, RoutingDirect, ReasonRouted, directGRPC},
		{"no Collector in region", dests, nil, region, "vpc-host", ReachabilityUnknown, RoutingDirect, ReasonNoCollectorForRegion, directGRPC},
		{"Collector has no ingress", dests, []CollectorTarget{{Region: region, Network: "vpc-host"}}, region, "vpc-host", ReachabilityUnknown, RoutingDirect, ReasonCollectorNotDeployed, directGRPC},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveRoutingForRegion(tt.destinations, tt.collectors, tt.region, tt.network, tt.reachability)
			if got.Outcome != tt.wantOutcome || got.Reason != tt.wantReason || got.Target.Endpoint != tt.wantEndpoint {
				t.Fatalf("routing = %+v, want outcome=%q reason=%q endpoint=%q", got, tt.wantOutcome, tt.wantReason, tt.wantEndpoint)
			}
			if got.Outcome == RoutingDirect && got.Reason != ReasonRouted && got.Note == "" {
				t.Fatal("fallback has no explanatory note")
			}
		})
	}
}

func TestNonVPCLambdaFallbackNote(t *testing.T) {
	got := ResolveRoutingForRegion(
		[]Destination{vendorDestination(), marker("eu-west-1")},
		[]CollectorTarget{regionalCollector("eu-west-1", "vpc-host")},
		"eu-west-1", "", ReachabilityUnreachable,
	)
	if !strings.Contains(got.Note, "Defaulting to the global OTLP destination") ||
		!strings.Contains(got.Note, "not attached to a VPC") {
		t.Fatalf("note = %q", got.Note)
	}
	if got.Headers["authorization"] != "Bearer vendor-token" {
		t.Fatalf("fallback lost terminal credential: %v", got.Headers)
	}
}

func TestNoGlobalTerminalIsAnError(t *testing.T) {
	got := ResolveRoutingForRegion([]Destination{marker("eu-west-1")}, nil, "eu-west-1", "vpc-host", ReachabilityUnknown)
	if got.Outcome != RoutingError || got.Found || got.Reason != ReasonNoDestination {
		t.Fatalf("routing = %+v, want no-applicable-destination error", got)
	}
	if !strings.Contains(got.Note, "No applicable destination") {
		t.Fatalf("note = %q", got.Note)
	}
}

func TestManagedCollectorMarkerIsNeverAnExporter(t *testing.T) {
	bogus := marker("eu-west-1")
	bogus.Config = map[string]any{"OTLP_GRPC_ENDPOINT": "must-not-export"}
	if _, found := ResolveOTLPTarget([]Destination{bogus}); found {
		t.Fatal("managed Collector marker resolved as a terminal exporter")
	}
}

func TestCollectorRouteKeepsSignalsButNotTerminalCredentials(t *testing.T) {
	got := ResolveRoutingForRegion(
		[]Destination{vendorDestination("TRACES"), marker("eu-west-1")},
		[]CollectorTarget{regionalCollector("eu-west-1", "vpc-host")},
		"eu-west-1", "vpc-host", ReachabilityUnknown,
	)
	if !got.Target.Traces || got.Target.Metrics || got.Target.Logs {
		t.Fatalf("signals = %+v", got.Target)
	}
	if len(got.Headers) != 0 {
		t.Fatalf("Collector route leaked terminal headers: %v", got.Headers)
	}
}

func TestNonRegionalCompatibilityPathUsesOnlyGlobalTerminal(t *testing.T) {
	dests := []Destination{vendorDestination(), marker("eu-west-1")}
	got := ResolveRouting(dests, []CollectorTarget{regionalCollector("eu-west-1", "vpc-host")}, "vpc-host")
	if got.Outcome != RoutingDirect || got.Target.Endpoint != directGRPC {
		t.Fatalf("routing = %+v, want global direct route", got)
	}
	target, found := ResolveOTLPTargetForNetwork(dests, nil, "vpc-host")
	if !found || target != got.Target {
		t.Fatalf("target wrapper = %+v/%v, routing = %+v", target, found, got.Target)
	}
}

func TestCollectorTargetCannotExpressHealthOrSharedGraph(t *testing.T) {
	want := map[string]bool{
		"Region": true, "Network": true, "Endpoint": true,
		"HTTPEndpoint": true, "Insecure": true, "EverReady": true,
	}
	typ := reflect.TypeOf(CollectorTarget{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if !want[name] {
			t.Errorf("CollectorTarget gained field %q. Selection may depend on configuration and on "+
				"LATCHED facts only; a field that can express whether a Collector is currently "+
				"answering will flap every workload on the network on a blip", name)
		}
		delete(want, name)
	}
	if len(want) != 0 || typ.NumField() != 6 {
		t.Fatalf("CollectorTarget fields changed; missing=%v fieldCount=%d", want, typ.NumField())
	}
}

// TestAnUnreadyCollectorIsNotRoutedTo is the cutover gate, and it lives in the resolver rather
// than in each instrumentor for the reason the design gives: every instrumentor, observer and planner
// resolves through this function, so a gate here cannot be forgotten by one of them — and one of them
// forgetting it is a fleet pointed at a hole.
func TestAnUnreadyCollectorIsNotRoutedTo(t *testing.T) {
	const region = "eu-west-1"
	dests := []Destination{vendorDestination(), marker(region)}
	target := regionalCollector(region, "vpc-host")
	target.EverReady = false

	got := ResolveRoutingForRegion(dests, []CollectorTarget{target}, region, "vpc-host", ReachabilityUnknown)

	if got.Via != RouteDirect {
		t.Fatalf("Via = %q, want direct until the Collector has been ready once", got.Via)
	}
	if got.Reason != ReasonCollectorNotReady {
		t.Errorf("Reason = %q, want %q", got.Reason, ReasonCollectorNotReady)
	}
	// The telemetry still goes somewhere, and it goes where it went before any Collector existed.
	direct, _ := ResolveOTLPTarget(dests)
	if got.Target.Endpoint != direct.Endpoint {
		t.Errorf("endpoint = %q, want the destination's own %q", got.Target.Endpoint, direct.Endpoint)
	}
	// And it carries the destination's credential, because it is exporting directly.
	if len(got.Headers) == 0 {
		t.Error("a gated workload got no headers, so it would export to the destination unauthenticated")
	}
}

// TestNotDeployedAndNotReadyAreDistinct. "It was never built" and "it was built and has never worked"
// send whoever reads the report to different places — a placement or quota problem versus a Collector
// that is running and failing.
func TestNotDeployedAndNotReadyAreDistinct(t *testing.T) {
	const region = "eu-west-1"
	dests := []Destination{vendorDestination(), marker(region)}

	notDeployed := regionalCollector(region, "vpc-host")
	notDeployed.Endpoint, notDeployed.HTTPEndpoint, notDeployed.EverReady = "", "", false
	notDeployedRouting := ResolveRoutingForRegion(dests, []CollectorTarget{notDeployed}, region, "vpc-host", ReachabilityUnknown)
	if notDeployedRouting.Reason != ReasonCollectorNotDeployed {
		t.Errorf("Reason = %q, want %q", notDeployedRouting.Reason, ReasonCollectorNotDeployed)
	}

	notReady := regionalCollector(region, "vpc-host")
	notReady.EverReady = false
	notReadyRouting := ResolveRoutingForRegion(dests, []CollectorTarget{notReady}, region, "vpc-host", ReachabilityUnknown)
	if notReadyRouting.Reason != ReasonCollectorNotReady {
		t.Errorf("Reason = %q, want %q", notReadyRouting.Reason, ReasonCollectorNotReady)
	}
}

// TestReadinessIsALatchNotAHealthCheck. Once it is true the workload is routed and stays routed; nothing
// in this package can express "currently unhealthy", which is what stops a health flap from rewriting
// every task definition on a network twice.
func TestReadinessIsALatchNotAHealthCheck(t *testing.T) {
	const region = "eu-west-1"
	dests := []Destination{vendorDestination(), marker(region)}
	target := regionalCollector(region, "vpc-host")

	if got := ResolveRoutingForRegion(dests, []CollectorTarget{target}, region, "vpc-host", ReachabilityUnknown); got.Via != RouteCollector {
		t.Fatalf("Via = %q, want collector once it has been ready", got.Via)
	}
	// There is no field to set that would take it back. The field-coverage test above is what enforces
	// that; this asserts the consequence, which is that resolution is a pure function of configuration
	// plus one monotonic bit.
	again := ResolveRoutingForRegion(dests, []CollectorTarget{target}, region, "vpc-host", ReachabilityUnknown)
	if again.Via != RouteCollector || again.Target.Endpoint != target.Endpoint {
		t.Errorf("a second resolution of the same state differed: %+v", again)
	}
}
