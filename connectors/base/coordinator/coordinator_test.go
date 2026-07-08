package coordinator

import (
	"context"
	"errors"
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/config"
	domainsrc "github.com/odigos-io/odigos-enterprise/connectors/base/domain/source"
	"github.com/odigos-io/odigos-enterprise/connectors/base/store"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

var testConnectorSpec = spec.CloudConnectorSpec{
	Workloads: spec.WorkloadDomain{
		Discovery: spec.Feature{
			Enabled: true,
			Types: []spec.Type{
				{Name: "workload-a"},
				{Name: "workload-b"},
			},
		},
		Instrumentation: spec.Feature{
			Enabled: true,
			Types: []spec.Type{
				{Name: "workload-a", Method: spec.MethodService},
				{Name: "workload-b", Method: spec.MethodSidecar},
			},
		},
	},
	ComputePlatforms: spec.ComputePlatformDomain{
		Discovery: spec.Feature{
			Enabled: true,
			Types: []spec.Type{
				{Name: "service-a"},
				{Name: "service-b"},
			},
		},
	},
}

// fakeProvider exercises the capability-driven detector/reconciler resolution without a database.
type fakeProvider struct {
	workloadDetectors map[string]api.Detector
	computePlatformDetectors  map[string]api.Detector
	recs              map[string]api.Reconciler
	svcRecs           map[string]api.ComputePlatformReconciler
}

func (f fakeProvider) Spec() spec.CloudConnectorSpec              { return testConnectorSpec }
func (f fakeProvider) SupportedKinds() []string                   { return []string{"cloud.workload-a"} }
func (f fakeProvider) SupportedWorkloadKinds() []string           { return []string{"cloud.workload-a"} }
func (f fakeProvider) SupportedComputePlatformKinds() []string            { return nil }
func (f fakeProvider) WorkloadDetectors() map[string]api.Detector { return f.workloadDetectors }
func (f fakeProvider) ComputePlatformDetectors() map[string]api.Detector  { return f.computePlatformDetectors }
func (f fakeProvider) Reconcilers() map[string]api.Reconciler     { return f.recs }
func (f fakeProvider) ComputePlatformReconcilers() map[string]api.ComputePlatformReconciler {
	return f.svcRecs
}
func (f fakeProvider) ListWorkloads(context.Context) ([]api.DiscoveredWorkload, error) {
	return nil, nil
}
func (f fakeProvider) ListComputePlatforms(context.Context) ([]api.DiscoveredComputePlatform, error) {
	return nil, nil
}
func (f fakeProvider) LookupWorkload(context.Context, string, string, string) (*api.DiscoveredWorkload, error) {
	return nil, nil
}
func (f fakeProvider) LookupComputePlatform(context.Context, string, string, string) (*api.DiscoveredComputePlatform, error) {
	return nil, nil
}

// CreateSource must reject a source with no region rather than falling back to a name+kind-only match
// (ambiguous across regions). A valid region passes the guard (and then fails for an unrelated reason with
// this fake provider — not the region error).
func TestCreateSourceRequiresRegion(t *testing.T) {
	st := &store.Store{}
	c := New(config.Config{}, config.NewManager(st, config.Defaults()), st, fakeProvider{})

	err := c.CreateSource(context.Background(), domainsrc.Source{Name: "svc", Kind: "cloud.workload-a"})
	if !errors.Is(err, ErrRegionRequired) {
		t.Fatalf("CreateSource with no region: err = %v, want ErrRegionRequired", err)
	}

	// With a region the guard passes; the fakeProvider then reports the workload as not found — a different
	// error, which proves the region check no longer blocks a well-formed request.
	err = c.CreateSource(context.Background(), domainsrc.Source{Name: "svc", Kind: "cloud.workload-a", Region: "us-east-1"})
	if errors.Is(err, ErrRegionRequired) {
		t.Fatalf("CreateSource with a region must pass the region guard, got %v", err)
	}
}

type noopDetector struct{}

func (noopDetector) Detect(context.Context) (api.DetectResult, error) {
	return api.DetectResult{}, nil
}

func newCoord(p api.Provider, caps spec.Capabilities) *Coordinator {
	st := &store.Store{}
	return New(config.Config{Capabilities: caps}, config.NewManager(st, config.Defaults()), st, p)
}

func detectorNames(nds []namedDetector) map[string]bool {
	m := map[string]bool{}
	for _, nd := range nds {
		m[nd.name] = true
	}
	return m
}

func TestActiveDetectorsResolvedFromCapabilities(t *testing.T) {
	p := fakeProvider{
		workloadDetectors: map[string]api.Detector{"workload-a": noopDetector{}, "workload-b": noopDetector{}},
		computePlatformDetectors:  map[string]api.Detector{"service-a": noopDetector{}, "service-b": noopDetector{}},
	}
	caps := spec.Capabilities{
		Workloads: []spec.WorkloadCapability{
			{Type: "workload-a", Discovery: true},
			{Type: "workload-b", Discovery: true},
		},
		ComputePlatforms: []spec.ComputePlatformCapability{
			{Type: "service-a", Discovery: true},
			{Type: "service-b", Discovery: true},
		},
	}
	c := newCoord(p, caps)
	wl := detectorNames(c.activeWorkloadDetectors())
	if !wl["workload-a"] || !wl["workload-b"] || len(wl) != 2 {
		t.Fatalf("active workload detectors = %v, want {workload-a, workload-b}", wl)
	}
	svc := detectorNames(c.activeComputePlatformDetectors())
	if !svc["service-a"] || !svc["service-b"] || len(svc) != 2 {
		t.Fatalf("active service detectors = %v, want {service-a, service-b}", svc)
	}
	if d := newCoord(p, spec.Capabilities{Workloads: []spec.WorkloadCapability{{Type: "workload-a"}}}).activeWorkloadDetectors(); len(d) != 0 {
		t.Fatalf("active workload detectors for discovery-off cap = %v, want none", d)
	}
}

func TestActiveDetectorsSkipsMissingDetector(t *testing.T) {
	p := fakeProvider{workloadDetectors: map[string]api.Detector{"workload-a": noopDetector{}}}
	caps := spec.Capabilities{Workloads: []spec.WorkloadCapability{
		{Type: "workload-a", Discovery: true},
		{Type: "workload-b", Discovery: true},
	}}
	dets := newCoord(p, caps).activeWorkloadDetectors()
	if len(dets) != 1 || dets[0].name != "workload-a" {
		t.Fatalf("active workload detectors = %v, want only workload-a", dets)
	}
}

type countingDetector struct{ calls int }

func (d *countingDetector) Detect(context.Context) (api.DetectResult, error) {
	d.calls++
	return api.DetectResult{Authenticated: true}, nil
}

func TestRunDiscoveryDedupsSharedDetector(t *testing.T) {
	shared := &countingDetector{}
	p := fakeProvider{computePlatformDetectors: map[string]api.Detector{"service-a": shared, "service-b": shared}}
	caps := spec.Capabilities{ComputePlatforms: []spec.ComputePlatformCapability{
		{Type: "service-a", Discovery: true},
		{Type: "service-b", Discovery: true},
	}}
	c := newCoord(p, caps)
	c.runDiscovery(context.Background(), "service", c.activeComputePlatformDetectors())

	if shared.calls != 1 {
		t.Errorf("shared compute-platform detector Detect calls = %d, want 1 (deduped across compute-platform types)", shared.calls)
	}
}

func TestActiveReconcilersGatedByCapability(t *testing.T) {
	p := fakeProvider{recs: map[string]api.Reconciler{"workload-a": nil, "workload-b": nil}}
	caps := spec.Capabilities{Workloads: []spec.WorkloadCapability{{Type: "workload-a", Instrumentation: true}}}
	got := newCoord(p, caps).activeReconcilers()
	if len(got) != 1 || got[0].name != "workload-a" {
		t.Fatalf("active reconcilers = %v, want [workload-a] (only the requested type)", got)
	}
}

func TestDestinationCatalogDefaultsToOTLP(t *testing.T) {
	c := newCoord(fakeProvider{}, spec.Capabilities{})
	types := c.DestinationCatalog().SupportedTypes()
	if len(types) != 1 || types[0] != api.DefaultDestinationType {
		t.Fatalf("destination catalog = %v, want [%q]", types, api.DefaultDestinationType)
	}
}

func TestEnrichWorkloads(t *testing.T) {
	workloads := []api.DiscoveredWorkload{
		{ServiceName: "svc-a", Kind: "cloud.workload-a", ExternalID: "ext:a"},
		{ServiceName: "svc-b", Kind: "cloud.workload-b", ExternalID: "ext:b"},
		{ServiceName: "svc-c", Kind: "cloud.workload-a", ExternalID: "ext:c"},
	}
	sources := []store.SourceRecord{
		{Name: "s1", Kind: "cloud.workload-a", ExternalResourceID: "ext:a", Enabled: true},
		{Name: "svc-b", Kind: "cloud.workload-b", Enabled: false},
	}
	out := enrichWorkloads(workloads, sources)

	if !out[0].SourceMatched || !out[0].SourceEnabled {
		t.Errorf("svc-a should be matched+enabled, got %+v", out[0])
	}
	if !out[1].SourceMatched || out[1].SourceEnabled {
		t.Errorf("svc-b should be matched+disabled, got %+v", out[1])
	}
	if out[2].SourceMatched {
		t.Errorf("svc-c should be unmatched, got %+v", out[2])
	}
}

func TestEnrichWorkloadsAppliesSourceOverrides(t *testing.T) {
	workloads := []api.DiscoveredWorkload{
		{ServiceName: "my-fn", Kind: "aws.lambda", ExternalID: "arn:aws:lambda:us-east-1:1:function:my-fn"},
	}
	sources := []store.SourceRecord{
		{
			Name:               "my-fn",
			Kind:               "aws.lambda",
			ServiceName:        "custom-otel",
			ExternalResourceID: "arn:aws:lambda:us-east-1:1:function:my-fn",
			Enabled:            true,
			Config: domainsrc.SourceConfig{
				RuntimeOverride: domainsrc.RuntimeOverride{Language: "java", Version: "17"},
				DistroOverride:  domainsrc.DistroOverride{OtelDistroName: "opentelemetry-java"},
			},
		},
	}
	out := enrichWorkloads(workloads, sources)
	if !out[0].SourceMatched {
		t.Fatal("expected source matched")
	}
	if out[0].Overrides.ServiceName != "custom-otel" {
		t.Errorf("service override = %q, want custom-otel", out[0].Overrides.ServiceName)
	}
	if out[0].Overrides.RuntimeLanguage != "java" || out[0].Overrides.RuntimeVersion != "17" {
		t.Errorf("runtime override = %+v, want java/17", out[0].Overrides)
	}
	if out[0].Overrides.OtelDistroName != "opentelemetry-java" {
		t.Errorf("distro override = %q", out[0].Overrides.OtelDistroName)
	}
	if got := out[0].ReportedServiceName(); got != "custom-otel" {
		t.Errorf("ReportedServiceName = %q, want custom-otel", got)
	}
}

func TestEnrichComputePlatforms(t *testing.T) {
	services := []api.DiscoveredComputePlatform{
		{Name: "cluster-a", Kind: "cloud.service-a", ExternalID: "ext:cluster:a"},
		{Name: "cluster-b", Kind: "cloud.service-a", ExternalID: "ext:cluster:b"},
	}
	sources := []store.SourceRecord{
		{Name: "cluster-a", Kind: "cloud.service-a", ExternalResourceID: "ext:cluster:a", Enabled: true},
		{Name: "cluster-b", Kind: "cloud.service-a", Enabled: false},
	}
	out := enrichComputePlatforms(services, sources)

	if out[0].Status != "Assigned" {
		t.Errorf("cluster-a should be Assigned, got %q", out[0].Status)
	}
	if out[1].Status != "Disabled" {
		t.Errorf("cluster-b should be Disabled, got %q", out[1].Status)
	}
}
