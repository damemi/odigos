package instrumentor

import (
	"sort"
	"strings"
	"testing"

	runpb "cloud.google.com/go/run/apiv2/runpb"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/plan"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/gcp/pkg/config"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// allSignalsDest is a destination that accepts every signal, which is the shape the plan's exporter values
// assume. testDest disables logs on purpose, so it would make the value comparison assert the wrong thing.
func allSignalsDest() domaindest.OTLPTarget {
	return domaindest.OTLPTarget{Endpoint: "collector.example:4317", Insecure: true, Traces: true, Metrics: true, Logs: true}
}

func TestDesiredPlansCoverEverySupportedLanguage(t *testing.T) {
	plans, err := DesiredPlans(testAgentImage)
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
		// DesiredPlan validates before returning, so this asserts the contract rather than re-checking it:
		// a plan that escaped validation would be one built by some other path.
		if err := p.Validate(); err != nil {
			t.Errorf("plan %s is invalid: %v", p.Key(), err)
		}
	}
}

func TestDesiredPlanRequiresTheAgentImage(t *testing.T) {
	if _, err := DesiredPlan(LangJava, ""); err == nil {
		t.Error("a plan built with no agent image was accepted; its stager could never start")
	}
	if _, err := DesiredPlan("go", testAgentImage); err == nil {
		t.Error("a plan for a language with no Cloud Run agent was accepted")
	}
}

