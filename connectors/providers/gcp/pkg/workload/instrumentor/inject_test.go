package instrumentor

import (
	"strings"
	"testing"

	runpb "cloud.google.com/go/run/apiv2/runpb"

	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
)

const testAgentImage = "registry.odigos.io/odigos-agents:test"

func serviceWith(containers ...*runpb.Container) *runpb.Service {
	return &runpb.Service{Template: &runpb.RevisionTemplate{Containers: containers}}
}

func testDest() domaindest.OTLPTarget {
	return domaindest.OTLPTarget{Endpoint: "collector.example:4317", Insecure: true, Traces: true, Metrics: true, Logs: false}
}

func TestApplyInstrumentationJava(t *testing.T) {
	svc := serviceWith(&runpb.Container{
		Image: "openjdk:21",
		Ports: []*runpb.ContainerPort{{ContainerPort: 8080}},
		Env:   []*runpb.EnvVar{{Name: "JAVA_TOOL_OPTIONS", Values: &runpb.EnvVar_Value{Value: "-Dexisting=1"}}},
	})

	if err := applyInstrumentation(svc, LangJava, testAgentImage, "my-svc", "us-central1", testDest(), false); err != nil {
		t.Fatalf("applyInstrumentation: %v", err)
	}

	// Sidecar + shared volume added.
	sc := findContainer(svc, sidecarContainerName)
	if sc == nil {
		t.Fatal("expected agent sidecar container")
	}
	if sc.GetImage() != testAgentImage {
		t.Errorf("sidecar image = %q", sc.GetImage())
	}
	if sc.GetStartupProbe().GetTcpSocket().GetPort() != startupProbePort {
		t.Errorf("sidecar startup probe port = %d", sc.GetStartupProbe().GetTcpSocket().GetPort())
	}
	if !strings.Contains(strings.Join(sc.GetCommand(), " "), "cp -r /instrumentations/java /var/odigos/java") {
		t.Errorf("sidecar command missing java copy: %v", sc.GetCommand())
	}
	if !hasVolume(svc, agentVolumeName) {
		t.Error("expected shared agent volume")
	}

	app := ingressContainer(svc)
	if app.GetName() == "" {
		t.Error("ingress container should be named for multi-container service")
	}
	if !containerHasMount(app, agentVolumeName) {
		t.Error("app container should mount the agent volume")
	}
	if !containerDependsOn(app, sidecarContainerName) {
		t.Error("app container should depend on the sidecar")
	}

	env := containerEnvMap(app)
	// Loader var appended to the customer's original value (not clobbered).
	if got := env["JAVA_TOOL_OPTIONS"]; got != "-Dexisting=1 -javaagent:/var/odigos/java/javaagent.jar" {
		t.Errorf("JAVA_TOOL_OPTIONS = %q", got)
	}
	if env[EnvServiceName] != "my-svc" {
		t.Errorf("OTEL_SERVICE_NAME = %q", env[EnvServiceName])
	}
	if env[EnvOTLPEndpoint] != "http://collector.example:4317" {
		t.Errorf("OTEL_EXPORTER_OTLP_ENDPOINT = %q", env[EnvOTLPEndpoint])
	}
	if env[EnvTracesExporter] != exporterOTLP || env[EnvLogsExporter] != exporterNone {
		t.Errorf("exporters = traces:%q logs:%q", env[EnvTracesExporter], env[EnvLogsExporter])
	}
	if _, ok := env[EnvOdigosManaged]; !ok {
		t.Error("expected ownership marker")
	}

	// Idempotency: converged, and re-applying does not accumulate the loader value.
	if !alreadyInstrumented(svc, LangJava, testAgentImage, "my-svc", "us-central1", testDest(), false) {
		t.Error("service should report instrumentedAsDesired after apply")
	}
	if err := applyInstrumentation(svc, LangJava, testAgentImage, "my-svc", "us-central1", testDest(), false); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if got := containerEnvMap(ingressContainer(svc))["JAVA_TOOL_OPTIONS"]; got != "-Dexisting=1 -javaagent:/var/odigos/java/javaagent.jar" {
		t.Errorf("re-apply accumulated loader value: %q", got)
	}
	if n := len(svc.GetTemplate().GetContainers()); n != 2 {
		t.Errorf("re-apply changed container count to %d, want 2", n)
	}
}

