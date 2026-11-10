package api

import "testing"

// TestParseIaCMarker pins the decoder's two deliberate leniencies, both of which exist because this value is
// authored by a customer's deployment definition rather than by the connector: a malformed payload degrades to
// "nothing recorded" instead of failing an observation, and a partial one yields the fields it does carry. What
// it must never do is invent a layer, since a wrong layer is reported to a customer as drift they cannot
// explain.
func TestParseIaCMarker(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantOK  bool
		wantSrc string
		wantLyr string
	}{
		{"absent", "", false, "", ""},
		{"malformed", "{not json", false, "", ""},
		{"full payload", `{"v":1,"source":"cdk","version":"v0.0.11","layer":"arn:layer:6"}`, true, "cdk", "arn:layer:6"},
		// What the published manual instructions tell a customer to paste: version and layer, no attribution.
		{"minimum a customer applies by hand", `{"v":1,"layer":"arn:layer:6"}`, true, "", "arn:layer:6"},
		// A platform with no layers, or an artifact that did not record one. The caller must see "" and report
		// the layer check as incomplete rather than resolve one and call the difference drift.
		{"no layer recorded", `{"v":1,"source":"terraform"}`, true, "terraform", ""},
	}
	for _, tc := range cases {
		m, ok := ParseIaCMarker(tc.raw)
		switch {
		case ok != tc.wantOK:
			t.Errorf("%s: ok = %v, want %v", tc.name, ok, tc.wantOK)
		case m.Source != tc.wantSrc:
			t.Errorf("%s: source = %q, want %q", tc.name, m.Source, tc.wantSrc)
		case m.Layer != tc.wantLyr:
			t.Errorf("%s: layer = %q, want %q", tc.name, m.Layer, tc.wantLyr)
		}
	}
}

// TestIaCMarkerIsNotTheOwnershipMarker: the two variables must stay distinct names. Collapsing them is the
// change that would make an IaC-instrumented workload eligible for a rollback with no snapshot to restore
// from, which is the failure the split exists to prevent.
func TestIaCMarkerIsNotTheOwnershipMarker(t *testing.T) {
	if EnvOdigosIaC == "ODIGOS_MANAGED" {
		t.Fatal("the IaC marker and the ownership marker are the same variable, so attribution now grants mutation rights")
	}
	if EnvOdigosIaC != "ODIGOS_IAC" {
		t.Errorf("EnvOdigosIaC = %q: the name is a published contract with every Odigos IaC artifact", EnvOdigosIaC)
	}
}
