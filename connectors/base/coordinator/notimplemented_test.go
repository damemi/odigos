package coordinator

import (
	"testing"

	"github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/base/config"
	"github.com/odigos-io/odigos/connectors/base/store"
	"github.com/odigos-io/odigos/connectors/spec"
)

// notImplSpec marks workload-b (instrumentation) as declared-but-unimplemented. Both service-a and
// service-b are compute-platform discovery types; installation supports only service-a (referenced under
// the installation feature), so service-b is an unimplemented compute-platform kind.
func notImplSpec() spec.CloudConnectorSpec {
	no := false
	return spec.CloudConnectorSpec{
		Workloads: spec.WorkloadDomain{
			Discovery: spec.Feature{
				Enabled: true,
				Types:   []spec.Type{{Name: "workload-a"}, {Name: "workload-b"}},
			},
			Instrumentation: spec.Feature{
				Enabled: true,
				Types: []spec.Type{
					{Name: "workload-a", Method: spec.MethodService},
					{Name: "workload-b", Method: spec.MethodSidecar, Implemented: &no},
				},
			},
		},
		ComputePlatforms: spec.ComputePlatformDomain{
			Discovery: spec.Feature{
				Enabled: true,
				Types:   []spec.Type{{Name: "service-a"}, {Name: "service-b"}},
			},
			Installation: spec.Feature{
				Enabled: true,
				Types:   []spec.Type{{Name: "service-a", Method: spec.MethodAgent}},
			},
		},
	}
}

// specProvider is a fakeProvider whose spec is overridable.
type specProvider struct {
	fakeProvider
	spec spec.CloudConnectorSpec
}

func (p specProvider) Spec() spec.CloudConnectorSpec { return p.spec }

func newSpecCoord(s spec.CloudConnectorSpec, caps spec.Capabilities) *Coordinator {
	st := &store.Store{}
	return New(config.Config{Capabilities: caps}, config.NewManager(st, config.Defaults()), st, specProvider{spec: s})
}

func TestMarkInstrumentationAvailabilityFlagsUnimplemented(t *testing.T) {
	caps := spec.Capabilities{Workloads: []spec.WorkloadCapability{
		{Type: "workload-a", Instrumentation: true},
		{Type: "workload-b", Instrumentation: true},
	}}
	c := newSpecCoord(notImplSpec(), caps)
	workloads := []api.DiscoveredWorkload{
		{ServiceName: "a", Kind: "workload-a", InstrumentationType: "workload-a"},
		{ServiceName: "b", Kind: "workload-b", InstrumentationType: "workload-b"},
	}
	c.markInstrumentationAvailability(workloads)

	if !workloads[0].InstrumentationImplemented || !workloads[0].InstrumentationAvailable {
		t.Errorf("workload-a should be implemented+available, got %+v", workloads[0])
	}
	// workload-b is declared but not implemented: not available, not implemented.
	if workloads[1].InstrumentationImplemented {
		t.Errorf("workload-b should be flagged not-implemented")
	}
	if workloads[1].InstrumentationAvailable {
		t.Errorf("workload-b should not be available (excluded from active types)")
	}
}

func TestMarkComputePlatformSupportOverridesStatus(t *testing.T) {
	c := newSpecCoord(notImplSpec(), spec.Capabilities{})
	services := []api.DiscoveredComputePlatform{
		{Name: "x", Kind: "service-a", Status: "Assigned"},
		{Name: "y", Kind: "service-b", Status: "Assigned"},
	}
	c.markComputePlatformSupport(services)

	if services[0].Status != "Assigned" {
		t.Errorf("service-a status = %q, want unchanged Assigned", services[0].Status)
	}
	if services[1].Status != api.ComputePlatformStatusNotImplemented {
		t.Errorf("service-b status = %q, want %q", services[1].Status, api.ComputePlatformStatusNotImplemented)
	}
}
