package coordinator

import (
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// TestMarkInstrumentationAvailabilityResolvesMode covers the split that keeps manual mode reportable without
// making it mutable: availability spans both modes, while ManagementMode records which one applies.
func TestMarkInstrumentationAvailabilityResolvesMode(t *testing.T) {
	caps := spec.Capabilities{Workloads: []spec.WorkloadCapability{
		{Type: "workload-a", Discovery: true, Instrumentation: true}, // automatic
		{Type: "workload-b", Discovery: true},                        // manual
	}}
	c := newCoord(fakeProvider{}, caps)

	workloads := []api.DiscoveredWorkload{
		{ServiceName: "auto", InstrumentationType: "workload-a"},
		{ServiceName: "manual", InstrumentationType: "workload-b"},
		// A kind the provider declares no instrumentation type for: genuinely nothing on offer in any mode.
		{ServiceName: "coverage-only", InstrumentationType: ""},
		// A type absent from the allow-list entirely.
		{ServiceName: "disabled", InstrumentationType: "workload-c"},
	}
	c.markInstrumentationAvailability(workloads)

	want := []struct {
		available bool
		mode      string
	}{
		{available: true, mode: spec.ModeAutomatic},
		{available: true, mode: spec.ModeManual},
		{available: false, mode: ""},
		{available: false, mode: ""},
	}
	for i, w := range workloads {
		if w.InstrumentationAvailable != want[i].available {
			t.Errorf("%s: available = %v, want %v", w.ServiceName, w.InstrumentationAvailable, want[i].available)
		}
		if w.ManagementMode != want[i].mode {
			t.Errorf("%s: mode = %q, want %q", w.ServiceName, w.ManagementMode, want[i].mode)
		}
	}
}

// TestManualModeTypeIsNeverReconciled is the safety property the whole design rests on: a manual-mode type is
// reportable (available, with a mode) yet has no reconciler in the runtime, so no code path — including the
// shutdown drain, which re-runs the same reconcilers — can mutate the workload.
func TestManualModeTypeIsNeverReconciled(t *testing.T) {
	p := fakeProvider{recs: map[string]api.Reconciler{"workload-a": nil, "workload-b": nil}}
	caps := spec.Capabilities{Workloads: []spec.WorkloadCapability{
		{Type: "workload-a", Discovery: true, Instrumentation: true}, // automatic
		{Type: "workload-b", Discovery: true},                        // manual
	}}
	c := newCoord(p, caps)

	got := c.activeReconcilers()
	if len(got) != 1 || got[0].name != "workload-a" {
		t.Fatalf("activeReconcilers = %v, want only the automatic-mode type workload-a", reconcilerNames(got))
	}

	// The same capability set must still report the manual-mode type as handled — that asymmetry is the point.
	handled := map[string]bool{}
	for _, ty := range c.provider.Spec().InstrumentationTypes(caps) {
		handled[ty.Name] = true
	}
	if !handled["workload-b"] {
		t.Error("manual-mode type must be reported as handled even though it is never reconciled")
	}
}

// TestMarkAwaitingCustomer covers who gets flagged as waiting on the customer's own deployment, and the
// precedence against a missing destination.
func TestMarkAwaitingCustomer(t *testing.T) {
	manual := func() api.DiscoveredWorkload {
		return api.DiscoveredWorkload{
			ServiceName:                "svc",
			ManagementMode:             spec.ModeManual,
			SourceMatched:              true,
			SourceEnabled:              true,
			InstrumentationAvailable:   true,
			InstrumentationImplemented: true,
		}
	}
	c := newCoord(fakeProvider{}, spec.Capabilities{})

	cases := map[string]struct {
		mutate func(*api.DiscoveredWorkload)
		want   string
	}{
		"marked, enabled, not yet applied": {mutate: func(*api.DiscoveredWorkload) {}, want: api.WaitingForCustomer},
		// Automatic mode is the connector's job; it must never tell the customer to go apply something.
		"automatic mode": {mutate: func(w *api.DiscoveredWorkload) { w.ManagementMode = spec.ModeAutomatic }, want: ""},
		// Already applied: the customer has nothing left to do.
		"already instrumented": {mutate: func(w *api.DiscoveredWorkload) { w.Instrumented = true }, want: ""},
		// Not marked / disabled: no intent, so nothing is being awaited.
		"no source":       {mutate: func(w *api.DiscoveredWorkload) { w.SourceMatched = false }, want: ""},
		"source disabled": {mutate: func(w *api.DiscoveredWorkload) { w.SourceEnabled = false }, want: ""},
		// Nothing to publish instructions for.
		"type unavailable": {mutate: func(w *api.DiscoveredWorkload) { w.InstrumentationAvailable = false }, want: ""},
		"not implemented":  {mutate: func(w *api.DiscoveredWorkload) { w.InstrumentationImplemented = false }, want: ""},
		// The instructions must carry an OTLP endpoint, so without a destination there is nothing for the
		// customer to apply yet and the earlier, more specific prerequisite must survive.
		"destination already wins": {
			mutate: func(w *api.DiscoveredWorkload) { w.WaitingFor = api.WaitingForDestination },
			want:   api.WaitingForDestination,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := manual()
			tc.mutate(&w)
			list := []api.DiscoveredWorkload{w}
			c.markAwaitingCustomer(list)
			if list[0].WaitingFor != tc.want {
				t.Errorf("waitingFor = %q, want %q", list[0].WaitingFor, tc.want)
			}
		})
	}
}

