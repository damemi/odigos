package instrumentor

import (
	"sort"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
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
				// The two markers are the ONE place the plan and the injector are supposed to disagree, and the
				// disagreement is the mode split itself: the injector stamps the ownership marker because it
				// performed the mutation and recorded a rollback snapshot, while the plan publishes the IaC
				// marker and tells the customer not to write the other. Comparing them here would demand the
				// connector claim a snapshot on a workload it never touched, which is the bug this split fixed.
				// TestFargatePlanMarkerIsReadBackAsInstrumented checks the part that does have to agree.
				if name == store.EnvOdigosIaC || name == store.EnvOdigosManaged {
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
			for name := range live {
				if name == store.EnvOdigosManaged {
					continue // see above: the injector's marker, which the plan deliberately says to remove
				}
				if _, ok := planned[name]; !ok {
					t.Errorf("the injector sets %s but the plan never mentions it", name)
				}
			}
		})
	}
}

// TestFargatePlanMarkerIsReadBackAsInstrumented is the check that replaces comparing the plan's marker to the
// injector's: a customer who applies exactly what the plan publishes must be recognized as instrumented, and
// the language they recorded must be the one the observer judges their activation environment against.
//
// Without it the marker is unverified in both directions — nothing else in this package reads a value produced
// by the manual path, so a typo in the published payload would surface only as customers reporting that Odigos
// never noticed their deployment.
func TestFargatePlanMarkerIsReadBackAsInstrumented(t *testing.T) {
	for _, lc := range planLangs {
		t.Run(lc.lang, func(t *testing.T) {
			p, err := DesiredFargatePlan(lc.lang, testAgentsImage, lc.version)
			if err != nil {
				t.Fatalf("DesiredFargatePlan: %v", err)
			}
			marker := fargatePlanValue(p, store.EnvOdigosIaC)
			if marker == "" {
				t.Fatalf("the plan publishes no %s value", store.EnvOdigosIaC)
			}

			m, ok := readECSMarker(map[string]string{store.EnvOdigosIaC: marker})
			if !ok {
				t.Fatalf("the marker the plan publishes (%s) is not read back as Odigos instrumentation", marker)
			}
			if m.Managed {
				t.Error("the IaC marker granted ownership; only the ownership marker may, because only it carries a rollback snapshot")
			}
			if m.Language != lc.lang {
				t.Errorf("read-back language = %q, want %q — the observer would judge this container against the wrong agent", m.Language, lc.lang)
			}
			if len(m.PrevEnv) != 0 {
				t.Errorf("the IaC marker carried a rollback snapshot (%v); manual mode has no mutation to undo", m.PrevEnv)
			}

			// And the container it is on must count as instrumented, which is the fact the report renders.
			td := planTaskDef()
			setContainerEnv(&td.ContainerDefinitions[0], store.EnvOdigosIaC, marker)
			if idx := instrumentedContainerIndexes(td); len(idx) != 1 || idx[0] != 0 {
				t.Errorf("a container carrying only the published marker was not reported as instrumented (indexes %v)", idx)
			}
			// ...and must NOT be mutable: nothing recorded what rolling it back would restore.
			if idx := managedContainerIndexes(td); len(idx) != 0 {
				t.Errorf("a container carrying only the IaC marker was treated as connector-owned (indexes %v), so a rollback would delete values nobody recorded", idx)
			}
		})
	}
}

