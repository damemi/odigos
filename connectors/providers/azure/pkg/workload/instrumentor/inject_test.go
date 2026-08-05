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

func TestDetectLanguageFromOverride(t *testing.T) {
	app := testApp("scratch")
	if got := detectLanguage("python", "", app); got != LangPython {
		t.Fatalf("detectLanguage = %q, want python", got)
	}
}
