package spec

import (
	"strings"
	"testing"
)

// The instrumentation artifacts are what a manual (IaC) mode customer actually applies. This file covers the
// spec-side invariants; whether a given artifact still says what the connector would DO is checked per
// provider (TestInstrumentationArtifactsMatchPlans), which is the only place that can read the injector's own
// recipes.

// TestEveryImplementedInstrumentationTypeHasInstructions is the completeness guard, and the reason it is worth
// having is the default: a workload type declared with `discovery: true, instrumentation: false` is manual mode,
// which is what a customer gets unless they explicitly hand instrumentation to Odigos. So the moment a new
// instrumentation type is declared, the common configuration for it is "the customer applies it themselves" —
// and with no artifacts the connector reports those workloads as awaiting a deployment it never told anyone how
// to make. That is indistinguishable, from the outside, from the feature being broken.
func TestEveryImplementedInstrumentationTypeHasInstructions(t *testing.T) {
	for _, s := range All() {
		if !s.Workloads.Instrumentation.Enabled {
			continue
		}
		for _, ty := range s.Workloads.Instrumentation.Types {
			// A declared-but-unimplemented type is reported as NotImplemented and never instrumented in either
			// mode, so there is nothing to instruct.
			if !ty.IsImplemented() {
				continue
			}
			arts := s.InstrumentationArtifacts(ty.Name)
			if len(arts) == 0 {
				t.Errorf("%s: instrumentation type %q is implemented but declares no artifacts in section %q, "+
					"so a manual-mode customer is told their workload is awaiting a deployment and never told what to apply",
					s.Provider, ty.Name, InstrumentationSection(ty.Name))
			}
		}
	}
}

// TestInstrumentationArtifactsAreSelfDescribing: these are copy-paste documents a customer works from with no
// UI around them yet, so each has to carry its own steps, a template, and a variant label to be chosen by.
func TestInstrumentationArtifactsAreSelfDescribing(t *testing.T) {
	for _, s := range All() {
		for _, a := range s.SetupArtifacts() {
			if !strings.HasPrefix(a.Section, instrumentationSectionPrefix) {
				continue
			}
			t.Run(s.Provider+"/"+a.ID, func(t *testing.T) {
				if a.Template == "" {
					t.Error("no template, so there is nothing for a customer to apply")
				}
				if len(a.Steps) == 0 {
					t.Error("no steps: the template alone does not say where it goes or what to do after applying it")
				}
				if a.Title == "" || a.Description == "" {
					t.Error("no title or description")
				}
				// Every instrumentation section declares several delivery variants, so each artifact needs a
				// label to be chosen by — unlike a single-artifact onboarding section.
				if a.Variant == "" || a.VariantLabel == "" {
					t.Error("no variant/variantLabel, so a chooser cannot offer it")
				}
				// {{permissions}} belongs to the onboarding policy artifacts; an instrumentation artifact that
				// embedded it would render the connector's IAM policy into a customer's task definition.
				if strings.Contains(a.Template, "{{permissions}}") {
					t.Error("embeds {{permissions}}, which is the onboarding policy placeholder")
				}
			})
		}
	}
}

// TestInstrumentationTemplatesDoNotCarryANotesHeading: the "Before you deploy" copy lives on
// prerequisites, not as a NOTES block inside the snippet a customer copies.
func TestInstrumentationTemplatesDoNotCarryANotesHeading(t *testing.T) {
	for _, s := range All() {
		for _, a := range s.SetupArtifacts() {
			if !strings.HasPrefix(a.Section, instrumentationSectionPrefix) {
				continue
			}
			for _, line := range strings.Split(a.Template, "\n") {
				if st := strings.TrimSpace(line); st == "# NOTES" || st == "NOTES" || st == "// NOTES" {
					t.Errorf("%s/%s: template still contains a NOTES heading; move that copy to prerequisites", s.Provider, a.ID)
				}
			}
		}
	}
}

