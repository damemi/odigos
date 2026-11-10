package coordinator

import (
	"context"
	"errors"
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// observingProvider is a fakeProvider that also implements the optional api.WorkloadObserverRegistry, which is
// how a provider opts into observation without every other provider having to.
type observingProvider struct {
	fakeProvider
	obs map[string]api.InstrumentationObserver
}

func (o observingProvider) WorkloadObservers() map[string]api.InstrumentationObserver { return o.obs }

type countingObserver struct {
	calls int
	err   error
}

func (c *countingObserver) Observe(context.Context) error {
	c.calls++
	return c.err
}

func observerNames(nos []namedObserver) map[string]bool {
	m := map[string]bool{}
	for _, no := range nos {
		m[no.name] = true
	}
	return m
}

// TestActiveObserversAreManualModeOnly is the complement of TestManualModeTypeIsNeverReconciled, and together
// they are the whole division of labour: a type is either reconciled (Odigos converges it) or observed (the
// customer owns it), never both and never neither.
//
// Observing an automatic-mode type would be a second read of the customer's control plane for an answer the
// reconciler is about to enforce anyway — pure cost. Not observing a manual-mode type leaves marker presence as
// the only signal, which is the false green the observer exists to eliminate.
func TestActiveObserversAreManualModeOnly(t *testing.T) {
	p := observingProvider{obs: map[string]api.InstrumentationObserver{
		"workload-a": &countingObserver{},
		"workload-b": &countingObserver{},
	}}
	caps := spec.Capabilities{Workloads: []spec.WorkloadCapability{
		{Type: "workload-a", Discovery: true, Instrumentation: true}, // automatic
		{Type: "workload-b", Discovery: true},                        // manual
	}}
	c := newCoord(p, caps)

	got := observerNames(c.activeObservers())
	if len(got) != 1 || !got["workload-b"] {
		t.Errorf("activeObservers = %v, want only the manual-mode type workload-b", got)
	}
	// The same capability set wires a reconciler for the automatic type and only that one — the two sets must
	// partition, not overlap.
	if recs := reconcilerNames(c.activeReconcilers()); len(recs) != 0 {
		// This fake registers no reconcilers, so an empty set here is expected; the partition itself is pinned
		// exhaustively by TestReconcilersNeverIncludeManualModeTypes.
		t.Errorf("activeReconcilers = %v, want none for a provider that registers no reconcilers", recs)
	}
}

// TestActiveObserversAcrossModes walks every capability combination for one type: an observer runs in exactly
// the manual case and nowhere else — including the case where the type is disabled entirely, where observing
// would mean reading a customer's resources the connector was not enabled for.
func TestActiveObserversAcrossModes(t *testing.T) {
	cases := []struct {
		discovery, instrumentation bool
		wantObserved               bool
	}{
		{discovery: false, instrumentation: false, wantObserved: false}, // not enabled at all
		{discovery: true, instrumentation: false, wantObserved: true},   // manual (IaC)
		{discovery: true, instrumentation: true, wantObserved: false},   // automatic
		{discovery: false, instrumentation: true, wantObserved: false},  // instrumentation without discovery
	}
	for _, tc := range cases {
		caps := spec.Capabilities{Workloads: []spec.WorkloadCapability{
			{Type: "workload-a", Discovery: tc.discovery, Instrumentation: tc.instrumentation},
		}}
		p := observingProvider{obs: map[string]api.InstrumentationObserver{"workload-a": &countingObserver{}}}
		got := len(newCoord(p, caps).activeObservers()) == 1
		if got != tc.wantObserved {
			t.Errorf("discovery=%v instrumentation=%v: observed = %v, want %v",
				tc.discovery, tc.instrumentation, got, tc.wantObserved)
		}
	}
}

// TestProviderWithoutObserversContributesNothing is why the registry is an optional interface asserted at one
// call site rather than a Provider method: a provider that has not implemented observation yet compiles
// untouched and simply supplies none. Adding it to Provider was verified to break all three provider modules in
// a single commit for the sake of returning an empty map.
func TestProviderWithoutObserversContributesNothing(t *testing.T) {
	caps := spec.Capabilities{Workloads: []spec.WorkloadCapability{{Type: "workload-a", Discovery: true}}}
	if got := newCoord(fakeProvider{}, caps).activeObservers(); len(got) != 0 {
		t.Errorf("a provider that does not implement the registry yielded %d observers", len(got))
	}
	// Implementing it with an empty map is equally fine and must not produce a loop that does nothing.
	if got := newCoord(observingProvider{}, caps).activeObservers(); len(got) != 0 {
		t.Errorf("an empty observer registry yielded %d observers", len(got))
	}
}

// TestRunObserveIsolatesFailuresAndHonorsDrain covers the pass itself: one failing observer must not stop the
// others (each type is independent, and the failure is recorded per-workload by the observer as an observation
// error rather than as drift), and a drain must stop observing entirely — refreshing a conformance verdict
// against intent that is being torn down would record drift that means nothing.
func TestRunObserveIsolatesFailuresAndHonorsDrain(t *testing.T) {
	failing := &countingObserver{err: errors.New("AccessDenied")}
	healthy := &countingObserver{}
	c := newCoord(fakeProvider{}, spec.Capabilities{})
	obs := []namedObserver{{name: "a", obs: failing}, {name: "b", obs: healthy}}

	c.runObserve(context.Background(), obs)
	if failing.calls != 1 || healthy.calls != 1 {
		t.Errorf("calls = (%d, %d), want (1, 1): a failing observer must not abort the pass", failing.calls, healthy.calls)
	}

	c.draining.Store(true)
	c.runObserve(context.Background(), obs)
	if failing.calls != 1 || healthy.calls != 1 {
		t.Errorf("calls = (%d, %d) after a drain started, want unchanged (1, 1)", failing.calls, healthy.calls)
	}
}

// TestManualModeTypeWithoutObserverIsNotSilent: a manual-mode type whose provider registers no observer can
// only report marker presence, which is precisely the false green this design rejects. It must not be treated as
// observed-and-fine, so it contributes no observer (and the resolver logs it).
func TestManualModeTypeWithoutObserverIsNotSilent(t *testing.T) {
	caps := spec.Capabilities{Workloads: []spec.WorkloadCapability{
		{Type: "workload-a", Discovery: true},
		{Type: "workload-b", Discovery: true},
	}}
	p := observingProvider{obs: map[string]api.InstrumentationObserver{"workload-a": &countingObserver{}}}

	got := observerNames(newCoord(p, caps).activeObservers())
	if len(got) != 1 || !got["workload-a"] {
		t.Errorf("activeObservers = %v, want only workload-a; workload-b has no observer to run", got)
	}
}
