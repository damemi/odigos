package collectorownership

import (
	"testing"

	"github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/spec"
)

var allModes = []string{spec.ModeAutomatic, spec.ModeManual, ""}

// TestPolicyFor pins what each mode does. The row that matters is manual: it observes and configures — that is
// manual mode working end to end — and converges nothing, which is why a manual-mode type has no deployer
// wired into the runtime at all.
func TestPolicyFor(t *testing.T) {
	cases := []struct {
		mode string
		want Policy
	}{
		{spec.ModeAutomatic, Policy{Publish: true, Observe: true, Configure: true, Converge: true, Adopt: true, Delete: true}},
		{spec.ModeManual, Policy{Publish: true, Observe: true, Configure: true}},
		{"", Policy{}},
	}
	for _, tc := range cases {
		if got := PolicyFor(tc.mode); got != tc.want {
			t.Errorf("mode %q: policy = %+v, want %+v", tc.mode, got, tc.want)
		}
	}
}

// TestNoTransitionDeletesOrRetags is the transition guarantee, asserted over the whole 3×3 table rather than
// argued in prose. A capability change says who converges the Collector from now on; it never destroys running
// infrastructure and never rewrites the record of who created it.
//
// automatic → "" is the row this protects: an older connector image that cannot parse the collectors key is
// indistinguishable from a capability a customer removed, so deleting on absence would make an image rollback
// delete a customer's Collector.
func TestNoTransitionDeletesOrRetags(t *testing.T) {
	for _, from := range allModes {
		for _, to := range allModes {
			tr := TransitionFor(from, to)
			if tr.Deletes {
				t.Errorf("%q -> %q deletes; deletion belongs to connector deletion, gated per object by MayDelete", from, to)
			}
			if tr.RewritesOwnershipTags {
				t.Errorf("%q -> %q rewrites ownership tags; the tag records who created an object, which a mode change does not alter", from, to)
			}
			if tr.Policy != PolicyFor(to) {
				t.Errorf("%q -> %q policy disagrees with PolicyFor(%q)", from, to, to)
			}
			if tr.Note == "" {
				t.Errorf("%q -> %q has no note; a mode change a customer cannot read about is a Collector that silently changed hands", from, to)
			}
		}
	}
}

// TestTransitionForHandOver: the two directions across the automatic/manual boundary are not symmetric, and
// the asymmetry is the whole point. Going to automatic starts converging and may adopt; going to manual stops
// converging and gives up deletion — without touching a single tag, because MayDelete's mode gate already
// covers it.
func TestTransitionForHandOver(t *testing.T) {
	toAuto := TransitionFor(spec.ModeManual, spec.ModeAutomatic)
	if !toAuto.Policy.Converge || !toAuto.Policy.Adopt {
		t.Errorf("manual -> automatic: %+v, want convergence and adoption", toAuto.Policy)
	}
	toManual := TransitionFor(spec.ModeAutomatic, spec.ModeManual)
	if toManual.Policy.Converge || toManual.Policy.Delete || toManual.Policy.Adopt {
		t.Errorf("automatic -> manual: %+v, want no convergence, no deletion, no adoption", toManual.Policy)
	}
	if !toManual.Policy.Observe || !toManual.Policy.Configure {
		t.Errorf("automatic -> manual: %+v, want the Collector still observed and still configured", toManual.Policy)
	}
}

// TestAdoptionNeverAcquiresOwnership: over every input, adoption must not grant deletion authority the object
// did not already carry. This is the structural half of "a customer's resources cannot be deleted as managed"
// — the tag gate closes the door, and this closes the only path that could reopen it.
func TestAdoptionNeverAcquiresOwnership(t *testing.T) {
	objs := []Object{
		{Tags: tags(mine()...)},
		{Tags: tags(with(mine(), api.TagManaged, "true")...)},
		{Tags: tags(with(mine(), api.TagIaC, `{"v":1}`)...)},
		{Tags: tags(with(mine(), api.TagConnectorID, "uid-0", api.TagManaged, "true")...)},
		{Tags: tags(with(mine(), api.TagConnector, "other", api.TagConnectorID, "uid-9")...)},
		{Tags: tags(), CustomerNamed: true},
		{Tags: tags()},
	}
	for _, o := range objs {
		for _, mode := range allModes {
			for _, recorded := range []bool{false, true} {
				o.Recorded = recorded
				if got := AdoptionOf(o, self, mode); got.AcquiresOwnership {
					t.Fatalf("adoption of %+v in %q mode acquires ownership; adopting is not creating", o.Tags, mode)
				}
			}
		}
	}
}