// TestReconcilersNeverIncludeManualModeTypes is the invariant the whole safety model rests on, asserted
// exhaustively rather than defended by a runtime check: a reconciler exists ONLY for a type whose derived mode
// is automatic.
//
// Two independent functions have to agree for that to hold — ActiveInstrumentationTypes (which the coordinator
// intersects with provider.Reconcilers) and Capabilities.WorkloadInstrumentationMode (which reporting and this
// PR's drain accounting use). They read the same capability booleans today, so they cannot diverge; this pins
// that so a later edit to either one fails in CI instead of quietly handing a manual-mode workload a
// reconciler, which would be a mutation path with nothing else in its way.
func TestReconcilersNeverIncludeManualModeTypes(t *testing.T) {
	p := fakeProvider{recs: map[string]api.Reconciler{"workload-a": nil, "workload-b": nil}}

	for _, aDiscovery := range []bool{false, true} {
		for _, aInstrumentation := range []bool{false, true} {
			for _, bDiscovery := range []bool{false, true} {
				for _, bInstrumentation := range []bool{false, true} {
					caps := spec.Capabilities{Workloads: []spec.WorkloadCapability{
						{Type: "workload-a", Discovery: aDiscovery, Instrumentation: aInstrumentation},
						{Type: "workload-b", Discovery: bDiscovery, Instrumentation: bInstrumentation},
					}}
					c := newCoord(p, caps)
					for _, nr := range c.activeReconcilers() {
						mode := caps.WorkloadInstrumentationMode(nr.name)
						if mode != spec.ModeAutomatic {
							t.Errorf("caps a=(%v,%v) b=(%v,%v): reconciler wired for %q whose mode is %q, want %q",
								aDiscovery, aInstrumentation, bDiscovery, bInstrumentation, nr.name, mode, spec.ModeAutomatic)
						}
					}

					// Same assertion with BOTH entries naming the same type. Nothing along the carrier enforces
					// one entry per type, and the two resolvers read that slice independently — this is where they
					// used to diverge and hand a manual-mode type a reconciler. spec.Capabilities.Validate rejects
					// the conflicting shape at startup; this pins that even if one slipped through, the gate and
					// the reported mode still agree.
					dupCaps := spec.Capabilities{Workloads: []spec.WorkloadCapability{
						{Type: "workload-a", Discovery: aDiscovery, Instrumentation: aInstrumentation},
						{Type: "workload-a", Discovery: bDiscovery, Instrumentation: bInstrumentation},
					}}
					for _, nr := range newCoord(p, dupCaps).activeReconcilers() {
						if mode := dupCaps.WorkloadInstrumentationMode(nr.name); mode != spec.ModeAutomatic {
							t.Errorf("duplicate entries (%v,%v)+(%v,%v): reconciler wired for %q whose mode is %q, want %q",
								aDiscovery, aInstrumentation, bDiscovery, bInstrumentation, nr.name, mode, spec.ModeAutomatic)
						}
					}
				}
			}
		}
	}
}

func reconcilerNames(nrs []namedReconciler) []string {
	out := make([]string, 0, len(nrs))
	for _, nr := range nrs {
		out = append(out, nr.name)
	}
	return out
}
