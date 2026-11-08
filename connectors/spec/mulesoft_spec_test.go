package spec

import (
	"strings"
	"testing"
)

func TestMuleSoftSpecLoads(t *testing.T) {
	s, ok := Get("mulesoft")
	if !ok {
		t.Fatal("mulesoft spec should be registered from spec/data/mulesoft.yaml")
	}
	if !s.Workloads.Discovery.Enabled {
		t.Error("workload discovery feature should be enabled")
	}
	// Mule apps run on MuleSoft-managed or Runtime Manager-reached platforms: nothing to discover or install.
	if s.ComputePlatforms.Discovery.Enabled || len(s.ComputePlatforms.Discovery.Types) != 0 {
		t.Error("compute-platform discovery should be disabled with no types")
	}
	if len(s.ConnectorFields()) != 1 || s.ConnectorFields()[0].Name != "organizationId" {
		t.Errorf("connector fields = %v, want [organizationId]", s.ConnectorFields())
	}

	wantEnv := map[string]string{
		"clientId":     "ANYPOINT_CLIENT_ID",
		"clientSecret": "ANYPOINT_CLIENT_SECRET",
	}
	credFields := s.CredentialFields()
	if len(credFields) != len(wantEnv) {
		t.Fatalf("credential fields = %d, want %d", len(credFields), len(wantEnv))
	}
	for _, f := range credFields {
		if want, ok := wantEnv[f.Name]; !ok || f.EnvVar != want {
			t.Errorf("credential field %q envVar = %q, want %q", f.Name, f.EnvVar, want)
		}
		if f.Name == "clientSecret" && !f.Secret {
			t.Error("clientSecret must be secret, or test-connection errors could echo it")
		}
		// No credential may choose the API host: test connection runs in Central's backend for any signed-in
		// user, and a host they supply would aim the backend's requests anywhere (SSRF).
		if strings.Contains(strings.ToLower(f.Name), "url") || strings.Contains(strings.ToLower(f.Name), "host") {
			t.Errorf("credential field %q looks like an API host override; the control plane is the only host selector", f.Name)
		}
	}

	for _, name := range []string{"mulesoft.cloudhub2-app", "mulesoft.private-space-app", "mulesoft.rtf-app", "mulesoft.hybrid-app", "mulesoft.omni-gateway"} {
		if len(s.Workloads.Discovery.PermissionsForType(name)) == 0 {
			t.Errorf("workload discovery type %q should declare permissions", name)
		}
	}
	for _, name := range []string{"mulesoft.cloudhub2-app", "mulesoft.private-space-app", "mulesoft.rtf-app", "mulesoft.omni-gateway"} {
		if got := s.Workloads.Instrumentation.MethodForType(name); got != MethodService {
			t.Errorf("%s instrumentation method = %q, want %q", name, got, MethodService)
		}
	}
	// Only CloudHub 2.0 shared-space instrumentation is verified against a real organization. Private spaces and
	// Runtime Fabric are built but stay declared unimplemented until they are, so nothing changes their apps.
	for name, want := range map[string]bool{
		"mulesoft.cloudhub2-app":     true,
		"mulesoft.omni-gateway":      true,
		"mulesoft.private-space-app": false,
		"mulesoft.rtf-app":           false,
	} {
		it, ok := s.Workloads.Instrumentation.findType(name)
		if !ok || it.IsImplemented() != want {
			t.Errorf("%s instrumentation implemented = %v (declared %v), want %v", name, it.IsImplemented(), ok, want)
		}
	}
	// Hybrid Direct Telemetry Stream is configured per server, not per app, so it is not a per-app instrumentation.
	if got := s.Workloads.Instrumentation.MethodForType("mulesoft.hybrid-app"); got != "" {
		t.Errorf("mulesoft.hybrid-app must not declare instrumentation, got method %q", got)
	}
}

func TestMuleSoftBootstrapRegionIsTheControlPlane(t *testing.T) {
	cfg := MustGet("mulesoft").Connector.BootstrapRegion
	for _, plane := range []string{"us", "eu"} {
		if got, err := cfg.Resolve(plane); err != nil || got != plane {
			t.Errorf("Resolve(%q) = %q, %v", plane, got, err)
		}
	}
	if _, err := cfg.Resolve("gov"); err == nil {
		t.Error("the Gov control plane is not supported and must be rejected")
	}
}

func TestMuleSoftCapabilityResolution(t *testing.T) {
	s := MustGet("mulesoft")
	auto := Capabilities{Workloads: []WorkloadCapability{
		{Type: "mulesoft.cloudhub2-app", Discovery: true, Instrumentation: true},
		{Type: "mulesoft.private-space-app", Discovery: true, Instrumentation: true},
		{Type: "mulesoft.rtf-app", Discovery: true, Instrumentation: true},
		{Type: "mulesoft.hybrid-app", Discovery: true},
	}}
	if got := s.ActiveWorkloadDiscoveryTypes(auto); len(got) != 4 {
		t.Errorf("active discovery types = %v, want 4", got)
	}
	// Asking for private-space or Runtime Fabric instrumentation discovers them and instruments nothing.
	if got := typeNames(s.ActiveInstrumentationTypes(auto)); len(got) != 1 || got[0] != "mulesoft.cloudhub2-app" {
		t.Errorf("active instrumentation types = %v, want only mulesoft.cloudhub2-app", got)
	}
	if got := typeNames(s.InstrumentationTypes(auto)); len(got) != 1 || got[0] != "mulesoft.cloudhub2-app" {
		t.Errorf("instrumentation types in either mode = %v, want only mulesoft.cloudhub2-app (no manual instructions for the others)", got)
	}
	if warnings := s.UnsupportedCapabilities(auto); len(warnings) != 2 {
		t.Errorf("warnings = %v, want one each for private-space and Runtime Fabric instrumentation", warnings)
	}
	perms := s.RequiredPermissions(auto)
	for _, want := range []string{"View Environment", "Read Applications", "Read Runtime Fabrics", "Read Servers", "Create Applications"} {
		if !containsStr(perms, want) {
			t.Errorf("automatic-mode permissions %v missing %q", perms, want)
		}
	}

	manual := Capabilities{Workloads: []WorkloadCapability{{Type: "mulesoft.cloudhub2-app", Discovery: true}}}
	if containsStr(s.RequiredPermissions(manual), "Create Applications") {
		t.Error("manual mode must not ask for the scope that modifies applications")
	}
}

