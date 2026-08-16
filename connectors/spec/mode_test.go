package spec

import "testing"

// modeSampleYAML declares two instrumentation types so a capability set can put one in each mode.
const modeSampleYAML = `
provider: sample
workloads:
  discovery:
    enabled: true
    types:
      - name: vm
      - name: fn
      - name: bucket   # discovery only: no instrumentation counterpart at all
  instrumentation:
    enabled: true
    types:
      - name: vm
        method: agent
        permissions:
          - sample:UpdateVM
      - name: fn
        method: service
        permissions:
          - sample:UpdateFn
`

func TestWorkloadInstrumentationMode(t *testing.T) {
	cases := []struct {
		name string
		caps []WorkloadCapability
		typ  string
		want string
	}{
		{
			name: "discovery and instrumentation granted is automatic",
			caps: []WorkloadCapability{{Type: "vm", Discovery: true, Instrumentation: true}},
			typ:  "vm",
			want: ModeAutomatic,
		},
		{
			// The headline rule: a discovered type is the customer's to instrument unless the connector was
			// granted instrumentation capability for it.
			name: "discovery only is manual",
			caps: []WorkloadCapability{{Type: "vm", Discovery: true}},
			typ:  "vm",
			want: ModeManual,
		},
		{
			name: "neither flag is not enabled at all",
			caps: []WorkloadCapability{{Type: "vm"}},
			typ:  "vm",
			want: "",
		},
		{
			name: "absent from the allow-list is not enabled at all",
			caps: []WorkloadCapability{{Type: "other", Discovery: true}},
			typ:  "vm",
			want: "",
		},
		{
			// Nonsense capability, but it must agree with ActiveInstrumentationTypes, which gates the
			// mutating reconciler on the instrumentation boolean alone. Reporting manual here would claim
			// Odigos does not mutate a type whose reconciler is in fact wired.
			name: "instrumentation without discovery is still automatic",
			caps: []WorkloadCapability{{Type: "vm", Instrumentation: true}},
			typ:  "vm",
			want: ModeAutomatic,
		},
		{
			name: "modes are independent per type",
			caps: []WorkloadCapability{
				{Type: "vm", Discovery: true, Instrumentation: true},
				{Type: "fn", Discovery: true},
			},
			typ:  "fn",
			want: ModeManual,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Capabilities{Workloads: c.caps}.WorkloadInstrumentationMode(c.typ)
			if got != c.want {
				t.Errorf("WorkloadInstrumentationMode(%q) = %q, want %q", c.typ, got, c.want)
			}
		})
	}
}

// TestInstrumentationTypesVersusActive is the load-bearing distinction of the whole feature: the permissive
// resolver must include manual-mode types (so they are reported rather than shown as unsupported), and the
// mutation gate must exclude them (so nothing can ever mutate them).
func TestInstrumentationTypesVersusActive(t *testing.T) {
	s, err := Parse([]byte(modeSampleYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	caps := Capabilities{Workloads: []WorkloadCapability{
		{Type: "vm", Discovery: true, Instrumentation: true}, // automatic
		{Type: "fn", Discovery: true},                        // manual
		{Type: "bucket", Discovery: true},                    // discovery-only, no instrumentation type declared
	}}

	if got := typeNames(s.ActiveInstrumentationTypes(caps)); len(got) != 1 || got[0] != "vm" {
		t.Errorf("ActiveInstrumentationTypes = %v, want only the automatic-mode type [vm]", got)
	}
	if got := typeNames(s.InstrumentationTypes(caps)); len(got) != 2 || got[0] != "vm" || got[1] != "fn" {
		t.Errorf("InstrumentationTypes = %v, want both modes [vm fn] in spec order", got)
	}
	// A discovery-only kind with no declared instrumentation type appears in neither, whatever its mode: there
	// is nothing to instrument and nothing to publish a plan for.
	for _, n := range typeNames(s.InstrumentationTypes(caps)) {
		if n == "bucket" {
			t.Error("InstrumentationTypes included a type with no instrumentation entry declared")
		}
	}
}

// TestManualModeGrantsNoMutationPermissions guards the security property of manual mode against the real
// provider specs: a connector that only discovers a type must not be told to grant permissions that would
// let Odigos change it.
func TestManualModeGrantsNoMutationPermissions(t *testing.T) {
	for _, provider := range Providers() {
		s, ok := Get(provider)
		if !ok {
			t.Fatalf("provider %q not retrievable", provider)
		}
		for _, it := range s.Workloads.Instrumentation.Types {
			manual := Capabilities{Workloads: []WorkloadCapability{{Type: it.Name, Discovery: true}}}
			auto := Capabilities{Workloads: []WorkloadCapability{{Type: it.Name, Discovery: true, Instrumentation: true}}}

			manualPerms := map[string]bool{}
			for _, p := range s.RequiredPermissions(manual) {
				manualPerms[p] = true
			}
			// Every permission the instrumentation type declares is a mutation-capable set; none may leak into
			// the manual-mode aggregation.
			for _, p := range it.Permissions {
				if manualPerms[p] && !discoveryDeclares(s, it.Name, p) {
					t.Errorf("%s/%s: manual mode aggregates instrumentation permission %q", provider, it.Name, p)
				}
			}
			// Automatic mode must keep demanding them, or the connector cannot do its job.
			autoPerms := map[string]bool{}
			for _, p := range s.RequiredPermissions(auto) {
				autoPerms[p] = true
			}
			for _, p := range it.Permissions {
				if !autoPerms[p] {
					t.Errorf("%s/%s: automatic mode lost instrumentation permission %q", provider, it.Name, p)
				}
			}
		}
	}
}

// discoveryDeclares reports whether a permission is also required just to DISCOVER the type. Those are
// legitimately present in manual mode (read-only, and discovery is what manual mode runs on), so they are not
// evidence of a mutation permission leaking in.
func discoveryDeclares(s CloudConnectorSpec, typeName, perm string) bool {
	for _, p := range s.Workloads.Discovery.Permissions {
		if p == perm {
			return true
		}
	}
	for _, t := range s.Workloads.Discovery.Types {
		if t.Name != typeName {
			continue
		}
		for _, p := range t.Permissions {
			if p == perm {
				return true
			}
		}
	}
	return false
}

func typeNames(types []Type) []string {
	out := make([]string, 0, len(types))
	for _, t := range types {
		out = append(out, t.Name)
	}
	return out
}
