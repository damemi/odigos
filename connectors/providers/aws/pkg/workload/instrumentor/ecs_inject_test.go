package instrumentor

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	domainsrc "github.com/odigos-io/odigos-enterprise/connectors/base/domain/source"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
)

// oneTarget is the single-container target set most of these tests exercise: multi-container
// instrumentation is the general case, but one app container remains the overwhelmingly common one.
func oneTarget(idx int, recipe agentRecipe, export ecsExportTarget) []containerTarget {
	return []containerTarget{{idx: idx, name: "app", recipe: recipe, export: export}}
}

const testAgentsImage = "registry.odigos.io/odigos-fargate-agents:test"

func testExport() ecsExportTarget {
	return ecsExportTarget{endpoint: "http://collector:4317", protocol: otlpProtocolGRPC, traces: true}
}

// sampleTaskDef is a minimal Fargate task definition with one essential app container carrying a
// customer JAVA_TOOL_OPTIONS value (exercises the append snapshot/restore path).
func sampleTaskDef() *ecstypes.TaskDefinition {
	return &ecstypes.TaskDefinition{
		Family:                  aws.String("checkout"),
		TaskDefinitionArn:       aws.String("arn:aws:ecs:us-east-1:111122223333:task-definition/checkout:7"),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("512"),
		Memory:                  aws.String("1024"),
		ExecutionRoleArn:        aws.String("arn:aws:iam::111122223333:role/exec"),
		TaskRoleArn:             aws.String("arn:aws:iam::111122223333:role/task"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:      aws.String("app"),
			Image:     aws.String("111122223333.dkr.ecr.us-east-1.amazonaws.com/checkout:latest"),
			Essential: aws.Bool(true),
			PortMappings: []ecstypes.PortMapping{
				{ContainerPort: aws.Int32(8080)},
			},
			Environment: []ecstypes.KeyValuePair{
				{Name: aws.String("JAVA_TOOL_OPTIONS"), Value: aws.String("-Xmx512m")},
				{Name: aws.String("APP_MODE"), Value: aws.String("prod")},
			},
		}},
	}
}

// tdFromRegisterInput reconstructs a TaskDefinition from a register input, as DescribeTaskDefinition
// would return the new revision — the shape the next reconcile cycle sees.
func tdFromRegisterInput(in *ecs.RegisterTaskDefinitionInput) *ecstypes.TaskDefinition {
	return &ecstypes.TaskDefinition{
		Family:                  in.Family,
		ContainerDefinitions:    in.ContainerDefinitions,
		Volumes:                 in.Volumes,
		Cpu:                     in.Cpu,
		Memory:                  in.Memory,
		NetworkMode:             in.NetworkMode,
		RequiresCompatibilities: in.RequiresCompatibilities,
		ExecutionRoleArn:        in.ExecutionRoleArn,
		TaskRoleArn:             in.TaskRoleArn,
		RuntimePlatform:         in.RuntimePlatform,
	}
}

func testRecipe(t *testing.T, lang, version string) agentRecipe {
	t.Helper()
	var r recipes
	rec, err := r.recipeFor(lang, version)
	if err != nil {
		t.Fatalf("recipeFor(%s, %q): %v", lang, version, err)
	}
	return rec
}

// TestApplyAlreadyInstrumentedRoundtrip: per language, apply → the resulting revision reads as
// converged; re-apply is stable (no env accumulation); a different debug flag or sidecar image breaks
// convergence.
func TestApplyAlreadyInstrumentedRoundtrip(t *testing.T) {
	langs := []struct{ lang, version string }{
		{LangJava, ""}, {LangPython, ""}, {LangNodeJS, ""}, {LangDotNet, ""}, {LangPHP, "8.3"}, {LangRuby, "3.3"},
	}
	for _, lc := range langs {
		t.Run(lc.lang, func(t *testing.T) {
			rec := testRecipe(t, lc.lang, lc.version)
			export := testExport()
			if rec.otlpProtocol == otlpProtocolHTTP {
				export = ecsExportTarget{endpoint: "http://collector:4318", protocol: otlpProtocolHTTP, traces: true}
			}

			td := sampleTaskDef()
			input, err := applyInstrumentation(td, oneTarget(0, rec, export), nil, testAgentsImage, "svc-checkout", "us-east-1", false)
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			next := tdFromRegisterInput(input)
			if !alreadyInstrumented(next, oneTarget(0, rec, export), testAgentsImage, "svc-checkout", "us-east-1", false) {
				t.Fatal("freshly applied task definition should read as already instrumented")
			}
			// Divergent desired state must break convergence (forces a converging re-instrument).
			if alreadyInstrumented(next, oneTarget(0, rec, export), testAgentsImage, "svc-checkout", "us-east-1", true) {
				t.Error("debug-flag change should not read as converged")
			}
			if alreadyInstrumented(next, oneTarget(0, rec, export), "other-image:v9", "svc-checkout", "us-east-1", false) {
				t.Error("sidecar image change should not read as converged")
			}

			// Re-apply on the instrumented revision: byte-stable env (append vars rebuilt from the
			// snapshot, never accumulated).
			input2, err := applyInstrumentation(next, oneTarget(0, rec, export), nil, testAgentsImage, "svc-checkout", "us-east-1", false)
			if err != nil {
				t.Fatalf("re-apply: %v", err)
			}
			app1 := findECSContainer(tdFromRegisterInput(input), "app")
			app2 := findECSContainer(tdFromRegisterInput(input2), "app")
			if !reflect.DeepEqual(containerEnvMap(app1), containerEnvMap(app2)) {
				t.Errorf("re-apply changed env:\n first=%v\nsecond=%v", containerEnvMap(app1), containerEnvMap(app2))
			}
			if lc.lang == LangJava {
				if got := containerEnvMap(app2)["JAVA_TOOL_OPTIONS"]; got != "-Xmx512m -javaagent:/var/odigos/java/javaagent.jar" {
					t.Errorf("JAVA_TOOL_OPTIONS = %q, want customer value first + single agent", got)
				}
			}
		})
	}
}

