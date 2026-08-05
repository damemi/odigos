package spec

import (
	"strings"
	"testing"
)

func TestAzureSpecLoads(t *testing.T) {
	s, ok := Get("azure")
	if !ok {
		t.Fatal("azure spec should be registered from spec/data/azure.yaml")
	}
	if s.Provider != "azure" {
		t.Fatalf("provider = %q, want azure", s.Provider)
	}

	if !s.Workloads.Discovery.Enabled {
		t.Error("workload discovery feature should be enabled")
	}
	if !s.ComputePlatforms.Discovery.Enabled {
		t.Error("compute-platform discovery feature should be enabled")
	}
	if !containsStr(s.Auth.Permissions, "Microsoft.Resources/subscriptions/read") {
		t.Errorf("auth baseline should include Microsoft.Resources/subscriptions/read, got %v", s.Auth.Permissions)
	}
	if len(s.ConnectorFields()) != 1 {
		t.Errorf("connector fields = %d, want 1 (subscriptionId)", len(s.ConnectorFields()))
	}

	methods := s.AuthMethods()
	if len(methods) != 2 {
		t.Fatalf("auth methods = %d, want 2 (workload-identity, service-principal)", len(methods))
	}
	sp, ok := s.AuthMethodByID("service-principal")
	if !ok || len(sp.Fields) != 3 {
		t.Errorf("service-principal method should declare 3 fields, got %v", sp.Fields)
	}

	credFields := s.CredentialFields()
	wantEnv := map[string]string{
		"tenantId":     "AZURE_TENANT_ID",
		"clientId":     "AZURE_CLIENT_ID",
		"clientSecret": "AZURE_CLIENT_SECRET",
	}
	if len(credFields) != len(wantEnv) {
		t.Fatalf("credential fields = %d, want %d", len(credFields), len(wantEnv))
	}
	for _, f := range credFields {
		if want, ok := wantEnv[f.Name]; !ok || f.EnvVar != want {
			t.Errorf("credential field %q envVar = %q, want %q", f.Name, f.EnvVar, want)
		}
	}

	for _, name := range []string{"azure.container-apps"} {
		if a := s.Workloads.Discovery.PermissionsForType(name); len(a) == 0 {
			t.Errorf("workload discovery type %q should declare permissions", name)
		}
	}
	for _, name := range []string{"azure.aks", "azure.vm"} {
		if a := s.ComputePlatforms.Discovery.PermissionsForType(name); len(a) == 0 {
			t.Errorf("compute-platform discovery type %q should declare permissions", name)
		}
	}

	// Container Apps instrumentation is implemented (init-container agent injection).
	if got := s.Workloads.Instrumentation.MethodForType("azure.container-apps"); got != MethodService {
		t.Errorf("azure.container-apps instrumentation method = %q, want %q", got, MethodService)
	}
	if s.UnimplementedInstrumentationKinds()["azure.container-apps"] {
		t.Error("azure.container-apps instrumentation should be implemented")
	}

	// Installation is not supported yet (declares no types).
	if s.ComputePlatforms.Installation.Enabled {
		t.Error("installation feature should be disabled (not supported yet)")
	}
	if len(s.ComputePlatforms.Installation.Types) != 0 {
		t.Errorf("installation feature should declare no types, got %v", s.ComputePlatforms.Installation.Types)
	}
}

func TestAzurePolicyPreviewArtifact(t *testing.T) {
	s := MustGet("azure")

	arts := s.SetupArtifactsForSection("permissions")
	if len(arts) != 1 {
		t.Fatalf("permissions setup artifacts = %d, want 1 (policy-preview)", len(arts))
	}
	a := arts[0]
	if a.ID != "policy-preview" {
		t.Errorf("artifact id = %q, want policy-preview", a.ID)
	}
	if a.Format != "json" {
		t.Errorf("artifact format = %q, want json", a.Format)
	}
	if !strings.Contains(a.Template, `"Actions"`) {
		t.Errorf("template should declare an Actions block, got:\n%s", a.Template)
	}
	if !strings.Contains(a.Template, "{{permissions}}") {
		t.Errorf("template should carry the {{permissions}} placeholder, got:\n%s", a.Template)
	}
	if a.ItemTemplate != `"{{permission}}"` {
		t.Errorf("itemTemplate = %q, want %q", a.ItemTemplate, `"{{permission}}"`)
	}
}

func TestAzureCapabilityResolution(t *testing.T) {
	s := MustGet("azure")

	caps := Capabilities{
		Workloads: []WorkloadCapability{
			{Type: "azure.container-apps", Discovery: true, Instrumentation: true},
		},
		ComputePlatforms: []ComputePlatformCapability{
			{Type: "azure.aks", Discovery: true},
			{Type: "azure.vm", Discovery: true},
		},
	}

	if got := s.ActiveWorkloadDiscoveryTypes(caps); len(got) != 1 || got[0].Name != "azure.container-apps" {
		t.Errorf("active workload discovery types = %v, want [azure.container-apps]", got)
	}
	if got := s.ActiveComputePlatformDiscoveryTypes(caps); len(got) != 2 {
		t.Errorf("active compute-platform discovery types = %v, want 2", got)
	}
	// Container Apps instrumentation is implemented, so it appears in active instrumentation.
	if got := s.ActiveInstrumentationTypes(caps); len(got) != 1 || got[0].Name != "azure.container-apps" {
		t.Errorf("active instrumentation types = %v, want [azure.container-apps]", got)
	}
	if s.UnimplementedInstrumentationKinds()["azure.container-apps"] {
		t.Error("expected azure.container-apps to be implemented")
	}
	if cp := s.ActiveComputePlatformTypes(caps); len(cp) != 0 {
		t.Errorf("active compute-platform types = %v, want none (installation not supported)", cp)
	}

	perms := s.RequiredPermissions(caps)
	for _, want := range []string{
		"Microsoft.Resources/subscriptions/read",
		"Microsoft.App/containerApps/read",
		"Microsoft.App/containerApps/write",
		"Microsoft.App/managedEnvironments/join/action",
		"Microsoft.ContainerService/managedClusters/read",
		"Microsoft.Compute/virtualMachines/read",
	} {
		if !containsStr(perms, want) {
			t.Errorf("required permissions %v missing %q", perms, want)
		}
	}
}

func TestAzureAuthResolve(t *testing.T) {
	auth := MustGet("azure").Auth

	// Ambient (no creds) resolves to workload-identity.
	chain, err := auth.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve ambient: %v", err)
	}
	if len(chain) != 1 || chain[0].ID != "workload-identity" {
		t.Errorf("ambient chain = %v, want [workload-identity]", chain)
	}

	// A complete service principal resolves to service-principal.
	chain, err = auth.Resolve(map[string]string{
		"tenantId":     "00000000-0000-0000-0000-000000000001",
		"clientId":     "00000000-0000-0000-0000-000000000002",
		"clientSecret": "secret",
	})
	if err != nil {
		t.Fatalf("resolve service-principal: %v", err)
	}
	if len(chain) != 1 || chain[0].ID != "service-principal" {
		t.Errorf("service-principal chain = %v, want [service-principal]", chain)
	}

	// Incomplete service principal is rejected.
	if _, err := auth.Resolve(map[string]string{"tenantId": "x", "clientId": "y"}); err == nil {
		t.Error("expected an error for incomplete service-principal credentials")
	}

	// An unknown field is rejected.
	if _, err := auth.Resolve(map[string]string{"accessKeyId": "x"}); err == nil {
		t.Error("expected an error for an unknown credential field")
	}
}
