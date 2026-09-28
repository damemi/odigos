package collectorplacement

import (
	"reflect"
	"strings"
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorsettings"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
)

const region = "eu-west-1"

func refs(ids ...string) []NetworkRef {
	out := make([]NetworkRef, 0, len(ids))
	for _, id := range ids {
		out = append(out, NetworkRef{Region: region, NetworkID: id})
	}
	return out
}

func source(ref, network string) Source {
	return Source{Ref: ref, Region: region, NetworkID: network}
}

func settings() collectorsettings.Settings {
	return collectorsettings.Read(map[string]any{collectorsettings.Key: map[string]any{
		collectorsettings.KeyEnabled: true,
		collectorsettings.KeyRegions: map[string]any{region: map[string]any{collectorsettings.KeyEnabled: true}},
	}})
}

func regionalSettings(host string) collectorsettings.Settings {
	return collectorsettings.Read(map[string]any{collectorsettings.Key: map[string]any{
		collectorsettings.KeyEnabled: true,
		collectorsettings.KeyRegions: map[string]any{
			region: map[string]any{collectorsettings.KeyHostNetworkID: host},
		},
	}})
}

func networkByID(t *testing.T, plan Plan, id string) Network {
	t.Helper()
	for _, network := range plan.Networks {
		if network.NetworkID == id {
			return network
		}
	}
	t.Fatalf("network %s missing from plan", id)
	return Network{}
}

func collectorIDs(plan Plan) []string {
	out := make([]string, 0, len(plan.CollectorNetworks()))
	for _, network := range plan.CollectorNetworks() {
		out = append(out, network.NetworkID)
	}
	return out
}

func problemCodes(plan Plan) []string {
	out := make([]string, 0, len(plan.Problems))
	for _, problem := range plan.Problems {
		out = append(out, problem.Code)
	}
	return out
}

func TestSingleSourceNetworkBecomesRegionalHost(t *testing.T) {
	plan := Resolve(Input{
		Networks: refs("vpc-app", "vpc-unused"),
		Sources:  []Source{source("svc", "vpc-app")},
		Settings: settings(),
	})
	if got := collectorIDs(plan); !reflect.DeepEqual(got, []string{"vpc-app"}) {
		t.Fatalf("Collector hosts = %v", got)
	}
	network := networkByID(t, plan, "vpc-app")
	if network.Role != RoleCollector || network.CollectorRegion != region || network.ServedBy != "vpc-app" {
		t.Fatalf("network = %+v", network)
	}
	if len(plan.Networks) != 1 {
		t.Fatalf("unused networks appeared in plan: %+v", plan.Networks)
	}
}

func TestConfiguredRegionWithNoSourcesUsesItsOnlyDiscoveredNetwork(t *testing.T) {
	plan := Resolve(Input{Networks: refs("vpc-only"), Settings: settings()})
	if got := collectorIDs(plan); !reflect.DeepEqual(got, []string{"vpc-only"}) {
		t.Fatalf("Collector hosts = %v, want the configured region's only network", got)
	}
	host := networkByID(t, plan, "vpc-only")
	if host.Role != RoleCollector || len(host.Sources) != 0 {
		t.Fatalf("source-free host = %+v", host)
	}
}

func TestConfiguredRegionWithNoSourcesUsesExplicitHost(t *testing.T) {
	plan := Resolve(Input{Networks: refs("vpc-a", "vpc-host"), Settings: regionalSettings("vpc-host")})
	if got := collectorIDs(plan); !reflect.DeepEqual(got, []string{"vpc-host"}) {
		t.Fatalf("Collector hosts = %v, want explicit source-free host", got)
	}
}

func TestConfiguredRegionWithNoSourcesRefusesAmbiguousNetworks(t *testing.T) {
	plan := Resolve(Input{Networks: refs("vpc-a", "vpc-b"), Settings: settings()})
	if len(plan.CollectorNetworks()) != 0 || !reflect.DeepEqual(problemCodes(plan), []string{"CollectorHostNetworkRequired"}) {
		t.Fatalf("ambiguous source-free plan = %+v", plan)
	}
	if !strings.Contains(plan.Problems[0].Message, "vpc-a, vpc-b") {
		t.Fatalf("problem does not list discovered candidates: %s", plan.Problems[0].Message)
	}
}

func TestMarkedSourceDoesNotCreateCollectorInUnconfiguredRegion(t *testing.T) {
	unconfigured := collectorsettings.Read(map[string]any{collectorsettings.Key: map[string]any{
		collectorsettings.KeyEnabled: true,
	}})
	plan := Resolve(Input{Networks: refs("vpc-app"), Sources: []Source{source("svc", "vpc-app")}, Settings: unconfigured})
	if len(plan.CollectorNetworks()) != 0 || len(plan.Regions) != 0 {
		t.Fatalf("source created an unconfigured Collector: %+v", plan)
	}
	network := networkByID(t, plan, "vpc-app")
	if network.Role != RoleNone || network.Reason != ReasonRegionDisabled {
		t.Fatalf("unconfigured source network = %+v", network)
	}
}

func TestMultipleSourceNetworksRequireExplicitRegionalHost(t *testing.T) {
	plan := Resolve(Input{
		Networks: refs("vpc-a", "vpc-b"),
		Sources:  []Source{source("svc-a", "vpc-a"), source("svc-b", "vpc-b")},
		Settings: settings(),
	})
	if len(plan.CollectorNetworks()) != 0 || len(plan.Regions) != 1 || plan.Regions[0].Host.NetworkID != "" {
		t.Fatalf("ambiguous placement selected a host: %+v", plan)
	}
	if !reflect.DeepEqual(problemCodes(plan), []string{"CollectorHostNetworkRequired"}) {
		t.Fatalf("problems = %v", problemCodes(plan))
	}
	if !strings.Contains(plan.Problems[0].Message, collectorsettings.KeyHostNetworkID) {
		t.Fatalf("problem does not name remediation: %s", plan.Problems[0].Message)
	}
}

