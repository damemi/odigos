package instrumentor

import (
	"maps"
	"strings"
	"testing"

	"github.com/odigos-io/odigos/connectors/anypoint"
	domaindest "github.com/odigos-io/odigos/connectors/base/domain/destination"
	store "github.com/odigos-io/odigos/connectors/providers/mulesoft/pkg/workload/store"
)

func version(t *testing.T, s string) anypoint.MuleVersion {
	t.Helper()
	v, ok := anypoint.ParseMuleVersion(s)
	if !ok {
		t.Fatalf("parse %q", s)
	}
	return v
}

var grpcDest = domaindest.OTLPTarget{Endpoint: "collector.example.com:4317", Traces: true, Logs: true, Metrics: true}

func workload(t *testing.T, ver string) Workload {
	return Workload{ServiceName: "order-api", EnvironmentName: "Production", OrgID: "org-1", TargetType: "shared-space", Version: version(t, ver)}
}

func TestDesiredPropertiesGRPC(t *testing.T) {
	d := DesiredProperties(workload(t, "4.12.0:2e-java17"), grpcDest, 1.0)
	for k, want := range map[string]string{
		PropTracesEnabled:  "true",
		PropTracesType:     "GRPC",
		PropTracesEndpoint: "https://collector.example.com:4317",
		PropLogsEndpoint:   "https://collector.example.com:4317",
		PropMetricsEnabled: "true",
		PropSampler:        "parentbased_traceidratio",
		PropSamplerArg:     "1.0",
		PropServiceName:    "order-api",
		PropLogCorrelation: "true",
	} {
		if got := d.Props[k]; got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if d.Attributes != "deployment.environment.name=Production,mulesoft.org.id=org-1,mulesoft.target.type=shared-space" {
		t.Errorf("attributes = %q", d.Attributes)
	}

	insecure := grpcDest
	insecure.Insecure = true
	if got := DesiredProperties(workload(t, "4.12.0"), insecure, 1).Props[PropTracesEndpoint]; got != "http://collector.example.com:4317" {
		t.Errorf("insecure endpoint = %q, want the http scheme (a bare host:port exports nowhere)", got)
	}
}

func TestDesiredPropertiesHTTPUsesPerSignalPaths(t *testing.T) {
	dest := domaindest.OTLPTarget{HTTPEndpoint: "https://otlp.example.com/", Traces: true, Logs: true}
	d := DesiredProperties(workload(t, "4.12.0"), dest, 0.25)
	if d.Props[PropTracesType] != "HTTP" || d.Props[PropTracesEndpoint] != "https://otlp.example.com/v1/traces" {
		t.Errorf("traces = %s %s", d.Props[PropTracesType], d.Props[PropTracesEndpoint])
	}
	if d.Props[PropLogsEndpoint] != "https://otlp.example.com/v1/logs" {
		t.Errorf("logs endpoint = %s", d.Props[PropLogsEndpoint])
	}
	if d.Props[PropSamplerArg] != "0.25" {
		t.Errorf("sampler arg = %s", d.Props[PropSamplerArg])
	}
	if _, ok := d.Props[PropMetricsEnabled]; ok {
		t.Error("metrics must not be enabled when the destination does not carry them")
	}
}

func TestMetricsNeedMule412(t *testing.T) {
	if _, ok := DesiredProperties(workload(t, "4.11.2:1e-java17"), grpcDest, 1).Props[PropMetricsEnabled]; ok {
		t.Error("Mule 4.11 cannot export metrics; enabling them would be a property the runtime ignores")
	}
	if Eligible(version(t, "4.10.3")) || !Eligible(version(t, "4.11.0")) {
		t.Error("Direct Telemetry Stream starts at Mule 4.11")
	}
}

// TestApplyRollbackRestoresTheExactPriorProperties is the property rollback rests on: whatever the application
// carried before, including a customer's own resource attributes and unrelated properties, comes back verbatim.
func TestApplyRollbackRestoresTheExactPriorProperties(t *testing.T) {
	before := map[string]string{
		"inventory.url":          "http://inventory-api",
		PropResourceAttributes:   "team=payments",
		PropLogCorrelation:       "false",
		"mule.openTelemetry.foo": "customer-tuning",
	}
	d := DesiredProperties(workload(t, "4.12.0"), grpcDest, 1)
	after, err := Apply(before, d, "", testDeployment)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(after[PropResourceAttributes], "team=payments,deployment.environment.name=") {
		t.Errorf("resource attributes = %q, want ours appended to the customer's", after[PropResourceAttributes])
	}
	if after["inventory.url"] != "http://inventory-api" || after["mule.openTelemetry.foo"] != "customer-tuning" {
		t.Error("unrelated properties must be left alone")
	}
	if after[store.PropOdigosManaged] == "" {
		t.Fatal("the ownership marker must be written")
	}

	restored, ok := Rollback(after, testDeployment)
	if !ok {
		t.Fatal("rollback refused a connector-instrumented application")
	}
	if !maps.Equal(restored, before) {
		t.Errorf("rollback = %v\nwant %v", restored, before)
	}
}

// TestApplyIsIdempotent: a PATCH redeploys the application, so re-applying to an already-instrumented
// application must produce exactly what it already has.
func TestApplyIsIdempotent(t *testing.T) {
	before := map[string]string{PropResourceAttributes: "team=payments"}
	d := DesiredProperties(workload(t, "4.12.0"), grpcDest, 1)
	once, err := Apply(before, d, "", testDeployment)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := Apply(once, d, once[store.PropOdigosManaged], testDeployment)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(once, twice) {
		t.Errorf("second apply changed the properties:\n%v\n%v", once, twice)
	}
}

func TestDroppingASignalRestoresItsPriorValue(t *testing.T) {
	before := map[string]string{}
	once, _ := Apply(before, DesiredProperties(workload(t, "4.12.0"), grpcDest, 1), "", testDeployment)
	tracesOnly := domaindest.OTLPTarget{Endpoint: grpcDest.Endpoint, Traces: true}
	next, _ := Apply(once, DesiredProperties(workload(t, "4.12.0"), tracesOnly, 1), once[store.PropOdigosManaged], testDeployment)
	for _, k := range []string{PropLogsEnabled, PropLogsEndpoint, PropMetricsEnabled} {
		if _, ok := next[k]; ok {
			t.Errorf("%s still set after the destination stopped carrying the signal", k)
		}
	}
}

func TestConflicts(t *testing.T) {
	if got := conflicts(map[string]string{PropTracesEndpoint: "x", PropResourceAttributes: "a=b"}, nil); len(got) != 1 || got[0] != PropTracesEndpoint {
		t.Errorf("conflicts = %v, want only the endpoint (attributes are merged, not owned)", got)
	}
	if got := conflicts(nil, []string{PropLogsEndpoint}); len(got) != 1 {
		t.Errorf("a secure property among the managed keys must conflict: its value cannot be snapshotted, got %v", got)
	}
	if got := conflicts(map[string]string{store.PropOdigosIaC: "{}"}, nil); len(got) != 1 {
		t.Errorf("a customer's IaC marker must block automatic instrumentation, got %v", got)
	}
	if got := conflicts(map[string]string{PropLogCorrelation: "true", "other": "x"}, nil); len(got) != 0 {
		t.Errorf("conflicts = %v, want none", got)
	}
}

func TestRollbackRefusesWithoutOurMarker(t *testing.T) {
	if _, ok := Rollback(map[string]string{store.PropOdigosIaC: `{"v":1}`}, testDeployment); ok {
		t.Error("an application the customer instrumented must never be rolled back")
	}
	if _, ok := Rollback(map[string]string{store.PropOdigosManaged: "not json"}, testDeployment); ok {
		t.Error("an unreadable marker proves nothing about the prior values")
	}
}

const testDeployment = "dep-1"

// TestCustomerAttributeEditsAreKept: the resource attributes are merged against the live value, so an attribute
// the customer adds after instrumentation causes no redeploy and survives the rollback, and a key of ours the
// customer had set before comes back with their value.
func TestCustomerAttributeEditsAreKept(t *testing.T) {
	before := map[string]string{PropResourceAttributes: "deployment.environment.name=prod-eu,team=payments"}
	d := DesiredProperties(workload(t, "4.12.0"), grpcDest, 1)
	once, err := Apply(before, d, "", testDeployment)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(once[PropResourceAttributes], "prod-eu") {
		t.Errorf("attributes = %q: our deployment.environment.name must replace the customer's while instrumented", once[PropResourceAttributes])
	}

	edited := maps.Clone(once)
	edited[PropResourceAttributes] = "region=eu," + once[PropResourceAttributes]
	again, err := Apply(edited, d, edited[store.PropOdigosManaged], testDeployment)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(again, edited) {
		t.Errorf("a customer attribute edit must not cause a redeploy:\n%v\n%v", edited, again)
	}

	restored, ok := Rollback(edited, testDeployment)
	if !ok {
		t.Fatal("rollback refused")
	}
	got := map[string]bool{}
	for _, p := range splitAttributes(restored[PropResourceAttributes]) {
		got[p] = true
	}
	for _, want := range []string{"region=eu", "team=payments", "deployment.environment.name=prod-eu"} {
		if !got[want] {
			t.Errorf("after rollback attributes = %q, missing %q", restored[PropResourceAttributes], want)
		}
	}
	if len(got) != 3 {
		t.Errorf("after rollback attributes = %q, want exactly the customer's three", restored[PropResourceAttributes])
	}

	// Untouched since instrumentation: the prior value comes back byte for byte.
	exact, _ := Rollback(once, testDeployment)
	if exact[PropResourceAttributes] != before[PropResourceAttributes] {
		t.Errorf("rollback = %q, want %q", exact[PropResourceAttributes], before[PropResourceAttributes])
	}
}

// TestMarkerFromAnotherDeploymentGrantsNothing: promoting an application copies its properties, marker included.
// The copy's snapshot describes the source application, so it must neither be applied over nor rolled back.
func TestMarkerFromAnotherDeploymentGrantsNothing(t *testing.T) {
	d := DesiredProperties(workload(t, "4.12.0"), grpcDest, 1)
	promoted, err := Apply(map[string]string{}, d, "", "dep-sandbox")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(promoted, d, promoted[store.PropOdigosManaged], "dep-production"); err == nil {
		t.Error("a copied marker must block instrumentation of the other deployment")
	}
	if _, ok := Rollback(promoted, "dep-production"); ok {
		t.Error("a copied marker must not be rolled back on the other deployment")
	}
	if _, err := Apply(map[string]string{store.PropOdigosManaged: "{broken"}, d, "{broken", testDeployment); err == nil {
		t.Error("an unreadable marker must block instrumentation instead of being re-snapshotted")
	}
}

// TestAPIManagerPairingRidesOnTheSpans: a paired application's resource attributes name its API Manager
// instance; when the pairing goes away the stale pairs are removed rather than left on every span; and a
// rollback takes them out with the rest of ours.
func TestAPIManagerPairingRidesOnTheSpans(t *testing.T) {
	current := map[string]string{"inventory.url": "http://inventory-api", PropResourceAttributes: "team=orders"}
	w := Workload{ServiceName: "order-api", EnvironmentName: "Production", OrgID: "org-1", TargetType: "shared-space", Version: version(t, "4.12.0"),
		API: &store.APIRef{ID: "18977143", AssetID: "order-api", Name: "Order API", Version: "v1"}}

	paired, err := Apply(current, DesiredProperties(w, grpcDest, 1), "", testDeployment)
	if err != nil {
		t.Fatal(err)
	}
	attrs := paired[PropResourceAttributes]
	for _, want := range []string{"team=orders", "mulesoft.api.id=18977143", "mulesoft.api.asset.id=order-api", "mulesoft.api.version=v1"} {
		if !containsAll(attrs, want, ",") {
			t.Errorf("attributes %q missing %s", attrs, want)
		}
	}

	w.API = nil
	unpaired, err := Apply(paired, DesiredProperties(w, grpcDest, 1), paired[store.PropOdigosManaged], testDeployment)
	if err != nil {
		t.Fatal(err)
	}
	if got := unpaired[PropResourceAttributes]; strings.Contains(got, "mulesoft.api.") || !strings.Contains(got, "team=orders") {
		t.Errorf("attributes after the pairing went away = %q, want the API pairs gone and the customer's kept", got)
	}

	restored, owned := Rollback(paired, testDeployment)
	if !owned || !maps.Equal(restored, current) {
		t.Errorf("rollback of a paired application = %v, want %v", restored, current)
	}
}

func TestAttributeValuesCannotBreakTheList(t *testing.T) {
	if got := attributeValue(" Order API, v1=x "); got != "Order_API__v1_x" {
		t.Errorf("attributeValue = %q", got)
	}
}
