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

func TestDestinationModelRoundTripPreservesDisabled(t *testing.T) {
	disabled := true
	in := model.CreateDestinationInput{
		Name: "paused", Type: "otlp", Disabled: &disabled,
		Signals: []model.TelemetrySignal{model.TelemetrySignalTraces}, Config: map[string]any{},
	}
	dest := DestinationFromCreateInput(in)
	if !dest.Disabled {
		t.Fatal("create input Disabled=true was dropped")
	}
	out := dest.ToDestinationModel()
	if !out.Disabled {
		t.Fatal("GraphQL Disabled=true was dropped")
	}

	enabled := false
	dest.ApplyUpdateInput(model.UpdateDestinationInput{Disabled: &enabled})
	if dest.Disabled {
		t.Fatal("update input Disabled=false was ignored")
	}

	// A create/update that omits Disabled must not flip an existing destination on.
	paused := Destination{Name: "kept", Type: "otlp", Disabled: true, Signals: []string{SignalTraces}}
	paused.ApplyUpdateInput(model.UpdateDestinationInput{})
	if !paused.Disabled {
		t.Fatal("omitted Disabled must leave the stored flag alone")
	}
	plain := DestinationFromCreateInput(model.CreateDestinationInput{
		Name: "plain", Type: "otlp",
		Signals: []model.TelemetrySignal{model.TelemetrySignalTraces}, Config: map[string]any{},
	})
	if plain.Disabled {
		t.Fatal("omitted create Disabled must default to enabled")
	}
}