func TestFargateInstrumentationArtifactsDeclarePrerequisites(t *testing.T) {
	const want = "https://docs.odigos.io/cloud-connectors/add-connectors/add-aws-connector"
	for _, a := range MustGet("aws").InstrumentationArtifacts("aws.fargate-task") {
		if len(a.Prerequisites) == 0 {
			t.Errorf("%s: no prerequisites, so Before you deploy has nothing to show for this snippet", a.ID)
			continue
		}
		joined := strings.Join(a.Prerequisites, "\n")
		if !strings.Contains(joined, "registry.odigos.io") || !strings.Contains(joined, want) {
			t.Errorf("%s: prerequisites must mention registry.odigos.io accessibility and link %s; got %v",
				a.ID, want, a.Prerequisites)
		}
	}
}

func TestGCPInstrumentationArtifactsDeclarePrerequisites(t *testing.T) {
	const want = "https://docs.odigos.io/cloud-connectors/add-connectors/add-gcp-connector"
	for _, a := range MustGet("gcp").InstrumentationArtifacts("gcp.cloud-run") {
		if len(a.Prerequisites) == 0 {
			t.Errorf("%s: no prerequisites, so Before you deploy has nothing to show for this snippet", a.ID)
			continue
		}
		joined := strings.Join(a.Prerequisites, "\n")
		if !strings.Contains(joined, "registry.odigos.io") || !strings.Contains(joined, want) {
			t.Errorf("%s: prerequisites must mention registry.odigos.io accessibility and link %s; got %v",
				a.ID, want, a.Prerequisites)
		}
	}
}

func TestAzureInstrumentationArtifactsDeclarePrerequisites(t *testing.T) {
	const want = "https://docs.odigos.io/cloud-connectors/add-connectors/add-azure-connector"
	for _, a := range MustGet("azure").InstrumentationArtifacts("azure.container-apps") {
		if len(a.Prerequisites) == 0 {
			t.Errorf("%s: no prerequisites, so Before you deploy has nothing to show for this snippet", a.ID)
			continue
		}
		joined := strings.Join(a.Prerequisites, "\n")
		if !strings.Contains(joined, "registry.odigos.io") || !strings.Contains(joined, want) {
			t.Errorf("%s: prerequisites must mention registry.odigos.io accessibility and link %s; got %v",
				a.ID, want, a.Prerequisites)
		}
	}
}

func TestLambdaInstrumentationArtifactsRequirePreloadedLayers(t *testing.T) {
	const want = "https://docs.odigos.io/cloud-connectors/aws/workloads/lambda/preload"
	for _, a := range MustGet("aws").InstrumentationArtifacts("aws.lambda") {
		if len(a.Prerequisites) == 0 {
			t.Errorf("%s: no prerequisites", a.ID)
			continue
		}
		joined := strings.Join(a.Prerequisites, "\n")
		if !strings.Contains(joined, "preloaded") || !strings.Contains(joined, want) {
			t.Errorf("%s: prerequisites must tell the customer to preload Odigos Lambda layers and link %s; got %v",
				a.ID, want, a.Prerequisites)
		}
	}
}

// TestInstrumentationArtifactsUseTheAngleBracketPlaceholderConvention pins the deliberate difference from the
// onboarding artifacts. {{token}} is substituted client-side from create-connector FORM values and left
// verbatim when there is none — so a per-workload token spelled that way reaches the customer as a literal
// "{{containerName}}" and reads as a rendering bug rather than as a field to fill in.
func TestInstrumentationArtifactsUseTheAngleBracketPlaceholderConvention(t *testing.T) {
	for _, s := range All() {
		for _, a := range s.SetupArtifacts() {
			if !strings.HasPrefix(a.Section, instrumentationSectionPrefix) {
				continue
			}
			if tok := setupTokenRe.FindString(a.Template); tok != "" {
				t.Errorf("%s/%s: template carries %s; per-workload values must use the <UPPER_SNAKE_CASE> convention, which nothing substitutes silently",
					s.Provider, a.ID, tok)
			}
		}
	}
}

func TestParseRejectsDuplicateArtifactID(t *testing.T) {
	// A duplicate id fails nowhere at runtime — one entry simply shadows the other wherever something indexes
	// by id — so parse is the only place it can be caught.
	_, err := Parse([]byte(`
provider: test
setup:
  artifacts:
    - id: policy-preview
      section: permissions
      format: json
      template: "{}"
    - id: policy-preview
      section: instrumentation:test.thing
      variant: manual
      format: text
      template: "x"
`))
	if err == nil {
		t.Fatal("a duplicate setup artifact id was accepted")
	}
	if !strings.Contains(err.Error(), "policy-preview") {
		t.Errorf("error should name the duplicated id, got: %v", err)
	}
}

