package coordinator

import (
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/store"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// Only a workload the reconcile would actually try to instrument can be "waiting". Everything else
// already has a more specific story (Unknown, NotImplemented, working) that must not be overwritten
// with a vaguer one.
func TestBlockableForInstrumentation(t *testing.T) {
	full := api.DiscoveredWorkload{
		SourceMatched: true, SourceEnabled: true, Instrumented: false,
		InstrumentationAvailable: true, InstrumentationImplemented: true,
	}
	if !blockableForInstrumentation(full) {
		t.Error("an enabled, implemented, not-yet-instrumented workload is blockable")
	}
	for name, mutate := range map[string]func(*api.DiscoveredWorkload){
		"no source":       func(w *api.DiscoveredWorkload) { w.SourceMatched = false },
		"source disabled": func(w *api.DiscoveredWorkload) { w.SourceEnabled = false },
		"already done":    func(w *api.DiscoveredWorkload) { w.Instrumented = true },
		"type inactive":   func(w *api.DiscoveredWorkload) { w.InstrumentationAvailable = false },
		"not implemented": func(w *api.DiscoveredWorkload) { w.InstrumentationImplemented = false },
	} {
		w := full
		mutate(&w)
		if blockableForInstrumentation(w) {
			t.Errorf("%s: must not be reported as merely waiting", name)
		}
	}
}

func TestRegionalCollectorMarkerSatisfiesDestinationPrerequisite(t *testing.T) {
	workloads := []api.DiscoveredWorkload{
		{Kind: "aws.fargate-task", Region: "us-east-1", SourceMatched: true, SourceEnabled: true, InstrumentationAvailable: true, InstrumentationImplemented: true},
		{Kind: "aws.lambda", Region: "us-east-1", InstrumentationAvailable: true, InstrumentationImplemented: true},
		{Kind: "aws.fargate-task", Region: "eu-west-1", SourceMatched: true, SourceEnabled: true, InstrumentationAvailable: true, InstrumentationImplemented: true},
	}
	markWaitingForDestination(workloads, []store.DestinationRecord{{Type: "odigos-managed-collector", Region: "us-east-1"}}, nil)
	if workloads[0].WaitingFor != "" {
		t.Errorf("marked Fargate in Collector region is waiting for a destination: %q", workloads[0].WaitingFor)
	}
	if workloads[1].WaitingFor != "" {
		t.Errorf("unmarked Lambda is waiting for a destination: %q", workloads[1].WaitingFor)
	}
	if workloads[2].WaitingFor != api.WaitingForDestination {
		t.Errorf("Fargate outside Collector region is waiting for %q, want destination", workloads[2].WaitingFor)
	}
}

func TestDisabledRegionalCollectorMarkerDoesNotSatisfyDestinationPrerequisite(t *testing.T) {
	workloads := []api.DiscoveredWorkload{
		{Kind: "aws.fargate-task", Region: "us-east-1", SourceMatched: true, SourceEnabled: true, InstrumentationAvailable: true, InstrumentationImplemented: true},
	}
	markWaitingForDestination(workloads, []store.DestinationRecord{
		{Type: "otlp", Region: ""},
		{Type: "odigos-managed-collector", Region: "us-east-1", Disabled: true},
	}, nil)
	if workloads[0].WaitingFor != api.WaitingForDestination {
		t.Errorf("Fargate next to a disabled Collector marker is waiting for %q, want destination", workloads[0].WaitingFor)
	}
}

func TestGlobalOnlyWorkloadCannotUseRegionalCollectorMarker(t *testing.T) {
	workloads := []api.DiscoveredWorkload{
		{Kind: "global-only", Region: "us-east-1", SourceMatched: true, SourceEnabled: true, InstrumentationAvailable: true, InstrumentationImplemented: true},
		{Kind: "global-only", Region: "us-east-1", SourceMatched: true, SourceEnabled: true, InstrumentationAvailable: true, InstrumentationImplemented: true,
			ManagementMode: spec.ModeManual, Instrumented: true, ConformanceObservable: true},
	}
	markWaitingForDestination(workloads, []store.DestinationRecord{{Type: "odigos-managed-collector", Region: "us-east-1"}}, func(kind string) bool { return kind == "global-only" })
	for _, workload := range workloads {
		if workload.WaitingFor != api.WaitingForDestination {
			t.Errorf("global-only workload must wait for a global destination: %+v", workload)
		}
	}
}

// TestUnverifiableWithoutDestination is the counterpart predicate, covering the case blockableForInstrumentation
// above deliberately excludes: a workload that IS instrumented.
//
// That exclusion is right for "would the reconcile try to instrument this" and wrong as a claim that the
// workload is fine. In manual mode Instrumented means only that a marker is present, and confirming it MATCHES
// needs the destination's endpoint as the desired value — so with no destination the connector is holding an
// applied instrumentation it has nothing to compare against, and must say so rather than report it as working.
func TestUnverifiableWithoutDestination(t *testing.T) {
	full := api.DiscoveredWorkload{
		ManagementMode: spec.ModeManual,
		SourceMatched:  true, SourceEnabled: true, Instrumented: true,
		InstrumentationAvailable: true, InstrumentationImplemented: true,
		ConformanceObservable: true,
	}
	if !unverifiableWithoutDestination(full) {
		t.Error("an instrumented manual-mode workload of an observable type cannot be verified without a destination")
	}
	for name, mutate := range map[string]func(*api.DiscoveredWorkload){
		// The complement of the predicate above: an uninstrumented workload is blockable, not unverifiable.
		"not instrumented": func(w *api.DiscoveredWorkload) { w.Instrumented = false },
		// Automatic mode has no observer by design; the reconciler is what guarantees correctness there, so
		// every automatic workload would otherwise report as unverified forever.
		"automatic mode": func(w *api.DiscoveredWorkload) { w.ManagementMode = spec.ModeAutomatic },
		// A type this connector registers no observer for — GCP Cloud Run and Azure Container Apps today, whose
		// IaC flows are not wired. Nothing will ever verify these, so promising it would be a standing lie.
		"type has no observer": func(w *api.DiscoveredWorkload) { w.ConformanceObservable = false },
		"no source":            func(w *api.DiscoveredWorkload) { w.SourceMatched = false },
		"source disabled":      func(w *api.DiscoveredWorkload) { w.SourceEnabled = false },
		"type inactive":        func(w *api.DiscoveredWorkload) { w.InstrumentationAvailable = false },
		"not implemented":      func(w *api.DiscoveredWorkload) { w.InstrumentationImplemented = false },
	} {
		w := full
		mutate(&w)
		if unverifiableWithoutDestination(w) {
			t.Errorf("%s: must not be flagged as unverifiable for lack of a destination", name)
		}
	}
}
