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
		Endpoint: "collector." + region + ".internal:4317", Insecure: true,
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
		"HTTPEndpoint": true, "Insecure": true,
	}
	typ := reflect.TypeOf(CollectorTarget{})
	for i := 0; i < typ.NumField(); i++ {
		delete(want, typ.Field(i).Name)
	}
	if len(want) != 0 || typ.NumField() != 5 {
		t.Fatalf("CollectorTarget fields changed; missing=%v fieldCount=%d", want, typ.NumField())
	}
}
