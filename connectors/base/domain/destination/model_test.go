package destination

import (
	"testing"

	"github.com/odigos-io/agents-api/go/server/graph/model"
)

func TestDestinationModelRoundTripPreservesRegion(t *testing.T) {
	region := "eu-west-1"
	in := model.CreateDestinationInput{
		Name: "regional-route", Type: CatalogTypeManagedCollector, Region: &region,
		Signals: []model.TelemetrySignal{model.TelemetrySignalTraces}, Config: map[string]any{},
	}
	dest := DestinationFromCreateInput(in)
	if dest.Region != region {
		t.Fatalf("domain region = %q, want %q", dest.Region, region)
	}
	out := dest.ToDestinationModel()
	if out.Region == nil || *out.Region != region {
		t.Fatalf("GraphQL region = %v, want %q", out.Region, region)
	}

	updated := "us-east-1"
	dest.ApplyUpdateInput(model.UpdateDestinationInput{Region: &updated})
	if dest.Region != updated {
		t.Fatalf("updated domain region = %q, want %q", dest.Region, updated)
	}
}
