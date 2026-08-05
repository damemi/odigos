package coordinator

import (
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
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
