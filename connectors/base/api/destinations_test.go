package api

import (
	"context"
	"testing"

	"github.com/odigos-io/odigos/connectors/spec"
)

type overrideProvider struct{ types []string }

func (overrideProvider) Spec() spec.CloudConnectorSpec              { return spec.CloudConnectorSpec{} }
func (overrideProvider) SupportedKinds() []string                   { return nil }
func (overrideProvider) SupportedWorkloadKinds() []string           { return nil }
func (overrideProvider) SupportedComputePlatformKinds() []string            { return nil }
func (overrideProvider) WorkloadDetectors() map[string]Detector     { return nil }
func (overrideProvider) ComputePlatformDetectors() map[string]Detector      { return nil }
func (overrideProvider) Reconcilers() map[string]Reconciler         { return nil }
func (overrideProvider) ComputePlatformReconcilers() map[string]ComputePlatformReconciler {
	return nil
}
func (overrideProvider) ListWorkloads(context.Context) ([]DiscoveredWorkload, error) {
	return nil, nil
}
func (overrideProvider) ListComputePlatforms(context.Context) ([]DiscoveredComputePlatform, error) {
	return nil, nil
}
func (overrideProvider) LookupWorkload(context.Context, string, string, string) (*DiscoveredWorkload, error) {
	return nil, nil
}
func (overrideProvider) LookupComputePlatform(context.Context, string, string, string) (*DiscoveredComputePlatform, error) {
	return nil, nil
}
func (p overrideProvider) SupportedDestinationTypes() []string { return p.types }

func TestSupportedDestinationTypes_DefaultsToOTLP(t *testing.T) {
	got := SupportedDestinationTypes(overrideProvider{})
	if len(got) != 1 || got[0] != DefaultDestinationType {
		t.Fatalf("got %v, want [%q]", got, DefaultDestinationType)
	}
}

func TestSupportedDestinationTypes_UsesProviderOverride(t *testing.T) {
	got := SupportedDestinationTypes(overrideProvider{types: []string{"otlp", "jaeger"}})
	if len(got) != 2 || got[0] != "otlp" || got[1] != "jaeger" {
		t.Fatalf("got %v, want [otlp jaeger]", got)
	}
}