func TestParseRejectsDuplicateVariantInOneSection(t *testing.T) {
	// Distinct variants in a section ARE the chooser, so two artifacts claiming the same variant give it two
	// identical options and only one can ever be selected.
	_, err := Parse([]byte(`
provider: test
setup:
  artifacts:
    - id: a
      section: instrumentation:test.thing
      variant: terraform
      format: shell
      template: "x"
    - id: b
      section: instrumentation:test.thing
      variant: terraform
      format: shell
      template: "y"
`))
	if err == nil {
		t.Fatal("two artifacts declaring the same variant in one section were accepted")
	}
	if !strings.Contains(err.Error(), "terraform") {
		t.Errorf("error should name the duplicated variant, got: %v", err)
	}
}

func TestParseAllowsTheSameVariantInDifferentSections(t *testing.T) {
	// Lambda and Fargate both offer a terraform variant, and they are different documents.
	if _, err := Parse([]byte(`
provider: test
setup:
  artifacts:
    - id: a
      section: instrumentation:test.one
      variant: terraform
      format: shell
      template: "x"
    - id: b
      section: instrumentation:test.two
      variant: terraform
      format: shell
      template: "y"
`)); err != nil {
		t.Fatalf("the same variant in two different sections was rejected: %v", err)
	}
}

func TestParseRejectsArtifactWithNoID(t *testing.T) {
	if _, err := Parse([]byte(`
provider: test
setup:
  artifacts:
    - section: permissions
      format: json
      template: "{}"
`)); err == nil {
		t.Fatal("an artifact with no id was accepted; nothing could reference it")
	}
}

func TestInstrumentationSectionNamespacesByType(t *testing.T) {
	if got := InstrumentationSection("aws.lambda"); got != "instrumentation:aws.lambda" {
		t.Errorf("InstrumentationSection = %q, want instrumentation:aws.lambda", got)
	}
	// Sectioning by type is what makes the artifacts selectable: Lambda's instructions must not appear in a
	// list of Fargate's.
	aws := MustGet("aws")
	lambda := aws.InstrumentationArtifacts("aws.lambda")
	fargate := aws.InstrumentationArtifacts("aws.fargate-task")
	if len(lambda) == 0 || len(fargate) == 0 {
		t.Fatalf("expected artifacts for both aws types, got %d and %d", len(lambda), len(fargate))
	}
	for _, a := range lambda {
		for _, b := range fargate {
			if a.ID == b.ID {
				t.Errorf("artifact %q is returned for both types", a.ID)
			}
		}
	}
	if got := aws.InstrumentationArtifacts("aws.nonexistent"); len(got) != 0 {
		t.Errorf("an unknown type returned %d artifacts", len(got))
	}
}

// ---------------------------------------------------------------------------------------------------------
// Language scoping
// ---------------------------------------------------------------------------------------------------------

// TestInstrumentationArtifactsForNarrowsByLanguage is the behaviour the whole per-language change rests on:
// a workload is served ITS language's documents and not the others'. A selection that returned everything
// would put five runtimes' environment variables in front of a customer who runs one, which is the shape this
// replaced; one that returned nothing would fall back to prose for a language that has real snippets.
func TestInstrumentationArtifactsForNarrowsByLanguage(t *testing.T) {
	for _, s := range All() {
		for _, ty := range s.Workloads.Instrumentation.Types {
			all := s.InstrumentationArtifacts(ty.Name)
			langs := map[string]bool{}
			for _, a := range all {
				if a.Language != "" {
					langs[a.Language] = true
				}
			}
			if len(langs) < 2 {
				// A language-neutral (or single-language) type has nothing to narrow.
				continue
			}
			for lang := range langs {
				got := s.InstrumentationArtifactsFor(ty.Name, lang)
				if len(got) == 0 {
					t.Errorf("%s/%s: no artifacts served for language %q, though it declares some", s.Provider, ty.Name, lang)
				}
				if len(got) == len(all) {
					t.Errorf("%s/%s: language %q was served all %d artifacts, so nothing was narrowed", s.Provider, ty.Name, lang, len(all))
				}
				for _, a := range got {
					if a.Language != lang {
						t.Errorf("%s/%s: language %q was served artifact %q, which declares language %q",
							s.Provider, ty.Name, lang, a.ID, a.Language)
					}
				}
			}
			// An unknown language must not silently receive another one's instructions. With no
			// language-neutral artifacts declared the honest answer is none, and the caller renders prose.
			if got := s.InstrumentationArtifactsFor(ty.Name, "cobol"); len(got) != 0 {
				t.Errorf("%s/%s: an unknown language was served %d artifacts, the first being %q",
					s.Provider, ty.Name, len(got), got[0].ID)
			}
		}
	}
}