// TestAdoptionOf pins the decisions, including the two that must not be confused: a predecessor CR's stack is
// adopted AND re-stamped and keeps its deletability, while a customer's artifact stack is adopted for
// convergence and never becomes deletable.
func TestAdoptionOf(t *testing.T) {
	cases := []struct {
		name              string
		obj               Object
		mode              string
		adopt, restamp    bool
		inheritsDeleteAut bool
	}{
		{
			name: "customer's artifact stack, flipped to automatic",
			obj:  Object{Tags: tags(with(mine(), api.TagIaC, `{"v":1,"source":"terraform"}`)...)},
			mode: spec.ModeAutomatic, adopt: true, restamp: false, inheritsDeleteAut: false,
		},
		{
			name: "predecessor CR's stack",
			obj:  Object{Tags: tags(with(mine(), api.TagConnectorID, "uid-0", api.TagManaged, "true")...)},
			mode: spec.ModeAutomatic, adopt: true, restamp: true, inheritsDeleteAut: true,
		},
		{
			name: "our own object missing from our state",
			obj:  Object{Tags: tags(with(mine(), api.TagManaged, "true")...)},
			mode: spec.ModeAutomatic, adopt: true, restamp: false, inheritsDeleteAut: true,
		},
		{
			name: "our own object already tracked",
			obj:  Object{Tags: tags(with(mine(), api.TagManaged, "true")...), Recorded: true},
			mode: spec.ModeAutomatic,
		},
		{
			name: "another connector's Collector",
			obj:  Object{Tags: tags(with(mine(), api.TagConnector, "other", api.TagConnectorID, "uid-9", api.TagManaged, "true")...)},
			mode: spec.ModeAutomatic,
		},
		{
			name: "a Collector object naming no connector",
			obj:  Object{Tags: tags(api.TagComponent, api.ComponentCollector)},
			mode: spec.ModeAutomatic,
		},
		{
			name: "a cluster the customer named in configuration",
			obj:  Object{Tags: tags(with(mine(), api.TagManaged, "true")...), CustomerNamed: true},
			mode: spec.ModeAutomatic,
		},
		{
			name: "manual mode adopts nothing, however it is tagged",
			obj:  Object{Tags: tags(with(mine(), api.TagManaged, "true")...)},
			mode: spec.ModeManual,
		},
	}
	for _, tc := range cases {
		got := AdoptionOf(tc.obj, self, tc.mode)
		if got.Adopt != tc.adopt || got.RestampConnectorID != tc.restamp || got.InheritsDeleteAuthority != tc.inheritsDeleteAut {
			t.Errorf("%s: adopt=%v restamp=%v inheritsDelete=%v, want adopt=%v restamp=%v inheritsDelete=%v",
				tc.name, got.Adopt, got.RestampConnectorID, got.InheritsDeleteAuthority,
				tc.adopt, tc.restamp, tc.inheritsDeleteAut)
		}
		if got.Reason == "" {
			t.Errorf("%s: no reason recorded; adoption is never silent", tc.name)
		}
	}
}

// TestAdoptedCustomerStackConvergesButSurvivesTeardown is the manual → automatic → connector-deleted path end
// to end, because it is the one a customer is most likely to walk and the one where a wrong answer is
// unrecoverable. Odigos takes over convergence and, at teardown, leaves every object standing.
func TestAdoptedCustomerStackConvergesButSurvivesTeardown(t *testing.T) {
	stack := []Object{
		{Ref: "service", Tags: tags(with(mine(), api.TagIaC, `{"v":1,"source":"terraform"}`)...)},
		{Ref: "nlb", Tags: tags(with(mine(), api.TagIaC, `{"v":1,"source":"terraform"}`)...)},
	}
	for i, o := range stack {
		a := AdoptionOf(o, self, spec.ModeAutomatic)
		if !a.Adopt || a.InheritsDeleteAuthority {
			t.Fatalf("%s: adopt=%v inheritsDelete=%v, want adopted for convergence with no deletion authority", o.Ref, a.Adopt, a.InheritsDeleteAuthority)
		}
		// Adoption records the ref, which is what makes the object converge-able from the next cycle.
		stack[i].Recorded = true
	}
	for _, o := range stack {
		if !MayMutate(o, self, spec.ModeAutomatic) {
			t.Errorf("%s: adopted but not mutable; adoption exists to bring an object under convergence", o.Ref)
		}
	}
	deletable, retained := Partition(stack, self, spec.ModeAutomatic)
	if len(deletable) != 0 {
		t.Errorf("teardown would delete %v; the customer applied this stack and Odigos never created it", refs(deletable))
	}
	if len(retained) != len(stack) {
		t.Errorf("retained %d of %d; everything left behind is reported", len(retained), len(stack))
	}
}
