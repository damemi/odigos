package instrumentationplan

import (
	"strings"
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/plan"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

var testNow = time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)

// stagedPlan is the shape the staged-agent providers produce, deliberately with its fragments OUT of
// application order so the prose fallback's ordering is observable.
func stagedPlan() plan.Plan {
	return plan.Plan{
		Type:     "gcp.cloud-run",
		Language: "java",
		Mode:     plan.ModeStagedAgent,
		Fragments: []plan.Fragment{
			{
				Anchor: plan.Anchor{Scope: plan.ScopeContainer, Field: plan.FieldEnv, Container: plan.PlaceholderContainer},
				Kind:   plan.KindEnv,
				Env: []plan.EnvVar{
					{Name: "OTEL_SERVICE_NAME", Value: plan.PlaceholderServiceName, Merge: plan.MergeSet},
					{Name: "JAVA_TOOL_OPTIONS", Value: "-javaagent:/var/odigos/java/javaagent.jar", Merge: plan.MergeAppend, Separator: " "},
					{Name: "OTEL_LOG_LEVEL", Merge: plan.MergeRemove},
				},
			},
			{
				Anchor: plan.Anchor{Scope: plan.ScopeWorkload, Field: plan.FieldVolumes},
				Kind:   plan.KindVolume,
				Volume: &plan.Volume{Name: "odigos-agent", Medium: "memory", SizeLimit: "500Mi"},
			},
			{
				Anchor: plan.Anchor{Scope: plan.ScopeWorkload, Field: plan.FieldContainers},
				Kind:   plan.KindHelperContainer,
				Helper: &plan.HelperContainer{
					Name:             "odigos-agent",
					Image:            "agents:v1",
					Command:          []string{"sh", "-c", "cp -r /instrumentations/java /var/odigos/java"},
					Mount:            plan.Mount{Volume: "odigos-agent", Path: "/var/odigos"},
					StartupProbePort: 2400,
				},
			},
		},
		Notes:        []string{"OTEL_SERVICE_NAME must be <ODIGOS_SOURCE_NAME>."},
		Placeholders: []plan.Placeholder{plan.ContainerPlaceholder, plan.ServiceNamePlaceholder},
	}
}

// stagedResult is a served plan with one hand-authored delivery artifact carrying both a placeholder the
// connector resolves and one only the customer can.
func stagedResult() api.InstrumentationPlanResult {
	return api.InstrumentationPlanResult{
		ServiceName:         "checkout",
		Kind:                "gcp.cloud-run",
		Region:              "us-central1",
		InstrumentationType: "gcp.cloud-run",
		Mode:                spec.ModeManual,
		Plan:                stagedPlan(),
		Artifacts: []api.InstrumentationArtifact{{
			ID: "instrument-cloud-run-terraform", Variant: "terraform", VariantLabel: "Terraform",
			Title: "Instrument a Cloud Run service", Description: "Anchored fragments.",
			Steps:  []string{"Set the name on <YOUR_CONTAINER>.", "Replace every <ANGLE_BRACKETED> value."},
			Format: "hcl",
			Template: `containers {
  name = "<YOUR_CONTAINER>"
  env { name = "OTEL_SERVICE_NAME"  value = "<ODIGOS_SOURCE_NAME>" }
  env { name = "OTEL_EXPORTER_OTLP_ENDPOINT" value = "<OTLP_ENDPOINT_URL>" }
}`,
			Prerequisites: []string{"OTEL_SERVICE_NAME must be <ODIGOS_SOURCE_NAME>."},
		}},
		Resolved: map[string]string{
			plan.PlaceholderServiceName:     "checkout",
			plan.PlaceholderOTLPEndpointURL: "https://collector.odigos.io:4317",
		},
	}
}

func TestToModelCarriesIdentityAndMode(t *testing.T) {
	got := ToModel(stagedResult(), testNow)

	if got.ServiceName != "checkout" || got.Kind != "gcp.cloud-run" {
		t.Errorf("identity = %s/%s", got.ServiceName, got.Kind)
	}
	if got.Region == nil || *got.Region != "us-central1" {
		t.Errorf("region = %v", got.Region)
	}
	if got.Mode != spec.ModeManual {
		t.Errorf("mode = %q, want %q", got.Mode, spec.ModeManual)
	}
	if got.UpdatedAt != "2026-08-12T10:00:00Z" {
		t.Errorf("updatedAt = %q", got.UpdatedAt)
	}
}

