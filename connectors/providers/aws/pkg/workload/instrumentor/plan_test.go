package instrumentor

import (
	"sort"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/plan"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/config"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// planLangs is every Fargate language plus the runtime version the version-templated agents need. The plan
// itself is built against plan.PlaceholderRuntimeVersion; a concrete version is used only where the test has
// to run the injector and compare.
var planLangs = []struct{ lang, version string }{
	{LangJava, ""}, {LangPython, ""}, {LangNodeJS, ""}, {LangDotNet, ""}, {LangPHP, "8.3"}, {LangRuby, "3.3"},
}

// ---------------------------------------------------------------------------------------------------------
// ECS Fargate
// ---------------------------------------------------------------------------------------------------------

func TestDesiredFargatePlansCoverEveryLanguage(t *testing.T) {
	plans, err := DesiredFargatePlans(testAgentsImage)
	if err != nil {
		t.Fatalf("DesiredFargatePlans: %v", err)
	}
	if len(plans) != len(planLangs) {
		t.Fatalf("plans = %d, want one per coverable language (%d)", len(plans), len(planLangs))
	}
	for _, p := range plans {
		if !CoverableLanguage(p.Language) {
			t.Errorf("plan for language %q the connector cannot instrument on Fargate", p.Language)
		}
		if err := p.Validate(); err != nil {
			t.Errorf("plan %s is invalid: %v", p.Key(), err)
		}
	}
}

func TestDesiredFargatePlanRequiresTheAgentsImage(t *testing.T) {
	if _, err := DesiredFargatePlan(LangJava, "", ""); err == nil {
		t.Error("a plan built with no agents image was accepted; its staging container could never start")
	}
	if _, err := DesiredFargatePlan("go", testAgentsImage, ""); err == nil {
		t.Error("a plan for Go was accepted, but Go needs eBPF and cannot be instrumented on Fargate at all")
	}
}

// TestFargatePlanEnvMatchesInjector runs the real applyInstrumentation and asserts the plan names exactly the
// variables the injector sets on the application container, and that the ones the plan says to REMOVE are the
// ones the injector deletes.
//
// The plan is the only thing a manual-mode customer sees, and containerDiff is what judges what they applied.
// A variable in one and not the other is a customer told their deployment drifted for following instructions.
func TestFargatePlanEnvMatchesInjector(t *testing.T) {
	for _, lc := range planLangs {
		t.Run(lc.lang, func(t *testing.T) {
			rec := testRecipe(t, lc.lang, lc.version)
			td := planTaskDef()
			if _, err := applyInstrumentation(td, oneTarget(0, rec, planExport(rec)), nil, testAgentsImage, "checkout", "us-east-1", false); err != nil {
				t.Fatalf("applyInstrumentation: %v", err)
			}
			live := containerEnvMap(&td.ContainerDefinitions[0])

			p, err := DesiredFargatePlan(lc.lang, testAgentsImage, lc.version)
			if err != nil {
				t.Fatalf("DesiredFargatePlan: %v", err)
			}
			planned := map[string]plan.Merge{}
			for _, f := range p.Fragments {
				for _, e := range f.Env {
					planned[e.Name] = e.Merge
				}
			}

			for name := range live {
				if _, ok := planned[name]; !ok {
					t.Errorf("the injector sets %s but the plan never mentions it, so a customer is never told to apply it and is then reported as drifted for the omission", name)
				}
			}
			for name, merge := range planned {
				_, set := live[name]
				switch merge {
				case plan.MergeRemove:
					if set {
						t.Errorf("the plan says to remove %s but the injector sets it to %q", name, live[name])
					}
				default:
					if !set {
						t.Errorf("the plan tells a customer to set %s but the injector never sets it, so applying the plan produces a value nothing expects", name)
					}
				}
			}
		})
	}
}

// TestFargatePlanValuesMatchInjector compares the values for everything that is not per-workload.
//
// The task definition here has NO pre-existing loader value, which is what makes an append's rendered value
// equal to the injector's result. That is also exactly the case the plan's prevEnv note is about: a container
// that does have one is compared against the recorded original joined to ours, so the plan tells the customer
// to record it.
func TestFargatePlanValuesMatchInjector(t *testing.T) {
	for _, lc := range planLangs {
		t.Run(lc.lang, func(t *testing.T) {
			rec := testRecipe(t, lc.lang, lc.version)
			td := planTaskDef()
			if _, err := applyInstrumentation(td, oneTarget(0, rec, planExport(rec)), nil, testAgentsImage, "checkout", "us-east-1", false); err != nil {
				t.Fatalf("applyInstrumentation: %v", err)
			}
			live := containerEnvMap(&td.ContainerDefinitions[0])

			// The concrete version, so the version-templated paths can be compared against real ones.
			p, err := DesiredFargatePlan(lc.lang, testAgentsImage, lc.version)
			if err != nil {
				t.Fatalf("DesiredFargatePlan: %v", err)
			}
			for _, f := range p.Fragments {
				for _, e := range f.Env {
					if e.Merge == plan.MergeRemove || strings.Contains(e.Value, "<") || e.Name == store.EnvOdigosManaged {
						continue
					}
					if live[e.Name] != e.Value {
						t.Errorf("%s: plan says %q, injector produced %q", e.Name, e.Value, live[e.Name])
					}
				}
			}
		})
	}
}

// TestFargatePlanLoaderSeparatorsComeFromTheDistro is the check the separator derivation exists for: a space
// for the *_OPTIONS variables and a colon for PYTHONPATH. Assuming one would produce a PYTHONPATH joined by a
// space — a single unusable path, with the process still starting and nothing instrumented.
func TestFargatePlanLoaderSeparatorsComeFromTheDistro(t *testing.T) {
	want := map[string]string{
		EnvNodeOptions:      " ",
		"JAVA_TOOL_OPTIONS": " ",
		"PYTHONPATH":        ":",
	}
	seen := map[string]bool{}
	for _, lc := range planLangs {
		p, err := DesiredFargatePlan(lc.lang, testAgentsImage, lc.version)
		if err != nil {
			t.Fatalf("DesiredFargatePlan(%s): %v", lc.lang, err)
		}
		for _, e := range plan.LoaderVars(p) {
			seen[e.Name] = true
			if w, ok := want[e.Name]; ok && e.Separator != w {
				t.Errorf("%s separator = %q, want %q (from the distro's replace pattern)", e.Name, e.Separator, w)
			}
			if e.Merge != plan.MergeAppend {
				t.Errorf("%s merge = %q, want append — addLoaderValue keeps the customer's value first on this platform", e.Name, e.Merge)
			}
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("no plan declares %s as a loader variable, so nothing tells a customer to merge rather than assign it", name)
		}
	}
}

// TestFargatePlanProtocolIsTheOneTheConnectorWouldChoose resolves the export target the way the reconcile does,
// against a destination offering BOTH transports, and requires the plan to name the same protocol.
//
// Comparing the plan against the recipe alone would be circular — the plan is built from the recipe. What
// matters is the protocol the connector actually picks, because endpoint and protocol are one decision: an
// http/protobuf agent pointed at a gRPC listener fails as a connection reset that points nowhere near the
// cause, and a customer following these instructions has no way to debug it.
func TestFargatePlanProtocolIsTheOneTheConnectorWouldChoose(t *testing.T) {
	dest := domaindest.OTLPTarget{
		Endpoint:     "collector:4317",
		HTTPEndpoint: "http://collector:4318",
		Traces:       true, Metrics: true, Logs: true,
	}
	for _, lc := range planLangs {
		t.Run(lc.lang, func(t *testing.T) {
			rec := testRecipe(t, lc.lang, lc.version)
			export, err := exportTargetFor(rec, dest)
			if err != nil {
				t.Fatalf("exportTargetFor: %v", err)
			}
			p, err := DesiredFargatePlan(lc.lang, testAgentsImage, lc.version)
			if err != nil {
				t.Fatalf("DesiredFargatePlan: %v", err)
			}
			if got := fargatePlanValue(p, EnvOtlpProtocol); got != export.protocol {
				t.Errorf("plan says %s = %q, but the connector would export over %q", EnvOtlpProtocol, got, export.protocol)
			}
		})
	}
}

// TestFargatePlanStagingMatchesInjector checks the parts a customer cannot reconstruct, plus the structural
// claim that is one decision with the SUCCESS dependency: the stager must be non-essential.
func TestFargatePlanStagingMatchesInjector(t *testing.T) {
	rec := testRecipe(t, LangJava, "")
	td := planTaskDef()
	if _, err := applyInstrumentation(td, oneTarget(0, rec, planExport(rec)), nil, testAgentsImage, "checkout", "us-east-1", false); err != nil {
		t.Fatalf("applyInstrumentation: %v", err)
	}
	sc := findECSContainer(td, sidecarContainerName)
	if sc == nil {
		t.Fatal("no staging container")
	}

	p, err := DesiredFargatePlan(LangJava, testAgentsImage, "")
	if err != nil {
		t.Fatalf("DesiredFargatePlan: %v", err)
	}
	var helper *plan.HelperContainer
	var mount *plan.Mount
	var dep *plan.StartupDependency
	for _, f := range p.Fragments {
		switch {
		case f.Helper != nil:
			helper = f.Helper
		case f.Mount != nil:
			mount = f.Mount
		case f.StartupDependency != nil:
			dep = f.StartupDependency
		}
	}
	if helper == nil || mount == nil || dep == nil {
		t.Fatal("plan is missing the helper container, the application mount or the startup dependency")
	}

	if helper.Name != aws.ToString(sc.Name) || helper.Image != aws.ToString(sc.Image) {
		t.Errorf("helper = %s/%s, staging container = %s/%s", helper.Name, helper.Image, aws.ToString(sc.Name), aws.ToString(sc.Image))
	}
	if strings.Join(helper.Command, " ") != strings.Join(sc.Command, " ") {
		t.Errorf("helper command = %v, staging command = %v", helper.Command, sc.Command)
	}
	// Run-to-completion and non-essential are one decision: ECS accepts a SUCCESS dependency only against a
	// non-essential container, so describing the stager as long-running would make the dependency invalid.
	if !helper.Init {
		t.Error("the ECS stager runs to completion (exits 0); describing it otherwise invalidates the SUCCESS dependency")
	}
	if aws.ToBool(sc.Essential) {
		t.Error("the injector's staging container is essential, which SUCCESS dependencies do not accept")
	}
	if dep.Condition != plan.ConditionSuccess {
		t.Errorf("dependency condition = %q, want %q", dep.Condition, plan.ConditionSuccess)
	}
	// Read-only in the application container: the agents are only read there and the stager owns the writes.
	if !mount.ReadOnly {
		t.Error("the application mount should be read-only, matching ensureAppMount")
	}
	if !hasAgentVolume(td) {
		t.Error("the injector adds no agent volume")
	}
}

func TestFargateInstrumentationArtifactsMatchPlans(t *testing.T) {
	plans, err := DesiredFargatePlans(config.DefaultFargateAgentsImage)
	if err != nil {
		t.Fatalf("DesiredFargatePlans: %v", err)
	}
	assertArtifactsMatchPlans(t, "aws.fargate-task", plans)
}

func TestFargateArtifactCoverageIsNotVacuous(t *testing.T) {
	plans, err := DesiredFargatePlans(config.DefaultFargateAgentsImage)
	if err != nil {
		t.Fatalf("DesiredFargatePlans: %v", err)
	}
	assertCoverageCatchesDrift(t, "aws.fargate-task", plans, "/var/odigos/java", "/var/odigos/jvm")
}

// ---------------------------------------------------------------------------------------------------------
// Lambda
// ---------------------------------------------------------------------------------------------------------

func TestDesiredLambdaPlansCoverEveryActivation(t *testing.T) {
	plans, err := DesiredLambdaPlans()
	if err != nil {
		t.Fatalf("DesiredLambdaPlans: %v", err)
	}
	// java, nodejs, python, ruby in handler mode; the same three minus ruby in web-server mode (ruby has no
	// verified web-server loader — the asymmetry webServerLoader declares).
	if len(plans) != 7 {
		t.Fatalf("plans = %d, want 7 (4 handler + 3 web-server)", len(plans))
	}
	keys := map[string]bool{}
	for _, p := range plans {
		keys[p.Key()] = true
		if err := p.Validate(); err != nil {
			t.Errorf("plan %s is invalid: %v", p.Key(), err)
		}
	}
	if keys["aws.lambda/ruby/web-server"] {
		t.Error("a ruby web-server plan was published, but there is no verified ruby web-server loader")
	}
	for _, want := range []string{"aws.lambda/java/handler", "aws.lambda/java/web-server", "aws.lambda/python/web-server"} {
		if !keys[want] {
			t.Errorf("no plan for %s", want)
		}
	}
}

func TestDesiredLambdaPlanRejectsUnknownInputs(t *testing.T) {
	if _, err := DesiredLambdaPlan("dotnet", ActivationHandler); err == nil {
		t.Error("a plan for .NET was accepted, but no OpenTelemetry Lambda layer is bundled for it")
	}
	if _, err := DesiredLambdaPlan("java", "sidecar"); err == nil {
		t.Error("an unknown activation was accepted")
	}
}

// TestLambdaHandlerPlanEnvMatchesInjector compares the plan against applyHandlerEnv, the function that
// actually writes a Lambda function's environment.
func TestLambdaHandlerPlanEnvMatchesInjector(t *testing.T) {
	dest := domaindest.OTLPTarget{Endpoint: "collector:4317", Insecure: true, Traces: true, Metrics: true, Logs: true}

	for _, lang := range []string{"java", "nodejs", "python", "ruby"} {
		t.Run(lang, func(t *testing.T) {
			runtime := planRuntimeFor(lang)
			live := map[string]string{}
			applyHandlerEnv(live, runtime, "my-fn", dest, false)

			p, err := DesiredLambdaPlan(lang, ActivationHandler)
			if err != nil {
				t.Fatalf("DesiredLambdaPlan: %v", err)
			}
			assertLambdaEnvAgrees(t, p, live)

			// The wrapper is language-specific and the wrong value fails at cold start, so it is compared
			// against the injector's own resolution rather than against a literal.
			if got := lambdaPlanValue(p, store.EnvExecWrapper); got != execWrapperForRuntime(runtime) {
				t.Errorf("%s: plan says %q, injector uses %q for runtime %s", store.EnvExecWrapper, got, execWrapperForRuntime(runtime), runtime)
			}
		})
	}
}

// TestLambdaWebServerPlanEnvMatchesInjector compares against applyWebServerEnv, and pins the one direction
// that differs from every other platform: PYTHONPATH is PREPENDED on Lambda so the layer's OpenTelemetry
// packages and its sitecustomize resolve before the application's own.
func TestLambdaWebServerPlanEnvMatchesInjector(t *testing.T) {
	dest := domaindest.OTLPTarget{Endpoint: "collector:4317", Insecure: true, Traces: true, Metrics: true, Logs: true}

	for _, lang := range []string{"java", "nodejs", "python"} {
		t.Run(lang, func(t *testing.T) {
			live := map[string]string{}
			applyWebServerEnv(live, managedManifest{Version: managedManifestVersion}, lang, "my-fn", dest)

			p, err := DesiredLambdaPlan(lang, ActivationWebServer)
			if err != nil {
				t.Fatalf("DesiredLambdaPlan: %v", err)
			}
			assertLambdaEnvAgrees(t, p, live)

			key, value, sep, _ := webServerLoader(lang)
			var loader *plan.EnvVar
			for _, e := range plan.LoaderVars(p) {
				if e.Name == key {
					e := e
					loader = &e
				}
			}
			if loader == nil {
				t.Fatalf("the plan declares no loader variable, so nothing tells a customer to load the agent")
			}
			if loader.Value != value || loader.Separator != sep {
				t.Errorf("loader = %s %q sep %q, injector uses %q sep %q", key, loader.Value, loader.Separator, value, sep)
			}
			wantMerge := plan.MergeAppend
			if lang == "python" {
				wantMerge = plan.MergePrepend
			}
			if loader.Merge != wantMerge {
				t.Errorf("%s merge = %q, want %q — addLoaderValue prepends PYTHONPATH so the layer's packages resolve before the application's own, and appends the *_OPTIONS variables",
					key, loader.Merge, wantMerge)
			}
		})
	}
}

// TestLambdaPlanMarkerRecordsTheLayer: the marker is what desiredLayerARN reads to decide which version the
// function should carry, so it must name the layer the customer attached — not be left to a resolver that
// would report a deliberately pinned version as drift.
func TestLambdaPlanMarkerRecordsTheLayer(t *testing.T) {
	p, err := DesiredLambdaPlan("java", ActivationHandler)
	if err != nil {
		t.Fatalf("DesiredLambdaPlan: %v", err)
	}
	marker := lambdaPlanValue(p, store.EnvOdigosManaged)
	if marker == "" {
		t.Fatalf("the plan does not tell a customer to set %s, without which the function reads as untouched", store.EnvOdigosManaged)
	}
	m, ok := parseManifest(marker)
	if !ok {
		t.Fatalf("the connector cannot parse the marker the plan publishes: %s", marker)
	}
	if m.Version != managedManifestVersion {
		t.Errorf("marker version = %d, want %d", m.Version, managedManifestVersion)
	}
	if m.Layer != PlaceholderLayerARN {
		t.Errorf("marker layer = %q, want the placeholder the customer replaces with the ARN they attached", m.Layer)
	}
	if got := lambdaPlanValue(p, ""); got != "" {
		t.Errorf("unexpected value for an unnamed variable: %q", got)
	}
}

func TestLambdaInstrumentationArtifactsMatchPlans(t *testing.T) {
	plans, err := DesiredLambdaPlans()
	if err != nil {
		t.Fatalf("DesiredLambdaPlans: %v", err)
	}
	assertArtifactsMatchPlans(t, "aws.lambda", plans)
}

func TestLambdaArtifactCoverageIsNotVacuous(t *testing.T) {
	plans, err := DesiredLambdaPlans()
	if err != nil {
		t.Fatalf("DesiredLambdaPlans: %v", err)
	}
	// The exec wrapper is the value whose drift is most dangerous: the wrong one fails the function at cold
	// start rather than merely failing to instrument it.
	assertCoverageCatchesDrift(t, "aws.lambda", plans, "/opt/otel-handler", "/opt/otel-wrapper")
}

func TestInstrumentationArtifactsDeclareTheExpectedVariants(t *testing.T) {
	for typeName, want := range map[string]string{
		"aws.lambda":       "aws-cli,cloudformation,manual,terraform",
		"aws.fargate-task": "cloudformation,manual,terraform",
	} {
		arts := spec.MustGet("aws").InstrumentationArtifacts(typeName)
		var variants []string
		for _, a := range arts {
			variants = append(variants, a.Variant)
			if a.Template == "" {
				t.Errorf("%s artifact %q has no template, so there is nothing for a customer to apply", typeName, a.ID)
			}
			if len(a.Steps) == 0 {
				t.Errorf("%s artifact %q declares no steps", typeName, a.ID)
			}
		}
		sort.Strings(variants)
		if got := strings.Join(variants, ","); got != want {
			t.Errorf("%s variants = %s, want %s", typeName, got, want)
		}
	}
}

// --- helpers ---

// planTaskDef is a task definition with one essential app container and NO pre-existing loader value, which is
// the case the plan's rendered append value can be compared against directly.
func planTaskDef() *ecstypes.TaskDefinition {
	return &ecstypes.TaskDefinition{
		Family:                  aws.String("checkout"),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:      aws.String("app"),
			Image:     aws.String("checkout:latest"),
			Essential: aws.Bool(true),
		}},
	}
}