func TestRollbackRestoresOriginal(t *testing.T) {
	svc := serviceWith(&runpb.Container{
		Name:  "app",
		Image: "openjdk:21",
		Ports: []*runpb.ContainerPort{{ContainerPort: 8080}},
		Env:   []*runpb.EnvVar{{Name: "JAVA_TOOL_OPTIONS", Values: &runpb.EnvVar_Value{Value: "-Dexisting=1"}}},
	})

	if err := applyInstrumentation(svc, LangJava, testAgentImage, "my-svc", "us-central1", testDest(), false); err != nil {
		t.Fatalf("applyInstrumentation: %v", err)
	}
	if !rollbackInstrumentation(svc) {
		t.Fatal("rollback should report the service was owned")
	}

	if findContainer(svc, sidecarContainerName) != nil {
		t.Error("sidecar should be removed on rollback")
	}
	if hasVolume(svc, agentVolumeName) {
		t.Error("agent volume should be removed on rollback")
	}
	app := ingressContainer(svc)
	if containerHasMount(app, agentVolumeName) {
		t.Error("agent volume mount should be removed on rollback")
	}
	if containerDependsOn(app, sidecarContainerName) {
		t.Error("dependency should be removed on rollback")
	}
	env := containerEnvMap(app)
	if got := env["JAVA_TOOL_OPTIONS"]; got != "-Dexisting=1" {
		t.Errorf("JAVA_TOOL_OPTIONS not restored to original: %q", got)
	}
	if _, ok := env[EnvOdigosManaged]; ok {
		t.Error("ownership marker should be removed on rollback")
	}
	if _, ok := env[EnvServiceName]; ok {
		t.Error("OTEL_SERVICE_NAME should be removed on rollback (was Odigos-added)")
	}
}

// TestRollbackRestoresPreexistingManagedEnv guards the concern that rollback might delete a customer env
// var that existed before we instrumented. A key we manage (OTEL_EXPORTER_OTLP_ENDPOINT) is snapshotted
// on first instrument and restored to the customer's value on rollback — never dropped — while a var we
// never touch is left alone the whole round-trip.
func TestRollbackRestoresPreexistingManagedEnv(t *testing.T) {
	const customerEndpoint = "http://customer-collector:4317"
	svc := serviceWith(&runpb.Container{
		Name:  "app",
		Image: "openjdk:21",
		Ports: []*runpb.ContainerPort{{ContainerPort: 8080}},
		Env: []*runpb.EnvVar{
			// A managed key the customer already set to their own value.
			{Name: EnvOTLPEndpoint, Values: &runpb.EnvVar_Value{Value: customerEndpoint}},
			// A var we never manage — must be untouched throughout.
			{Name: "MY_APP_VAR", Values: &runpb.EnvVar_Value{Value: "keep-me"}},
		},
	})

	if err := applyInstrumentation(svc, LangJava, testAgentImage, "my-svc", "us-central1", testDest(), false); err != nil {
		t.Fatalf("applyInstrumentation: %v", err)
	}
	// While instrumented we override the endpoint with our destination.
	if got := containerEnvMap(ingressContainer(svc))[EnvOTLPEndpoint]; got != "http://collector.example:4317" {
		t.Errorf("instrumented OTEL_EXPORTER_OTLP_ENDPOINT = %q, want our destination", got)
	}

	if !rollbackInstrumentation(svc) {
		t.Fatal("rollback should report the service was owned")
	}
	env := containerEnvMap(ingressContainer(svc))
	// The customer's pre-existing managed key is restored to their value, not deleted.
	if got := env[EnvOTLPEndpoint]; got != customerEndpoint {
		t.Errorf("OTEL_EXPORTER_OTLP_ENDPOINT not restored to customer value: got %q, want %q", got, customerEndpoint)
	}
	// A var we never touched survives the whole round-trip.
	if got := env["MY_APP_VAR"]; got != "keep-me" {
		t.Errorf("unmanaged MY_APP_VAR = %q, want %q", got, "keep-me")
	}
}