// TestFargatePlanValuesMatchInjector compares the values for everything that is not per-workload.
//
// The task definition here has NO pre-existing loader value, which is what makes an append's rendered value
// equal to the injector's result. A container that DOES have one is the case loaderActivated covers: the
// manual-mode check asks that the Odigos fragment be present and last rather than reconstructing an exact
// value, so the customer's own value needs no recording.
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
					// Both markers are excluded: the injector writes only its own, and the plan only its own.
					if e.Merge == plan.MergeRemove || strings.Contains(e.Value, "<") ||
						e.Name == store.EnvOdigosManaged || e.Name == store.EnvOdigosIaC {
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

// TestFargateArtifactsWireTheMarkerToTheIaCVariable is the check a literal sweep cannot make: every artifact
// names both markers — one to set, one to warn against — so only the payload's ATTACHMENT says which variable a
// customer actually writes. Getting it wrong hands them the ownership marker, which claims a rollback snapshot
// that was never taken and is also what authorizes Odigos to mutate their task definition.
func TestFargateArtifactsWireTheMarkerToTheIaCVariable(t *testing.T) {
	plans, err := DesiredFargatePlans(config.DefaultFargateAgentsImage)
	if err != nil {
		t.Fatalf("DesiredFargatePlans: %v", err)
	}
	artifacts := awsInstrumentationArtifacts(t, "aws.fargate-task")
	for _, p := range plans {
		byLang := map[string]plan.Artifact{}
		for id, a := range artifacts {
			if a.Language == p.Language {
				byLang[id] = a
			}
		}
		problems := plan.MarkerAttachmentProblems(byLang, fargateIaCMarkerValue(p.Language), store.EnvOdigosIaC, store.EnvOdigosManaged)
		if len(problems) > 0 {
			t.Errorf("%s: %s", p.Language, strings.Join(problems, "\n  - "))
		}
	}
}

func TestLambdaArtifactsWireTheMarkerToTheIaCVariable(t *testing.T) {
	artifacts := awsInstrumentationArtifacts(t, "aws.lambda")
	for _, lang := range []string{"java", "nodejs", "python", "ruby"} {
		byLang := map[string]plan.Artifact{}
		for id, a := range artifacts {
			if a.Language == lang {
				byLang[id] = a
			}
		}
		problems := plan.MarkerAttachmentProblems(byLang, lambdaIaCMarkerValue(), store.EnvOdigosIaC, store.EnvOdigosManaged)
		if len(problems) > 0 {
			t.Errorf("%s: %s", lang, strings.Join(problems, "\n  - "))
		}
	}
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
			planCfg, err := renderCollectorConfig(dest, nil)
			if err != nil {
				t.Fatalf("renderCollectorConfig: %v", err)
			}
			applyHandlerEnv(live, runtime, "", "my-fn", dest, false, planCfg)

			p, err := DesiredLambdaPlan(lang, ActivationHandler)
			if err != nil {
				t.Fatalf("DesiredLambdaPlan: %v", err)
			}
			assertLambdaEnvAgrees(t, p, live)

			// The wrapper is language-specific and the wrong value fails at cold start, so it is compared
			// against the injector's own resolution rather than against a literal.
			if got := lambdaPlanValue(p, store.EnvExecWrapper); got != execWrapperForRuntime(runtime, "") {
				t.Errorf("%s: plan says %q, injector uses %q for runtime %s", store.EnvExecWrapper, got, execWrapperForRuntime(runtime, ""), runtime)
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
			planCfg, err := renderCollectorConfig(dest, nil)
			if err != nil {
				t.Fatalf("renderCollectorConfig: %v", err)
			}
			applyWebServerEnv(live, managedManifest{Version: managedManifestVersion}, lang, "my-fn", dest, planCfg)

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
//
// It also pins WHICH marker manual mode publishes. The plan must name the IaC marker and not the ownership
// marker: the latter's payload is a rollback snapshot of a mutation, and in manual mode no mutation happens,
// so instructing a customer to hand-author one would have the connector honouring a rollback record nothing
// produced. Reading it back through the base decoder is what proves the published instructions and the
// observer agree on the same variable.
func TestLambdaPlanMarkerRecordsTheLayer(t *testing.T) {
	p, err := DesiredLambdaPlan("java", ActivationHandler)
	if err != nil {
		t.Fatalf("DesiredLambdaPlan: %v", err)
	}
	if got := lambdaPlanValue(p, store.EnvOdigosManaged); got != "" {
		t.Errorf("the plan tells a manual-mode customer to hand-author %s (%q), which claims a rollback snapshot no mutation produced", store.EnvOdigosManaged, got)
	}
	marker := lambdaPlanValue(p, store.EnvOdigosIaC)
	if marker == "" {
		t.Fatalf("the plan does not tell a customer to set %s, without which the function reads as untouched", store.EnvOdigosIaC)
	}
	m, ok := baseapi.ParseIaCMarker(marker)
	if !ok {
		t.Fatalf("the connector cannot parse the marker the plan publishes: %s", marker)
	}
	if m.Version != baseapi.IaCMarkerVersion {
		t.Errorf("marker version = %d, want %d", m.Version, baseapi.IaCMarkerVersion)
	}
	if m.Layer != PlaceholderLayerARN {
		t.Errorf("marker layer = %q, want the placeholder the customer replaces with the ARN they attached", m.Layer)
	}
	// The plan's marker must be the one recordedLayer reads, or manual mode publishes instructions the
	// observer then measures drift against a different variable from.
	if got := recordedLayer(map[string]string{store.EnvOdigosIaC: marker}); got != PlaceholderLayerARN {
		t.Errorf("recordedLayer read %q from the marker the plan publishes, want %q", got, PlaceholderLayerARN)
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

// TestInstrumentationArtifactsDeclareTheExpectedVariants pins the chooser EVERY supported language offers.
//
// Per language, deliberately. The variant list is what a client renders as the "Terraform / CloudFormation /
// …" chooser, and the language is picked for the customer from the workload's runtime — so a language missing a
// variant is a customer whose toolchain silently is not offered, on a workload the connector reports as
// awaiting exactly that document. The coverage guard checks the CONTENT of what exists; this checks that it
// exists at all, for each language a plan can be built for.
func TestInstrumentationArtifactsDeclareTheExpectedVariants(t *testing.T) {
	for typeName, want := range map[string]map[string]string{
		"aws.lambda": {
			"java":   "aws-cli,cdk,cloudformation,terraform",
			"nodejs": "aws-cli,cdk,cloudformation,terraform",
			"python": "aws-cli,cdk,cloudformation,terraform",
			"ruby":   "aws-cli,cdk,cloudformation,terraform",
		},
		"aws.fargate-task": {
			"dotnet": "cdk,cloudformation,terraform",
			"java":   "cdk,cloudformation,terraform",
			"nodejs": "cdk,cloudformation,terraform",
			"php":    "cdk,cloudformation,terraform",
			"python": "cdk,cloudformation,terraform",
			"ruby":   "cdk,cloudformation,terraform",
		},
	} {
		byLang := map[string][]string{}
		for _, a := range spec.MustGet("aws").InstrumentationArtifacts(typeName) {
			byLang[a.Language] = append(byLang[a.Language], a.Variant)
			if a.Template == "" {
				t.Errorf("%s artifact %q has no template, so there is nothing for a customer to apply", typeName, a.ID)
			}
			if len(a.Steps) == 0 {
				t.Errorf("%s artifact %q declares no steps", typeName, a.ID)
			}
		}
		for lang, wantVariants := range want {
			variants := byLang[lang]
			sort.Strings(variants)
			if got := strings.Join(variants, ","); got != wantVariants {
				t.Errorf("%s/%s variants = %s, want %s", typeName, lang, got, wantVariants)
			}
			delete(byLang, lang)
		}
		for lang := range byLang {
			t.Errorf("%s declares artifacts for unexpected language %q", typeName, lang)
		}
	}
}

// TestInstrumentationArtifactsCarryOnlyTheirOwnLanguage is the ABSENCE half of the per-language change, and
// the coverage guard structurally cannot make it: that guard only ever asserts a value is present somewhere, so
// a Java document still listing Python's exec wrapper passes every check while being the exact hazard — the
// wrong wrapper fails a function at cold start, and it sits in the snippet a Java customer is told to copy.
func TestInstrumentationArtifactsCarryOnlyTheirOwnLanguage(t *testing.T) {
	// Values belonging to exactly one language, from the injector's own recipes.
	owned := map[string]map[string]string{
		"aws.lambda": {
			"/opt/otel-instrument": "python",
			"PYTHONPATH":           "python",
			"-javaagent:/opt/opentelemetry-javaagent.jar": "java",
			"JAVA_TOOL_OPTIONS":                           "java",
			"--import /opt/init.mjs":                      "nodejs",
			"NODE_OPTIONS":                                "nodejs",
		},
		"aws.fargate-task": {
			"/instrumentations/java":   "java",
			"JAVA_TOOL_OPTIONS":        "java",
			"/instrumentations/python": "python",
			"OTEL_PYTHON_CONFIGURATOR": "python",
			"PYTHONPATH":               "python",
			"/instrumentations/nodejs": "nodejs",
			"NODE_OPTIONS":             "nodejs",
			"/instrumentations/dotnet": "dotnet",
			"CORECLR_PROFILER_PATH":    "dotnet",
			"/instrumentations/php":    "php",
			"PHP_INI_SCAN_DIR":         "php",
			"/instrumentations/ruby":   "ruby",
			"RUBYOPT":                  "ruby",
		},
	}
	for typeName, lits := range owned {
		for _, a := range spec.MustGet("aws").InstrumentationArtifacts(typeName) {
			for lit, ownerLang := range lits {
				if strings.Contains(a.Template, lit) && a.Language != ownerLang {
					t.Errorf("%s artifact %q serves language %q but carries %q, which belongs to %s",
						typeName, a.ID, a.Language, lit, ownerLang)
				}
			}
		}
	}
}

// TestInstrumentationArtifactsAreServedPerLanguage checks the SELECTION, which is a separate failure from the
// documents' content: every artifact can be correct and a workload still be handed the wrong set if the
// language does not reach the lookup.
func TestInstrumentationArtifactsAreServedPerLanguage(t *testing.T) {
	s := spec.MustGet("aws")
	for _, tc := range []struct {
		typeName, lang, wantIn string
	}{
		{"aws.lambda", "python", "/opt/otel-instrument"},
		{"aws.lambda", "java", "<JAVA_LAMBDA_EXEC_WRAPPER>"},
		{"aws.lambda", "ruby", "/opt/otel-handler"},
		{"aws.lambda", "nodejs", "--import /opt/init.mjs"},
		{"aws.fargate-task", "ruby", "RUBYOPT"},
		{"aws.fargate-task", "php", "PHP_INI_SCAN_DIR"},
		{"aws.fargate-task", "dotnet", "CORECLR_PROFILER_PATH"},
	} {
		arts := s.InstrumentationArtifactsFor(tc.typeName, tc.lang)
		if len(arts) == 0 {
			t.Errorf("%s/%s: no artifacts served, so this workload is told nothing", tc.typeName, tc.lang)
			continue
		}
		for _, a := range arts {
			if a.Language != tc.lang {
				t.Errorf("%s/%s was served artifact %q, which serves %q", tc.typeName, tc.lang, a.ID, a.Language)
			}
			if !strings.Contains(a.Template, tc.wantIn) {
				t.Errorf("%s/%s artifact %q omits %q, which this language needs", tc.typeName, tc.lang, a.ID, tc.wantIn)
			}
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
		// The marker is the ONE variable the plan and the injector are supposed to disagree about, and the
		// disagreement is the manual/automatic split itself: the plan is what a customer applies in manual
		// mode, where nothing will ever mutate the function, so it names the IaC marker; the injector mutates
		// the function itself and stamps the ownership marker, whose payload is the rollback snapshot only a
		// mutation can produce. (The ownership marker is also stamped by instrument() after applyHandlerEnv /
		// applyWebServerEnv return, so it is absent from the env either of those builds regardless.)
		// TestLambdaPlanMarkerRecordsTheLayer checks the plan's marker against what the connector reads back,
		// which is the part that does have to agree.
		if name == store.EnvOdigosIaC {
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

// assertArtifactsMatchPlans checks the published instructions against the injector's recipes PER LANGUAGE,
// grouped the way the connector serves them: a customer opening a Java service is handed the Java documents
// alone, so those are what have to carry Java's values in full.
func assertArtifactsMatchPlans(t *testing.T, typeName string, plans []plan.Plan) {
	t.Helper()
	artifacts := awsInstrumentationArtifacts(t, typeName)
	if problems := plan.CoverageProblemsByLanguage(artifacts, plans); len(problems) > 0 {
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
	stale := map[string]plan.Artifact{}
	for id, a := range awsInstrumentationArtifacts(t, typeName) {
		stale[id] = plan.Artifact{Language: a.Language, Body: strings.ReplaceAll(a.Body, from, to)}
	}
	if problems := plan.CoverageProblemsByLanguage(stale, plans); len(problems) == 0 {
		t.Fatalf("replacing %q with %q in every %s artifact was not detected, so the coverage guard is not actually checking anything", from, to, typeName)
	}
}

func awsInstrumentationArtifacts(t *testing.T, typeName string) map[string]plan.Artifact {
	t.Helper()
	arts := spec.MustGet("aws").InstrumentationArtifacts(typeName)
	if len(arts) == 0 {
		t.Fatalf("spec/data/aws.yaml declares no instrumentation artifacts for %s, so a manual-mode customer is told nothing", typeName)
	}
	out := make(map[string]plan.Artifact, len(arts))
	for _, a := range arts {
		out[a.ID] = plan.Artifact{Language: a.Language, Body: a.Template}
	}
	return out
}
