package resources

import (
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cloudconnectorsv1 "github.com/odigos-io/odigos/api/cloud-connectors/v1alpha1"

	"github.com/odigos-io/odigos/connectors/spec"
)

const collectorType = "aws.fargate-collector"

func connector(mutate func(*cloudconnectorsv1.OdigosCloudConnector)) *cloudconnectorsv1.OdigosCloudConnector {
	cr := &cloudconnectorsv1.OdigosCloudConnector{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-prod", Namespace: "odigos-system"},
		Spec: cloudconnectorsv1.OdigosCloudConnectorSpec{
			Provider: "aws",
			Account:  cloudconnectorsv1.CloudConnectorAccount{ID: "123456789012"},
		},
	}
	if mutate != nil {
		mutate(cr)
	}
	return cr
}

func annotate(value string) func(*cloudconnectorsv1.OdigosCloudConnector) {
	return func(cr *cloudconnectorsv1.OdigosCloudConnector) {
		cr.Annotations = map[string]string{AnnotationCollectorsCapability: value}
	}
}

func withCollectors(entries ...cloudconnectorsv1.CloudConnectorCollectorCapability) func(*cloudconnectorsv1.OdigosCloudConnector) {
	return func(cr *cloudconnectorsv1.OdigosCloudConnector) { cr.Spec.Collectors = entries }
}

// TestCollectorCapabilitiesFromField is the CRD path: spec.collectors[] reaches the connector as the
// type the connector itself unmarshals into.
func TestCollectorCapabilitiesFromField(t *testing.T) {
	cr := connector(withCollectors(
		cloudconnectorsv1.CloudConnectorCollectorCapability{Type: collectorType, Management: true, Deployment: true},
	))

	entries, problems := CollectorCapabilities(cr)
	if len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}
	want := []spec.CollectorCapability{{Type: collectorType, Management: true, Deployment: true}}
	if len(entries) != 1 || entries[0] != want[0] {
		t.Errorf("entries = %+v, want %+v", entries, want)
	}
}

// TestCollectorCapabilitiesFromAnnotation is the compatibility path: a cluster whose CRD predates
// spec.collectors[] can still enable a Collector, which is the whole reason the annotation exists.
func TestCollectorCapabilitiesFromAnnotation(t *testing.T) {
	cr := connector(annotate(`[{"type":"aws.fargate-collector","management":true}]`))

	entries, problems := CollectorCapabilities(cr)
	if len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}
	want := spec.CollectorCapability{Type: collectorType, Management: true}
	if len(entries) != 1 || entries[0] != want {
		t.Errorf("entries = %+v, want %+v", entries, want)
	}
}

// TestFieldWinsOverAnnotation: the annotation is a shim, and once the field exists it decides. The
// shadowed annotation is reported rather than ignored quietly — it is exactly what somebody edits when
// they want the capability to change, and then wonders why nothing happened.
func TestFieldWinsOverAnnotation(t *testing.T) {
	cr := connector(func(cr *cloudconnectorsv1.OdigosCloudConnector) {
		withCollectors(cloudconnectorsv1.CloudConnectorCollectorCapability{
			Type: collectorType, Management: true,
		})(cr)
		annotate(`[{"type":"aws.fargate-collector","management":true,"deployment":true}]`)(cr)
	})

	entries, problems := CollectorCapabilities(cr)
	if len(entries) != 1 || entries[0].Deployment {
		t.Errorf("the annotation overrode the field: %+v", entries)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], AnnotationCollectorsCapability) {
		t.Errorf("a shadowed annotation was not reported: %v", problems)
	}
}

// TestNoCapabilityWithoutEitherSource: the default is no managed Collector. A connector that was never
// asked for one must not be handed the capability.
func TestNoCapabilityWithoutEitherSource(t *testing.T) {
	entries, problems := CollectorCapabilities(connector(nil))
	if len(entries) != 0 || len(problems) != 0 {
		t.Errorf("entries = %v, problems = %v; want neither", entries, problems)
	}

	// An empty or whitespace annotation is absence, not a parse failure. Helm renders an empty string
	// for an unset value, and that must not read as a broken annotation.
	for _, blank := range []string{"", "   ", "\n"} {
		entries, problems := CollectorCapabilities(connector(annotate(blank)))
		if len(entries) != 0 || len(problems) != 0 {
			t.Errorf("annotation %q: entries = %v, problems = %v; want neither", blank, entries, problems)
		}
	}
}