func TestPythonInjection(t *testing.T) {
	svc := serviceWith(&runpb.Container{Image: "python:3.12", Ports: []*runpb.ContainerPort{{ContainerPort: 8080}}})
	if err := applyInstrumentation(svc, LangPython, testAgentImage, "py-svc", "europe-west1", testDest(), false); err != nil {
		t.Fatalf("applyInstrumentation: %v", err)
	}
	env := containerEnvMap(ingressContainer(svc))
	if env["OTEL_PYTHON_CONFIGURATOR"] != "odigos-python-configurator" {
		t.Errorf("OTEL_PYTHON_CONFIGURATOR = %q", env["OTEL_PYTHON_CONFIGURATOR"])
	}
	// With no pre-existing value the agent dirs are the whole value.
	if env["PYTHONPATH"] != "/var/odigos/python:/var/odigos/python/opentelemetry/instrumentation/auto_instrumentation" {
		t.Errorf("PYTHONPATH = %q", env["PYTHONPATH"])
	}
}

// TestPythonPathOrdering verifies the customer's original PYTHONPATH is kept FIRST and our agent dirs are
// appended after it, matching the OSS community distro pattern ({{ORIGINAL}}:{{agents}}).
func TestPythonPathOrdering(t *testing.T) {
	svc := serviceWith(&runpb.Container{
		Image: "python:3.12",
		Ports: []*runpb.ContainerPort{{ContainerPort: 8080}},
		Env:   []*runpb.EnvVar{{Name: "PYTHONPATH", Values: &runpb.EnvVar_Value{Value: "/app/libs"}}},
	})
	if err := applyInstrumentation(svc, LangPython, testAgentImage, "py-svc", "europe-west1", testDest(), false); err != nil {
		t.Fatalf("applyInstrumentation: %v", err)
	}
	got := containerEnvMap(ingressContainer(svc))["PYTHONPATH"]
	want := "/app/libs:/var/odigos/python:/var/odigos/python/opentelemetry/instrumentation/auto_instrumentation"
	if got != want {
		t.Errorf("PYTHONPATH = %q, want %q (customer value first)", got, want)
	}
}

// TestNodeInjection verifies Node uses the upstream env-driven auto-instrumentation register hook (not
// the OpAMP-driven Odigos distro) and that the sidecar stages the upstream nodejs agent dir.
func TestNodeInjection(t *testing.T) {
	svc := serviceWith(&runpb.Container{
		Image: "node:20",
		Ports: []*runpb.ContainerPort{{ContainerPort: 8080}},
		Env:   []*runpb.EnvVar{{Name: "NODE_OPTIONS", Values: &runpb.EnvVar_Value{Value: "--max-old-space-size=256"}}},
	})
	if err := applyInstrumentation(svc, LangNodeJS, testAgentImage, "node-svc", "us-east1", testDest(), false); err != nil {
		t.Fatalf("applyInstrumentation: %v", err)
	}

	const register = "--require /var/odigos/nodejs/node_modules/@opentelemetry/auto-instrumentations-node/build/src/register.js"
	// Customer's original NODE_OPTIONS kept first, our register hook appended.
	if got := containerEnvMap(ingressContainer(svc))["NODE_OPTIONS"]; got != "--max-old-space-size=256 "+register {
		t.Errorf("NODE_OPTIONS = %q", got)
	}

	sc := findContainer(svc, sidecarContainerName)
	if sc == nil {
		t.Fatal("expected agent sidecar container")
	}
	if cmd := strings.Join(sc.GetCommand(), " "); !strings.Contains(cmd, "cp -r /instrumentations/nodejs /var/odigos/nodejs") {
		t.Errorf("sidecar command missing nodejs copy: %v", sc.GetCommand())
	}
	// The OpAMP-driven Odigos Node distro dirs must NOT be staged.
	if cmd := strings.Join(sc.GetCommand(), " "); strings.Contains(cmd, "nodejs-community") || strings.Contains(cmd, "opentelemetry-node") {
		t.Errorf("sidecar should not stage the OpAMP Node distro dirs: %v", sc.GetCommand())
	}
}