// planExport is the export target for a recipe with every signal enabled, which is what the plan's exporter
// values assume (the injector sets each from what the destination actually accepts).
func planExport(rec agentRecipe) ecsExportTarget {
	return ecsExportTarget{
		endpoint: "http://collector:4317",
		protocol: rec.otlpProtocol,
		traces:   true, metrics: true, logs: true,
	}
}

// planRuntimeFor is a representative AWS runtime identifier for a layer language, so the test can call the
// runtime-keyed injector helpers.
func planRuntimeFor(lang string) string {
	switch lang {
	case "python":
		return "python3.12"
	case "nodejs":
		return "nodejs20.x"
	case "java":
		return "java21"
	default:
		return "ruby3.3"
	}
}

// fargatePlanValue is lambdaPlanValue under a name that reads correctly at the ECS call sites; both look up one
// planned variable's value.
func fargatePlanValue(p plan.Plan, name string) string { return lambdaPlanValue(p, name) }

func lambdaPlanValue(p plan.Plan, name string) string {
	if name == "" {
		return ""
	}
	for _, f := range p.Fragments {
		for _, e := range f.Env {
			if e.Name == name {
				return e.Value
			}
		}
	}
	return ""
}

// assertLambdaEnvAgrees compares a plan's variable set against a live env map built by the injector: every
// variable one names the other must name, and a variable the plan says to REMOVE must be one the injector does
// not set.
func assertLambdaEnvAgrees(t *testing.T, p plan.Plan, live map[string]string) {
	t.Helper()
	planned := map[string]plan.Merge{}
	for _, f := range p.Fragments {
		for _, e := range f.Env {
			planned[e.Name] = e.Merge
		}
	}
	for name := range live {
		if _, ok := planned[name]; !ok {
			t.Errorf("the injector sets %s but the plan never mentions it, so a customer is never told to apply it and is then reported as drifted for the omission", name)
		}
	}
	for name, merge := range planned {
		// The marker is stamped by instrument() after applyHandlerEnv / applyWebServerEnv return, so it is
		// legitimately absent from the env either of those builds. TestLambdaPlanMarkerRecordsTheLayer checks
		// it against what the connector reads back, which is the part that has to agree.
		if name == store.EnvOdigosManaged {
			continue
		}
		_, set := live[name]
		switch merge {
		case plan.MergeRemove:
			if set {
				t.Errorf("the plan says to remove %s but the injector sets it to %q", name, live[name])
			}
		default:
			if !set {
				t.Errorf("the plan tells a customer to set %s but the injector never sets it, so applying the plan produces a value nothing expects", name)
			}
		}
	}
}

