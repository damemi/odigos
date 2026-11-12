package spec

import (
	"strings"
	"testing"
)

// TestCollectorTaxonomyAccepts covers the shapes a provider legitimately declares. Management-only is the
// important one: a provider that can configure a Collector but has not built its deployer yet declares
// management types and an empty deployment feature, and every such Collector is manual mode.
func TestCollectorTaxonomyAccepts(t *testing.T) {
	good := map[string]string{
		"management only, no deployment feature at all": `
provider: sample
collectors:
  management:
    enabled: true
    types: [{name: sample.collector}]
`,
		"management only, deployment declared empty": `
provider: sample
collectors:
  management:
    enabled: true
    types: [{name: sample.collector}]
  deployment:
    enabled: false
    types: []
`,
		"every provider method": `
provider: sample
collectors:
  management:
    types: [{name: a}, {name: b}, {name: c}]
  deployment:
    types:
      - {name: a, method: fargate-service}
      - {name: b, method: container-app}
      - {name: c, method: cloud-run-service}
`,
		"a management type with no deployment counterpart alongside one that has": `
provider: sample
collectors:
  management:
    types: [{name: a}, {name: b}]
  deployment:
    types: [{name: a, method: fargate-service}]
`,
		"no collectors domain at all": `
provider: sample
workloads:
  discovery:
    types: [{name: vm}]
`,
	}
	for name, y := range good {
		if _, err := Parse([]byte(y)); err != nil {
			t.Errorf("%s should parse, got: %v", name, err)
		}
	}
}

// TestCollectorTaxonomyRejects covers every way the collectors domain can be declared wrong. Each row is
// a mistake that would otherwise surface as wrong runtime behaviour in a customer's account rather than
// as a parse failure at startup.
func TestCollectorTaxonomyRejects(t *testing.T) {
	bad := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "deployment type with no management counterpart",
			yaml: `
provider: sample
collectors:
  management:
    types: [{name: a}]
  deployment:
    types: [{name: a, method: fargate-service}, {name: orphan, method: container-app}]
`,
			want: "no matching discovery type in the same domain",
		},
		{
			name: "management type declaring a method",
			yaml: `
provider: sample
collectors:
  management:
    types: [{name: a, method: fargate-service}]
`,
			want: "must not set a method",
		},
		{
			name: "deployment type with no method",
			yaml: `
provider: sample
collectors:
  management:
    types: [{name: a}]
  deployment:
    types: [{name: a}]
`,
			want: "requires a method",
		},
		{
			// The reason the valid-method set is per domain rather than shared: an instrumentation method
			// on a Collector deployment type names something that cannot be deployed.
			name: "deployment type using an instrumentation method",
			yaml: `
provider: sample
collectors:
  management:
    types: [{name: a}]
  deployment:
    types: [{name: a, method: sidecar}]
`,
			want: "invalid method",
		},
		{
			// And the mirror image, which a shared set would also have let through.
			name: "instrumentation type using a collector method",
			yaml: `
provider: sample
workloads:
  discovery:
    types: [{name: vm}]
  instrumentation:
    types: [{name: vm, method: fargate-service}]
`,
			want: "invalid method",
		},
		{
			name: "duplicate management type",
			yaml: `
provider: sample
collectors:
  management:
    types: [{name: a}, {name: a}]
`,
			want: "duplicate type",
		},
		{
			name: "duplicate deployment type",
			yaml: `
provider: sample
collectors:
  management:
    types: [{name: a}]
  deployment:
    types: [{name: a, method: fargate-service}, {name: a, method: container-app}]
`,
			want: "duplicate type",
		},
		{
			name: "management type name colliding with a workload type",
			yaml: `
provider: sample
workloads:
  discovery:
    types: [{name: shared}]
collectors:
  management:
    types: [{name: shared}]
`,
			want: "already declared in another domain",
		},
		{
			// The case a naive chain would have missed: threading only the previous domain's set would
			// have compared collectors against compute platforms and let a workload collision through.
			name: "management type name colliding with a workload type when a compute platform exists too",
			yaml: `
provider: sample
workloads:
  discovery:
    types: [{name: shared}]
computePlatforms:
  discovery:
    types: [{name: cluster}]
collectors:
  management:
    types: [{name: shared}]
`,
			want: "already declared in another domain",
		},
		{
			name: "management type name colliding with a compute-platform type",
			yaml: `
provider: sample
computePlatforms:
  discovery:
    types: [{name: cluster}]
collectors:
  management:
    types: [{name: cluster}]
`,
			want: "already declared in another domain",
		},
		{
			name: "empty management type name",
			yaml: `
provider: sample
collectors:
  management:
    types: [{name: ""}]
`,
			want: "type name is required",
		},
	}
	for _, tc := range bad {
		_, err := Parse([]byte(tc.yaml))
		if err == nil {
			t.Errorf("%s should be rejected", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", tc.name, err, tc.want)
		}
	}
}

// TestCollectorTaxonomyValidatesWhenDisabled: the check runs regardless of the Enabled flag, so a
// disabled-but-declared feature cannot smuggle an invalid taxonomy past parse and start failing the day
// somebody flips it on.
func TestCollectorTaxonomyValidatesWhenDisabled(t *testing.T) {
	y := `
provider: sample
collectors:
  management:
    enabled: false
    types: [{name: a}]
  deployment:
    enabled: false
    types: [{name: a, method: sidecar}]
`
	if _, err := Parse([]byte(y)); err == nil {
		t.Error("an invalid method in a disabled feature was accepted")
	}
}

// TestMethodListIsDeterministic: the valid-method list appears in a parse error, so it must not reorder
// between runs of the same invalid spec — a message that shuffles makes a test flaky and a support
// transcript useless.
func TestMethodListIsDeterministic(t *testing.T) {
	y := `
provider: sample
collectors:
  management:
    types: [{name: a}]
  deployment:
    types: [{name: a}]
`
	var first string
	for i := 0; i < 20; i++ {
		_, err := Parse([]byte(y))
		if err == nil {
			t.Fatal("expected a parse error")
		}
		if i == 0 {
			first = err.Error()
			continue
		}
		if err.Error() != first {
			t.Fatalf("error text changed between runs:\n%s\n%s", first, err.Error())
		}
	}
	// And it must name the collector methods rather than the instrumentation ones, which are not options.
	if !strings.Contains(first, MethodFargateService) || strings.Contains(first, MethodSidecar) {
		t.Errorf("the error lists the wrong method set: %s", first)
	}
}

// TestCollectorMethodsAreDistinctFromInstrumentationMethods: the two vocabularies must share no value.
// If they ever overlapped, the per-domain split would stop catching a method used in the wrong domain.
func TestCollectorMethodsAreDistinctFromInstrumentationMethods(t *testing.T) {
	instrumentation := []string{MethodAgent, MethodSidecar, MethodService}
	collectors := []string{MethodFargateService, MethodContainerApp, MethodCloudRunService}
	for _, i := range instrumentation {
		for _, c := range collectors {
			if i == c {
				t.Errorf("method %q is valid in both domains", i)
			}
		}
	}
}
