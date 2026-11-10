package resources

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	cloudconnectorsv1 "github.com/odigos-io/odigos/api/cloud-connectors/v1alpha1"

	"github.com/odigos-io/odigos-enterprise/connectors/opamp"
)

const (
	prodEndpoint    = "wss://central.odigos.io/v1/opamp"
	stagingEndpoint = "wss://central.staging.odigos.io/v1/opamp"
)

func managedCollector() func(*cloudconnectorsv1.OdigosCloudConnector) {
	return withCollectors(cloudconnectorsv1.CloudConnectorCollectorCapability{
		Type: collectorType, Management: true, Deployment: true,
	})
}

// envVar returns a connector Deployment's env var by name, and whether it is present at all. Presence
// matters separately from value: the controller omits the endpoint rather than stamping it empty.
func envVar(t *testing.T, cr *cloudconnectorsv1.OdigosCloudConnector, params DeploymentParams, name string) (string, bool) {
	t.Helper()
	dep := BuildDeployment(cr, params)
	for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

func paramsWith(endpoint string) DeploymentParams {
	return DeploymentParams{
		Namespace:     "odigos-system",
		WorkloadName:  WorkloadName("aws", "aws-prod"),
		Image:         "registry.odigos.io/connector-aws:test",
		SchemaName:    "connector_aws",
		OpAMPEndpoint: endpoint,
	}
}

// TestEndpointIsStampedPerEnvironment is the "correct endpoint" criterion. Staging and production are
// different Central installations, so the value the controller was given is the value the connector
// gets — no default, no rewriting.
func TestEndpointIsStampedPerEnvironment(t *testing.T) {
	for _, configured := range []string{prodEndpoint, stagingEndpoint} {
		cr := connector(managedCollector())
		resolved, problems := OpAMPEndpoint(cr, configured)
		if len(problems) != 0 {
			t.Fatalf("OpAMPEndpoint(%q) = %v", configured, problems)
		}
		if resolved != configured {
			t.Errorf("resolved %q to %q; a well-formed endpoint must not be rewritten", configured, resolved)
		}

		got, present := envVar(t, cr, paramsWith(resolved), opamp.EnvEndpoint)
		if !present {
			t.Fatalf("%s was not stamped onto the connector Deployment", opamp.EnvEndpoint)
		}
		if got != configured {
			t.Errorf("%s = %q, want %q", opamp.EnvEndpoint, got, configured)
		}
	}
}

// TestHostOnlyEndpointGetsTheDefaultPath: an operator who configured the host has said everything
// Odigos needs. The path is the gateway's, not theirs, so it is filled in rather than refused.
func TestHostOnlyEndpointGetsTheDefaultPath(t *testing.T) {
	resolved, problems := OpAMPEndpoint(connector(managedCollector()), "wss://central.odigos.io")
	if len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}
	if resolved != prodEndpoint {
		t.Errorf("resolved = %q, want %q", resolved, prodEndpoint)
	}
}

// TestEndpointIsStampedEvenWithoutTheCapability: enabling a managed Collector later must not need a
// controller change, so the endpoint travels whenever it is configured. It is inert until the
// capability turns up.
func TestEndpointIsStampedEvenWithoutTheCapability(t *testing.T) {
	cr := connector(nil)
	resolved, problems := OpAMPEndpoint(cr, prodEndpoint)
	if len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}
	if got, present := envVar(t, cr, paramsWith(resolved), opamp.EnvEndpoint); !present || got != prodEndpoint {
		t.Errorf("%s = %q (present %v), want %q", opamp.EnvEndpoint, got, present, prodEndpoint)
	}
}

// TestUnsetEndpointOmitsTheVariable: absent, not empty. A connector that has an endpoint and one that
// does not then differ by the presence of the variable rather than by its value — and turning the
// feature off does not leave a variable behind claiming otherwise.
//
// It is also the pod-churn property: the env var is part of the pod template, so a variable that
// appeared unconditionally would roll every connector in an installation that does not use Collectors.
func TestUnsetEndpointOmitsTheVariable(t *testing.T) {
	for _, configured := range []string{"", "   "} {
		cr := connector(nil)
		resolved, problems := OpAMPEndpoint(cr, configured)
		if len(problems) != 0 {
			t.Errorf("an unset endpoint on a connector with no Collector reported %v", problems)
		}
		if _, present := envVar(t, cr, paramsWith(resolved), opamp.EnvEndpoint); present {
			t.Errorf("%s was stamped for endpoint %q", opamp.EnvEndpoint, configured)
		}
	}
}

