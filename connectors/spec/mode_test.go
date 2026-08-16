package spec

import (
	"strings"
	"testing"
)

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

// TestDuplicateCapabilityEntriesCannotSplitModeFromGate is a regression guard for a real hole: nothing along
// the carrier enforces one capability entry per workload type — the CRD list has no listMapKey, the GraphQL
// input appends verbatim, connector-runtime marshals verbatim, and the connector json.Unmarshals it.
//
// The mode resolver used to return on the FIRST matching entry while the reconciler gate OR-ed over ALL of
// them, so [{lambda, manual}, {lambda, automatic}] reported the type as manual — "waiting for your
// deployment" — while the reconciler was wired and rewriting the function. Both now read the same OR, so the
// report can never claim Odigos is hands-off while it is mutating.
func TestDuplicateCapabilityEntriesCannotSplitModeFromGate(t *testing.T) {
	s, err := Parse([]byte(modeSampleYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	orders := map[string][]WorkloadCapability{
		"manual first": {
			{Type: "vm", Discovery: true},
			{Type: "vm", Discovery: true, Instrumentation: true},
		},
		"automatic first": {
			{Type: "vm", Discovery: true, Instrumentation: true},
			{Type: "vm", Discovery: true},
		},
	}
	for name, workloads := range orders {
		t.Run(name, func(t *testing.T) {
			caps := Capabilities{Workloads: workloads}
			mode := caps.WorkloadInstrumentationMode("vm")
			wired := len(typeNames(s.ActiveInstrumentationTypes(caps))) > 0

			if wired && mode != ModeAutomatic {
				t.Errorf("reconciler is wired but the reported mode is %q: the connector would mutate a workload while reporting it as the customer's", mode)
			}
			if !wired && mode == ModeAutomatic {
				t.Errorf("reported mode is automatic but no reconciler is wired: the workload would report as Odigos-managed while nothing manages it")
			}
		})
	}
}

// TestValidateRejectsConflictingDuplicates covers the first line of defense: a contradictory allow-list is
// refused at startup rather than resolved into one of the two things the customer asked for. Redundant but
// identical entries are merely sloppy and must keep working.
func TestValidateRejectsConflictingDuplicates(t *testing.T) {
	cases := []struct {
		name    string
		caps    Capabilities
		wantErr bool
	}{
		{
			name: "conflicting workload duplicates",
			caps: Capabilities{Workloads: []WorkloadCapability{
				{Type: "vm", Discovery: true},
				{Type: "vm", Discovery: true, Instrumentation: true},
			}},
			wantErr: true,
		},
		{
			name: "identical workload duplicates are redundant, not contradictory",
			caps: Capabilities{Workloads: []WorkloadCapability{
				{Type: "vm", Discovery: true, Instrumentation: true},
				{Type: "vm", Discovery: true, Instrumentation: true},
			}},
			wantErr: false,
		},
		{
			name: "distinct types are the normal case",
			caps: Capabilities{Workloads: []WorkloadCapability{
				{Type: "vm", Discovery: true, Instrumentation: true},
				{Type: "fn", Discovery: true},
			}},
			wantErr: false,
		},
		{
			name: "conflicting compute-platform duplicates",
			caps: Capabilities{ComputePlatforms: []ComputePlatformCapability{
				{Type: "eks", Discovery: true},
				{Type: "eks", Discovery: true, Installation: true},
			}},
			wantErr: true,
		},
		{
			name:    "empty is valid",
			caps:    Capabilities{},
			wantErr: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.caps.Validate()
			if c.wantErr && err == nil {
				t.Error("Validate accepted a contradictory allow-list")
			}
			if !c.wantErr && err != nil {
				t.Errorf("Validate rejected a usable allow-list: %v", err)
			}
		})
	}
}

func typeNames(types []Type) []string {
	out := make([]string, 0, len(types))
	for _, t := range types {
		out = append(out, t.Name)
	}
	return out
}

// manualObservationReads names, per provider workload type, the permissions MANUAL mode needs in order to judge
// whether the customer actually applied instrumentation — beyond what merely listing the inventory requires.
//
// These live on each provider's DISCOVERY type, because discovery is the only thing manual mode runs; see the
// comment in RequiredPermissions for why a separate observe-permissions list was rejected. An empty list is a
// deliberate assertion rather than an omission: Lambda's ListFunctions already returns the function's
// environment and layers, so conformance there costs no extra permission.
var manualObservationReads = map[string]map[string][]string{
	"aws": {
		"aws.lambda":       {},
		"aws.fargate-task": {"ecs:DescribeTaskDefinition"},
	},
	"gcp": {
		"gcp.cloud-run": {"run.revisions.list", "run.revisions.get"},
	},
	"azure": {
		"azure.container-apps": {"Microsoft.App/containerApps/revisions/read"},
	},
}

// TestManualModeGrantsObservationReads is the positive counterpart to TestManualModeGrantsNoMutationPermissions:
// that one proves mutation permissions stay out of manual mode, this one proves the reads observation needs are
// actually in. Without it, dropping a read from a discovery type would silently produce a connector whose
// rendered policy cannot support the conformance checks manual mode is built on (a 403 in the field, not a test
// failure).
func TestManualModeGrantsObservationReads(t *testing.T) {
	for provider, byType := range manualObservationReads {
		s, ok := Get(provider)
		if !ok {
			t.Fatalf("provider %q not retrievable", provider)
		}
		for typeName, reads := range byType {
			caps := Capabilities{Workloads: []WorkloadCapability{{Type: typeName, Discovery: true}}}
			if got := caps.WorkloadInstrumentationMode(typeName); got != ModeManual {
				t.Fatalf("%s/%s: test set up the wrong mode: got %q, want %q", provider, typeName, got, ModeManual)
			}
			perms := s.RequiredPermissions(caps)
			for _, want := range reads {
				if !containsStr(perms, want) {
					t.Errorf("%s/%s manual mode: required permissions %v missing observation read %q",
						provider, typeName, perms, want)
				}
			}
		}
	}
}

// TestEveryInstrumentationTypeDeclaresObservationReads makes the decision above mandatory. A newly declared
// instrumentation type must state what manual mode needs to read — even if the answer is "nothing" — rather than
// inheriting silence and shipping a mode that cannot observe.
func TestEveryInstrumentationTypeDeclaresObservationReads(t *testing.T) {
	for _, provider := range Providers() {
		s := MustGet(provider)
		byType, ok := manualObservationReads[provider]
		if !ok {
			t.Errorf("provider %q declares no manual observation-read expectations", provider)
			continue
		}
		for _, it := range s.Workloads.Instrumentation.Types {
			if _, ok := byType[it.Name]; !ok {
				t.Errorf("%s: instrumentation type %q has no manual observation-read expectation; decide what manual "+
					"mode must read to verify it and record it in manualObservationReads", provider, it.Name)
			}
		}
	}
}

// writeVerbs mark a provider permission as write-capable. Matching is case-sensitive and anchored on separators
// where a bare verb would be ambiguous — "put" alone would match "compute.instances.list".
var writeVerbs = []string{
	"Update", "Create", "Delete", "Register", "Publish", "TagResource", "PassRole",
	".update", ".create", ".delete", ".setIamPolicy", "actAs",
	"/write", "join/action",
}

// TestManualModeUnionHasNoWriteVerb generalizes TestManualModeGrantsNoMutationPermissions, which can only catch
// permissions some instrumentation type happens to declare. This catches a write-capable permission added
// anywhere the manual-mode aggregation reaches — including a discovery type, which is where this wave put the
// observation reads and therefore the most likely place for one to slip in.
func TestManualModeUnionHasNoWriteVerb(t *testing.T) {
	for _, provider := range Providers() {
		s := MustGet(provider)
		for _, it := range s.Workloads.Instrumentation.Types {
			caps := Capabilities{Workloads: []WorkloadCapability{{Type: it.Name, Discovery: true}}}
			for _, p := range s.RequiredPermissions(caps) {
				for _, verb := range writeVerbs {
					if strings.Contains(p, verb) {
						t.Errorf("%s/%s manual mode aggregates write-capable permission %q (matched %q)",
							provider, it.Name, p, verb)
					}
				}
			}
		}
	}
}

func TestUnsupportedCapabilities(t *testing.T) {
	s, err := Parse([]byte(modeSampleYAML))
	if err != nil {
		t.Fatalf("parse sample spec: %v", err)
	}
	cases := []struct {
		name      string
		caps      Capabilities
		wantCount int
		wantMatch string
	}{
		{
			name: "manual mode on a discovery-only type is fine",
			caps: Capabilities{Workloads: []WorkloadCapability{{Type: "bucket", Discovery: true}}},
		},
		{
			name: "automatic mode is fine where instrumentation is declared",
			caps: Capabilities{Workloads: []WorkloadCapability{{Type: "vm", Discovery: true, Instrumentation: true}}},
		},
		{
			name:      "automatic mode on a type with no instrumentation",
			caps:      Capabilities{Workloads: []WorkloadCapability{{Type: "bucket", Discovery: true, Instrumentation: true}}},
			wantCount: 1,
			wantMatch: "declares none for it",
		},
		{
			name:      "unknown workload type",
			caps:      Capabilities{Workloads: []WorkloadCapability{{Type: "nope", Discovery: true}}},
			wantCount: 1,
			wantMatch: "not a workload type this provider discovers",
		},
		{
			name:      "instrumentation without discovery finds nothing to instrument",
			caps:      Capabilities{Workloads: []WorkloadCapability{{Type: "vm", Instrumentation: true}}},
			wantCount: 1,
			wantMatch: "without discovery",
		},
		{
			name: "a switched-off type is not a warning",
			caps: Capabilities{Workloads: []WorkloadCapability{{Type: "vm"}}},
		},
		{
			name: "duplicate entries warn once, on the OR-ed flags",
			caps: Capabilities{Workloads: []WorkloadCapability{
				{Type: "bucket", Discovery: true},
				{Type: "bucket", Discovery: true, Instrumentation: true},
			}},
			wantCount: 1,
			wantMatch: "declares none for it",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := s.UnsupportedCapabilities(c.caps)
			if len(got) != c.wantCount {
				t.Fatalf("UnsupportedCapabilities = %v (%d warnings), want %d", got, len(got), c.wantCount)
			}
			if c.wantMatch != "" && !strings.Contains(got[0], c.wantMatch) {
				t.Errorf("warning %q does not mention %q", got[0], c.wantMatch)
			}
		})
	}
}

// TestRealProviderCapabilitiesAreHonorable guards against a spec that cannot serve its own advertised types: for
// every provider, enabling everything it declares must produce no warnings.
func TestRealProviderCapabilitiesAreHonorable(t *testing.T) {
	for _, provider := range Providers() {
		s := MustGet(provider)
		var caps Capabilities
		for _, t := range s.Workloads.Discovery.Types {
			_, instrumentable := s.Workloads.Instrumentation.findType(t.Name)
			caps.Workloads = append(caps.Workloads, WorkloadCapability{
				Type: t.Name, Discovery: true, Instrumentation: instrumentable,
			})
		}
		if got := s.UnsupportedCapabilities(caps); len(got) != 0 {
			t.Errorf("%s: enabling every declared type produced warnings: %v", provider, got)
		}
	}
}
