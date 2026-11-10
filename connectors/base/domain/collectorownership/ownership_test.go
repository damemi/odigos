package collectorownership

import (
	"testing"

	"github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/spec"
)

var self = Identity{ConnectorName: "aws", ConnectorID: "uid-1"}

// tags builds a tag set through the real parser, so the tests exercise the same path an observer does.
func tags(kv ...string) api.CollectorTags {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return api.ParseCollectorTags(m)
}

// mine is the identity set the deployer writes on everything it creates, for this connector.
func mine() []string {
	return []string{
		api.TagComponent, api.ComponentCollector,
		api.TagConnector, "aws",
		api.TagConnectorID, "uid-1",
		api.TagNetwork, "vpc-0abc",
	}
}

func with(base []string, kv ...string) []string { return append(append([]string{}, base...), kv...) }

// TestOriginOf pins who a tag set says created an object. The case that carries the design is "artifact stack
// with a stray ownership tag": a customer who pastes the ownership tag into their own document must still land
// on OriginCustomer, or the one realistic way a customer's stack becomes deletable is left open.
func TestOriginOf(t *testing.T) {
	cases := []struct {
		name string
		tags api.CollectorTags
		want Origin
	}{
		{"deployer-created", tags(with(mine(), api.TagManaged, "true")...), OriginOdigos},
		{"artifact stack", tags(with(mine(), api.TagIaC, `{"v":1,"source":"terraform"}`)...), OriginCustomer},
		{"artifact stack with a stray ownership tag", tags(with(mine(),
			api.TagManaged, "true",
			api.TagIaC, `{"v":1,"source":"terraform"}`)...), OriginCustomer},
		{"identity tags only", tags(mine()...), OriginCustomer},
		{"ownership tag set to something else", tags(with(mine(), api.TagManaged, "yes")...), OriginCustomer},
		{"no tags at all", tags(), OriginCustomer},
	}
	for _, tc := range cases {
		if got := OriginOf(tc.tags); got != tc.want {
			t.Errorf("%s: origin = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestAttributionOf pins the identity resolution, including the two cases that are easy to get backwards: a
// renamed connector still owns its objects (the id is unique and wins), and a recreated one recognises its
// predecessor's rather than treating them as a stranger's.
func TestAttributionOf(t *testing.T) {
	cases := []struct {
		name string
		tags api.CollectorTags
		want Attribution
	}{
		{"not a collector object", tags(api.TagConnector, "aws"), AttributionNone},
		{"this connector", tags(mine()...), AttributionMine},
		{"renamed connector, same CR", tags(with(mine(), api.TagConnector, "aws-old")...), AttributionMine},
		{"recreated CR, same name", tags(with(mine(), api.TagConnectorID, "uid-0")...), AttributionPredecessor},
		{"another connector", tags(with(mine(), api.TagConnector, "aws-staging", api.TagConnectorID, "uid-9")...), AttributionForeign},
		{"collector object naming nobody", tags(api.TagComponent, api.ComponentCollector, api.TagNetwork, "vpc-0abc"), AttributionUnattributed},
	}
	for _, tc := range cases {
		if got := AttributionOf(tc.tags, self); got != tc.want {
			t.Errorf("%s: attribution = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestMayMutate: the capability is what grants infrastructure writes, and attribution is what scopes them.
func TestMayMutate(t *testing.T) {
	odigosOwned := Object{Ref: "svc", Tags: tags(with(mine(), api.TagManaged, "true")...), Recorded: true}
	cases := []struct {
		name string
		obj  Object
		mode string
		want bool
	}{
		{"automatic, ours", odigosOwned, spec.ModeAutomatic, true},
		{"manual, ours", odigosOwned, spec.ModeManual, false},
		{"capability removed, ours", odigosOwned, "", false},
		{"automatic, customer-created", Object{Tags: tags(with(mine(), api.TagIaC, `{"v":1}`)...)}, spec.ModeAutomatic, true},
		{"automatic, predecessor's", Object{Tags: tags(with(mine(), api.TagConnectorID, "uid-0")...)}, spec.ModeAutomatic, true},
		{"automatic, another connector's", Object{Tags: tags(with(mine(), api.TagConnector, "other", api.TagConnectorID, "uid-9")...)}, spec.ModeAutomatic, false},
		{"automatic, untagged", Object{Tags: tags()}, spec.ModeAutomatic, false},
		{"automatic, customer-named in config", Object{Tags: tags(with(mine(), api.TagManaged, "true")...), CustomerNamed: true}, spec.ModeAutomatic, false},
	}
	for _, tc := range cases {
		if got := MayMutate(tc.obj, self, tc.mode); got != tc.want {
			t.Errorf("%s: MayMutate = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestMayDelete walks every way an object can fail the delete gate. Each row is a hole the gate closes, and
// the four conditions are deliberately independent: no single tag, and no single record, is ever sufficient.
func TestMayDelete(t *testing.T) {
	cases := []struct {
		name string
		obj  Object
		mode string
		want bool
	}{
		{
			name: "created by this connector and recorded",
			obj:  Object{Ref: "svc", Tags: tags(with(mine(), api.TagManaged, "true")...), Recorded: true},
			mode: spec.ModeAutomatic, want: true,
		},
		{
			name: "created by a predecessor CR and re-recorded on adoption",
			obj:  Object{Ref: "svc", Tags: tags(with(mine(), api.TagConnectorID, "uid-0", api.TagManaged, "true")...), Recorded: true},
			mode: spec.ModeAutomatic, want: true,
		},
		{
			name: "ownership marker but nothing in this connector's state",
			obj:  Object{Ref: "svc", Tags: tags(with(mine(), api.TagManaged, "true")...)},
			mode: spec.ModeAutomatic, want: false,
		},
		{
			name: "recorded but never carried the ownership marker",
			obj:  Object{Ref: "svc", Tags: tags(mine()...), Recorded: true},
			mode: spec.ModeAutomatic, want: false,
		},
		{
			name: "handed over to the customer: Odigos created it, they now deploy it",
			obj:  Object{Ref: "svc", Tags: tags(with(mine(), api.TagManaged, "true")...), Recorded: true},
			mode: spec.ModeManual, want: false,
		},
		{
			name: "capability removed entirely",
			obj:  Object{Ref: "svc", Tags: tags(with(mine(), api.TagManaged, "true")...), Recorded: true},
			mode: "", want: false,
		},
		{
			name: "another connector's Collector in the same account",
			obj:  Object{Ref: "svc", Tags: tags(with(mine(), api.TagConnector, "other", api.TagConnectorID, "uid-9", api.TagManaged, "true")...), Recorded: true},
			mode: spec.ModeAutomatic, want: false,
		},
		{
			name: "a cluster the customer named in the connector configuration",
			obj:  Object{Ref: "cluster", Tags: tags(with(mine(), api.TagManaged, "true")...), CustomerNamed: true, Recorded: true},
			mode: spec.ModeAutomatic, want: false,
		},
	}
	for _, tc := range cases {
		if got := MayDelete(tc.obj, self, tc.mode); got != tc.want {
			t.Errorf("%s: MayDelete = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestCustomerResourcesAreNeverDeletableAsManaged is the acceptance criterion itself, stated once over every
// mode: a stack a customer applied from a published artifact must not become deletable through ANY combination
// of mode, adoption, recording and stray tags. Everything else in this package is machinery for this.
func TestCustomerResourcesAreNeverDeletableAsManaged(t *testing.T) {
	customerStacks := map[string]api.CollectorTags{
		"applied from an artifact":                   tags(with(mine(), api.TagIaC, `{"v":1,"source":"cloudformation"}`)...),
		"artifact stack with a pasted ownership tag": tags(with(mine(), api.TagIaC, `{"v":1}`, api.TagManaged, "true")...),
		"built by hand, tagged for this connector":   tags(mine()...),
		"built by hand, untagged":                    tags(),
	}
	for name, tg := range customerStacks {
		for _, mode := range []string{spec.ModeAutomatic, spec.ModeManual, ""} {
			// Recorded is forced on: the strongest case against the gate is a stack the connector has already
			// adopted and is actively converging.
			obj := Object{Ref: "svc", Tags: tg, Recorded: true}
			if MayDelete(obj, self, mode) {
				t.Errorf("%s in %q mode is deletable; a customer's Collector must never be removed as managed infrastructure", name, mode)
			}
		}
	}
}

// TestClassifyDistinguishesTheFourCells: mode and origin are independent, and the four combinations must be
// distinguishable by a reader — the report is where "Odigos will delete this at teardown" and "Odigos will
// leave this behind" have to differ.
func TestClassifyDistinguishesTheFourCells(t *testing.T) {
	odigos := tags(with(mine(), api.TagManaged, "true")...)
	customer := tags(with(mine(), api.TagIaC, `{"v":1}`)...)
	cases := []struct {
		name              string
		obj               Object
		mode              string
		wantOrigin        Origin
		mutable, deletion bool
	}{
		{"managed", Object{Tags: odigos, Recorded: true}, spec.ModeAutomatic, OriginOdigos, true, true},
		{"adopted", Object{Tags: customer, Recorded: true}, spec.ModeAutomatic, OriginCustomer, true, false},
		{"handed over", Object{Tags: odigos, Recorded: true}, spec.ModeManual, OriginOdigos, false, false},
		{"the customer's", Object{Tags: customer, Recorded: true}, spec.ModeManual, OriginCustomer, false, false},
	}
	for _, tc := range cases {
		got := Classify(tc.obj, self, tc.mode)
		if got.Origin != tc.wantOrigin || got.Mutable != tc.mutable || got.Deletable != tc.deletion {
			t.Errorf("%s: origin=%q mutable=%v deletable=%v, want origin=%q mutable=%v deletable=%v",
				tc.name, got.Origin, got.Mutable, got.Deletable, tc.wantOrigin, tc.mutable, tc.deletion)
		}
	}
}

// TestPartitionKeepsTheTeardownSetHonest: a teardown pass acts on the deletable half and REPORTS the other, so
// leaked infrastructure is visible. Nothing may be dropped from both halves.
func TestPartitionKeepsTheTeardownSetHonest(t *testing.T) {
	objs := []Object{
		{Ref: "service", Tags: tags(with(mine(), api.TagManaged, "true")...), Recorded: true},
		{Ref: "nlb", Tags: tags(with(mine(), api.TagManaged, "true")...), Recorded: true},
		{Ref: "customer-cluster", Tags: tags(mine()...), CustomerNamed: true, Recorded: true},
		{Ref: "their-collector", Tags: tags(with(mine(), api.TagConnector, "other", api.TagConnectorID, "uid-9", api.TagManaged, "true")...), Recorded: true},
	}
	deletable, retained := Partition(objs, self, spec.ModeAutomatic)
	if len(deletable) != 2 || deletable[0].Ref != "service" || deletable[1].Ref != "nlb" {
		t.Errorf("deletable = %v, want the two objects this connector created", refs(deletable))
	}
	if len(retained) != 2 {
		t.Errorf("retained = %v, want the customer's cluster and the other connector's Collector", refs(retained))
	}
	if len(deletable)+len(retained) != len(objs) {
		t.Fatal("partition dropped an object; a teardown candidate must be either deleted or reported")
	}
}

// TestTeardownInManualModeTouchesNothing is the manual-mode test in pure form: delete the connector while
// a type is manual and nothing in the customer's account is a candidate, whatever it is tagged with.
func TestTeardownInManualModeTouchesNothing(t *testing.T) {
	objs := []Object{
		{Ref: "service", Tags: tags(with(mine(), api.TagManaged, "true")...), Recorded: true},
		{Ref: "nlb", Tags: tags(with(mine(), api.TagIaC, `{"v":1}`)...), Recorded: true},
	}
	deletable, retained := Partition(objs, self, spec.ModeManual)
	if len(deletable) != 0 {
		t.Errorf("manual-mode teardown would delete %v; it must delete nothing", refs(deletable))
	}
	if len(retained) != len(objs) {
		t.Errorf("retained %d of %d objects; everything left behind must be reported", len(retained), len(objs))
	}
}

func refs(objs []Object) []string {
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		out = append(out, o.Ref)
	}
	return out
}