// TestMissingEndpointIsActionableWhenACollectorWasAskedFor is the "missing config is actionable"
// criterion. The fix is a chart value, and the person who can change it is looking at the connector
// they just enabled — so the message has to name the value, not just the symptom.
func TestMissingEndpointIsActionableWhenACollectorWasAskedFor(t *testing.T) {
	cr := connector(managedCollector())

	resolved, problems := OpAMPEndpoint(cr, "")
	if resolved != "" {
		t.Errorf("resolved = %q, want empty", resolved)
	}
	if len(problems) != 1 {
		t.Fatalf("problems = %v, want exactly one", problems)
	}
	for _, want := range []string{
		"cloudConnectors.managedCollectors.externalEndpoint", // the value to set
		"odigos-central",                // where to set it
		opamp.DefaultPath,               // what it should look like
		"no Collector will be deployed", // the consequence
	} {
		if !strings.Contains(problems[0], want) {
			t.Errorf("the message does not mention %q: %s", want, problems[0])
		}
	}
}

// TestMisconfiguredEndpointIsReportedAndNotStamped: a value somebody set and got wrong is a different
// message from one never set, and neither is stamped — a Collector pointed at nothing would still hold
// a credential and still report as running.
func TestMisconfiguredEndpointIsReportedAndNotStamped(t *testing.T) {
	cases := map[string]string{
		"plaintext to a real host": "ws://central.odigos.io/v1/opamp",
		"not a URL":                "central.odigos.io",
		"credentials in the URL":   "wss://user:pass@central.odigos.io/v1/opamp",
		"a query string":           "wss://central.odigos.io/v1/opamp?x=1",
	}

	for name, configured := range cases {
		t.Run(name, func(t *testing.T) {
			cr := connector(managedCollector())
			resolved, problems := OpAMPEndpoint(cr, configured)
			if resolved != "" {
				t.Errorf("a rejected endpoint was still stamped: %q", resolved)
			}
			if len(problems) != 1 {
				t.Fatalf("problems = %v, want exactly one", problems)
			}
			if !strings.Contains(problems[0], "cannot be used") {
				t.Errorf("a misconfigured endpoint was reported as unset: %s", problems[0])
			}
			if _, present := envVar(t, cr, paramsWith(resolved), opamp.EnvEndpoint); present {
				t.Errorf("%s was stamped for a rejected endpoint", opamp.EnvEndpoint)
			}
		})
	}
}

// TestNoEndpointComplaintWithoutACollector: an installation that does not use managed Collectors has
// no use for the endpoint. Warning on every connector in it is how an operator learns to ignore the
// warning that matters.
func TestNoEndpointComplaintWithoutACollector(t *testing.T) {
	for _, configured := range []string{"", "nonsense", "ws://central.odigos.io/v1/opamp"} {
		if _, problems := OpAMPEndpoint(connector(nil), configured); len(problems) != 0 {
			t.Errorf("endpoint %q on a connector with no Collector reported %v", configured, problems)
		}
	}

	// And a capability that was itself refused does not earn an endpoint complaint either — one broken
	// thing should produce one report, not two.
	badCapability := connector(withCollectors(
		cloudconnectorsv1.CloudConnectorCollectorCapability{Type: collectorType, Deployment: true},
	))
	if _, problems := OpAMPEndpoint(badCapability, ""); len(problems) != 0 {
		t.Errorf("a dropped capability also reported a missing endpoint: %v", problems)
	}
}

// TestEndpointChangeIsTheOnlyDeploymentDifference is the "safe endpoint changes" criterion at the
// controller: moving the endpoint changes exactly one env var, so the pod rolls once and nothing else
// about the connector is disturbed.
func TestEndpointChangeIsTheOnlyDeploymentDifference(t *testing.T) {
	cr := connector(managedCollector())

	before := BuildDeployment(cr, paramsWith(prodEndpoint))
	after := BuildDeployment(cr, paramsWith(stagingEndpoint))

	if len(before.Spec.Template.Spec.Containers[0].Env) != len(after.Spec.Template.Spec.Containers[0].Env) {
		t.Fatalf("the env var count changed: %d -> %d",
			len(before.Spec.Template.Spec.Containers[0].Env),
			len(after.Spec.Template.Spec.Containers[0].Env))
	}

	var differing []string
	beforeEnv := before.Spec.Template.Spec.Containers[0].Env
	afterEnv := after.Spec.Template.Spec.Containers[0].Env
	for i := range beforeEnv {
		if !envEqual(beforeEnv[i], afterEnv[i]) {
			differing = append(differing, beforeEnv[i].Name)
		}
	}
	if len(differing) != 1 || differing[0] != opamp.EnvEndpoint {
		t.Errorf("moving the endpoint changed %v, want only %s", differing, opamp.EnvEndpoint)
	}

	// The selector is immutable, so it must be untouched — a changed one orphans the running Deployment
	// and its live instrumentation.
	if before.Spec.Selector.String() != after.Spec.Selector.String() {
		t.Error("moving the endpoint changed the immutable pod selector")
	}
}

func envEqual(a, b corev1.EnvVar) bool {
	return a.Name == b.Name && a.Value == b.Value &&
		(a.ValueFrom == nil) == (b.ValueFrom == nil)
}
