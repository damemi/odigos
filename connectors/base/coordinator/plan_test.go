package coordinator

import (
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// languageScopedSpec declares one variant per language for one instrumentation type, which is the shape every
// provider's real spec has.
var languageScopedSpec = spec.CloudConnectorSpec{
	Setup: spec.Setup{Artifacts: []spec.SetupArtifact{
		{ID: "tf-java", Section: spec.InstrumentationSection("workload-a"), Language: "java", Variant: "terraform", Template: "javaagent.jar"},
		{ID: "cfn-java", Section: spec.InstrumentationSection("workload-a"), Language: "java", Variant: "cloudformation", Template: "javaagent.jar"},
		{ID: "tf-python", Section: spec.InstrumentationSection("workload-a"), Language: "python", Variant: "terraform", Template: "PYTHONPATH"},
	}},
}

// TestInstrumentationArtifactsNarrowedToThePlansLanguage is the data-access half of serving per-language
// instructions: the artifacts exist and are correct, and a customer is still handed the wrong ones if the
// plan's language does not reach the lookup.
func TestInstrumentationArtifactsNarrowedToThePlansLanguage(t *testing.T) {
	c := newSpecCoord(languageScopedSpec, spec.Capabilities{})

	java := c.instrumentationArtifacts("workload-a", "java")
	if len(java) != 2 {
		t.Fatalf("java was served %d artifacts, want its 2 variants", len(java))
	}
	for _, a := range java {
		if a.ID == "tf-python" {
			t.Error("java was served python's Terraform snippet")
		}
	}

	if got := c.instrumentationArtifacts("workload-a", "python"); len(got) != 1 || got[0].ID != "tf-python" {
		t.Errorf("python was served %v, want just tf-python", got)
	}
}

// A language nothing was authored for must serve NO artifact rather than another language's. The caller then
// renders the plan as prose, which describes the same desired state — where a Java snippet handed to a Ruby
// service is instructions that cannot work.
func TestInstrumentationArtifactsAreEmptyForAnUnauthoredLanguage(t *testing.T) {
	c := newSpecCoord(languageScopedSpec, spec.Capabilities{})
	if got := c.instrumentationArtifacts("workload-a", "ruby"); got != nil {
		t.Errorf("ruby was served %d artifacts, want none so the caller falls back to prose", len(got))
	}
}

// A type whose instrumentation does not depend on the language declares language-neutral artifacts, and every
// language — including a plan carrying none — must still be served them.
func TestInstrumentationArtifactsServeLanguageNeutralToEveryLanguage(t *testing.T) {
	neutral := spec.CloudConnectorSpec{Setup: spec.Setup{Artifacts: []spec.SetupArtifact{
		{ID: "tf", Section: spec.InstrumentationSection("workload-a"), Variant: "terraform", Template: "x"},
	}}}
	c := newSpecCoord(neutral, spec.Capabilities{})
	for _, lang := range []string{"", "java", "ruby"} {
		if got := c.instrumentationArtifacts("workload-a", lang); len(got) != 1 {
			t.Errorf("language %q was served %d artifacts, want the 1 language-neutral one", lang, len(got))
		}
	}
}
