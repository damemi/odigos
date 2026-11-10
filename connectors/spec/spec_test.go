package spec

import "testing"

const sampleYAML = `
provider: sample
auth:
  permissions:
    - sample:GetIdentity
  methods:
    - id: default
      displayName: Default
      fields: []
workloads:
  discovery:
    enabled: true
    permissions:
      - sample:ListRegions
    types:
      - name: vm
        permissions:
          - sample:ListVMs
  instrumentation:
    enabled: false
    types:
      - name: vm
        method: agent
        permissions:
          - sample:UpdateVM
`

func TestEmbeddedDataLoads(t *testing.T) {
	providers := Providers()
	if len(providers) == 0 {
		t.Fatal("no embedded specs loaded from spec/data/")
	}
	if _, ok := Get("aws"); !ok {
		t.Fatalf("Providers() = %v, want aws registered from spec/data/aws.yaml", providers)
	}
}

func TestParseAndHelpers(t *testing.T) {
	s, err := Parse([]byte(sampleYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.Provider != "sample" {
		t.Fatalf("provider = %q, want sample", s.Provider)
	}

	caps := Capabilities{Workloads: []WorkloadCapability{{Type: "vm", Discovery: true, Instrumentation: true}}}

	// Discovery is enabled, so a vm discovery capability activates the vm discovery type.
	if got := s.ActiveDiscoveryTypes(caps); len(got) != 1 || got[0].Name != "vm" {
		t.Errorf("ActiveDiscoveryTypes = %v, want [vm]", got)
	}
	// Instrumentation feature is disabled -> no active instrumentation types even though the cap asks.
	if got := s.ActiveInstrumentationTypes(caps); got != nil {
		t.Errorf("ActiveInstrumentationTypes = %v, want nil (feature disabled)", got)
	}
	// A capability with discovery off contributes nothing.
	if got := s.ActiveDiscoveryTypes(Capabilities{Workloads: []WorkloadCapability{{Type: "vm"}}}); got != nil {
		t.Errorf("ActiveDiscoveryTypes(discovery off) = %v, want nil", got)
	}

	// RequiredPermissions unions auth baseline + discovery feature baseline + active type permissions.
	perms := s.RequiredPermissions(caps)
	for _, want := range []string{"sample:GetIdentity", "sample:ListRegions", "sample:ListVMs"} {
		if !contains(perms, want) {
			t.Errorf("RequiredPermissions = %v, want to include %q", perms, want)
		}
	}
	// Disabled instrumentation permissions are excluded.
	if contains(perms, "sample:UpdateVM") {
		t.Errorf("RequiredPermissions should not include disabled instrumentation permissions: %v", perms)
	}

	if got := s.Workloads.Instrumentation.MethodForType("vm"); got != MethodAgent {
		t.Errorf("MethodForType(vm) = %q, want %q", got, MethodAgent)
	}
}

func TestParseRequiresProvider(t *testing.T) {
	if _, err := Parse([]byte("displayName: foo\n")); err == nil {
		t.Error("Parse should reject a spec without provider")
	}
}

func TestParseRejectsBrokenCollectorDocumentTemplate(t *testing.T) {
	_, err := Parse([]byte("provider: sample\ncollectors:\n  instructions:\n    documents:\n      - id: terraform\n        variant: terraform\n        template: '{{if .ScalingAutomatic}}'\n"))
	if err == nil {
		t.Fatal("unclosed Collector document template was accepted")
	}
}

// TestParseRejectsUnknownFields locks in strict decoding. Without it a mistyped key is silently dropped and
// the connector runs with the default the author thought they had overridden — e.g. `permission:` instead of
// `permissions:` yields a type that requests no permissions at all, which only shows up as a denied API call
// in a customer account.
func TestParseRejectsUnknownFields(t *testing.T) {
	cases := map[string]string{
		"top level": "provider: sample\nprovidr: typo\n",
		"nested type": `
provider: sample
workloads:
  discovery:
    types:
      - name: vm
        permission:
          - sample:ListVMs
`,
		"auth field": `
provider: sample
auth:
  methods:
    - id: default
      fields:
        - name: token
          showInUI: true
`,
	}
	for name, y := range cases {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: Parse should reject an undeclared key", name)
		}
	}

	// The real provider YAMLs must keep parsing under strict decoding — this is what makes the rule
	// enforceable rather than aspirational (showInUi, for one, is declared precisely because they use it).
	for _, p := range Providers() {
		if _, ok := Get(p); !ok {
			t.Errorf("provider %q registered but not retrievable", p)
		}
	}
}

// TestValidateFeatureTypes covers the two-domain taxonomy rules enforced at parse time.
func TestValidateFeatureTypes(t *testing.T) {
	// Discovery-only is allowed: a discovery type with no instrumentation counterpart parses fine, and a
	// compute-platform discovery type with no installation counterpart parses fine.
	discoveryOnly := `
provider: sample
workloads:
  discovery:
    types:
      - name: vm
      - name: bucket   # new service: discovery only, instrumentation still being built
  instrumentation:
    enabled: false
    types:
      - name: vm
        method: agent
computePlatforms:
  discovery:
    types:
      - name: cluster
  installation:
    enabled: false
    types: []
`
	if _, err := Parse([]byte(discoveryOnly)); err != nil {
		t.Errorf("discovery-only type should be allowed, got: %v", err)
	}

	bad := []struct {
		name string
		yaml string
	}{
		{"instrumentation without discovery", `
provider: sample
workloads:
  discovery:
    types: [{name: vm}]
  instrumentation:
    types: [{name: vm, method: agent}, {name: orphan, method: sidecar}]
`},
		{"discovery type with method", `
provider: sample
workloads:
  discovery:
    types: [{name: vm, method: agent}]
`},
		{"instrumentation missing method", `
provider: sample
workloads:
  discovery:
    types: [{name: vm}]
  instrumentation:
    types: [{name: vm}]
`},
		{"instrumentation invalid method", `
provider: sample
workloads:
  discovery:
    types: [{name: vm}]
  instrumentation:
    types: [{name: vm, method: wizardry}]
`},
		{"duplicate discovery type", `
provider: sample
workloads:
  discovery:
    types: [{name: vm}, {name: vm}]
`},
		{"type name reused across domains", `
provider: sample
workloads:
  discovery:
    types: [{name: vm}]
computePlatforms:
  discovery:
    types: [{name: vm}]
`},
		{"installation without compute-platform discovery", `
provider: sample
computePlatforms:
  discovery:
    types: [{name: cluster}]
  installation:
    types: [{name: orphan, method: agent}]
`},
		{"installation invalid method", `
provider: sample
computePlatforms:
  discovery:
    types: [{name: cluster}]
  installation:
    types: [{name: cluster, method: wizardry}]
`},
	}
	for _, tc := range bad {
		if _, err := Parse([]byte(tc.yaml)); err == nil {
			t.Errorf("%s: expected a validation error, got none", tc.name)
		}
	}
}

func TestRegistry(t *testing.T) {
	s, err := Parse([]byte(sampleYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	Register(s)

	got, ok := Get("sample")
	if !ok || got.Provider != "sample" {
		t.Fatalf("Get(sample) = %v, %v", got, ok)
	}
	found := false
	for _, p := range Providers() {
		if p == "sample" {
			found = true
		}
	}
	if !found {
		t.Errorf("Providers() = %v, should include sample", Providers())
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