// TestToModelSubstitutesResolvedValuesIntoContent is the point of serving these through the connector at all:
// the artifact is authored once per TYPE and this is what makes it about one workload.
func TestToModelSubstitutesResolvedValuesIntoContent(t *testing.T) {
	got := ToModel(stagedResult(), testNow)
	doc := got.Documents[0]

	if !strings.Contains(doc.Content, `value = "checkout"`) {
		t.Errorf("service name was not substituted:\n%s", doc.Content)
	}
	if strings.Contains(doc.Content, plan.PlaceholderServiceName) {
		t.Errorf("resolved token still present:\n%s", doc.Content)
	}
	if !strings.Contains(doc.Content, "https://collector.odigos.io:4317") {
		t.Errorf("endpoint was not substituted:\n%s", doc.Content)
	}
}

// TestToModelSubstitutesStepsAndNotes covers the two places a resolved value would otherwise contradict the
// snippet: a step or a caveat still naming the token the snippet already resolved.
func TestToModelSubstitutesStepsAndNotes(t *testing.T) {
	got := ToModel(stagedResult(), testNow)

	if len(got.Notes) != 1 || !strings.Contains(got.Notes[0], "checkout") {
		t.Errorf("notes were not substituted: %v", got.Notes)
	}
	if strings.Contains(strings.Join(got.Documents[0].Steps, "\n"), plan.PlaceholderServiceName) {
		t.Errorf("steps kept a resolved token: %v", got.Documents[0].Steps)
	}
}

// TestToModelServesSpecPrerequisitesAsNotes is the path the UI's "Before you deploy" section actually
// reads: authored on the artifact, not scraped from a NOTES heading in the snippet, and not the
// planner's notes when the spec declared its own.
func TestToModelServesSpecPrerequisitesAsNotes(t *testing.T) {
	res := stagedResult()
	res.Plan.Notes = []string{"planner caveat that must not reach the client"}
	res.Artifacts[0].Prerequisites = []string{"Apply this to <ODIGOS_SOURCE_NAME>."}

	got := ToModel(res, testNow)
	if len(got.Notes) != 1 || got.Notes[0] != "Apply this to checkout." {
		t.Errorf("notes = %v, want the substituted spec prerequisite", got.Notes)
	}
}

// TestToModelFallsBackToPlanNotesWhenPrerequisitesAreAbsent covers a type whose snippets have not
// grown a prerequisites list yet. Plan.Notes is the fallback so "Before you deploy" is not empty.
func TestToModelFallsBackToPlanNotesWhenPrerequisitesAreAbsent(t *testing.T) {
	res := stagedResult()
	res.Artifacts[0].Prerequisites = nil
	res.Plan.Notes = []string{"OTEL_SERVICE_NAME must be <ODIGOS_SOURCE_NAME>."}

	got := ToModel(res, testNow)
	if len(got.Notes) != 1 || !strings.Contains(got.Notes[0], "checkout") {
		t.Errorf("notes = %v, want the substituted planner notes", got.Notes)
	}
}

// TestUnresolvedReportsWhatIsLeftWithItsMeaning is what makes an unfilled blank read as a field rather than
// as a rendering bug.
func TestUnresolvedReportsWhatIsLeftWithItsMeaning(t *testing.T) {
	got := ToModel(stagedResult(), testNow)
	doc := got.Documents[0]

	if !strings.Contains(doc.Content, plan.PlaceholderContainer) {
		t.Fatalf("an unresolvable token should stay verbatim:\n%s", doc.Content)
	}
	if len(doc.Unresolved) != 1 {
		t.Fatalf("unresolved = %d entries, want 1: %+v", len(doc.Unresolved), doc.Unresolved)
	}
	if doc.Unresolved[0].Placeholder != plan.PlaceholderContainer {
		t.Errorf("unresolved token = %q", doc.Unresolved[0].Placeholder)
	}
	if doc.Unresolved[0].Description != plan.ContainerPlaceholder.Description {
		t.Errorf("description = %q, want the base's canonical wording", doc.Unresolved[0].Description)
	}
}

// TestUnresolvedIsDerivedFromContentNotSteps guards the false positive that reaches a customer as a fill-in
// field for a value no snippet contains: the artifacts' steps say "replace every <ANGLE_BRACKETED> value",
// which is prose ABOUT the convention.
func TestUnresolvedIsDerivedFromContentNotSteps(t *testing.T) {
	got := ToModel(stagedResult(), testNow)
	for _, in := range got.Documents[0].Unresolved {
		if in.Placeholder == "<ANGLE_BRACKETED>" {
			t.Fatalf("a token that appears only in the steps was reported as a blank to fill: %+v", got.Documents[0].Unresolved)
		}
	}
}

// TestUnresolvedIsNilWhenNothingIsLeft keeps a client from rendering an empty "you still need to supply"
// section for a fully resolved document.
func TestUnresolvedIsNilWhenNothingIsLeft(t *testing.T) {
	res := stagedResult()
	res.Resolved[plan.PlaceholderContainer] = "api"

	got := ToModel(res, testNow)
	if got.Documents[0].Unresolved != nil {
		t.Errorf("unresolved = %+v, want nil", got.Documents[0].Unresolved)
	}
}