func TestApplyWiresSidecarVolumeAndDependency(t *testing.T) {
	rec := testRecipe(t, LangJava, "")
	td := sampleTaskDef()
	input, err := applyInstrumentation(td, oneTarget(0, rec, testExport()), nil, testAgentsImage, "svc", "eu-west-1", false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	next := tdFromRegisterInput(input)

	sc := findECSContainer(next, sidecarContainerName)
	if sc == nil {
		t.Fatal("sidecar container missing")
	}
	if sc.Essential == nil || *sc.Essential {
		t.Error("sidecar must be non-essential (run-to-completion)")
	}
	if got := strings.Join(sc.Command, " "); !strings.Contains(got, "cp -r /instrumentations/java /var/odigos/java") {
		t.Errorf("sidecar command = %q, want agent copy", got)
	}
	if strings.Contains(strings.Join(sc.Command, " "), "nc -l") {
		t.Error("sidecar must not hold a port on ECS (SUCCESS dependency covers ordering)")
	}
	if !hasAgentVolume(next) {
		t.Error("shared agent volume missing")
	}
	app := findECSContainer(next, "app")
	if !containerHasMount(app) {
		t.Error("app container mount missing")
	}
	if !containerDependsOnSidecar(app) {
		t.Error("app container must depend on the sidecar with condition SUCCESS")
	}
	env := containerEnvMap(app)
	if env["OTEL_SERVICE_NAME"] != "svc" {
		t.Errorf("OTEL_SERVICE_NAME = %q", env["OTEL_SERVICE_NAME"])
	}
	if env[envResourceAttributes] != "cloud.provider=aws,cloud.platform=aws_ecs,cloud.region=eu-west-1" {
		t.Errorf("resource attributes = %q", env[envResourceAttributes])
	}
	if env[EnvTracesExporter] != exporterOTLP || env[EnvMetricsExporter] != exporterNone || env[EnvLogsExporter] != exporterNone {
		t.Errorf("per-signal exporters wrong: traces=%q metrics=%q logs=%q", env[EnvTracesExporter], env[EnvMetricsExporter], env[EnvLogsExporter])
	}
}

func TestFargateDirectRoutingRendersHeadersDeterministically(t *testing.T) {
	rec := testRecipe(t, LangJava, "")
	export := ecsExportTarget{
		endpoint: "http://global.example:4317", protocol: otlpProtocolGRPC, traces: true,
		headers: map[string]string{"z-tenant": "a,b", "a-token": "x=y"},
	}
	in, err := applyInstrumentation(sampleTaskDef(), oneTarget(0, rec, export), nil, testAgentsImage, "svc", "us-east-1", false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	env := containerEnvMap(findECSContainer(tdFromRegisterInput(in), "app"))
	if got, want := env[envOTLPHeaders], "a-token=x%3Dy,z-tenant=a%2Cb"; got != want {
		t.Errorf("%s = %q, want deterministic sorted encoding %q", envOTLPHeaders, got, want)
	}
}

func TestFargateCollectorRoutingRemovesAndRollsBackPriorHeaders(t *testing.T) {
	rec := testRecipe(t, LangJava, "")
	original := sampleTaskDef()
	const customerHeaders = "authorization=Bearer+customer"
	setContainerEnv(&original.ContainerDefinitions[0], envOTLPHeaders, customerHeaders)

	// A regional Collector owns the credential hop, so no terminal destination headers may be carried into
	// the task. The connector's manifest must nevertheless restore the customer's prior value on rollback.
	collectorExport := ecsExportTarget{endpoint: "http://regional.internal:4317", protocol: otlpProtocolGRPC, traces: true}
	in, err := applyInstrumentation(original, oneTarget(0, rec, collectorExport), nil, testAgentsImage, "svc", "us-east-1", false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	instrumented := tdFromRegisterInput(in)
	app := findECSContainer(instrumented, "app")
	if _, present := containerEnvMap(app)[envOTLPHeaders]; present {
		t.Fatalf("Collector route retained prior %s=%q", envOTLPHeaders, containerEnvMap(app)[envOTLPHeaders])
	}

	clean, owned := rollbackInstrumentation(instrumented, nil)
	if !owned {
		t.Fatal("Collector-routed task definition must retain an Odigos ownership marker for rollback")
	}
	rolledBack := findECSContainer(tdFromRegisterInput(clean), "app")
	if got := containerEnvMap(rolledBack)[envOTLPHeaders]; got != customerHeaders {
		t.Errorf("rollback %s = %q, want customer's prior value %q", envOTLPHeaders, got, customerHeaders)
	}
}

func TestResolverSelectedEndpointDrivesAlreadyInstrumented(t *testing.T) {
	rec := testRecipe(t, LangJava, "")
	const region = "us-east-1"
	dests := []domaindest.Destination{
		{Name: "global", Type: "otlp", Signals: []string{"TRACES"}, Config: map[string]any{"OTLP_GRPC_ENDPOINT": "global.example:4317"}},
		{Name: "regional", Type: domaindest.CatalogTypeManagedCollector, Region: region, Signals: []string{"TRACES"}},
	}
	direct := domaindest.ResolveRoutingForRegion(dests, nil, region, "vpc-app", domaindest.ReachabilityUnknown)
	collector := domaindest.ResolveRoutingForRegion(dests, []domaindest.CollectorTarget{{
		Region: region, Network: "vpc-app", Endpoint: "regional.example:4317", Insecure: true, EverReady: true,
	}}, region, "vpc-app", domaindest.ReachabilityReachable)
	if direct.Via != domaindest.RouteDirect || collector.Via != domaindest.RouteCollector {
		t.Fatalf("resolver routes = direct:%q collector:%q", direct.Via, collector.Via)
	}
	directExport, err := exportTargetFor(rec, direct.Target)
	if err != nil {
		t.Fatalf("direct export target: %v", err)
	}
	in, err := applyInstrumentation(sampleTaskDef(), oneTarget(0, rec, directExport), nil, testAgentsImage, "svc", region, false)
	if err != nil {
		t.Fatalf("apply direct route: %v", err)
	}
	td := tdFromRegisterInput(in)
	if !alreadyInstrumented(td, oneTarget(0, rec, directExport), testAgentsImage, "svc", region, false) {
		t.Fatal("task definition must converge for the direct route it was rendered with")
	}
	collectorExport, err := exportTargetFor(rec, collector.Target)
	if err != nil {
		t.Fatalf("Collector export target: %v", err)
	}
	collectorExport.headers = collector.Headers
	if alreadyInstrumented(td, oneTarget(0, rec, collectorExport), testAgentsImage, "svc", region, false) {
		t.Fatal("a resolver-selected Collector endpoint must make the prior direct task definition non-converged")
	}
}

// TestRollbackRestoresOriginal: rollback of an instrumented revision restores the exact pre-Odigos
// container (env, mounts, dependencies) and drops the sidecar + volume.
func TestRollbackRestoresOriginal(t *testing.T) {
	rec := testRecipe(t, LangJava, "")
	original := sampleTaskDef()
	wantEnv := containerEnvMap(&original.ContainerDefinitions[0])

	td := sampleTaskDef()
	input, err := applyInstrumentation(td, oneTarget(0, rec, testExport()), nil, testAgentsImage, "svc", "us-east-1", true)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	instrumented := tdFromRegisterInput(input)

	clean, owned := rollbackInstrumentation(instrumented, nil)
	if !owned {
		t.Fatal("instrumented task definition must be recognized as owned")
	}
	app := findECSContainer(tdFromRegisterInput(clean), "app")
	if app == nil {
		t.Fatal("app container missing after rollback")
	}
	if got := containerEnvMap(app); !reflect.DeepEqual(got, wantEnv) {
		t.Errorf("rollback env = %v, want original %v", got, wantEnv)
	}
	if len(app.DependsOn) != 0 || len(app.MountPoints) != 0 {
		t.Errorf("rollback left dependsOn/mounts: %v %v", app.DependsOn, app.MountPoints)
	}
	cleanTD := tdFromRegisterInput(clean)
	if findECSContainer(cleanTD, sidecarContainerName) != nil || hasAgentVolume(cleanTD) {
		t.Error("rollback left the sidecar container or agent volume")
	}
}

func TestRollbackNeverTouchesUnowned(t *testing.T) {
	td := sampleTaskDef()
	if _, owned := rollbackInstrumentation(td, nil); owned {
		t.Error("a task definition without the ownership marker must never be rolled back")
	}
	// A customer's own OTel env is not ours either.
	setContainerEnv(&td.ContainerDefinitions[0], "OTEL_EXPORTER_OTLP_ENDPOINT", "http://their-collector:4318")
	if _, owned := rollbackInstrumentation(td, nil); owned {
		t.Error("customer OTel env without the marker must not read as owned")
	}
}

func essentialContainer(name string, ess bool, ports int) ecstypes.ContainerDefinition {
	c := ecstypes.ContainerDefinition{Name: aws.String(name), Essential: aws.Bool(ess)}
	for p := 0; p < ports; p++ {
		c.PortMappings = append(c.PortMappings, ecstypes.PortMapping{ContainerPort: aws.Int32(int32(8080 + p))})
	}
	return c
}

func TestCandidateContainerIndexes(t *testing.T) {
	names := func(td *ecstypes.TaskDefinition, idxs []int) []string {
		var out []string
		for _, i := range idxs {
			out = append(out, aws.ToString(td.ContainerDefinitions[i].Name))
		}
		return out
	}
	cases := []struct {
		name       string
		containers []ecstypes.ContainerDefinition
		want       []string // nil = expect unsupported error
	}{
		{"single essential", []ecstypes.ContainerDefinition{essentialContainer("app", true, 0)}, []string{"app"}},
		{"essential defaults true", []ecstypes.ContainerDefinition{{Name: aws.String("app")}}, []string{"app"}},
		{"non-essential helper excluded", []ecstypes.ContainerDefinition{essentialContainer("app", true, 1), essentialContainer("logrouter", false, 0)}, []string{"app"}},
		{"all essential are candidates", []ecstypes.ContainerDefinition{essentialContainer("a", true, 1), essentialContainer("b", true, 1)}, []string{"a", "b"}},
		{"no essential", []ecstypes.ContainerDefinition{essentialContainer("helper", false, 0)}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			td := &ecstypes.TaskDefinition{Family: aws.String("f"), ContainerDefinitions: tc.containers}
			idxs, err := candidateContainerIndexes(td)
			if tc.want == nil {
				if err == nil {
					t.Fatalf("want unsupported error, got %v", idxs)
				}
				if !strings.HasPrefix(err.Error(), "unsupported") {
					t.Errorf("error %q must carry the unsupported prefix", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := names(td, idxs); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("candidates = %v, want %v", got, tc.want)
			}
		})
	}

	// An already-owned container stays a candidate alongside the rest, rather than becoming the only
	// one. Instrumentation is a set now: a customer adding a second instrumentable container to an
	// already-instrumented task must have it picked up, which a marker-wins rule would prevent.
	td := &ecstypes.TaskDefinition{Family: aws.String("f"), ContainerDefinitions: []ecstypes.ContainerDefinition{
		essentialContainer("web", true, 1),
		essentialContainer("owned", true, 0),
	}}
	setContainerEnv(&td.ContainerDefinitions[1], store.EnvOdigosManaged, `{"v":1}`)
	idxs, err := candidateContainerIndexes(td)
	if err != nil || !reflect.DeepEqual(names(td, idxs), []string{"web", "owned"}) {
		t.Errorf("candidates = %v err=%v, want both containers", names(td, idxs), err)
	}

	// A container we own is a candidate even after it stops being essential: dropping it silently
	// would strand our env in it, with no sidecar staging the agent it points at.
	td.ContainerDefinitions[1].Essential = aws.Bool(false)
	idxs, err = candidateContainerIndexes(td)
	if err != nil || !reflect.DeepEqual(names(td, idxs), []string{"web", "owned"}) {
		t.Errorf("candidates = %v err=%v, want the owned non-essential container kept", names(td, idxs), err)
	}
}

// perImageInspector serves a distinct image config per image reference.
type perImageInspector map[string]*ImageConfig

func (p perImageInspector) ImageConfig(_ context.Context, ref string) (*ImageConfig, error) {
	if cfg, ok := p[ref]; ok {
		return cfg, nil
	}
	return nil, errors.New("image not found")
}

func TestApplyGuards(t *testing.T) {
	rec := testRecipe(t, LangJava, "")

	// Managed key delivered via ECS secrets → unsupported.
	td := sampleTaskDef()
	td.ContainerDefinitions[0].Secrets = []ecstypes.Secret{{Name: aws.String("JAVA_TOOL_OPTIONS"), ValueFrom: aws.String("arn:...:secret")}}
	if _, err := applyInstrumentation(td, oneTarget(0, rec, testExport()), nil, testAgentsImage, "svc", "us-east-1", false); err == nil || !strings.HasPrefix(err.Error(), "unsupported") {
		t.Errorf("secrets collision should be an unsupported error, got %v", err)
	}

	// Task definition at the container limit → unsupported.
	td = sampleTaskDef()
	for i := 0; i < maxTaskDefContainers-1; i++ {
		td.ContainerDefinitions = append(td.ContainerDefinitions, ecstypes.ContainerDefinition{
			Name: aws.String("extra" + strings.Repeat("x", i+1)), Essential: aws.Bool(false),
		})
	}
	if _, err := applyInstrumentation(td, oneTarget(0, rec, testExport()), nil, testAgentsImage, "svc", "us-east-1", false); err == nil || !strings.HasPrefix(err.Error(), "unsupported") {
		t.Errorf("container-limit overflow should be an unsupported error, got %v", err)
	}
}

// TestRegisterInputCopiesEveryField reflects over RegisterTaskDefinitionInput so a new revision-defining
// SDK field cannot be silently dropped (which would corrupt customer task definitions on re-register).
func TestRegisterInputCopiesEveryField(t *testing.T) {
	// Fields registerInputFromTaskDefinition copies from the described task definition (+ Tags from the
	// describe output). Anything the SDK adds beyond this set must be triaged and added to the copy.
	copied := map[string]bool{
		"Family": true, "ContainerDefinitions": true, "Cpu": true, "Memory": true,
		"EnableFaultInjection": true, "EphemeralStorage": true, "ExecutionRoleArn": true,
		"TaskRoleArn": true, "InferenceAccelerators": true, "IpcMode": true, "NetworkMode": true,
		"PidMode": true, "PlacementConstraints": true, "ProxyConfiguration": true,
		"RequiresCompatibilities": true, "RuntimePlatform": true, "Volumes": true, "Tags": true,
	}
	typ := reflect.TypeOf(ecs.RegisterTaskDefinitionInput{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		if !copied[f.Name] {
			t.Errorf("RegisterTaskDefinitionInput field %q is not copied by registerInputFromTaskDefinition — the SDK added a revision-defining field; copy it (or explicitly exempt it here)", f.Name)
		}
	}

	// And the copy itself round-trips the sample fixture's populated fields.
	td := sampleTaskDef()
	in := registerInputFromTaskDefinition(td, []ecstypes.Tag{{Key: aws.String("team"), Value: aws.String("core")}})
	if aws.ToString(in.Family) != "checkout" || in.NetworkMode != ecstypes.NetworkModeAwsvpc ||
		aws.ToString(in.ExecutionRoleArn) != aws.ToString(td.ExecutionRoleArn) ||
		aws.ToString(in.TaskRoleArn) != aws.ToString(td.TaskRoleArn) || len(in.Tags) != 1 {
		t.Errorf("register input did not round-trip the task definition: %+v", in)
	}
}

func TestGRPCEndpointURL(t *testing.T) {
	cases := []struct {
		endpoint string
		insecure bool
		want     string
	}{
		{"collector:4317", true, "http://collector:4317"},
		{"collector:4317", false, "https://collector:4317"},
		{"https://collector:4317", true, "https://collector:4317"}, // already a URL — passthrough
		{"", true, ""},
	}
	for _, tc := range cases {
		if got := grpcEndpointURL(tc.endpoint, tc.insecure); got != tc.want {
			t.Errorf("grpcEndpointURL(%q, %v) = %q, want %q", tc.endpoint, tc.insecure, got, tc.want)
		}
	}
}

// --- multi-container selection ---

// bothTransports is a destination offering gRPC and HTTP, so per-language protocol choice is free.
func bothTransports() domaindest.OTLPTarget {
	return domaindest.OTLPTarget{Endpoint: "collector:4317", Insecure: true, HTTPEndpoint: "http://collector:4318", Traces: true}
}

func imageContainer(name, image string) ecstypes.ContainerDefinition {
	c := essentialContainer(name, true, 0)
	c.Image = aws.String(image)
	return c
}

func taskDef(family string, containers ...ecstypes.ContainerDefinition) *ecstypes.TaskDefinition {
	return &ecstypes.TaskDefinition{Family: aws.String(family), ContainerDefinitions: containers}
}

func targetNames(targets []containerTarget) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, t.name+"="+t.recipe.lang)
	}
	sort.Strings(out)
	return out
}

// The headline case: dev-simple-demo-single-fargate packs six applications in six languages as
// essential containers. Detection cannot choose between six correct answers, and it no longer has to —
// every one of them is instrumented, each with its own agent.
func TestSelectTargetsInstrumentsEveryDetectedContainer(t *testing.T) {
	td := taskDef("dev-simple-demo-single-fargate",
		imageContainer("frontend", "openjdk:21"),
		imageContainer("inventory", "python:3.12"),
		imageContainer("coupon", "node:20"),
		imageContainer("currency", "php:8.3-fpm"),
		imageContainer("geolocation", "ruby:3.3"),
	)
	i := &ecsInspector{}
	targets, excluded, err := i.selectTargets(context.Background(), td, enabledFargateSource{}, store.ECSService{}, bothTransports())
	if err != nil {
		t.Fatalf("a multi-application task definition must instrument, not error: %v", err)
	}
	want := []string{"coupon=nodejs", "currency=php", "frontend=java", "geolocation=ruby", "inventory=python"}
	if got := targetNames(targets); !reflect.DeepEqual(got, want) {
		t.Errorf("targets = %v, want %v", got, want)
	}
	if len(excluded) != 0 {
		t.Errorf("nothing should be excluded here, got %v", excluded)
	}
	// Each container keeps its own transport: php and ruby are http/protobuf-only, java is not pinned.
	for _, tg := range targets {
		if (tg.recipe.lang == LangPHP || tg.recipe.lang == LangRuby) && tg.export.protocol != otlpProtocolHTTP {
			t.Errorf("%s must export http/protobuf, got %q", tg.name, tg.export.protocol)
		}
	}
}

// The currency-nginx pair: a PHP-FPM app behind an nginx proxy, both essential and both port-mapped.
// nginx's inline fastcgi config names index.php, so a naive scan detects php on both — only the
// confidence gate separates them. Instrumenting everything must NOT mean instrumenting the proxy.
func TestSelectTargetsSkipsWeakDetections(t *testing.T) {
	php := imageContainer("currency", "registry/odigos-demo-currency:v1")
	php.PortMappings = []ecstypes.PortMapping{{ContainerPort: aws.Int32(9000)}}
	nginx := imageContainer("nginx", "nginx:alpine")
	nginx.PortMappings = []ecstypes.PortMapping{{ContainerPort: aws.Int32(8085)}}
	nginx.Command = []string{"sh", "-c", "printf 'fastcgi_pass 127.0.0.1:9000; fastcgi_param SCRIPT_FILENAME /app/index.php;' > /etc/nginx/conf.d/default.conf && nginx -g 'daemon off;'"}

	td := taskDef("currency-nginx", php, nginx)
	i := &ecsInspector{images: perImageInspector{
		"registry/odigos-demo-currency:v1": {Env: []string{"PHP_VERSION=8.3.2"}},
		"nginx:alpine":                     {Env: []string{"NGINX_VERSION=1.27.0"}},
	}}

	targets, excluded, err := i.selectTargets(context.Background(), td, enabledFargateSource{}, store.ECSService{}, bothTransports())
	if err != nil {
		t.Fatalf("selection failed: %v", err)
	}
	if got := targetNames(targets); !reflect.DeepEqual(got, []string{"currency=php"}) {
		t.Errorf("targets = %v, want only the php application container", got)
	}
	if targets[0].runtime.version != "8.3" {
		t.Errorf("version = %q, want 8.3 from the image config", targets[0].runtime.version)
	}
	if reason := excluded["nginx"]; !strings.Contains(reason, "weak") {
		t.Errorf("nginx exclusion = %q, want it to name the weak signal", reason)
	}

	// Naming the proxy instrumets it anyway: an explicit instruction beats detection.
	src := enabledFargateSource{containers: []domainsrc.ContainerOverride{
		{ContainerName: "nginx", RuntimeOverride: domainsrc.RuntimeOverride{Language: "php", Version: "8.3"}},
	}}
	targets, _, err = i.selectTargets(context.Background(), td, src, store.ECSService{}, bothTransports())
	if err != nil {
		t.Fatalf("named container selection failed: %v", err)
	}
	if got := targetNames(targets); !reflect.DeepEqual(got, []string{"currency=php", "nginx=php"}) {
		t.Errorf("targets = %v, want both once nginx is named", got)
	}
}

// One container failing must not take the whole task down with it: its siblings still get instrumented
// and the failure is reported against that container alone.
func TestSelectTargetsIsolatesPerContainerFailures(t *testing.T) {
	td := taskDef("mixed",
		imageContainer("api", "openjdk:21"),
		imageContainer("legacy", "golang:1.23"),
	)
	i := &ecsInspector{}
	targets, excluded, err := i.selectTargets(context.Background(), td, enabledFargateSource{}, store.ECSService{}, bothTransports())
	if err != nil {
		t.Fatalf("one uninstrumentable container must not fail the task: %v", err)
	}
	if got := targetNames(targets); !reflect.DeepEqual(got, []string{"api=java"}) {
		t.Errorf("targets = %v, want just the java container", got)
	}
	if reason := excluded["legacy"]; !strings.Contains(reason, "eBPF") {
		t.Errorf("go container exclusion = %q, want the eBPF limit named", reason)
	}

	// A destination that cannot carry php's http/protobuf excludes only php.
	grpcOnly := domaindest.OTLPTarget{Endpoint: "collector:4317", Traces: true}
	td = taskDef("mixed2", imageContainer("api", "openjdk:21"), imageContainer("web", "php:8.3-fpm"))
	targets, excluded, err = i.selectTargets(context.Background(), td, enabledFargateSource{}, store.ECSService{}, grpcOnly)
	if err != nil {
		t.Fatalf("selection failed: %v", err)
	}
	if got := targetNames(targets); !reflect.DeepEqual(got, []string{"api=java"}) {
		t.Errorf("targets = %v, want java only against a gRPC-only destination", got)
	}
	if reason := excluded["web"]; !strings.Contains(reason, "OTLP_HTTP_ENDPOINT") {
		t.Errorf("php exclusion = %q, want the missing HTTP endpoint named", reason)
	}
}

// When nothing can be instrumented the workload still errors, and the message names what each
// container detected as so the operator can act.
func TestSelectTargetsErrorsWhenNothingInstrumentable(t *testing.T) {
	td := taskDef("f", imageContainer("a", "registry/a:1"), imageContainer("b", "registry/b:1"))
	i := &ecsInspector{}
	_, _, err := i.selectTargets(context.Background(), td, enabledFargateSource{}, store.ECSService{}, bothTransports())
	if err == nil || !strings.HasPrefix(err.Error(), "unsupported") {
		t.Fatalf("want unsupported error, got %v", err)
	}
	for _, want := range []string{"a=undetected", "b=undetected"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should report %q, got %q", want, err)
		}
	}
}

// A workload-level override describes "the application", so it applies when exactly one container is
// instrumented — and must NOT be broadcast to every container, which would turn a proxy into a target.
func TestSelectTargetsWorkloadOverrideAppliesOnlyWhenUnambiguous(t *testing.T) {
	i := &ecsInspector{images: perImageInspector{
		"registry/odigos-demo-currency:v1": {Env: []string{"PHP_VERSION=8.1.0"}},
		"nginx:alpine":                     {Env: []string{"NGINX_VERSION=1.27.0"}},
	}}
	td := taskDef("f",
		imageContainer("app", "registry/odigos-demo-currency:v1"),
		imageContainer("nginx", "nginx:alpine"),
	)
	src := enabledFargateSource{language: "php", version: "8.4.1"}
	targets, _, err := i.selectTargets(context.Background(), td, src, store.ECSService{}, bothTransports())
	if err != nil {
		t.Fatalf("selection failed: %v", err)
	}
	if got := targetNames(targets); !reflect.DeepEqual(got, []string{"app=php"}) {
		t.Errorf("targets = %v — the workload override must not make nginx a target", got)
	}
	// Applied to the single target: the override's 8.4 beats the image's PHP_VERSION=8.1.0.
	if targets[0].runtime.version != "8.4" {
		t.Errorf("version = %q, want the override's 8.4", targets[0].runtime.version)
	}

	// With two real applications the workload override is ambiguous and is ignored; each container
	// keeps its own detected language rather than all being forced to one.
	td = taskDef("f2", imageContainer("api", "openjdk:21"), imageContainer("worker", "ruby:3.3"))
	targets, _, err = i.selectTargets(context.Background(), td, enabledFargateSource{language: "java"}, store.ECSService{}, bothTransports())
	if err != nil {
		t.Fatalf("selection failed: %v", err)
	}
	if got := targetNames(targets); !reflect.DeepEqual(got, []string{"api=java", "worker=ruby"}) {
		t.Errorf("targets = %v, want each container's own language", got)
	}
}

// Disabling containers narrows the set; disabling all of them is a terminal, self-explanatory state.
func TestSelectTargetsRespectsDisabledContainers(t *testing.T) {
	td := taskDef("f", imageContainer("api", "openjdk:21"), imageContainer("worker", "ruby:3.3"))
	i := &ecsInspector{}
	ctx := context.Background()

	src := enabledFargateSource{containers: []domainsrc.ContainerOverride{{ContainerName: "worker", Disabled: true}}}
	targets, excluded, err := i.selectTargets(ctx, td, src, store.ECSService{}, bothTransports())
	if err != nil {
		t.Fatalf("selection failed: %v", err)
	}
	if got := targetNames(targets); !reflect.DeepEqual(got, []string{"api=java"}) {
		t.Errorf("targets = %v, want the disabled container dropped", got)
	}
	if !strings.Contains(excluded["worker"], "disabled") {
		t.Errorf("worker exclusion = %q, want it to say disabled", excluded["worker"])
	}

	src = enabledFargateSource{containers: []domainsrc.ContainerOverride{
		{ContainerName: "api", Disabled: true}, {ContainerName: "worker", Disabled: true},
	}}
	if _, _, err := i.selectTargets(ctx, td, src, store.ECSService{}, bothTransports()); err == nil {
		t.Error("disabling every container must be a terminal state, not a silent no-op")
	}
}

// --- multi-container injection ---

// Two applications in two languages in one task: each container gets its own agent and activation env,
// and ONE sidecar stages the union of both agent directories.
func TestApplyInstrumentsEveryTargetWithItsOwnAgent(t *testing.T) {
	java := testRecipe(t, LangJava, "")
	ruby := testRecipe(t, LangRuby, "3.3")
	httpExport := ecsExportTarget{endpoint: "http://collector:4318", protocol: otlpProtocolHTTP, traces: true}

	td := taskDef("multi", imageContainer("api", "openjdk:21"), imageContainer("worker", "ruby:3.3"))
	targets := []containerTarget{
		{idx: 0, name: "api", recipe: java, export: testExport()},
		{idx: 1, name: "worker", recipe: ruby, export: httpExport},
	}
	input, err := applyInstrumentation(td, targets, nil, testAgentsImage, "svc", "us-east-1", false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	next := tdFromRegisterInput(input)

	apiEnv := containerEnvMap(findECSContainer(next, "api"))
	if !strings.Contains(apiEnv["JAVA_TOOL_OPTIONS"], "javaagent") {
		t.Errorf("api missing the java agent: %v", apiEnv["JAVA_TOOL_OPTIONS"])
	}
	if apiEnv["RUBYOPT"] != "" {
		t.Errorf("api must not receive ruby activation env, got %q", apiEnv["RUBYOPT"])
	}
	workerEnv := containerEnvMap(findECSContainer(next, "worker"))
	if !strings.Contains(workerEnv["RUBYOPT"], "/var/odigos/ruby/3.3") {
		t.Errorf("worker missing the ruby agent: %v", workerEnv["RUBYOPT"])
	}
	if workerEnv["JAVA_TOOL_OPTIONS"] != "" {
		t.Errorf("worker must not receive java activation env, got %q", workerEnv["JAVA_TOOL_OPTIONS"])
	}
	// Per-container transports survive: ruby is http/protobuf-only while java stays on gRPC.
	if apiEnv[EnvOtlpProtocol] != otlpProtocolGRPC || workerEnv[EnvOtlpProtocol] != otlpProtocolHTTP {
		t.Errorf("protocols not per-container: api=%q worker=%q", apiEnv[EnvOtlpProtocol], workerEnv[EnvOtlpProtocol])
	}

	// One sidecar, staging both languages.
	sc := findECSContainer(next, sidecarContainerName)
	if sc == nil {
		t.Fatal("sidecar missing")
	}
	cmd := strings.Join(sc.Command, " ")
	// Agent directories are per LANGUAGE; the runtime version is templated into the activation env
	// (RUBYOPT points at /var/odigos/ruby/3.3), not into what the sidecar stages.
	for _, want := range []string{"/instrumentations/java", "/instrumentations/ruby"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("sidecar command missing %q: %s", want, cmd)
		}
	}
	if n := len(next.ContainerDefinitions); n != 3 {
		t.Errorf("got %d containers, want 2 apps + 1 sidecar", n)
	}

	// Both containers mount the shared volume and gate on the sidecar.
	for _, name := range []string{"api", "worker"} {
		c := findECSContainer(next, name)
		if !containerHasMount(c) || !containerDependsOnSidecar(c) {
			t.Errorf("%s missing mount or sidecar dependency", name)
		}
	}

	// Converged, and re-applying is byte-stable.
	if !alreadyInstrumented(next, targets, testAgentsImage, "svc", "us-east-1", false) {
		t.Error("freshly applied multi-container task should read as converged")
	}
	input2, err := applyInstrumentation(next, targets, nil, testAgentsImage, "svc", "us-east-1", false)
	if err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	for _, name := range []string{"api", "worker"} {
		a := containerEnvMap(findECSContainer(next, name))
		b := containerEnvMap(findECSContainer(tdFromRegisterInput(input2), name))
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s env drifted on re-apply:\n first=%v\nsecond=%v", name, a, b)
		}
	}
}