// TestInstrumentationArtifactsForFallsBackToLanguageNeutral: a type whose instrumentation does not depend on
// the language declares neutral artifacts, and every language must still be served them. Without the
// fallback, adding the language dimension would have silently emptied such a type's instructions.
func TestInstrumentationArtifactsForFallsBackToLanguageNeutral(t *testing.T) {
	s, err := Parse([]byte(`
provider: test
setup:
  artifacts:
    - id: neutral-terraform
      section: instrumentation:test.thing
      variant: terraform
      format: shell
      template: "x"
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, lang := range []string{"", "java", "cobol"} {
		if got := s.InstrumentationArtifactsFor("test.thing", lang); len(got) != 1 {
			t.Errorf("language %q was served %d artifacts, want the 1 language-neutral one", lang, len(got))
		}
	}
}

func TestParseAllowsTheSameVariantForDifferentLanguages(t *testing.T) {
	// One Terraform snippet per language IS the shape: only one language's set is ever served, so they are
	// not two options in one chooser.
	if _, err := Parse([]byte(`
provider: test
setup:
  artifacts:
    - id: a
      section: instrumentation:test.thing
      language: java
      variant: terraform
      format: shell
      template: "x"
    - id: b
      section: instrumentation:test.thing
      language: python
      variant: terraform
      format: shell
      template: "y"
`)); err != nil {
		t.Fatalf("the same variant for two languages was rejected: %v", err)
	}
}

func TestParseRejectsDuplicateVariantForOneLanguage(t *testing.T) {
	_, err := Parse([]byte(`
provider: test
setup:
  artifacts:
    - id: a
      section: instrumentation:test.thing
      language: java
      variant: terraform
      format: shell
      template: "x"
    - id: b
      section: instrumentation:test.thing
      language: java
      variant: terraform
      format: shell
      template: "y"
`))
	if err == nil {
		t.Fatal("two artifacts declaring the same variant for the same language were accepted")
	}
	if !strings.Contains(err.Error(), "java") || !strings.Contains(err.Error(), "terraform") {
		t.Errorf("error should name the language and the variant, got: %v", err)
	}
}

// TestParseRejectsMixingLanguageScopedAndNeutralArtifacts pins why the mix cannot be allowed: the selection
// prefers a language's own artifacts and only falls back to the neutral ones when it has none, so a section
// holding both would serve Java its Terraform snippet and silently withhold the CloudFormation one beside it.
func TestParseRejectsMixingLanguageScopedAndNeutralArtifacts(t *testing.T) {
	_, err := Parse([]byte(`
provider: test
setup:
  artifacts:
    - id: a
      section: instrumentation:test.thing
      language: java
      variant: terraform
      format: shell
      template: "x"
    - id: b
      section: instrumentation:test.thing
      variant: cloudformation
      format: yaml
      template: "y"
`))
	if err == nil {
		t.Fatal("a section mixing language-scoped and language-neutral artifacts was accepted")
	}
	if !strings.Contains(err.Error(), "instrumentation:test.thing") {
		t.Errorf("error should name the section, got: %v", err)
	}
}

// TestParseRejectsLanguageOutsideAnInstrumentationSection: nothing selects by language anywhere else, so the
// key would read as a scoping that quietly does nothing — an IAM policy document has no language.
func TestParseRejectsLanguageOutsideAnInstrumentationSection(t *testing.T) {
	_, err := Parse([]byte(`
provider: test
setup:
  artifacts:
    - id: policy-preview
      section: permissions
      language: java
      format: json
      template: "{}"
`))
	if err == nil {
		t.Fatal("a language on an onboarding artifact was accepted")
	}
	if !strings.Contains(err.Error(), "policy-preview") {
		t.Errorf("error should name the artifact, got: %v", err)
	}
}