func TestMuleSoftAuthResolve(t *testing.T) {
	auth := MustGet("mulesoft").Auth
	chain, err := auth.Resolve(map[string]string{"clientId": "id", "clientSecret": "secret"})
	if err != nil || len(chain) != 1 || chain[0].ID != "connected-app" {
		t.Fatalf("resolve = %v, %v; want [connected-app]", chain, err)
	}
	if _, err := auth.Resolve(map[string]string{"clientId": "id"}); err == nil {
		t.Error("a client id without a secret must be rejected")
	}
}

// TestMuleSoftInstrumentationArtifactsAreTheSamePropertySet: the three delivery variants spell one property set
// three ways. A key added to one and forgotten in another hands a customer a different configuration depending
// on the tool they use, which conformance then reports as drift.
func TestMuleSoftInstrumentationArtifactsAreTheSamePropertySet(t *testing.T) {
	keys := []string{
		"ODIGOS_IAC",
		"mule.openTelemetry.tracer.exporter.enabled",
		"mule.openTelemetry.tracer.exporter.type",
		"mule.openTelemetry.tracer.exporter.endpoint",
		"mule.openTelemetry.tracer.exporter.sampler",
		"mule.openTelemetry.tracer.exporter.sampler.arg",
		"mule.openTelemetry.logging.exporter.enabled",
		"mule.openTelemetry.logging.exporter.type",
		"mule.openTelemetry.logging.exporter.endpoint",
		"mule.openTelemetry.meter.exporter.enabled",
		"mule.openTelemetry.meter.exporter.type",
		"mule.openTelemetry.meter.exporter.endpoint",
		"mule.openTelemetry.exporter.resource.service.name",
		"mule.openTelemetry.exporter.resource.attributes",
		"mule.put.trace.id.and.span.id.in.mdc",
	}
	s := MustGet("mulesoft")
	for _, typ := range []string{"mulesoft.cloudhub2-app", "mulesoft.private-space-app", "mulesoft.rtf-app"} {
		arts := s.InstrumentationArtifacts(typ)
		if len(arts) != 3 {
			t.Fatalf("%s: %d artifacts, want 3 (runtime-manager, maven, anypoint-cli)", typ, len(arts))
		}
		for _, a := range arts {
			for _, k := range keys {
				if !strings.Contains(a.Template, k) {
					t.Errorf("%s/%s: template is missing %s", typ, a.ID, k)
				}
			}
			// The automatic-mode marker is the connector's own rollback record; publishing it would invite a
			// customer to claim a rollback nothing recorded.
			if strings.Contains(a.Template, "ODIGOS_MANAGED") {
				t.Errorf("%s/%s: template carries ODIGOS_MANAGED, which only the connector may write", typ, a.ID)
			}
			if !strings.Contains(strings.Join(a.Prerequisites, "\n"), "https://docs.odigos.io/cloud-connectors/add-connectors/add-mulesoft-connector") {
				t.Errorf("%s/%s: prerequisites must link the MuleSoft connector docs", typ, a.ID)
			}
		}
	}
}

// TestMuleSoftGatewayInstructionsCarryTheTracingSettings: both gateway variants publish the provider, the
// destination placeholders and the odigos.iac label the observer looks for. A variant without the label would
// leave a correctly configured gateway reported as never instrumented.
func TestMuleSoftGatewayInstructionsCarryTheTracingSettings(t *testing.T) {
	arts := MustGet("mulesoft").InstrumentationArtifacts("mulesoft.omni-gateway")
	if len(arts) != 2 {
		t.Fatalf("%d gateway artifacts, want 2 (runtime-manager, configuration-resource)", len(arts))
	}
	for _, a := range arts {
		for _, want := range []string{"opentelemetry", "<OTLP_SERVICE_TYPE>", "<OTLP_TRACES_ENDPOINT>", "<ODIGOS_SOURCE_NAME>", "odigos.iac", "omni-gateway"} {
			if !strings.Contains(a.Template, want) {
				t.Errorf("%s: template is missing %s", a.ID, want)
			}
		}
		if strings.Contains(a.Template, "odigos.managed") {
			t.Errorf("%s: template carries odigos.managed, which only the connector may set", a.ID)
		}
	}
}

// API Manager pairing is optional: the scope is in the published list, but a connector that only discovers
// applications must not need anything that could change one.
func TestMuleSoftAPIManagerScopeIsReadOnly(t *testing.T) {
	s := MustGet("mulesoft")
	caps := Capabilities{Workloads: []WorkloadCapability{{Type: "mulesoft.cloudhub2-app", Discovery: true}}}
	if !containsStr(s.RequiredPermissions(caps), "View APIs Configuration") {
		t.Error("discovery should ask for View APIs Configuration so applications can be paired with their API")
	}
	for _, p := range s.RequiredPermissions(caps) {
		if strings.HasPrefix(p, "Manage") || strings.HasPrefix(p, "Create") {
			t.Errorf("discovery-only connector asks for %q", p)
		}
	}
}