// TestEmptyResolutionIsNotSubstituted covers the difference between "Odigos does not know your container's
// name" and "your container has no name". The second renders as a valid snippet that instruments nothing.
func TestEmptyResolutionIsNotSubstituted(t *testing.T) {
	res := stagedResult()
	res.Resolved[plan.PlaceholderContainer] = ""

	got := ToModel(res, testNow)
	if !strings.Contains(got.Documents[0].Content, plan.PlaceholderContainer) {
		t.Errorf("an empty resolution erased the placeholder:\n%s", got.Documents[0].Content)
	}
}

// TestProseFallbackWhenNoArtifactsAreDeclared covers a provider that has not authored per-tool snippets for a
// type. The desired state is fully known either way, so the answer is prose — not an empty response.
func TestProseFallbackWhenNoArtifactsAreDeclared(t *testing.T) {
	res := stagedResult()
	res.Artifacts = nil

	got := ToModel(res, testNow)
	if len(got.Documents) != 1 {
		t.Fatalf("documents = %d, want exactly the prose fallback", len(got.Documents))
	}
	doc := got.Documents[0]
	if doc.Variant != VariantManual {
		t.Errorf("variant = %q, want %q", doc.Variant, VariantManual)
	}
	if doc.ID == "" || !strings.HasPrefix(doc.ID, "gcp.cloud-run") {
		t.Errorf("id = %q, want one derived from the plan type", doc.ID)
	}
	// Application order, not authoring order: the volume has to exist before the container that mounts it.
	volumeAt := strings.Index(doc.Content, `volume named "odigos-agent"`)
	envAt := strings.Index(doc.Content, "OTEL_SERVICE_NAME")
	if volumeAt < 0 || envAt < 0 || volumeAt > envAt {
		t.Errorf("prose is not in application order:\n%s", doc.Content)
	}
}

// TestProseFallbackKeepsMergeDirection is the property that used to be carried by the fragments on the wire.
// Now that only text crosses, "append X, keeping what is there" has to survive the rendering — a document
// that reads as an assignment silently discards whatever the customer had set.
func TestProseFallbackKeepsMergeDirection(t *testing.T) {
	res := stagedResult()
	res.Artifacts = nil

	content := ToModel(res, testNow).Documents[0].Content
	for _, want := range []string{"JAVA_TOOL_OPTIONS", "append", "keep the current value first"} {
		if !strings.Contains(content, want) {
			t.Errorf("prose lost the append direction (missing %q):\n%s", want, content)
		}
	}
	// The removal is an instruction too: OTEL_LOG_LEVEL is compared against unset, so a leftover value is
	// reported as drift.
	if !strings.Contains(content, "OTEL_LOG_LEVEL") || !strings.Contains(content, "remove it") {
		t.Errorf("prose lost the removal instruction:\n%s", content)
	}
}

// TestProseFallbackIsSubstitutedToo — the fallback is a document like any other.
func TestProseFallbackIsSubstitutedToo(t *testing.T) {
	res := stagedResult()
	res.Artifacts = nil

	doc := ToModel(res, testNow).Documents[0]
	if strings.Contains(doc.Content, plan.PlaceholderServiceName) {
		t.Errorf("prose fallback was not substituted:\n%s", doc.Content)
	}
	if len(doc.Unresolved) != 1 || doc.Unresolved[0].Placeholder != plan.PlaceholderContainer {
		t.Errorf("prose fallback did not report its remaining blank: %+v", doc.Unresolved)
	}
}

// TestDocumentDefaultsAreDisplayable keeps a sparsely declared artifact from reaching a client as a code
// block with no syntax and a chooser entry with no label.
func TestDocumentDefaultsAreDisplayable(t *testing.T) {
	res := stagedResult()
	res.Artifacts[0].Variant = ""
	res.Artifacts[0].VariantLabel = ""
	res.Artifacts[0].Format = ""

	doc := ToModel(res, testNow).Documents[0]
	if doc.Variant != VariantManual || doc.VariantLabel != VariantManual || doc.Format != formatText {
		t.Errorf("defaults = variant %q label %q format %q", doc.Variant, doc.VariantLabel, doc.Format)
	}
}

// TestDocumentsAreNeverEmpty is the schema's promise. A client that got an empty list would have to explain
// the absence itself, and there is nothing to explain: the connector always knows the change.
func TestDocumentsAreNeverEmpty(t *testing.T) {
	res := stagedResult()
	res.Artifacts = nil
	res.Resolved = nil
	res.Plan.Notes = nil

	if got := ToModel(res, testNow); len(got.Documents) == 0 {
		t.Fatal("no documents were served for a plan the connector fully knows")
	}
}