func assertArtifactsMatchPlans(t *testing.T, typeName string, plans []plan.Plan) {
	t.Helper()
	artifacts := awsInstrumentationArtifacts(t, typeName)
	if problems := plan.CoverageProblems(artifacts, plans); len(problems) > 0 {
		t.Errorf("the published %s instrumentation instructions no longer match what the connector applies.\n"+
			"Fix connectors/spec/data/aws.yaml (section %q), or the recipe if the recipe is what changed:\n  - %s",
			typeName, spec.InstrumentationSection(typeName), strings.Join(problems, "\n  - "))
	}
}

// assertCoverageCatchesDrift proves the coverage guard is not vacuous, by making exactly the kind of change it
// exists to catch and requiring it to complain. Without this, a bug that made CoverageProblems return nothing
// would leave every artifact check passing silently.
func assertCoverageCatchesDrift(t *testing.T, typeName string, plans []plan.Plan, from, to string) {
	t.Helper()
	stale := map[string]string{}
	for id, body := range awsInstrumentationArtifacts(t, typeName) {
		stale[id] = strings.ReplaceAll(body, from, to)
	}
	if problems := plan.CoverageProblems(stale, plans); len(problems) == 0 {
		t.Fatalf("replacing %q with %q in every %s artifact was not detected, so the coverage guard is not actually checking anything", from, to, typeName)
	}
}

func awsInstrumentationArtifacts(t *testing.T, typeName string) map[string]string {
	t.Helper()
	arts := spec.MustGet("aws").InstrumentationArtifacts(typeName)
	if len(arts) == 0 {
		t.Fatalf("spec/data/aws.yaml declares no instrumentation artifacts for %s, so a manual-mode customer is told nothing", typeName)
	}
	out := make(map[string]string, len(arts))
	for _, a := range arts {
		out[a.ID] = a.Template
	}
	return out
}