// TestPlanEnvMatchesInjector is the check that makes the published instructions trustworthy: it runs the real
// applyInstrumentation and asserts the plan names exactly the variables the injector sets, and that the ones
// the plan says to REMOVE are the ones the injector deletes.
//
// A substring check against a template cannot see a variable that was added to the injector and never added to
// the plan. That variable then never reaches the published instructions, the customer applies everything they
// were told to, and the observer reports drift on a component they were never asked for.
func TestPlanEnvMatchesInjector(t *testing.T) {
	for lang := range languageSpecs {
		t.Run(lang, func(t *testing.T) {
			svc := serviceWith(&runpb.Container{
				Name:  "api",
				Image: "app:1",
				Ports: []*runpb.ContainerPort{{ContainerPort: 8080}},
			})
			if err := applyInstrumentation(svc, lang, testAgentImage, "my-svc", "us-central1", testDest(), false); err != nil {
				t.Fatalf("applyInstrumentation: %v", err)
			}
			live := containerEnvMap(ownedContainer(svc))

			p, err := DesiredPlan(lang, testAgentImage)
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
			// claim a snapshot on a service it never touched. TestPlanMarkerIsWhatTheConnectorReads covers the
			// part that does have to agree.
			isMarker := func(name string) bool { return name == EnvOdigosManaged || name == EnvOdigosIaC }
			for name := range live {
				if isMarker(name) {
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

// TestPlanValuesMatchInjector checks the VALUES, not just the names, for everything that is not per-workload.
// The per-workload values are placeholders by design, so they are skipped here and covered by the marker and
// staging assertions instead.
//
// The destination enables every signal, which is what the plan's values assume: the injector sets each
// exporter from what the destination actually accepts, and the plan says so in a note rather than guessing.
func TestPlanValuesMatchInjector(t *testing.T) {
	const region, svcName = "us-central1", "my-svc"
	dest := allSignalsDest()

	for lang := range languageSpecs {
		t.Run(lang, func(t *testing.T) {
			svc := serviceWith(&runpb.Container{Name: "api", Image: "app:1", Ports: []*runpb.ContainerPort{{ContainerPort: 8080}}})
			if err := applyInstrumentation(svc, lang, testAgentImage, svcName, region, dest, false); err != nil {
				t.Fatalf("applyInstrumentation: %v", err)
			}
			live := containerEnvMap(ownedContainer(svc))

			p, err := DesiredPlan(lang, testAgentImage)
			if err != nil {
				t.Fatalf("DesiredPlan: %v", err)
			}
			for _, f := range p.Fragments {
				for _, e := range f.Env {
					// Values carrying a placeholder are per-workload by construction; the injector fills them
					// from the destination and the source, so they cannot be compared for equality.
					if e.Merge == plan.MergeRemove || strings.Contains(e.Value, "<") {
						continue
					}
					// Both markers are compared structurally below: the injector writes only its own, and the
					// plan only its own, so there is no value here to compare.
					if e.Name == EnvOdigosManaged || e.Name == EnvOdigosIaC {
						continue
					}
					// The container started with no value of its own, so for an append the injector's result IS
					// our value — which makes the same equality check correct for every merge.
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
// Odigos the right to mutate their service.
//
// Nothing else in this package reads a value produced by the manual path, so without this a typo in the
// published payload would surface only as customers reporting that Odigos never noticed their deployment.
func TestPlanMarkerIsWhatTheConnectorReads(t *testing.T) {
	for lang := range languageSpecs {
		t.Run(lang, func(t *testing.T) {
			p, err := DesiredPlan(lang, testAgentImage)
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
				t.Fatalf("the plan does not tell a customer to set %s, without which the service reads as untouched however completely the rest is applied", EnvOdigosIaC)
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

			// The service it is on must read as instrumented, and must NOT read as connector-owned: nothing
			// recorded what rolling it back would restore, so a mutation gate that accepted it would delete
			// values the customer never handed over.
			svc := serviceWith(&runpb.Container{Name: "api", Image: "app:1", Ports: []*runpb.ContainerPort{{ContainerPort: 8080}}})
			setContainerEnv(ingressContainer(svc), EnvOdigosIaC, marker)
			if serviceOwned(svc) {
				t.Error("the IaC marker granted ownership; only the ownership marker may, because only it carries a rollback snapshot")
			}
		})
	}
}

// TestPlanStagingMatchesInjector checks the parts a customer cannot reconstruct: the volume, the sidecar's
// image and command, the mount path, and the probe port that is the whole ordering mechanism.
func TestPlanStagingMatchesInjector(t *testing.T) {
	svc := serviceWith(&runpb.Container{Name: "api", Image: "app:1", Ports: []*runpb.ContainerPort{{ContainerPort: 8080}}})
	if err := applyInstrumentation(svc, LangJava, testAgentImage, "my-svc", "us-central1", testDest(), false); err != nil {
		t.Fatalf("applyInstrumentation: %v", err)
	}
	sc := findContainer(svc, sidecarContainerName)
	if sc == nil {
		t.Fatal("no sidecar")
	}

	p, err := DesiredPlan(LangJava, testAgentImage)
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
	}
	if helper == nil || vol == nil {
		t.Fatal("plan declares no helper container or no volume")
	}

	if helper.Name != sc.GetName() || helper.Image != sc.GetImage() {
		t.Errorf("helper = %s/%s, sidecar = %s/%s", helper.Name, helper.Image, sc.GetName(), sc.GetImage())
	}
	if strings.Join(helper.Command, " ") != strings.Join(sc.GetCommand(), " ") {
		t.Errorf("helper command = %v, sidecar command = %v", helper.Command, sc.GetCommand())
	}
	if got := int(sc.GetStartupProbe().GetTcpSocket().GetPort()); helper.StartupProbePort != got {
		t.Errorf("helper probe port = %d, sidecar probe port = %d", helper.StartupProbePort, got)
	}
	// Cloud Run's stager cannot exit, so it must never be described as run-to-completion: a customer who
	// declared it that way would get a container the platform keeps restarting.
	if helper.Init {
		t.Error("the Cloud Run stager is not run-to-completion — it holds a port, which is what the probe waits on")
	}
	if !hasVolume(svc, vol.Name) {
		t.Errorf("plan volume %q is not the one the injector adds", vol.Name)
	}
	if vol.SizeLimit != agentVolumeSizeLimit {
		t.Errorf("plan volume size limit = %q, want %q", vol.SizeLimit, agentVolumeSizeLimit)
	}
}

// TestInstrumentationArtifactsMatchPlans is the anti-drift guard over the published artifacts. The templates
// are hand-authored per delivery tool AND per language — HCL and a gcloud YAML round-trip share no syntax, and
// each language stages a different agent — so this asserts they still say what the plans mean rather than
// pretending the values can be generated. Grouped by language, matching how they are served: a Java service is
// handed the Java documents alone, so those are what have to carry Java's values in full.
func TestInstrumentationArtifactsMatchPlans(t *testing.T) {
	plans, err := DesiredPlans(config.DefaultAgentImage)
	if err != nil {
		t.Fatalf("DesiredPlans: %v", err)
	}
	artifacts := instrumentationArtifacts(t, "gcp.cloud-run")

	if problems := plan.CoverageProblemsByLanguage(artifacts, plans); len(problems) > 0 {
		t.Errorf("the published Cloud Run instrumentation instructions no longer match what the connector applies.\n"+
			"Fix connectors/spec/data/gcp.yaml (section %q), or the recipe if the recipe is what changed:\n  - %s",
			spec.InstrumentationSection("gcp.cloud-run"), strings.Join(problems, "\n  - "))
	}
}

// TestInstrumentationArtifactsWireTheMarkerToTheIaCVariable is the check a literal sweep cannot make: every
// artifact names both markers — one to set, one to warn against — so only the payload's ATTACHMENT says which
// variable a customer actually writes. Getting it wrong hands them the ownership marker, which claims a rollback
// snapshot that was never taken and is also what authorizes Odigos to mutate their workload.
func TestInstrumentationArtifactsWireTheMarkerToTheIaCVariable(t *testing.T) {
	artifacts := instrumentationArtifacts(t, "gcp.cloud-run")
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

// TestInstrumentationArtifactCoverageIsNotVacuous proves the guard above fires. Without this, a bug that made
// CoverageProblems return nothing would leave the whole check passing silently forever.
func TestInstrumentationArtifactCoverageIsNotVacuous(t *testing.T) {
	plans, err := DesiredPlans(config.DefaultAgentImage)
	if err != nil {
		t.Fatalf("DesiredPlans: %v", err)
	}
	artifacts := instrumentationArtifacts(t, "gcp.cloud-run")

	// Simulate the exact drift this exists to catch: the agent directory moved and nobody updated the docs.
	stale := map[string]plan.Artifact{}
	for id, a := range artifacts {
		stale[id] = plan.Artifact{Language: a.Language, Body: strings.ReplaceAll(a.Body, "/var/odigos/java", "/var/odigos/jvm")}
	}
	if problems := plan.CoverageProblemsByLanguage(stale, plans); len(problems) == 0 {
		t.Fatal("a moved java agent directory was not detected, so the coverage guard is not actually checking anything")
	}
}

// TestInstrumentationArtifactsDeclareTheExpectedVariants pins the chooser EVERY supported language offers.
//
// Per language, deliberately: the language is picked for the customer from the service's detected runtime (or
// its override), so a language missing a variant is a customer whose toolchain silently is not offered, on a
// service the connector reports as awaiting exactly that document.
func TestInstrumentationArtifactsDeclareTheExpectedVariants(t *testing.T) {
	const want = "gcloud,manual,terraform" // manual is the provider-agnostic reference
	byLang := map[string][]string{}
	for _, a := range spec.MustGet("gcp").InstrumentationArtifacts("gcp.cloud-run") {
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
	for _, a := range spec.MustGet("gcp").InstrumentationArtifacts("gcp.cloud-run") {
		for lit, ownerLang := range owned {
			if strings.Contains(a.Template, lit) && a.Language != ownerLang {
				t.Errorf("artifact %q serves language %q but carries %q, which belongs to %s",
					a.ID, a.Language, lit, ownerLang)
			}
		}
	}
}

// TestInstrumentationArtifactsAreServedPerLanguage checks the SELECTION, which is a separate failure from the
// documents' content: every document can be correct and a service still be handed the wrong set if the language
// does not reach the lookup.
func TestInstrumentationArtifactsAreServedPerLanguage(t *testing.T) {
	s := spec.MustGet("gcp")
	for _, tc := range []struct{ lang, wantIn string }{
		{LangJava, "/var/odigos/java/javaagent.jar"},
		{LangPython, "OTEL_PYTHON_CONFIGURATOR"},
		{LangNodeJS, "NODE_OPTIONS"},
		{LangDotNet, "CORECLR_PROFILER_PATH"},
	} {
		arts := s.InstrumentationArtifactsFor("gcp.cloud-run", tc.lang)
		if len(arts) == 0 {
			t.Errorf("%s: no artifacts served, so this service is told nothing", tc.lang)
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
	arts := spec.MustGet("gcp").InstrumentationArtifacts(typeName)
	if len(arts) == 0 {
		t.Fatalf("spec/data/gcp.yaml declares no instrumentation artifacts for %s, so a manual-mode customer is told nothing", typeName)
	}
	out := make(map[string]plan.Artifact, len(arts))
	for _, a := range arts {
		out[a.ID] = plan.Artifact{Language: a.Language, Body: a.Template}
	}
	return out
}