// TestContradictoryCapabilityIsDroppedNotPassedOn is the "no infrastructure on unsupported versions"
// criterion's sibling: a list the connector would refuse to start on is dropped here instead, so the
// connector keeps instrumenting workloads and simply has no Collector deployer.
func TestContradictoryCapabilityIsDroppedNotPassedOn(t *testing.T) {
	cases := map[string]func(*cloudconnectorsv1.OdigosCloudConnector){
		// Odigos would build a Collector it never configures, which serves an inert fallback for ever.
		"deployment without management": withCollectors(
			cloudconnectorsv1.CloudConnectorCollectorCapability{Type: collectorType, Deployment: true},
		),
		"conflicting duplicate": withCollectors(
			cloudconnectorsv1.CloudConnectorCollectorCapability{Type: collectorType, Management: true},
			cloudconnectorsv1.CloudConnectorCollectorCapability{Type: collectorType, Management: true, Deployment: true},
		),
		"deployment without management, via the annotation": annotate(
			`[{"type":"aws.fargate-collector","deployment":true}]`),
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cr := connector(mutate)
			entries, problems := CollectorCapabilities(cr)
			if len(entries) != 0 {
				t.Errorf("a contradictory list was passed to the connector: %+v", entries)
			}
			if len(problems) == 0 {
				t.Error("the capability was dropped silently")
			}

			// And the connector must be given no collectors at all, so no deployer is wired.
			if got := capabilitiesJSON(cr); strings.Contains(got, "collectors") {
				t.Errorf("ODIGOS_CAPABILITIES still carries a collectors key: %s", got)
			}
		})
	}
}

// TestMalformedAnnotationIsRefused: the annotation is hand-written, so it is parsed strictly. Guessing
// at a typo would enable something other than what was asked for, on the one capability that spends
// the customer's money.
func TestMalformedAnnotationIsRefused(t *testing.T) {
	cases := map[string]string{
		"not JSON":              `aws.fargate-collector`,
		"an object, not a list": `{"type":"aws.fargate-collector","management":true}`,
		"truncated":             `[{"type":"aws.fargate-collector"`,
		"misspelled key":        `[{"type":"aws.fargate-collector","managment":true}]`,
		"an entry with no type": `[{"management":true}]`,
		"an empty type":         `[{"type":"   ","management":true}]`,
		"wrong value type":      `[{"type":"aws.fargate-collector","management":"yes"}]`,
	}

	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			cr := connector(annotate(value))
			entries, problems := CollectorCapabilities(cr)
			if len(entries) != 0 {
				t.Errorf("a malformed annotation produced a capability: %+v", entries)
			}
			if len(problems) != 1 {
				t.Fatalf("problems = %v, want exactly one", problems)
			}
			// The message has to name the annotation and say what the consequence was, or an operator
			// sees an event that tells them nothing actionable.
			if !strings.Contains(problems[0], AnnotationCollectorsCapability) {
				t.Errorf("the problem does not name the annotation: %s", problems[0])
			}
			if !strings.Contains(problems[0], "no Collector infrastructure") {
				t.Errorf("the problem does not say what the consequence is: %s", problems[0])
			}
		})
	}
}

// TestCapabilitiesJSONIsUnchangedWithoutCollectors is the safe-upgrade criterion, and it is about pod
// churn rather than correctness: ODIGOS_CAPABILITIES is a container env var, so a collectors key that
// appeared unconditionally would roll every connector pod in the fleet on upgrade.
func TestCapabilitiesJSONIsUnchangedWithoutCollectors(t *testing.T) {
	cr := connector(func(cr *cloudconnectorsv1.OdigosCloudConnector) {
		cr.Spec.Workloads = []cloudconnectorsv1.CloudConnectorWorkloadCapability{
			{Type: "aws.lambda", Discovery: true, Instrumentation: true},
		}
		cr.Spec.ComputePlatforms = []cloudconnectorsv1.CloudConnectorComputePlatformCapability{
			{Type: "aws.ecs-cluster", Discovery: true},
		}
	})

	want := `{"workloads":[{"type":"aws.lambda","discovery":true,"instrumentation":true}],` +
		`"computePlatforms":[{"type":"aws.ecs-cluster","discovery":true}]}`
	if got := capabilitiesJSON(cr); got != want {
		t.Errorf("capabilitiesJSON changed for a connector with no Collector:\n got %s\nwant %s", got, want)
	}

	// And a CR with nothing enabled at all still serializes to the empty object.
	if got := capabilitiesJSON(connector(nil)); got != "{}" {
		t.Errorf("capabilitiesJSON = %s, want {}", got)
	}
}

// TestCapabilitiesJSONCarriesCollectors: the key the connector reads, in the shape it reads.
func TestCapabilitiesJSONCarriesCollectors(t *testing.T) {
	cr := connector(withCollectors(
		cloudconnectorsv1.CloudConnectorCollectorCapability{Type: collectorType, Management: true, Deployment: true},
	))

	var parsed spec.Capabilities
	if err := json.Unmarshal([]byte(capabilitiesJSON(cr)), &parsed); err != nil {
		t.Fatalf("the connector could not parse ODIGOS_CAPABILITIES: %v", err)
	}
	if err := parsed.Validate(); err != nil {
		t.Fatalf("the connector would refuse to start on what the controller emitted: %v", err)
	}
	if got := parsed.CollectorMode(collectorType); got != spec.ModeAutomatic {
		t.Errorf("CollectorMode = %q, want %q", got, spec.ModeAutomatic)
	}
}

