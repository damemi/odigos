package instrumentor

import (
	"sort"
	"strings"
	"testing"

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

			for name := range live {
				// The fixture container declares JAVA_VERSION of its own; it is a language-detection hint the
				// injector never touches, not part of the instrumentation.
				if name == "JAVA_VERSION" {
					continue
				}
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
					if e.Merge == plan.MergeRemove || strings.Contains(e.Value, "<") || e.Name == EnvOdigosManaged {
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

// TestPlanMarkerIsWhatTheConnectorReads compares the marker structurally. The plan's marker is deliberately not
// byte-identical to the injector's: the injector writes a prevEnv rollback snapshot of every key it overwrote,
// and in manual mode there is nothing for Odigos to roll back — the customer's own definition is that record.
// What must match is what the connector reads back: the wire version and the language.
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
					if e.Name == EnvOdigosManaged {
						marker = e.Value
					}
				}
			}
			if marker == "" {
				t.Fatalf("the plan does not tell a customer to set %s, without which the app reads as untouched however completely the rest is applied", EnvOdigosManaged)
			}
			m, ok := parseManifest(marker)
			if !ok {
				t.Fatalf("the connector cannot parse the marker the plan publishes: %s", marker)
			}
			if m.Version != managedManifestVersion {
				t.Errorf("marker version = %d, want %d", m.Version, managedManifestVersion)
			}
			if m.Language != lang {
				t.Errorf("marker language = %q, want %q — it is what the activation comparison is keyed on", m.Language, lang)
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
// hand-authored per delivery tool — HCL, ARM JSON and an az-cli YAML round-trip share no syntax — so this
// asserts they still say what the plans mean rather than pretending the values can be generated.
func TestInstrumentationArtifactsMatchPlans(t *testing.T) {
	plans, err := DesiredPlans(config.DefaultAgentImage)
	if err != nil {
		t.Fatalf("DesiredPlans: %v", err)
	}
	artifacts := instrumentationArtifacts(t, "azure.container-apps")

	if problems := plan.CoverageProblems(artifacts, plans); len(problems) > 0 {
		t.Errorf("the published Container Apps instrumentation instructions no longer match what the connector applies.\n"+
			"Fix connectors/spec/data/azure.yaml (section %q), or the recipe if the recipe is what changed:\n  - %s",
			spec.InstrumentationSection("azure.container-apps"), strings.Join(problems, "\n  - "))
	}
}

// TestInstrumentationArtifactCoverageIsNotVacuous proves the guard above fires, so a bug that made
// CoverageProblems return nothing cannot leave it passing silently forever.
func TestInstrumentationArtifactCoverageIsNotVacuous(t *testing.T) {
	plans, err := DesiredPlans(config.DefaultAgentImage)
	if err != nil {
		t.Fatalf("DesiredPlans: %v", err)
	}
	stale := map[string]string{}
	for id, body := range instrumentationArtifacts(t, "azure.container-apps") {
		stale[id] = strings.ReplaceAll(body, "/var/odigos/java", "/var/odigos/jvm")
	}
	if problems := plan.CoverageProblems(stale, plans); len(problems) == 0 {
		t.Fatal("a moved java agent directory was not detected, so the coverage guard is not actually checking anything")
	}
}

func TestInstrumentationArtifactsDeclareTheExpectedVariants(t *testing.T) {
	arts := spec.MustGet("azure").InstrumentationArtifacts("azure.container-apps")
	var variants []string
	for _, a := range arts {
		variants = append(variants, a.Variant)
		if a.Template == "" {
			t.Errorf("artifact %q has no template, so there is nothing for a customer to apply", a.ID)
		}
		if len(a.Steps) == 0 {
			t.Errorf("artifact %q declares no steps", a.ID)
		}
	}
	sort.Strings(variants)
	if strings.Join(variants, ",") != "az-cli,bicep,manual,terraform" {
		t.Errorf("variants = %v, want az-cli, bicep, manual (the tool-agnostic reference) and terraform", variants)
	}
}

func instrumentationArtifacts(t *testing.T, typeName string) map[string]string {
	t.Helper()
	arts := spec.MustGet("azure").InstrumentationArtifacts(typeName)
	if len(arts) == 0 {
		t.Fatalf("spec/data/azure.yaml declares no instrumentation artifacts for %s, so a manual-mode customer is told nothing", typeName)
	}
	out := make(map[string]string, len(arts))
	for _, a := range arts {
		out[a.ID] = a.Template
	}
	return out
}
