package instrumentor

import (
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"

	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
)

func testApp(image string) *armappcontainers.ContainerApp {
	return &armappcontainers.ContainerApp{
		Properties: &armappcontainers.ContainerAppProperties{
			Template: &armappcontainers.Template{
				Containers: []*armappcontainers.Container{{
					Name:  to.Ptr("app"),
					Image: to.Ptr(image),
					Env: []*armappcontainers.EnvironmentVar{
						{Name: to.Ptr("JAVA_VERSION"), Value: to.Ptr("17")},
					},
				}},
			},
		},
	}
}

func TestApplyAndRollbackInstrumentation(t *testing.T) {
	app := testApp("mcr.microsoft.com/openjdk:17")
	dest := domaindest.OTLPTarget{Endpoint: "collector:4317", Traces: true, Metrics: true, Logs: true, Insecure: true}
	const agentImage = "agents:test"

	if err := applyInstrumentation(app, LangJava, agentImage, "hello", "eastus", dest, false); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !alreadyInstrumented(app, LangJava, agentImage, "hello", "eastus", dest, false) {
		t.Fatal("expected alreadyInstrumented after apply")
	}
	env := containerEnvMap(primaryContainer(app))
	if !strings.Contains(env["JAVA_TOOL_OPTIONS"], "javaagent") {
		t.Fatalf("JAVA_TOOL_OPTIONS = %q, want javaagent", env["JAVA_TOOL_OPTIONS"])
	}
	if env[EnvOdigosManaged] == "" {
		t.Fatal("missing ODIGOS_MANAGED marker")
	}
	if findInitContainer(app, initContainerName) == nil {
		t.Fatal("missing init container")
	}
	if !hasVolume(app, agentVolumeName) {
		t.Fatal("missing agent volume")
	}

	// Idempotent re-apply.
	if err := applyInstrumentation(app, LangJava, agentImage, "hello", "eastus", dest, false); err != nil {
		t.Fatalf("re-apply: %v", err)
	}

	if !rollbackInstrumentation(app) {
		t.Fatal("rollback returned false")
	}
	env = containerEnvMap(primaryContainer(app))
	if env[EnvOdigosManaged] != "" {
		t.Fatal("ODIGOS_MANAGED should be cleared")
	}
	if findInitContainer(app, initContainerName) != nil {
		t.Fatal("init container should be removed")
	}
	if hasVolume(app, agentVolumeName) {
		t.Fatal("agent volume should be removed")
	}
	if env["JAVA_VERSION"] != "17" {
		t.Fatalf("customer env JAVA_VERSION lost: %q", env["JAVA_VERSION"])
	}
}

// TestCollectorEndpointEchoesDestination pins ODIGOS_OTLP_ENDPOINT to the user-configured
// destination as raw host:port (same convention as AWS for this name), not the
// scheme-qualified URL written to OTEL_EXPORTER_OTLP_ENDPOINT. This is not an ownership
// signal — that is ODIGOS_MANAGED alone — but it must still participate in
// alreadyInstrumented, or every reconcile would create a fresh revision.
func TestCollectorEndpointEchoesDestination(t *testing.T) {
	app := testApp("mcr.microsoft.com/openjdk:17")
	dest := domaindest.OTLPTarget{Endpoint: "collector:4317", Traces: true, Metrics: true, Logs: true, Insecure: true}
	const agentImage = "agents:test"

	if err := applyInstrumentation(app, LangJava, agentImage, "hello", "eastus", dest, false); err != nil {
		t.Fatalf("apply: %v", err)
	}
	env := containerEnvMap(primaryContainer(app))
	if got := env[EnvCollectorEndpoint]; got != "collector:4317" {
		t.Errorf("ODIGOS_OTLP_ENDPOINT = %q, want the destination host:port", got)
	}
	if env[EnvCollectorEndpoint] == env[EnvOTLPEndpoint] {
		t.Errorf("ODIGOS_OTLP_ENDPOINT should carry host:port, not the SDK's URL form (both are %q)", env[EnvCollectorEndpoint])
	}
	if !alreadyInstrumented(app, LangJava, agentImage, "hello", "eastus", dest, false) {
		t.Error("app should be converged right after apply")
	}

	moved := dest
	moved.Endpoint = "other-collector:4317"
	if alreadyInstrumented(app, LangJava, agentImage, "hello", "eastus", moved, false) {
		t.Error("a destination change should leave the app non-converged")
	}
	if err := applyInstrumentation(app, LangJava, agentImage, "hello", "eastus", moved, false); err != nil {
		t.Fatalf("re-apply after destination change: %v", err)
	}
	if got := containerEnvMap(primaryContainer(app))[EnvCollectorEndpoint]; got != moved.Endpoint {
		t.Errorf("ODIGOS_OTLP_ENDPOINT = %q, want %q after re-instrument", got, moved.Endpoint)
	}
}

// TestRollbackRemovesCollectorEndpointForLegacyMarker covers an app instrumented by a connector build that
// predates ODIGOS_OTLP_ENDPOINT: its marker snapshot never mentions the key, so a rollback driven only by
// the snapshot would leave the var behind — and the detector treats a non-empty ODIGOS_OTLP_ENDPOINT as
// proof of Odigos ownership, so the workload would report as instrumented forever after being rolled back.
func TestRollbackRemovesCollectorEndpointForLegacyMarker(t *testing.T) {
	app := testApp("mcr.microsoft.com/openjdk:17")
	dest := domaindest.OTLPTarget{Endpoint: "collector:4317", Traces: true, Insecure: true}
	const agentImage = "agents:test"

	if err := applyInstrumentation(app, LangJava, agentImage, "hello", "eastus", dest, false); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Rewrite the marker to a pre-upgrade snapshot: same manifest, minus any knowledge of the new key.
	c := primaryContainer(app)
	m, ok := parseManifest(containerEnvMap(c)[EnvOdigosManaged])
	if !ok {
		t.Fatal("expected a parseable marker after apply")
	}
	delete(m.PrevEnv, EnvCollectorEndpoint)
	legacy, err := marshalManifest(m)
	if err != nil {
		t.Fatalf("marshalManifest: %v", err)
	}
	setContainerEnv(c, EnvOdigosManaged, legacy)

	if !rollbackInstrumentation(app) {
		t.Fatal("rollback returned false")
	}
	env := containerEnvMap(primaryContainer(app))
	if v := env[EnvCollectorEndpoint]; v != "" {
		t.Errorf("ODIGOS_OTLP_ENDPOINT survived rollback as %q; the detector would still report this app instrumented", v)
	}
	if env[EnvOdigosManaged] != "" {
		t.Error("ODIGOS_MANAGED should be cleared")
	}
}

func TestDetectLanguageFromOverride(t *testing.T) {
	app := testApp("scratch")
	if got := detectLanguage("python", "", app); got != LangPython {
		t.Fatalf("detectLanguage = %q, want python", got)
	}
}
