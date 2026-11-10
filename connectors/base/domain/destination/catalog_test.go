package destination

import (
	"testing"

	"github.com/odigos-io/odigos/destinations"
)

func TestDefaultCatalog_OTLPOnly(t *testing.T) {
	if err := destinations.Load(); err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	types, err := DefaultCatalog().SupportedCatalogDestinationTypes()
	if err != nil {
		t.Fatalf("SupportedCatalogDestinationTypes: %v", err)
	}
	if len(types) != 1 {
		t.Fatalf("want 1 destination type, got %d", len(types))
	}
	if types[0].Type != CatalogTypeOTLP {
		t.Fatalf("want type %q, got %q", CatalogTypeOTLP, types[0].Type)
	}
}

func TestNewCatalog_EmptyFallsBackToOTLP(t *testing.T) {
	c := NewCatalog(nil)
	if !c.IsSupported(CatalogTypeOTLP) {
		t.Fatal("expected OTLP to be supported by default")
	}
	if c.IsSupported("jaeger") {
		t.Fatal("expected jaeger to be unsupported by default")
	}
	if c.IsSupported(CatalogTypeManagedCollector) {
		t.Fatal("managed Collector marker must require explicit catalog capability")
	}
}

func TestManagedCollectorCatalogEntryIsSyntheticAndExplicit(t *testing.T) {
	types, err := NewCatalogWithManagedCollector(nil).SupportedCatalogDestinationTypes()
	if err != nil {
		t.Fatalf("SupportedCatalogDestinationTypes: %v", err)
	}
	if len(types) != 2 || types[1].Type != CatalogTypeManagedCollector {
		t.Fatalf("want OTLP plus managed Collector marker, got %+v", types)
	}
	if types[1].DisplayName != ManagedCollectorDisplayName || len(types[1].Fields) != 0 {
		t.Fatalf("marker catalog entry = %+v, want display name and no fields", types[1])
	}
}

func TestManagedCollectorCatalogRequiresInstalledRegion(t *testing.T) {
	catalog := NewCatalogWithManagedCollector(nil).WithInstalledManagedCollectorRegions([]string{"eu-west-1"})
	if catalog.IsSupported(CatalogTypeManagedCollector) == false {
		t.Fatal("regional applicability must not remove marker validation support")
	}
	types, err := catalog.SupportedCatalogDestinationTypes()
	if err != nil || len(types) != 2 || types[1].Type != CatalogTypeManagedCollector {
		t.Fatalf("catalog types with installed region = %v, %v; marker should be offered when at least one region has an installed Collector", types, err)
	}
	if err := catalog.ValidateWithCatalog(Destination{Name: "bad", Type: CatalogTypeManagedCollector, Region: "us-east-1", Signals: []string{"TRACES"}}); err == nil {
		t.Fatal("marker in a region without an installed Collector was accepted")
	}
	if err := catalog.ValidateWithCatalog(Destination{Name: "ok", Type: CatalogTypeManagedCollector, Region: "eu-west-1", Signals: []string{"TRACES"}}); err != nil {
		t.Fatalf("marker in installed region rejected: %v", err)
	}
}

func TestManagedCollectorCatalogHiddenWithoutInstalledCollectors(t *testing.T) {
	catalog := NewCatalogWithManagedCollector(nil).WithInstalledManagedCollectorRegions(nil)
	types, err := catalog.SupportedCatalogDestinationTypes()
	if err != nil || len(types) != 1 {
		t.Fatalf("catalog types without installed Collectors = %v, %v; want only terminal destinations", types, err)
	}
}

func TestValidateWithCatalog_RejectsNonOTLP(t *testing.T) {
	err := DefaultCatalog().ValidateWithCatalog(Destination{
		Name:    "x",
		Type:    "jaeger",
		Signals: []string{"TRACES"},
		Config:  map[string]any{"endpoint": "localhost:4317"},
	})
	if err == nil {
		t.Fatal("expected error for non-otlp type")
	}
}

func TestValidateWithCatalog_AllowsConfiguredType(t *testing.T) {
	catalog := NewCatalog([]string{"otlp", "jaeger"})
	if !catalog.IsSupported("jaeger") {
		t.Fatal("expected jaeger to be supported in custom catalog")
	}
	err := catalog.ValidateWithCatalog(Destination{
		Name:    "x",
		Type:    "jaeger",
		Signals: []string{"TRACES"},
	})
	if err == nil {
		t.Fatal("expected validation error for incomplete jaeger config")
	}
	if err.Error() == "" {
		t.Fatal("expected non-empty validation error")
	}
}

func TestManagedCollectorMarkerValidation(t *testing.T) {
	catalog := NewCatalogWithManagedCollector(nil)
	base := Destination{
		Name:    "regional-route",
		Type:    CatalogTypeManagedCollector,
		Region:  "eu-west-1",
		Signals: []string{"TRACES", "METRICS", "LOGS"},
	}
	if err := catalog.ValidateWithCatalog(base); err != nil {
		t.Fatalf("valid marker rejected: %v", err)
	}

	for name, mutate := range map[string]func(*Destination){
		"missing region":       func(d *Destination) { d.Region = "" },
		"configuration fields": func(d *Destination) { d.Config = map[string]any{"endpoint": "ignored"} },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			mutate(&candidate)
			if err := catalog.ValidateWithCatalog(candidate); err == nil {
				t.Fatal("invalid marker was accepted")
			}
		})
	}
}

func TestValidateDestinationsMarkerUniquenessAndGlobalTerminal(t *testing.T) {
	marker := Destination{
		Name: "regional-route", Type: CatalogTypeManagedCollector, Region: "eu-west-1",
		Signals: []string{"TRACES"},
	}
	terminal := Destination{
		Name: "vendor", Type: CatalogTypeOTLP,
		Config: map[string]any{"OTLP_GRPC_ENDPOINT": "vendor:4317"}, Signals: []string{"TRACES"},
	}
	catalog := NewCatalogWithManagedCollector(nil)
	if err := catalog.ValidateDestinations([]Destination{marker, terminal}); err != nil {
		t.Fatalf("valid destination set rejected: %v", err)
	}
	duplicate := marker
	duplicate.Name = "regional-route-copy"
	if err := catalog.ValidateDestinations([]Destination{marker, duplicate, terminal}); err == nil {
		t.Fatal("duplicate regional markers were accepted")
	}
	if err := catalog.ValidateDestinations([]Destination{marker}); err != nil {
		t.Fatalf("marker may be configured before a terminal is added: %v", err)
	}
}

func TestOTLPRegionRemainsGlobal(t *testing.T) {
	err := NewCatalogWithManagedCollector(nil).ValidateWithCatalog(Destination{
		Name: "vendor", Type: CatalogTypeOTLP, Region: "eu-west-1",
		Config: map[string]any{"OTLP_GRPC_ENDPOINT": "vendor:4317"}, Signals: []string{"TRACES"},
	})
	if err == nil {
		t.Fatal("regional OTLP terminal was accepted")
	}
}