func TestApplyUnsupportedLanguage(t *testing.T) {
	svc := serviceWith(&runpb.Container{Image: "golang:1.22"})
	if err := applyInstrumentation(svc, "go", testAgentImage, "go-svc", "us-central1", testDest(), false); err == nil {
		t.Fatal("expected error for unsupported language")
	}
}

func TestRollbackNotOwned(t *testing.T) {
	// A service with a customer's own OTel setup (no Odigos marker) must be left untouched.
	svc := serviceWith(&runpb.Container{
		Image: "openjdk:21",
		Env:   []*runpb.EnvVar{{Name: "JAVA_TOOL_OPTIONS", Values: &runpb.EnvVar_Value{Value: "-javaagent:/customer/agent.jar"}}},
	})
	if rollbackInstrumentation(svc) {
		t.Fatal("rollback should report not-owned for a service without the marker")
	}
	if got := containerEnvMap(ingressContainer(svc))["JAVA_TOOL_OPTIONS"]; got != "-javaagent:/customer/agent.jar" {
		t.Errorf("customer env should be untouched: %q", got)
	}
}

// TestCollectorEndpointEchoesDestination pins the odigos-namespaced ownership var to the user-configured
// destination. It carries the RAW host:port (matching the AWS connector's convention for the same variable
// name), which is deliberately not the scheme-qualified URL the SDK var gets — the detector derives Odigos
// ownership from this var, so a customer who happens to set OTEL_EXPORTER_OTLP_ENDPOINT themselves is never
// mistaken for us.
func TestCollectorEndpointEchoesDestination(t *testing.T) {
	svc := serviceWith(&runpb.Container{Image: "openjdk:21", Ports: []*runpb.ContainerPort{{ContainerPort: 8080}}})
	if err := applyInstrumentation(svc, LangJava, testAgentImage, "my-svc", "us-central1", testDest(), false); err != nil {
		t.Fatalf("applyInstrumentation: %v", err)
	}
	env := containerEnvMap(ingressContainer(svc))
	if got := env[EnvCollectorEndpoint]; got != "collector.example:4317" {
		t.Errorf("ODIGOS_OTLP_ENDPOINT = %q, want the destination host:port", got)
	}
	if env[EnvCollectorEndpoint] == env[EnvOTLPEndpoint] {
		t.Errorf("ODIGOS_OTLP_ENDPOINT should carry host:port, not the SDK's URL form (both are %q)", env[EnvCollectorEndpoint])
	}
	// Converged immediately: if the new var were not part of the comparison, every reconcile would register
	// a fresh revision forever.
	if !alreadyInstrumented(svc, LangJava, testAgentImage, "my-svc", "us-central1", testDest(), false) {
		t.Error("service should be converged right after apply")
	}
}

