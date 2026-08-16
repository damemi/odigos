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
