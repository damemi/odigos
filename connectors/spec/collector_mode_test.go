package spec

import (
	"encoding/json"
	"strings"
	"testing"
)

// collectorSampleYAML declares two managed-Collector types so a capability set can put one in each mode,
// plus a management-only type with no deployment counterpart — the shape a provider declares when it can
// configure a Collector it cannot yet deploy.
const collectorSampleYAML = `
provider: sample
collectors:
  management:
    enabled: true
    permissions:
      - sample:DescribeRegions
    types:
      - name: sample.fargate-collector
        permissions:
          - sample:DescribeService
      - name: sample.container-app-collector
        permissions:
          - sample:DescribeApp
      - name: sample.unowned-collector
        permissions:
          - sample:DescribeUnowned
  deployment:
    enabled: true
    types:
      - name: sample.fargate-collector
        method: fargate-service
        permissions:
          - sample:CreateService
      - name: sample.container-app-collector
        method: container-app
        permissions:
          - sample:CreateApp
`

func collectorSpec(t *testing.T) CloudConnectorSpec {
	t.Helper()
	s, err := Parse([]byte(collectorSampleYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return s
}

// TestCollectorMode walks every combination of the two booleans, which is the whole derivation. The row
// that is not obvious is the last: a contradictory entry still reports automatic, because the deployer
// gate keys on the deployment boolean alone and a mode that disagreed with it would tell a customer their
// Collector is theirs while Odigos was building it. Validate refuses that entry separately, so this
// agreement is a second line of defense rather than a licence to write it.
func TestCollectorMode(t *testing.T) {
	cases := []struct {
		name string
		caps []CollectorCapability
		want string
	}{
		{"not in the allow-list at all", nil, ""},
		{"both off", []CollectorCapability{{Type: "sample.fargate-collector"}}, ""},
		{"management only", []CollectorCapability{{Type: "sample.fargate-collector", Management: true}}, ModeManual},
		{"both on", []CollectorCapability{{Type: "sample.fargate-collector", Management: true, Deployment: true}}, ModeAutomatic},
		{"deployment without management (contradiction)", []CollectorCapability{{Type: "sample.fargate-collector", Deployment: true}}, ModeAutomatic},
		{"another type entirely", []CollectorCapability{{Type: "sample.container-app-collector", Management: true}}, ""},
	}
	for _, tc := range cases {
		got := Capabilities{Collectors: tc.caps}.CollectorMode("sample.fargate-collector")
		if got != tc.want {
			t.Errorf("%s: CollectorMode = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestCollectorTypesVersusActiveDeployment: the two resolvers must not be interchangeable, and the two
// ways of confusing them fail differently. Using the gate for reporting makes a manual-mode network claim
// no Collector is defined; using the reporting resolver as a gate hands Odigos a customer's
// infrastructure.
func TestCollectorTypesVersusActiveDeployment(t *testing.T) {
	s := collectorSpec(t)
	caps := Capabilities{Collectors: []CollectorCapability{
		{Type: "sample.fargate-collector", Management: true, Deployment: true},
		{Type: "sample.container-app-collector", Management: true},
	}}

	if got := typeNames(s.ActiveCollectorDeploymentTypes(caps)); len(got) != 1 || got[0] != "sample.fargate-collector" {
		t.Errorf("ActiveCollectorDeploymentTypes = %v, want only the automatic-mode type", got)
	}
	both := typeNames(s.CollectorTypes(caps))
	if len(both) != 2 {
		t.Errorf("CollectorTypes = %v, want both modes", both)
	}
	// The manual-mode type must be present here or its network reports "no Collector defined" rather than
	// "waiting for you to deploy it".
	var found bool
	for _, n := range both {
		if n == "sample.container-app-collector" {
			found = true
		}
	}
	if !found {
		t.Errorf("CollectorTypes = %v, missing the manual-mode type", both)
	}
}

// TestManualModeCollectorGrantsNoDeploymentPermissions is the observe-only criterion. A customer running
// every Collector in manual mode must be asked for reads and nothing else — aggregating the deployment
// type's permissions would demand exactly the create/delete grants manual mode exists to avoid.
func TestManualModeCollectorGrantsNoDeploymentPermissions(t *testing.T) {
	s := collectorSpec(t)
	manual := Capabilities{Collectors: []CollectorCapability{
		{Type: "sample.fargate-collector", Management: true},
	}}
	perms := s.RequiredPermissions(manual)

	// The observation reads must be there: manual mode still describes, health-checks and reports.
	for _, want := range []string{"sample:DescribeRegions", "sample:DescribeService"} {
		if !containsStr(perms, want) {
			t.Errorf("manual mode does not grant %q; the Collector could not be observed at all: %v", want, perms)
		}
	}
	// And not one mutating permission.
	if containsStr(perms, "sample:CreateService") {
		t.Errorf("manual mode grants sample:CreateService: %v", perms)
	}
	// Another type's reads must not leak in either.
	if containsStr(perms, "sample:DescribeApp") {
		t.Errorf("manual mode for one type granted another type's permissions: %v", perms)
	}

	// Automatic mode is the same set plus the mutating one, so the difference between the modes is exactly
	// the deployment type's permissions and nothing else.
	auto := Capabilities{Collectors: []CollectorCapability{
		{Type: "sample.fargate-collector", Management: true, Deployment: true},
	}}
	autoPerms := s.RequiredPermissions(auto)
	if !containsStr(autoPerms, "sample:CreateService") {
		t.Errorf("automatic mode does not grant sample:CreateService: %v", autoPerms)
	}
	for _, p := range perms {
		if !containsStr(autoPerms, p) {
			t.Errorf("automatic mode dropped %q, which manual mode requires", p)
		}
	}
	if len(autoPerms) != len(perms)+1 {
		t.Errorf("automatic mode adds %d permissions over manual, want exactly the one deployment permission (manual %v, automatic %v)",
			len(autoPerms)-len(perms), perms, autoPerms)
	}
}

// TestNoCollectorCapabilityGrantsNothing: a connector with no collectors capability must not be asked for
// a single Collector permission, or every existing customer's policy grows for a feature they have not
// enabled.
func TestNoCollectorCapabilityGrantsNothing(t *testing.T) {
	s := collectorSpec(t)
	perms := s.RequiredPermissions(Capabilities{})
	for _, p := range perms {
		if strings.HasPrefix(p, "sample:") {
			t.Errorf("an empty capability set still requires %q: %v", p, perms)
		}
	}
}

// TestDuplicateCollectorEntriesCannotSplitModeFromGate: duplicates do reach the runtime — nothing along
// the CRD list, the GraphQL input or the ODIGOS_CAPABILITIES unmarshal enforces one entry per type — so
// the report and the gate must resolve from the same OR, in either declaration order. This is the
// deterministic-duplicate-resolution criterion: the answer must not depend on which entry came first.
func TestDuplicateCollectorEntriesCannotSplitModeFromGate(t *testing.T) {
	s := collectorSpec(t)
	orders := map[string][]CollectorCapability{
		"manual first": {
			{Type: "sample.fargate-collector", Management: true},
			{Type: "sample.fargate-collector", Management: true, Deployment: true},
		},
		"automatic first": {
			{Type: "sample.fargate-collector", Management: true, Deployment: true},
			{Type: "sample.fargate-collector", Management: true},
		},
	}
	var modes, gates []string
	for name, colls := range orders {
		caps := Capabilities{Collectors: colls}
		mode := caps.CollectorMode("sample.fargate-collector")
		wired := len(typeNames(s.ActiveCollectorDeploymentTypes(caps))) > 0

		if wired && mode != ModeAutomatic {
			t.Errorf("%s: deployer is wired but the reported mode is %q: the connector would build a Collector while reporting it as the customer's", name, mode)
		}
		if !wired && mode == ModeAutomatic {
			t.Errorf("%s: reported mode is automatic but no deployer is wired: the Collector would report as Odigos-managed while nothing manages it", name)
		}
		modes = append(modes, mode)
		if wired {
			gates = append(gates, "wired")
		} else {
			gates = append(gates, "unwired")
		}
	}
	// Order-independence, stated separately from the agreement above: both orders must produce the SAME
	// answer, not merely two self-consistent ones.
	if modes[0] != modes[1] || gates[0] != gates[1] {
		t.Errorf("declaration order changed the outcome: modes %v gates %v", modes, gates)
	}
}

// TestValidateRejectsCollectorContradictions covers the first line of defense. The deployment-without-
// management row is refused even when declared exactly once, which is the difference from the workload
// domain: there is no reading of "create a Collector you will never configure" that the customer meant.
func TestValidateRejectsCollectorContradictions(t *testing.T) {
	cases := []struct {
		name    string
		caps    Capabilities
		wantErr string
	}{
		{
			name: "deployment without management, declared once",
			caps: Capabilities{Collectors: []CollectorCapability{{Type: "c", Deployment: true}}},
			// The message has to say what to do about it, not merely that it is wrong.
			wantErr: "never configures",
		},
		{
			name: "deployment without management, folded from two entries",
			caps: Capabilities{Collectors: []CollectorCapability{
				{Type: "c", Deployment: true},
				{Type: "c"},
			}},
			wantErr: "declare it once",
		},
		{
			name: "conflicting duplicates",
			caps: Capabilities{Collectors: []CollectorCapability{
				{Type: "c", Management: true},
				{Type: "c", Management: true, Deployment: true},
			}},
			wantErr: "declare it once",
		},
		{
			name: "identical duplicates are merely redundant",
			caps: Capabilities{Collectors: []CollectorCapability{
				{Type: "c", Management: true, Deployment: true},
				{Type: "c", Management: true, Deployment: true},
			}},
		},
		{
			name: "manual mode",
			caps: Capabilities{Collectors: []CollectorCapability{{Type: "c", Management: true}}},
		},
		{
			name: "switched off entirely",
			caps: Capabilities{Collectors: []CollectorCapability{{Type: "c"}}},
		},
	}
	for _, tc := range cases {
		err := tc.caps.Validate()
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%s: Validate = %v, want nil", tc.name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: Validate = nil, want an error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: Validate = %v, want it to mention %q", tc.name, err, tc.wantErr)
		}
	}
}

// TestOlderConnectorImageIgnoresTheCollectorsKey is the fail-safe criterion, exercised the way it
// actually happens: ODIGOS_CAPABILITIES is parsed with encoding/json, which ignores unknown fields, so an
// image predating this domain reads a capability set carrying `collectors` as if it were not there —
// wires no deployer, and cannot create infrastructure in a customer's account.
//
// This is the whole reason manual mode is `deployment: false` rather than a new field beside
// `deployment: true`. The inverse encoding would have made an older image read manual mode as plain
// automatic deployment and build a Collector the customer meant to own.
func TestOlderConnectorImageIgnoresTheCollectorsKey(t *testing.T) {
	// What connector-runtime marshals for a customer who enabled automatic deployment.
	current := Capabilities{Collectors: []CollectorCapability{
		{Type: "sample.fargate-collector", Management: true, Deployment: true},
	}}
	raw, err := json.Marshal(current)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"collectors"`) {
		t.Fatalf("capabilities did not marshal a collectors key: %s", raw)
	}

	// An older image's struct: the two domains it knows about, and no collectors field.
	var older struct {
		Workloads        []WorkloadCapability        `json:"workloads,omitempty"`
		ComputePlatforms []ComputePlatformCapability `json:"computePlatforms,omitempty"`
	}
	if err := json.Unmarshal(raw, &older); err != nil {
		t.Fatalf("an older image failed to parse capabilities carrying the new key: %v", err)
	}
	if len(older.Workloads) != 0 || len(older.ComputePlatforms) != 0 {
		t.Errorf("the collectors key leaked into another domain: %+v", older)
	}

	// And with the key dropped, every resolver reports the type as absent — no mode, no deployer, no
	// permissions.
	s := collectorSpec(t)
	empty := Capabilities{}
	if mode := empty.CollectorMode("sample.fargate-collector"); mode != "" {
		t.Errorf("an older image derived mode %q for a type it cannot see", mode)
	}
	if got := typeNames(s.ActiveCollectorDeploymentTypes(empty)); len(got) != 0 {
		t.Errorf("an older image wired a deployer for %v", got)
	}
	if got := typeNames(s.CollectorTypes(empty)); len(got) != 0 {
		t.Errorf("an older image resolved Collector types %v", got)
	}
}

// TestCollectorMutationGateIgnoresManagement: the gate keys on the deployment boolean alone, which is
// what makes the fail-safe hold — and what makes the contradictory entry above resolve to automatic.
func TestCollectorMutationGateIgnoresManagement(t *testing.T) {
	s := collectorSpec(t)
	withoutManagement := Capabilities{Collectors: []CollectorCapability{
		{Type: "sample.fargate-collector", Deployment: true},
	}}
	if got := typeNames(s.ActiveCollectorDeploymentTypes(withoutManagement)); len(got) != 1 {
		t.Errorf("ActiveCollectorDeploymentTypes = %v, want the type gated on deployment alone", got)
	}
	// Management alone must never wire it, in any spec.
	managementOnly := Capabilities{Collectors: []CollectorCapability{
		{Type: "sample.fargate-collector", Management: true},
	}}
	if got := typeNames(s.ActiveCollectorDeploymentTypes(managementOnly)); len(got) != 0 {
		t.Errorf("management alone wired a deployer for %v", got)
	}
}

// TestCollectorTypeWithNoDeploymentCounterpart: a provider that can configure a Collector it cannot yet
// deploy declares a management type with no deployment entry. It must resolve to manual mode and report,
// never to a deployer that does not exist.
func TestCollectorTypeWithNoDeploymentCounterpart(t *testing.T) {
	s := collectorSpec(t)
	caps := Capabilities{Collectors: []CollectorCapability{
		{Type: "sample.unowned-collector", Management: true, Deployment: true},
	}}
	if mode := caps.CollectorMode("sample.unowned-collector"); mode != ModeAutomatic {
		t.Errorf("mode = %q, want automatic — the customer did ask for it", mode)
	}
	if got := typeNames(s.ActiveCollectorDeploymentTypes(caps)); len(got) != 0 {
		t.Errorf("wired a deployer for a type with no deployment entry: %v", got)
	}
	if got := typeNames(s.CollectorTypes(caps)); len(got) != 1 || got[0] != "sample.unowned-collector" {
		t.Errorf("CollectorTypes = %v, want the type reported anyway", got)
	}
	// And the gap is warned about rather than silent, because the customer asked for something this
	// provider cannot do.
	warnings := s.UnsupportedCapabilities(caps)
	var warned bool
	for _, w := range warnings {
		if strings.Contains(w, "sample.unowned-collector") && strings.Contains(w, "automatic deployment") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("warnings = %v, want one naming the type and the missing deployment", warnings)
	}
}

// TestUnsupportedCollectorCapabilityIsWarnedNotFatal: a capability for a type this provider does not
// declare is ignored with a warning. Refusing to start would cost the connector its heartbeat too, which
// is strictly less observable than the state being warned about.
func TestUnsupportedCollectorCapabilityIsWarnedNotFatal(t *testing.T) {
	s := collectorSpec(t)
	caps := Capabilities{Collectors: []CollectorCapability{{Type: "sample.nonexistent", Management: true}}}
	if err := caps.Validate(); err != nil {
		t.Errorf("Validate = %v, want nil: an unknown type is a warning, not a refusal", err)
	}
	warnings := s.UnsupportedCapabilities(caps)
	var warned bool
	for _, w := range warnings {
		if strings.Contains(w, "sample.nonexistent") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("warnings = %v, want one naming the unknown type", warnings)
	}
	// Switched off entirely: nothing to warn about.
	off := Capabilities{Collectors: []CollectorCapability{{Type: "sample.nonexistent"}}}
	for _, w := range s.UnsupportedCapabilities(off) {
		if strings.Contains(w, "sample.nonexistent") {
			t.Errorf("warned about a type that is switched off: %s", w)
		}
	}
}

// TestCollectorSection pins the artifact section prefix. It is a published contract with central-backend's
// uninterpreted artifact mapping, so a client filtering by section keys on this exact string.
func TestCollectorSection(t *testing.T) {
	if got := CollectorSection("aws.fargate-collector"); got != "collector:aws.fargate-collector" {
		t.Errorf("CollectorSection = %q", got)
	}
	// It must not collide with the instrumentation prefix, or a client showing one would show the other.
	if strings.HasPrefix(CollectorSection("x"), InstrumentationSection("")) {
		t.Error("the collector and instrumentation section prefixes overlap")
	}
}
