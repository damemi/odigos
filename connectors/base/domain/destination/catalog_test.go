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