// TestManagementOnlyIsManualMode: management without deployment is the manual path — Odigos configures
// a Collector the customer deployed and never touches their infrastructure. It must not derive to
// automatic, because automatic is the mode that grants mutation.
func TestManagementOnlyIsManualMode(t *testing.T) {
	cr := connector(withCollectors(
		cloudconnectorsv1.CloudConnectorCollectorCapability{Type: collectorType, Management: true},
	))

	var parsed spec.Capabilities
	if err := json.Unmarshal([]byte(capabilitiesJSON(cr)), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := parsed.CollectorMode(collectorType); got != spec.ModeManual {
		t.Errorf("CollectorMode = %q, want %q", got, spec.ModeManual)
	}
	if types := parsed.Collectors; len(types) != 1 || types[0].Deployment {
		t.Errorf("deployment leaked into a management-only capability: %+v", types)
	}
}

// TestOlderConnectorImageIgnoresTheCollectorsKey is the "older images fail safely" criterion.
//
// A connector image built before the collectors domain has no Collectors field on spec.Capabilities.
// It parses ODIGOS_CAPABILITIES with plain encoding/json and no strict mode, so the unknown key is
// ignored and it wires no Collector deployer — which is why the controller does not need to know the
// connector's version to stay safe in front of it.
func TestOlderConnectorImageIgnoresTheCollectorsKey(t *testing.T) {
	// spec.Capabilities as it was before the collectors domain existed.
	type olderCapabilities struct {
		Workloads        []spec.WorkloadCapability        `json:"workloads,omitempty"`
		ComputePlatforms []spec.ComputePlatformCapability `json:"computePlatforms,omitempty"`
	}

	cr := connector(func(cr *cloudconnectorsv1.OdigosCloudConnector) {
		cr.Spec.Workloads = []cloudconnectorsv1.CloudConnectorWorkloadCapability{
			{Type: "aws.lambda", Discovery: true, Instrumentation: true},
		}
		withCollectors(cloudconnectorsv1.CloudConnectorCollectorCapability{
			Type: collectorType, Management: true, Deployment: true,
		})(cr)
	})
	emitted := capabilitiesJSON(cr)
	if !strings.Contains(emitted, "collectors") {
		t.Fatalf("the controller emitted no collectors key, so this test proves nothing: %s", emitted)
	}

	var older olderCapabilities
	if err := json.Unmarshal([]byte(emitted), &older); err != nil {
		t.Fatalf("an older connector image failed to parse ODIGOS_CAPABILITIES: %v", err)
	}
	// The domains it does understand must survive intact — an old image has to keep instrumenting.
	if len(older.Workloads) != 1 || !older.Workloads[0].Instrumentation {
		t.Errorf("an older image lost its workload capabilities: %+v", older.Workloads)
	}
}

// TestNewerConnectorImageReadsAnOlderControllersOutput is the downgrade direction: a new connector
// image in front of a controller that emits no collectors key must derive no Collector, not an empty
// one that reads as enabled.
func TestNewerConnectorImageReadsAnOlderControllersOutput(t *testing.T) {
	var parsed spec.Capabilities
	older := `{"workloads":[{"type":"aws.lambda","discovery":true,"instrumentation":true}]}`
	if err := json.Unmarshal([]byte(older), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := parsed.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(parsed.Collectors) != 0 {
		t.Errorf("Collectors = %+v, want none", parsed.Collectors)
	}
	if got := parsed.CollectorMode(collectorType); got != "" {
		t.Errorf("CollectorMode = %q, want the empty mode", got)
	}
	if types := parsed.Collectors; len(types) != 0 {
		t.Errorf("a Collector was derived from a controller that never mentioned one: %+v", types)
	}
}

// TestCollectorCapabilitiesDoesNotMutateTheCR: the resolver runs on an object from the informer cache
// on every reconcile, and it is also called twice per reconcile (once for the event, once for the
// Deployment). Editing the CR there would be a change every other reader sees.
func TestCollectorCapabilitiesDoesNotMutateTheCR(t *testing.T) {
	cr := connector(func(c *cloudconnectorsv1.OdigosCloudConnector) {
		withCollectors(cloudconnectorsv1.CloudConnectorCollectorCapability{
			Type: collectorType, Management: true,
		})(c)
		annotate(`[{"type":"other","management":true}]`)(c)
	})
	before, err := json.Marshal(cr)
	if err != nil {
		t.Fatal(err)
	}

	first, firstProblems := CollectorCapabilities(cr)
	second, secondProblems := CollectorCapabilities(cr)

	after, err := json.Marshal(cr)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("CollectorCapabilities mutated the CR")
	}
	// Deterministic, so the event the controller emits and the env var it writes agree.
	if len(first) != len(second) || len(firstProblems) != len(secondProblems) {
		t.Errorf("two calls disagreed: %v/%v vs %v/%v", first, firstProblems, second, secondProblems)
	}
}