// TestDestinationChangeForcesReinstrument proves the echoed endpoint participates in convergence: pointing
// the connector at a new destination must make the service non-converged so it is re-instrumented.
func TestDestinationChangeForcesReinstrument(t *testing.T) {
	svc := serviceWith(&runpb.Container{Image: "openjdk:21", Ports: []*runpb.ContainerPort{{ContainerPort: 8080}}})
	if err := applyInstrumentation(svc, LangJava, testAgentImage, "my-svc", "us-central1", testDest(), false); err != nil {
		t.Fatalf("applyInstrumentation: %v", err)
	}
	moved := testDest()
	moved.Endpoint = "other-collector.example:4317"
	if alreadyInstrumented(svc, LangJava, testAgentImage, "my-svc", "us-central1", moved, false) {
		t.Error("a destination change should leave the service non-converged")
	}
	if err := applyInstrumentation(svc, LangJava, testAgentImage, "my-svc", "us-central1", moved, false); err != nil {
		t.Fatalf("re-apply after destination change: %v", err)
	}
	if got := containerEnvMap(ingressContainer(svc))[EnvCollectorEndpoint]; got != moved.Endpoint {
		t.Errorf("ODIGOS_OTLP_ENDPOINT = %q, want %q after re-instrument", got, moved.Endpoint)
	}
	if !alreadyInstrumented(svc, LangJava, testAgentImage, "my-svc", "us-central1", moved, false) {
		t.Error("service should converge on the new destination")
	}
}

// TestRollbackRemovesCollectorEndpointForLegacyMarker covers a service instrumented by a connector build
// that predates ODIGOS_OTLP_ENDPOINT: its marker snapshot never mentions the key, so a rollback driven only
// by the snapshot would leave the var behind — and the detector treats a non-empty ODIGOS_OTLP_ENDPOINT as
// proof of Odigos ownership, so the workload would report as instrumented forever after being rolled back.
func TestRollbackRemovesCollectorEndpointForLegacyMarker(t *testing.T) {
	svc := serviceWith(&runpb.Container{Name: "app", Image: "openjdk:21", Ports: []*runpb.ContainerPort{{ContainerPort: 8080}}})
	if err := applyInstrumentation(svc, LangJava, testAgentImage, "my-svc", "us-central1", testDest(), false); err != nil {
		t.Fatalf("applyInstrumentation: %v", err)
	}

	// Rewrite the marker to a pre-upgrade snapshot: same manifest, minus any knowledge of the new key.
	app := ingressContainer(svc)
	marker, _ := containerEnvValue(app, EnvOdigosManaged)
	m, ok := parseManifest(marker)
	if !ok {
		t.Fatal("expected a parseable marker after apply")
	}
	delete(m.PrevEnv, EnvCollectorEndpoint)
	legacy, err := marshalManifest(m)
	if err != nil {
		t.Fatalf("marshalManifest: %v", err)
	}
	setContainerEnv(app, EnvOdigosManaged, legacy)

	if !rollbackInstrumentation(svc) {
		t.Fatal("rollback should report the service was owned")
	}
	env := containerEnvMap(ingressContainer(svc))
	if v, ok := env[EnvCollectorEndpoint]; ok {
		t.Errorf("ODIGOS_OTLP_ENDPOINT survived rollback as %q; the detector would still report this service instrumented", v)
	}
	if _, ok := env[EnvOdigosManaged]; ok {
		t.Error("ownership marker should be removed on rollback")
	}
}

func TestOTLPEndpointURL(t *testing.T) {
	cases := []struct {
		dest domaindest.OTLPTarget
		want string
	}{
		{domaindest.OTLPTarget{Endpoint: "c:4317", Insecure: true}, "http://c:4317"},
		{domaindest.OTLPTarget{Endpoint: "c:4317", Insecure: false}, "https://c:4317"},
		{domaindest.OTLPTarget{Endpoint: "https://c:4317"}, "https://c:4317"},
		{domaindest.OTLPTarget{Endpoint: ""}, ""},
	}
	for _, c := range cases {
		if got := otlpEndpointURL(c.dest); got != c.want {
			t.Errorf("otlpEndpointURL(%+v) = %q, want %q", c.dest, got, c.want)
		}
	}
}
