package store

import "testing"

// Only this connector's record for this deployment makes an application the connector's. Everything else with
// an Odigos marker is instrumented but not Odigos's to change, which is what OriginExternal means to the
// reconciler.
func TestMarkerOriginIsBoundToTheDeployment(t *testing.T) {
	const dep = "dep-1"
	ours := `{"v":1,"dep":"dep-1","prevProps":{"mule.openTelemetry.tracer.exporter.enabled":null}}`
	cases := []struct {
		name   string
		props  map[string]string
		secure []string
		want   string
	}{
		{"no marker", map[string]string{"inventory.url": "http://x"}, nil, ""},
		{"this deployment's record", map[string]string{PropOdigosManaged: ours}, nil, OriginConnector},
		{"a record that predates the deployment field", map[string]string{PropOdigosManaged: `{"v":1,"prevProps":{}}`}, nil, OriginConnector},
		{"copied from another deployment", map[string]string{PropOdigosManaged: `{"v":1,"dep":"dep-2","prevProps":{}}`}, nil, OriginExternal},
		{"edited until unreadable", map[string]string{PropOdigosManaged: "true"}, nil, OriginExternal},
		{"no prevProps", map[string]string{PropOdigosManaged: `{"v":1,"dep":"dep-1"}`}, nil, OriginExternal},
		{"set as a secure property", nil, []string{PropOdigosManaged}, OriginExternal},
		{"the customer's IaC marker", map[string]string{PropOdigosIaC: `{"v":1,"lang":"mule"}`}, nil, OriginExternal},
		{"both, ours wins", map[string]string{PropOdigosManaged: ours, PropOdigosIaC: "x"}, nil, OriginConnector},
	}
	for _, tc := range cases {
		if got := MarkerOrigin(tc.props, tc.secure, dep); got != tc.want {
			t.Errorf("%s: origin = %q, want %q", tc.name, got, tc.want)
		}
	}
}