// The instrumented set must match the target set exactly. A container we still own but no longer
// target is drift, and reporting it as converged would strand our env there forever.
func TestAlreadyInstrumentedRequiresExactTargetSet(t *testing.T) {
	java := testRecipe(t, LangJava, "")
	ruby := testRecipe(t, LangRuby, "3.3")
	httpExport := ecsExportTarget{endpoint: "http://collector:4318", protocol: otlpProtocolHTTP, traces: true}
	both := []containerTarget{
		{idx: 0, name: "api", recipe: java, export: testExport()},
		{idx: 1, name: "worker", recipe: ruby, export: httpExport},
	}

	td := taskDef("multi", imageContainer("api", "openjdk:21"), imageContainer("worker", "ruby:3.3"))
	input, err := applyInstrumentation(td, both, nil, testAgentsImage, "svc", "us-east-1", false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	next := tdFromRegisterInput(input)

	// Dropping a target leaves an owned-but-untargeted container: not converged.
	justAPI := both[:1]
	if alreadyInstrumented(next, justAPI, testAgentsImage, "svc", "us-east-1", false) {
		t.Error("a container we own but no longer target must not read as converged")
	}
	// And re-applying the smaller set restores that container in the same revision.
	input2, err := applyInstrumentation(next, justAPI, nil, testAgentsImage, "svc", "us-east-1", false)
	if err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	shrunk := tdFromRegisterInput(input2)
	workerEnv := containerEnvMap(findECSContainer(shrunk, "worker"))
	if workerEnv[store.EnvOdigosManaged] != "" || workerEnv["RUBYOPT"] != "" {
		t.Errorf("dropped container not restored: %v", workerEnv)
	}
	if !alreadyInstrumented(shrunk, justAPI, testAgentsImage, "svc", "us-east-1", false) {
		t.Error("after shrinking, the smaller set should read as converged")
	}
	// The sidecar now stages only java.
	if cmd := strings.Join(findECSContainer(shrunk, sidecarContainerName).Command, " "); strings.Contains(cmd, "ruby") {
		t.Errorf("sidecar still stages ruby after the target was dropped: %s", cmd)
	}
}

// Rollback restores EVERY instrumented container, not just the first one found.
func TestRollbackRestoresAllInstrumentedContainers(t *testing.T) {
	java := testRecipe(t, LangJava, "")
	python := testRecipe(t, LangPython, "")

	api := imageContainer("api", "openjdk:21")
	api.Environment = []ecstypes.KeyValuePair{{Name: aws.String("JAVA_TOOL_OPTIONS"), Value: aws.String("-Xmx512m")}}
	worker := imageContainer("worker", "python:3.12")
	worker.Environment = []ecstypes.KeyValuePair{{Name: aws.String("PYTHONPATH"), Value: aws.String("/app")}}
	td := taskDef("multi", api, worker)

	input, err := applyInstrumentation(td, []containerTarget{
		{idx: 0, name: "api", recipe: java, export: testExport()},
		{idx: 1, name: "worker", recipe: python, export: testExport()},
	}, nil, testAgentsImage, "svc", "us-east-1", false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	next := tdFromRegisterInput(input)

	rb, owned := rollbackInstrumentation(next, nil)
	if !owned {
		t.Fatal("rollback should recognize an instrumented task definition")
	}
	clean := tdFromRegisterInput(rb)
	if findECSContainer(clean, sidecarContainerName) != nil || hasAgentVolume(clean) {
		t.Error("sidecar and volume must be removed")
	}
	if got := containerEnvMap(findECSContainer(clean, "api")); got["JAVA_TOOL_OPTIONS"] != "-Xmx512m" || got[store.EnvOdigosManaged] != "" {
		t.Errorf("api not restored to its pre-Odigos env: %v", got)
	}
	if got := containerEnvMap(findECSContainer(clean, "worker")); got["PYTHONPATH"] != "/app" || got[store.EnvOdigosManaged] != "" {
		t.Errorf("worker not restored to its pre-Odigos env: %v", got)
	}
	for _, name := range []string{"api", "worker"} {
		c := findECSContainer(clean, name)
		if containerHasMount(c) || containerDependsOnSidecar(c) {
			t.Errorf("%s kept the Odigos mount or dependency", name)
		}
	}
}

// The sidecar command must be byte-stable regardless of target order, or every reconcile would see a
// different command, fail convergence, and churn a new task-definition revision.
func TestUnionAgentDirsIsStableAndDeduplicated(t *testing.T) {
	java := testRecipe(t, LangJava, "")
	ruby := testRecipe(t, LangRuby, "3.3")
	a := unionAgentDirs([]containerTarget{{recipe: java}, {recipe: ruby}, {recipe: java}})
	b := unionAgentDirs([]containerTarget{{recipe: ruby}, {recipe: java}})
	if !reflect.DeepEqual(a, b) {
		t.Errorf("order changed the result: %v vs %v", a, b)
	}
	seen := map[string]int{}
	for _, d := range a {
		seen[d]++
	}
	for d, n := range seen {
		if n > 1 {
			t.Errorf("directory %q appears %d times", d, n)
		}
	}
}

func colonLoader() appendEnvVar {
	return appendEnvVar{
		name:    "PYTHONPATH",
		pattern: "{{ORIGINAL_ENV_VALUE}}:{{ODIGOS_AGENTS_DIR}}/python",
	}
}

func spaceLoader() appendEnvVar {
	return appendEnvVar{
		name:    "JAVA_TOOL_OPTIONS",
		pattern: "{{ORIGINAL_ENV_VALUE}} -javaagent:{{ODIGOS_AGENTS_DIR}}/java/javaagent.jar",
	}
}

// TestLoaderActivatedPinsSuffixForIaCAndExactMatchForManaged is the case plan_test.go calls out as
// uncovered: a container that already had a loader value. Managed mode owns the whole string from the
// snapshot. IaC mode only asks that our fragment is last — a prepend or mid-PYTHONPATH splice loads a
// different agent first and must not read as conformant.
func TestLoaderActivatedPinsSuffixForIaCAndExactMatchForManaged(t *testing.T) {
	iac := ecsMarker{}
	python := colonLoader()
	ours := python.evaluate("")
	if ours == "" {
		t.Fatal("test setup: evaluate(\"\") must produce the Odigos fragment")
	}

	t.Run("iac suffix after customer value is activated", func(t *testing.T) {
		if !loaderActivated("/app:"+ours, python, iac) {
			t.Error("PYTHONPATH ending in the agent path must count as activated")
		}
	})
	t.Run("iac fragment alone is activated", func(t *testing.T) {
		if !loaderActivated(ours, python, iac) {
			t.Error("a container that carried no prior value must count as activated when the live value is ours")
		}
	})
	t.Run("iac prepend is not activated", func(t *testing.T) {
		if loaderActivated(ours+":/app", python, iac) {
			t.Error("agent path first, customer path last, must not count as activated — the customer copy wins")
		}
	})
	t.Run("iac mid-string splice is not activated", func(t *testing.T) {
		if loaderActivated("/app:"+ours+":/vendor", python, iac) {
			t.Error("agent path in the middle of PYTHONPATH must not count as activated")
		}
	})
	t.Run("iac substring without the separator is not activated", func(t *testing.T) {
		if loaderActivated("/opt"+ours, python, iac) {
			t.Error("a value that merely contains the fragment must not count as activated")
		}
	})

	java := spaceLoader()
	javaOurs := java.evaluate("")
	t.Run("iac space-joined suffix is activated", func(t *testing.T) {
		if !loaderActivated("-Xmx512m "+javaOurs, java, iac) {
			t.Error("JAVA_TOOL_OPTIONS ending in the agent fragment must count as activated")
		}
	})
	t.Run("iac space-joined prepend is not activated", func(t *testing.T) {
		if loaderActivated(javaOurs+" -Xmx512m", java, iac) {
			t.Error("agent fragment first on JAVA_TOOL_OPTIONS must not count as activated")
		}
	})

	prior := "-Xmx512m"
	managed := ecsMarker{Managed: true, PrevEnv: map[string]*string{"JAVA_TOOL_OPTIONS": &prior}}
	exact := java.evaluate(prior)
	t.Run("managed exact snapshot match is activated", func(t *testing.T) {
		if !loaderActivated(exact, java, managed) {
			t.Error("managed mode must accept the value rebuilt from the snapshot")
		}
	})
	t.Run("managed extra suffix is not activated", func(t *testing.T) {
		if loaderActivated(exact+" extra", java, managed) {
			t.Error("managed mode owns the whole value; extra tokens are drift")
		}
	})
	t.Run("managed customer-only value is not activated", func(t *testing.T) {
		if loaderActivated(prior, java, managed) {
			t.Error("the pre-Odigos snapshot value alone is not an activated loader")
		}
	})

	t.Run("empty fragment is always activated", func(t *testing.T) {
		empty := appendEnvVar{name: "FOO", pattern: ""}
		if !loaderActivated("anything", empty, iac) {
			t.Error("nothing to require means the loader check must pass")
		}
	})
}
