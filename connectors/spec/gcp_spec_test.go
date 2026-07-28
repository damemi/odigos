package spec

import "testing"

func TestGCPSpecLoads(t *testing.T) {
	s, ok := Get("gcp")
	if !ok {
		t.Fatal("gcp spec should be registered from spec/data/gcp.yaml")
	}
	if s.Provider != "gcp" {
		t.Fatalf("provider = %q, want gcp", s.Provider)
	}

	if !s.Workloads.Discovery.Enabled {
		t.Error("workload discovery feature should be enabled")
	}
	if !s.ComputePlatforms.Discovery.Enabled {
		t.Error("compute-platform discovery feature should be enabled")
	}
	if !containsStr(s.Auth.Permissions, "resourcemanager.projects.get") {
		t.Errorf("auth baseline should include resourcemanager.projects.get, got %v", s.Auth.Permissions)
	}
	if len(s.ConnectorFields()) != 1 {
		t.Errorf("connector fields = %d, want 1 (projectId)", len(s.ConnectorFields()))
	}

	methods := s.AuthMethods()
	if len(methods) != 2 {
		t.Fatalf("auth methods = %d, want 2 (workload-identity, service-account-key)", len(methods))
	}
	saKey, ok := s.AuthMethodByID("service-account-key")
	if !ok || len(saKey.Fields) != 1 {
		t.Errorf("service-account-key method should declare 1 field, got %v", saKey.Fields)
	}

	credFields := s.CredentialFields()
	wantEnv := map[string]string{
		"credentialsJson": "ODIGOS_GCP_CREDENTIALS_JSON",
	}
	if len(credFields) != len(wantEnv) {
		t.Fatalf("credential fields = %d, want %d", len(credFields), len(wantEnv))
	}
	for _, f := range credFields {
		if want, ok := wantEnv[f.Name]; !ok || f.EnvVar != want {
			t.Errorf("credential field %q envVar = %q, want %q", f.Name, f.EnvVar, want)
		}
	}

	for _, name := range []string{"gcp.cloud-run"} {
		if a := s.Workloads.Discovery.PermissionsForType(name); len(a) == 0 {
			t.Errorf("workload discovery type %q should declare permissions", name)
		}
	}
	for _, name := range []string{"gcp.gke", "gcp.gce"} {
		if a := s.ComputePlatforms.Discovery.PermissionsForType(name); len(a) == 0 {
			t.Errorf("compute-platform discovery type %q should declare permissions", name)
		}
	}

	// Cloud Run instrumentation is implemented (agent injected into the service revision).
	if got := s.Workloads.Instrumentation.MethodForType("gcp.cloud-run"); got != MethodService {
		t.Errorf("gcp.cloud-run instrumentation method = %q, want %q", got, MethodService)
	}
	if s.UnimplementedInstrumentationKinds()["gcp.cloud-run"] {
		t.Error("gcp.cloud-run instrumentation should be implemented")
	}

	// Installation is not supported yet (declares no types).
	if s.ComputePlatforms.Installation.Enabled {
		t.Error("installation feature should be disabled (not supported yet)")
	}
	if len(s.ComputePlatforms.Installation.Types) != 0 {
		t.Errorf("installation feature should declare no types, got %v", s.ComputePlatforms.Installation.Types)
	}
}

func TestGCPCapabilityResolution(t *testing.T) {
	s := MustGet("gcp")

	caps := Capabilities{
		Workloads: []WorkloadCapability{
			{Type: "gcp.cloud-run", Discovery: true, Instrumentation: true},
		},
		ComputePlatforms: []ComputePlatformCapability{
			{Type: "gcp.gke", Discovery: true},
			{Type: "gcp.gce", Discovery: true},
		},
	}

	if got := s.ActiveWorkloadDiscoveryTypes(caps); len(got) != 1 || got[0].Name != "gcp.cloud-run" {
		t.Errorf("active workload discovery types = %v, want [gcp.cloud-run]", got)
	}
	if got := s.ActiveComputePlatformDiscoveryTypes(caps); len(got) != 2 {
		t.Errorf("active compute-platform discovery types = %v, want 2", got)
	}
	// Cloud Run instrumentation is implemented, so it is actively instrumented when the capability is on.
	if got := s.ActiveInstrumentationTypes(caps); len(got) != 1 || got[0].Name != "gcp.cloud-run" {
		t.Errorf("active instrumentation types = %v, want [gcp.cloud-run]", got)
	}
	if cp := s.ActiveComputePlatformTypes(caps); len(cp) != 0 {
		t.Errorf("active compute-platform types = %v, want none (installation not supported)", cp)
	}

	perms := s.RequiredPermissions(caps)
	for _, want := range []string{"resourcemanager.projects.get", "run.services.list", "run.services.update", "container.clusters.list", "compute.instances.list"} {
		if !containsStr(perms, want) {
			t.Errorf("required permissions %v missing %q", perms, want)
		}
	}
}

func TestGCPAuthResolve(t *testing.T) {
	auth := MustGet("gcp").Auth

	// Ambient (no creds) resolves to workload-identity.
	chain, err := auth.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve ambient: %v", err)
	}
	if len(chain) != 1 || chain[0].ID != "workload-identity" {
		t.Errorf("ambient chain = %v, want [workload-identity]", chain)
	}

	// A service account key resolves to service-account-key.
	chain, err = auth.Resolve(map[string]string{"credentialsJson": "{}"})
	if err != nil {
		t.Fatalf("resolve service-account-key: %v", err)
	}
	if len(chain) != 1 || chain[0].ID != "service-account-key" {
		t.Errorf("service-account-key chain = %v, want [service-account-key]", chain)
	}

	// An unknown field is rejected.
	if _, err := auth.Resolve(map[string]string{"accessKeyId": "x"}); err == nil {
		t.Error("expected an error for an unknown credential field")
	}
}
