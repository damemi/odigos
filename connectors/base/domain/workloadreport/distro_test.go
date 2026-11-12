package workloadreport

import (
	"testing"

	"github.com/odigos-io/odigos/connectors/base/api"
)

func TestProviderDistroCatalogReachesGraphQL(t *testing.T) {
	w := api.DiscoveredWorkload{ServiceName: "fn", Kind: "aws.lambda", EffectiveDistro: "java-lambda-otel-agent", InstrumentationVersion: "2.32.0", AvailableDistros: []api.InstrumentationDistro{{Name: "java-lambda-otel-agent", DisplayName: "Java Lambda OpenTelemetry agent", Language: "java", Version: "2.32.0", IsDefault: true}}}
	got := ToModel(w).Desired
	if got.OtelDistroName == nil || *got.OtelDistroName != w.EffectiveDistro || got.InstrumentationVersion == nil || *got.InstrumentationVersion != "2.32.0" || len(got.AvailableDistros) != 1 || !got.AvailableDistros[0].IsDefault {
		t.Fatalf("incomplete provider choices: %+v", got)
	}
	empty := ToModel(api.DiscoveredWorkload{AvailableDistros: []api.InstrumentationDistro{}}).Desired.AvailableDistros
	if empty == nil {
		t.Fatal("empty supported catalog must differ from a provider without catalogs")
	}
	legacy := ToModel(api.DiscoveredWorkload{}).Desired.AvailableDistros
	if legacy != nil {
		t.Fatal("legacy provider unexpectedly advertises catalog")
	}
}