func TestExplicitHostProducesOneCollectorAndConsumerLocations(t *testing.T) {
	plan := Resolve(Input{
		Networks: refs("vpc-a", "vpc-host", "vpc-b"),
		Sources:  []Source{source("svc-a", "vpc-a"), source("svc-b", "vpc-b")},
		Settings: regionalSettings("vpc-host"),
	})
	if got := collectorIDs(plan); !reflect.DeepEqual(got, []string{"vpc-host"}) {
		t.Fatalf("Collector hosts = %v", got)
	}
	for _, id := range []string{"vpc-a", "vpc-b"} {
		network := networkByID(t, plan, id)
		if network.Role != RoleConsumer || network.ServedBy != "vpc-host" || network.CollectorRegion != region {
			t.Errorf("%s = %+v", id, network)
		}
	}
	host := networkByID(t, plan, "vpc-host")
	if host.Role != RoleCollector || len(host.Sources) != 0 {
		t.Fatalf("host = %+v", host)
	}
}

func TestUnknownExplicitHostIsRefused(t *testing.T) {
	plan := Resolve(Input{
		Networks: refs("vpc-app"), Sources: []Source{source("svc", "vpc-app")}, Settings: regionalSettings("vpc-missing"),
	})
	if len(plan.CollectorNetworks()) != 0 || !reflect.DeepEqual(problemCodes(plan), []string{"CollectorHostNetworkUnknown"}) {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestNonVPCSourcesAreSeparated(t *testing.T) {
	plan := Resolve(Input{
		Networks: refs("vpc-app"),
		Sources:  []Source{source("svc", "vpc-app"), source("lambda-b", ""), source("lambda-a", "")},
		Settings: settings(),
	})
	if !reflect.DeepEqual(plan.Unattached, []string{"lambda-a", "lambda-b"}) {
		t.Fatalf("unattached = %v", plan.Unattached)
	}
	if got := collectorIDs(plan); !reflect.DeepEqual(got, []string{"vpc-app"}) {
		t.Fatalf("hosts = %v", got)
	}
}

func TestFeatureAndRegionDisablePlacement(t *testing.T) {
	tests := []struct {
		name     string
		settings collectorsettings.Settings
		reason   Reason
	}{
		{
			name: "feature",
			settings: collectorsettings.Read(map[string]any{collectorsettings.Key: map[string]any{
				collectorsettings.KeyEnabled: false,
			}}),
			reason: ReasonFeatureDisabled,
		},
		{
			name: "region",
			settings: collectorsettings.Read(map[string]any{collectorsettings.Key: map[string]any{
				collectorsettings.KeyEnabled: true,
				collectorsettings.KeyRegions: map[string]any{region: map[string]any{collectorsettings.KeyEnabled: false}},
			}}),
			reason: ReasonRegionDisabled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := Resolve(Input{Networks: refs("vpc-app"), Sources: []Source{source("svc", "vpc-app")}, Settings: tt.settings})
			if len(plan.CollectorNetworks()) != 0 {
				t.Fatalf("disabled placement has hosts: %v", collectorIDs(plan))
			}
			if got := networkByID(t, plan, "vpc-app"); got.Role != RoleNone || got.Reason != tt.reason {
				t.Fatalf("network = %+v", got)
			}
		})
	}
}

func TestUnknownWorkloadNetworkIsReportedAndExcluded(t *testing.T) {
	plan := Resolve(Input{
		Networks: refs("vpc-app"),
		Sources:  []Source{source("known", "vpc-app"), source("unknown", "vpc-hidden")},
		Settings: settings(),
	})
	if len(plan.Problems) != 1 || plan.Problems[0].Code != "CollectorSourceOnUnknownNetwork" || plan.Problems[0].Severity != collectorstatus.SeverityDegraded {
		t.Fatalf("problems = %+v", plan.Problems)
	}
	if got := collectorIDs(plan); !reflect.DeepEqual(got, []string{"vpc-app"}) {
		t.Fatalf("hosts = %v", got)
	}
}

func TestSameNetworkIDRemainsScopedByRegion(t *testing.T) {
	multiRegion := collectorsettings.Read(map[string]any{collectorsettings.Key: map[string]any{
		collectorsettings.KeyEnabled: true,
		collectorsettings.KeyRegions: map[string]any{
			"eu-west-1": map[string]any{collectorsettings.KeyEnabled: true},
			"us-east-1": map[string]any{collectorsettings.KeyEnabled: true},
		},
	}})
	plan := Resolve(Input{
		Networks: []NetworkRef{{Region: "eu-west-1", NetworkID: "vpc-same"}, {Region: "us-east-1", NetworkID: "vpc-same"}},
		Sources:  []Source{{Ref: "eu", Region: "eu-west-1", NetworkID: "vpc-same"}, {Ref: "us", Region: "us-east-1", NetworkID: "vpc-same"}},
		Settings: multiRegion,
	})
	if len(plan.Regions) != 2 || !reflect.DeepEqual(plan.CollectorRegions(), []string{"eu-west-1", "us-east-1"}) {
		t.Fatalf("regions = %+v", plan.Regions)
	}
	if plan.ServedBy("eu-west-1", "vpc-same") != "vpc-same" || plan.ServedBy("us-east-1", "vpc-same") != "vpc-same" {
		t.Fatal("region-scoped network lookup failed")
	}
}
