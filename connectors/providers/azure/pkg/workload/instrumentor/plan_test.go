package instrumentor

import (
	"sort"
	"strings"
	"testing"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/plan"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/azure/pkg/config"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

const planAgentImage = "agents:test"

// allSignalsDest accepts every signal, which is the shape the plan's exporter values assume: the injector sets
// each exporter from what the destination actually accepts, and the plan says so in a note rather than guessing
// a destination it cannot see.
func allSignalsDest() domaindest.OTLPTarget {
	return domaindest.OTLPTarget{Endpoint: "collector:4317", Traces: true, Metrics: true, Logs: true, Insecure: true}
}

func TestDesiredPlansCoverEverySupportedLanguage(t *testing.T) {
	plans, err := DesiredPlans(planAgentImage)
	if err != nil {
		t.Fatalf("DesiredPlans: %v", err)
	}
	if len(plans) != len(languageSpecs) {
		t.Fatalf("plans = %d, want one per supported language (%d)", len(plans), len(languageSpecs))
	}
	for _, p := range plans {
		if _, ok := languageSpecs[p.Language]; !ok {
			t.Errorf("plan for unsupported language %q", p.Language)
		}
		if err := p.Validate(); err != nil {
			t.Errorf("plan %s is invalid: %v", p.Key(), err)
		}
	}
}

func TestDesiredPlanRequiresTheAgentImage(t *testing.T) {
	if _, err := DesiredPlan(LangJava, ""); err == nil {
		t.Error("a plan built with no agent image was accepted; its init container could never start")
	}
	if _, err := DesiredPlan("go", planAgentImage); err == nil {
		t.Error("a plan for a language with no Container Apps agent was accepted")
	}
}

// TestPlanEnvMatchesInjector runs the real applyInstrumentation and asserts the plan names exactly the
// variables the injector sets, and that the ones the plan says to REMOVE are the ones the injector deletes.
//
// This is what a substring check against a template cannot do: a variable added to the injector and never added
// to the plan is one the customer is never told to apply, and is then reported as drift they cannot act on.
func TestPlanEnvMatchesInjector(t *testing.T) {
	for lang := range languageSpecs {
		t.Run(lang, func(t *testing.T) {
			app := testApp("app:1")
			if err := applyInstrumentation(app, lang, planAgentImage, "hello", "eastus", allSignalsDest(), false); err != nil {
				t.Fatalf("applyInstrumentation: %v", err)
			}
			live := containerEnvMap(ownedContainer(app))

			p, err := DesiredPlan(lang, planAgentImage)
			if err != nil {
				t.Fatalf("DesiredPlan: %v", err)
			}
			planned := map[string]plan.Merge{}
			for _, f := range p.Fragments {
				for _, e := range f.Env {
					planned[e.Name] = e.Merge
				}
			}

			// The two markers are the ONE place the plan and the injector are supposed to disagree, and the
			// disagreement is the mode split itself: the injector stamps the ownership marker because it
			// performed the mutation and recorded a rollback snapshot, while the plan publishes the IaC marker
			// and tells the customer not to write the other. Comparing them here would demand the connector
			// claim a snapshot on an app it never touched. TestPlanMarkerIsWhatTheConnectorReads covers the part
			// that does have to agree.
			isMarker := func(name string) bool { return name == EnvOdigosManaged || name == EnvOdigosIaC }
			for name := range live {
				// The fixture container declares JAVA_VERSION of its own; it is a language-detection hint the
				// injector never touches, not part of the instrumentation.
				if name == "JAVA_VERSION" || isMarker(name) {
					continue
				}
				if _, ok := planned[name]; !ok {
					t.Errorf("the injector sets %s but the plan never mentions it, so a customer is never told to apply it and is then reported as drifted for the omission", name)
				}
			}
			for name, merge := range planned {
				if isMarker(name) {
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
		})
	}
}

// TestPlanValuesMatchInjector compares the values for everything that is not per-workload. Per-workload values
// are placeholders by construction and are covered by the marker and staging assertions instead.
func TestPlanValuesMatchInjector(t *testing.T) {
	for lang := range languageSpecs {
		t.Run(lang, func(t *testing.T) {
			app := testApp("app:1")
			if err := applyInstrumentation(app, lang, planAgentImage, "hello", "eastus", allSignalsDest(), false); err != nil {
				t.Fatalf("applyInstrumentation: %v", err)
			}
			live := containerEnvMap(ownedContainer(app))

			p, err := DesiredPlan(lang, planAgentImage)
			if err != nil {
				t.Fatalf("DesiredPlan: %v", err)
			}
			for _, f := range p.Fragments {
				for _, e := range f.Env {
					// Both markers are compared structurally below: the injector writes only its own, and the
					// plan only its own, so there is no value here to compare.
					if e.Merge == plan.MergeRemove || strings.Contains(e.Value, "<") ||
						e.Name == EnvOdigosManaged || e.Name == EnvOdigosIaC {
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

// TestPlanMarkerIsWhatTheConnectorReads is the check that replaces comparing the plan's marker with the
// injector's: a customer who applies exactly what the plan publishes must be recognized as instrumented, the
// language they recorded must be the one the activation comparison is keyed on, and the marker must NOT grant
// Odigos the right to mutate their app.
//
// Nothing else in this package reads a value produced by the manual path, so without this a typo in the
// published payload would surface only as customers reporting that Odigos never noticed their deployment.
func TestPlanMarkerIsWhatTheConnectorReads(t *testing.T) {
	for lang := range languageSpecs {
		t.Run(lang, func(t *testing.T) {
			p, err := DesiredPlan(lang, planAgentImage)
			if err != nil {
				t.Fatalf("DesiredPlan: %v", err)
			}
			var marker string
			for _, f := range p.Fragments {
				for _, e := range f.Env {
					if e.Name == EnvOdigosIaC {
						marker = e.Value
					}
				}
			}
			if marker == "" {
				t.Fatalf("the plan does not tell a customer to set %s, without which the app reads as untouched however completely the rest is applied", EnvOdigosIaC)
			}
			m, ok := baseapi.ParseIaCMarker(marker)
			if !ok {
				t.Fatalf("the connector cannot parse the marker the plan publishes: %s", marker)
			}
			if m.Version != baseapi.IaCMarkerVersion {
				t.Errorf("marker version = %d, want %d", m.Version, baseapi.IaCMarkerVersion)
			}
			if m.Language != lang {
				t.Errorf("marker language = %q, want %q — it is what the activation comparison is keyed on", m.Language, lang)
			}

			// The app it is on must NOT read as connector-owned: nothing recorded what rolling it back would
			// restore, so a mutation gate that accepted it would delete values the customer never handed over.
			app := testApp("app:1")
			setContainerEnv(primaryContainer(app), EnvOdigosIaC, marker)
			if appOwned(app) {
				t.Error("the IaC marker granted ownership; only the ownership marker may, because only it carries a rollback snapshot")
			}
		})
	}
}

// TestPlanStagingMatchesInjector checks the parts a customer cannot reconstruct, and the one structural claim
// that differs from Cloud Run: Container Apps has real init containers, so the stager is run-to-completion and
// needs no probe and no explicit dependency.
func TestPlanStagingMatchesInjector(t *testing.T) {
	app := testApp("app:1")
	if err := applyInstrumentation(app, LangJava, planAgentImage, "hello", "eastus", allSignalsDest(), false); err != nil {
		t.Fatalf("applyInstrumentation: %v", err)
	}
	ic := findInitContainer(app, initContainerName)
	if ic == nil {
		t.Fatal("no init container")
	}

	p, err := DesiredPlan(LangJava, planAgentImage)
	if err != nil {
		t.Fatalf("DesiredPlan: %v", err)
	}
	var helper *plan.HelperContainer
	var vol *plan.Volume
	for _, f := range p.Fragments {
		if f.Helper != nil {
			helper = f.Helper
		}
		if f.Volume != nil {
			vol = f.Volume
		}
		if f.Kind == plan.KindStartupDependency {
			t.Error("the plan declares a startup dependency, but Container Apps orders init containers itself — a customer would be told to declare something the platform has no field for")
		}
	}
	if helper == nil || vol == nil {
		t.Fatal("plan declares no helper container or no volume")
	}

	if helper.Name != deref(ic.Name) || helper.Image != deref(ic.Image) {
		t.Errorf("helper = %s/%s, init container = %s/%s", helper.Name, helper.Image, deref(ic.Name), deref(ic.Image))
	}
	var cmd []string
	for _, c := range ic.Command {
		cmd = append(cmd, deref(c))
	}
	if strings.Join(helper.Command, " ") != strings.Join(cmd, " ") {
		t.Errorf("helper command = %v, init container command = %v", helper.Command, cmd)
	}
	if !helper.Init {
		t.Error("the Container Apps stager IS run-to-completion; describing it otherwise would have a customer declare a probe the platform does not need")
	}
	if helper.StartupProbePort != 0 {
		t.Errorf("helper declares startup probe port %d, but an init container is ordered by the platform", helper.StartupProbePort)
	}
	if !hasVolume(app, vol.Name) {
		t.Errorf("plan volume %q is not the one the injector adds", vol.Name)
	}
	// Container Apps accepts no size limit on an EmptyDir volume, so declaring one would have a customer paste
	// a field the API rejects.
	if vol.SizeLimit != "" {
		t.Errorf("plan volume declares size limit %q, which Container Apps does not accept", vol.SizeLimit)
	}
}

// TestInstrumentationArtifactsMatchPlans is the anti-drift guard over the published artifacts. The templates are
// hand-authored per delivery tool AND per language — HCL, ARM JSON and an az-cli YAML round-trip share no
// syntax, and each language stages a different agent — so this asserts they still say what the plans mean
// rather than pretending the values can be generated. Grouped by language, matching how they are served: a
// Java app is handed the Java documents alone, so those are what have to carry Java's values in full.
func TestInstrumentationArtifactsMatchPlans(t *testing.T) {
	plans, err := DesiredPlans(config.DefaultAgentImage)
	if err != nil {
		t.Fatalf("DesiredPlans: %v", err)
	}
	artifacts := instrumentationArtifacts(t, "azure.container-apps")

	if problems := plan.CoverageProblemsByLanguage(artifacts, plans); len(problems) > 0 {
		t.Errorf("the published Container Apps instrumentation instructions no longer match what the connector applies.\n"+
			"Fix connectors/spec/data/azure.yaml (section %q), or the recipe if the recipe is what changed:\n  - %s",
			spec.InstrumentationSection("azure.container-apps"), strings.Join(problems, "\n  - "))
	}
}

// TestInstrumentationArtifactsWireTheMarkerToTheIaCVariable is the check a literal sweep cannot make: every
// artifact names both markers — one to set, one to warn against — so only the payload's ATTACHMENT says which
// variable a customer actually writes. Getting it wrong hands them the ownership marker, which claims a rollback
// snapshot that was never taken and is also what authorizes Odigos to mutate their workload.
func TestInstrumentationArtifactsWireTheMarkerToTheIaCVariable(t *testing.T) {
	artifacts := instrumentationArtifacts(t, "azure.container-apps")
	for lang := range languageSpecs {
		byLang := map[string]plan.Artifact{}
		for id, a := range artifacts {
			if a.Language == lang {
				byLang[id] = a
			}
		}
		problems := plan.MarkerAttachmentProblems(byLang, iacMarkerValue(lang), EnvOdigosIaC, EnvOdigosManaged)
		if len(problems) > 0 {
			t.Errorf("%s: %s", lang, strings.Join(problems, "\n  - "))
		}
	}
}

// TestInstrumentationArtifactCoverageIsNotVacuous proves the guard above fires, so a bug that made
// CoverageProblems return nothing cannot leave it passing silently forever.
func TestInstrumentationArtifactCoverageIsNotVacuous(t *testing.T) {
	plans, err := DesiredPlans(config.DefaultAgentImage)
	if err != nil {
		t.Fatalf("DesiredPlans: %v", err)
	}
	stale := map[string]plan.Artifact{}
	for id, a := range instrumentationArtifacts(t, "azure.container-apps") {
		stale[id] = plan.Artifact{Language: a.Language, Body: strings.ReplaceAll(a.Body, "/var/odigos/java", "/var/odigos/jvm")}
	}
	if problems := plan.CoverageProblemsByLanguage(stale, plans); len(problems) == 0 {
		t.Fatal("a moved java agent directory was not detected, so the coverage guard is not actually checking anything")
	}
}

// TestInstrumentationArtifactsDeclareTheExpectedVariants pins the chooser EVERY supported language offers.
//
// Per language, deliberately: the language is picked for the customer from the app's detected runtime (or its
// override), so a language missing a variant is a customer whose toolchain silently is not offered, on an app
// the connector reports as awaiting exactly that document.
func TestInstrumentationArtifactsDeclareTheExpectedVariants(t *testing.T) {
	const want = "az-cli,bicep,manual,terraform" // manual is the tool-agnostic reference
	byLang := map[string][]string{}
	for _, a := range spec.MustGet("azure").InstrumentationArtifacts("azure.container-apps") {
		byLang[a.Language] = append(byLang[a.Language], a.Variant)
		if a.Template == "" {
			t.Errorf("artifact %q has no template, so there is nothing for a customer to apply", a.ID)
		}
		if len(a.Steps) == 0 {
			t.Errorf("artifact %q declares no steps", a.ID)
		}
	}
	for _, lang := range []string{LangJava, LangPython, LangNodeJS, LangDotNet} {
		variants := byLang[lang]
		sort.Strings(variants)
		if got := strings.Join(variants, ","); got != want {
			t.Errorf("%s variants = %s, want %s", lang, got, want)
		}
		delete(byLang, lang)
	}
	for lang := range byLang {
		t.Errorf("artifacts declared for unexpected language %q", lang)
	}
}

// TestInstrumentationArtifactsCarryOnlyTheirOwnLanguage is the ABSENCE half of the per-language change, and
// the coverage guard structurally cannot make it: that guard only ever asserts a value is present somewhere,
// so a Java document still listing .NET's profiler variables passes every check while putting four runtimes'
// environment in front of a reader who has one.
func TestInstrumentationArtifactsCarryOnlyTheirOwnLanguage(t *testing.T) {
	// Values belonging to exactly one language, from languageSpecs.
	owned := map[string]string{
		"/instrumentations/java":   LangJava,
		"JAVA_TOOL_OPTIONS":        LangJava,
		"/instrumentations/python": LangPython,
		"OTEL_PYTHON_CONFIGURATOR": LangPython,
		"PYTHONPATH":               LangPython,
		"/instrumentations/nodejs": LangNodeJS,
		"NODE_OPTIONS":             LangNodeJS,
		"/instrumentations/dotnet": LangDotNet,
		"CORECLR_PROFILER_PATH":    LangDotNet,
	}
	for _, a := range spec.MustGet("azure").InstrumentationArtifacts("azure.container-apps") {
		for lit, ownerLang := range owned {
			if strings.Contains(a.Template, lit) && a.Language != ownerLang {
				t.Errorf("artifact %q serves language %q but carries %q, which belongs to %s",
					a.ID, a.Language, lit, ownerLang)
			}
		}
	}
}

// TestInstrumentationArtifactsAreServedPerLanguage checks the SELECTION, which is a separate failure from the
// documents' content: every document can be correct and a app still be handed the wrong set if the language
// does not reach the lookup.
func TestInstrumentationArtifactsAreServedPerLanguage(t *testing.T) {
	s := spec.MustGet("azure")
	for _, tc := range []struct{ lang, wantIn string }{
		{LangJava, "/var/odigos/java/javaagent.jar"},
		{LangPython, "OTEL_PYTHON_CONFIGURATOR"},
		{LangNodeJS, "NODE_OPTIONS"},
		{LangDotNet, "CORECLR_PROFILER_PATH"},
	} {
		arts := s.InstrumentationArtifactsFor("azure.container-apps", tc.lang)
		if len(arts) == 0 {
			t.Errorf("%s: no artifacts served, so this app is told nothing", tc.lang)
			continue
		}
		for _, a := range arts {
			if a.Language != tc.lang {
				t.Errorf("%s was served artifact %q, which serves %q", tc.lang, a.ID, a.Language)
			}
			if !strings.Contains(a.Template, tc.wantIn) {
				t.Errorf("%s artifact %q omits %q, which this language needs", tc.lang, a.ID, tc.wantIn)
			}
		}
	}
}

// instrumentationArtifacts is the type's published templates, keyed by artifact id so a coverage failure names
// the one to go fix, each tagged with the language it serves.
func instrumentationArtifacts(t *testing.T, typeName string) map[string]plan.Artifact {
	t.Helper()
	arts := spec.MustGet("azure").InstrumentationArtifacts(typeName)
	if len(arts) == 0 {
		t.Fatalf("spec/data/azure.yaml declares no instrumentation artifacts for %s, so a manual-mode customer is told nothing", typeName)
	}
	out := make(map[string]plan.Artifact, len(arts))
	for _, a := range arts {
		out[a.ID] = plan.Artifact{Language: a.Language, Body: a.Template}
	}
	return out
}
