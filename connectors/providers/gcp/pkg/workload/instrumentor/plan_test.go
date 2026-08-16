package instrumentor

import (
	"sort"
	"strings"
	"testing"

	runpb "cloud.google.com/go/run/apiv2/runpb"

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
					// The marker is compared structurally below: the plan deliberately omits the rollback
					// snapshot the injector writes.
					if e.Name == EnvOdigosManaged {
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

// TestPlanMarkerIsWhatTheConnectorReads compares the marker structurally, because the plan's marker is
// deliberately NOT byte-identical to the injector's: the injector writes a prevEnv rollback snapshot of every
// key it overwrote, and in manual mode there is nothing for Odigos to roll back — the customer's own
// deployment definition is that record. What must match is the part the connector reads back: the wire version
// and the language, which is what ownership and the activation comparison are keyed on.
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
					if e.Name == EnvOdigosManaged {
						marker = e.Value
					}
				}
			}
			if marker == "" {
				t.Fatalf("the plan does not tell a customer to set %s, without which the service reads as untouched however completely the rest is applied", EnvOdigosManaged)
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
// are hand-authored per delivery tool — HCL and a gcloud YAML round-trip share no syntax — so this asserts they
// still say what the plans mean rather than pretending the values can be generated.
func TestInstrumentationArtifactsMatchPlans(t *testing.T) {
	plans, err := DesiredPlans(config.DefaultAgentImage)
	if err != nil {
		t.Fatalf("DesiredPlans: %v", err)
	}
	artifacts := instrumentationArtifacts(t, "gcp.cloud-run")

	if problems := plan.CoverageProblems(artifacts, plans); len(problems) > 0 {
		t.Errorf("the published Cloud Run instrumentation instructions no longer match what the connector applies.\n"+
			"Fix connectors/spec/data/gcp.yaml (section %q), or the recipe if the recipe is what changed:\n  - %s",
			spec.InstrumentationSection("gcp.cloud-run"), strings.Join(problems, "\n  - "))
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
	stale := map[string]string{}
	for id, body := range artifacts {
		stale[id] = strings.ReplaceAll(body, "/var/odigos/java", "/var/odigos/jvm")
	}
	if problems := plan.CoverageProblems(stale, plans); len(problems) == 0 {
		t.Fatal("a moved java agent directory was not detected, so the coverage guard is not actually checking anything")
	}
}

func TestInstrumentationArtifactsDeclareTheExpectedVariants(t *testing.T) {
	arts := spec.MustGet("gcp").InstrumentationArtifacts("gcp.cloud-run")
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
	if strings.Join(variants, ",") != "gcloud,manual,terraform" {
		t.Errorf("variants = %v, want gcloud, manual (the provider-agnostic reference) and terraform", variants)
	}
}

// instrumentationArtifacts is the type's published templates, keyed by artifact id so a coverage failure names
// the one to go fix.
func instrumentationArtifacts(t *testing.T, typeName string) map[string]string {
	t.Helper()
	arts := spec.MustGet("gcp").InstrumentationArtifacts(typeName)
	if len(arts) == 0 {
		t.Fatalf("spec/data/gcp.yaml declares no instrumentation artifacts for %s, so a manual-mode customer is told nothing", typeName)
	}
	out := make(map[string]string, len(arts))
	for _, a := range arts {
		out[a.ID] = a.Template
	}
	return out
}
