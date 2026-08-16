package plan

import (
	"strings"
	"testing"
)

// stagedPlan is a well-formed staged-agent plan, the shape Cloud Run / Container Apps / ECS Fargate all
// produce. Tests perturb a copy of it rather than hand-building each case, so a new Validate rule shows up
// as a failure in the one place that asserts the whole shape is accepted.
func stagedPlan() Plan {
	return Plan{
		Type:     "gcp.cloud-run",
		Language: "java",
		Mode:     ModeStagedAgent,
		Fragments: []Fragment{
			{
				Anchor: Anchor{Scope: ScopeWorkload, Field: FieldVolumes},
				Kind:   KindVolume,
				Volume: &Volume{Name: "odigos-agent", Medium: "memory", SizeLimit: "500Mi"},
			},
			{
				Anchor: Anchor{Scope: ScopeWorkload, Field: FieldContainers},
				Kind:   KindHelperContainer,
				Helper: &HelperContainer{
					Name:             "odigos-agent",
					Image:            "odigos/agents:v1",
					Command:          []string{"sh", "-c", "cp -r /instrumentations/java /var/odigos/java"},
					Mount:            Mount{Volume: "odigos-agent", Path: "/var/odigos"},
					StartupProbePort: 2400,
				},
			},
			{
				Anchor: Anchor{Scope: ScopeContainer, Field: FieldVolumeMounts, Container: "api"},
				Kind:   KindVolumeMount,
				Mount:  &Mount{Volume: "odigos-agent", Path: "/var/odigos"},
			},
			{
				Anchor:            Anchor{Scope: ScopeContainer, Field: FieldDependsOn, Container: "api"},
				Kind:              KindStartupDependency,
				StartupDependency: &StartupDependency{On: "odigos-agent", Condition: ConditionStarted},
			},
			{
				Anchor: Anchor{Scope: ScopeContainer, Field: FieldEnv, Container: "api"},
				Kind:   KindEnv,
				Env: []EnvVar{
					{Name: "OTEL_SERVICE_NAME", Value: "api", Merge: MergeSet},
					{Name: "JAVA_TOOL_OPTIONS", Value: "-javaagent:/var/odigos/java/javaagent.jar", Merge: MergeAppend, Separator: " "},
				},
			},
		},
	}
}

func TestValidateAcceptsWellFormedPlan(t *testing.T) {
	if err := stagedPlan().Validate(); err != nil {
		t.Fatalf("well-formed staged-agent plan rejected: %v", err)
	}
	layer := Plan{
		Type:     "aws.lambda",
		Language: "python",
		Mode:     ModeLayer,
		Fragments: []Fragment{
			{
				Anchor: Anchor{Scope: ScopeWorkload, Field: FieldLayers},
				Kind:   KindLayer,
				Layer:  &Layer{Placeholder: "<OTEL_LAYER_ARN>"},
			},
			{
				Anchor: Anchor{Scope: ScopeWorkload, Field: FieldEnv},
				Kind:   KindEnv,
				Env:    []EnvVar{{Name: "AWS_LAMBDA_EXEC_WRAPPER", Value: "/opt/otel-instrument", Merge: MergeSet}},
			},
		},
		Placeholders: []Placeholder{{Token: "<OTEL_LAYER_ARN>", Description: "the layer version ARN for your region, runtime and architecture"}},
	}
	if err := layer.Validate(); err != nil {
		t.Fatalf("well-formed layer plan rejected: %v", err)
	}
}

// TestValidateRejectsLoaderWithoutSeparator is the rule the whole package exists for: an append with no
// separator can only render as a literal, and a literal loader value silently discards whatever the customer
// already had in JAVA_TOOL_OPTIONS / PYTHONPATH / NODE_OPTIONS.
func TestValidateRejectsLoaderWithoutSeparator(t *testing.T) {
	for _, merge := range []Merge{MergeAppend, MergePrepend} {
		p := stagedPlan()
		env := p.Fragments[4].Env
		env[1].Merge = merge
		env[1].Separator = ""

		err := p.Validate()
		if err == nil {
			t.Fatalf("merge %q with no separator was accepted", merge)
		}
		if !strings.Contains(err.Error(), "JAVA_TOOL_OPTIONS") || !strings.Contains(err.Error(), "separator") {
			t.Errorf("error should name the variable and the missing separator, got: %v", err)
		}
		// The message has to say WHY, or the next author "fixes" it by switching to MergeSet.
		if !strings.Contains(err.Error(), "discards") {
			t.Errorf("error should explain that a literal discards the customer's value, got: %v", err)
		}
	}
}

func TestValidateRejectsSeparatorOnPlainSet(t *testing.T) {
	p := stagedPlan()
	p.Fragments[4].Env[0].Separator = ":"
	if err := p.Validate(); err == nil {
		t.Fatal("a MergeSet variable declaring a separator was accepted; the separator would be silently ignored")
	}
}

// TestValidateRejectsSecret guards the one thing these artifacts must never become: a document carrying a
// credential into a customer's repository or a support ticket.
func TestValidateRejectsSecret(t *testing.T) {
	p := stagedPlan()
	p.Fragments[4].Env = append(p.Fragments[4].Env, EnvVar{
		Name: "OTEL_EXPORTER_OTLP_HEADERS", Value: "authorization=Bearer t0ken", Merge: MergeSet, Secret: true,
	})
	err := p.Validate()
	if err == nil {
		t.Fatal("a secret environment variable was accepted into a copy-paste artifact")
	}
	if !strings.Contains(err.Error(), "OTEL_EXPORTER_OTLP_HEADERS") {
		t.Errorf("error should name the offending variable, got: %v", err)
	}
	if strings.Contains(err.Error(), "t0ken") {
		t.Errorf("the validation error must not echo the secret value, got: %v", err)
	}
}

func TestValidateRejectsEmptyEnvValue(t *testing.T) {
	p := stagedPlan()
	p.Fragments[4].Env[0].Value = ""
	if err := p.Validate(); err == nil {
		t.Fatal("an empty env value was accepted; it renders as NAME= and reads as a deliberate blank")
	}
}

func TestValidateRejectsContainerAnchorWithoutContainer(t *testing.T) {
	p := stagedPlan()
	p.Fragments[4].Anchor.Container = ""
	if err := p.Validate(); err == nil {
		t.Fatal("a container-scoped anchor with no container was accepted; a customer cannot follow it on a multi-container definition")
	}
}

func TestValidateRejectsWorkloadAnchorNamingAContainer(t *testing.T) {
	p := stagedPlan()
	p.Fragments[0].Anchor.Container = "api"
	if err := p.Validate(); err == nil {
		t.Fatal("a workload-scoped anchor naming a container was accepted")
	}
}

// TestValidateRejectsPayloadKindMismatch covers the authoring mistake that would otherwise reach a renderer
// as a nil dereference: the payload switch is keyed on Kind, so a volume payload labelled KindVolumeMount
// lands in the mount case with f.Mount == nil.
func TestValidateRejectsPayloadKindMismatch(t *testing.T) {
	p := stagedPlan()
	p.Fragments[0].Kind = KindVolumeMount // payload is still a *Volume
	err := p.Validate()
	if err == nil {
		t.Fatal("a fragment whose Kind does not match its payload was accepted")
	}
	if !strings.Contains(err.Error(), "payload set is") {
		t.Errorf("error should name the mismatch, got: %v", err)
	}

	q := stagedPlan()
	q.Fragments[0].Mount = &Mount{Volume: "odigos-agent", Path: "/var/odigos"}
	if err := q.Validate(); err == nil {
		t.Fatal("a fragment with two payloads set was accepted")
	}
}

func TestValidateRejectsDuplicateKeyAtOneAnchor(t *testing.T) {
	p := stagedPlan()
	p.Fragments[4].Env = append(p.Fragments[4].Env, EnvVar{Name: "OTEL_SERVICE_NAME", Value: "other", Merge: MergeSet})
	err := p.Validate()
	if err == nil {
		t.Fatal("the same variable declared twice at one anchor was accepted; the two instructions contradict each other")
	}
	if !strings.Contains(err.Error(), "declared twice") {
		t.Errorf("error should say the key is declared twice, got: %v", err)
	}
}

// TestValidateRejectsUngatedStager covers the race that produces an intermittent missing-agent at cold
// start: a long-lived stager the platform has no way to wait for.
func TestValidateRejectsUngatedStager(t *testing.T) {
	p := stagedPlan()
	p.Fragments[1].Helper.StartupProbePort = 0
	if err := p.Validate(); err == nil {
		t.Fatal("a non-init helper container with no startup-probe port was accepted")
	}

	q := stagedPlan()
	q.Fragments[1].Helper.Init = true
	q.Fragments[1].Helper.StartupProbePort = 0
	if err := q.Validate(); err != nil {
		t.Fatalf("a run-to-completion init container needs no probe port, but was rejected: %v", err)
	}
}

func TestValidateRejectsLayerWithNeitherARNNorPlaceholder(t *testing.T) {
	p := Plan{
		Type: "aws.lambda", Mode: ModeLayer,
		Fragments: []Fragment{{
			Anchor: Anchor{Scope: ScopeWorkload, Field: FieldLayers},
			Kind:   KindLayer,
			Layer:  &Layer{},
		}},
	}
	if err := p.Validate(); err == nil {
		t.Fatal("a layer fragment with no ARN and no placeholder was accepted; it renders as a blank")
	}
}

func TestValidateRejectsEmptyPlanAndUnknownMode(t *testing.T) {
	if err := (Plan{Type: "aws.lambda", Mode: ModeLayer}).Validate(); err == nil {
		t.Error("a plan with no fragments was accepted; there would be nothing for a customer to apply")
	}
	p := stagedPlan()
	p.Mode = "sidecar"
	if err := p.Validate(); err == nil {
		t.Error("an unknown injection mode was accepted")
	}
}

// TestValidateReportsEveryProblem: these are authoring errors surfaced by a provider's test, so fixing them
// one per run is wasted work.
func TestValidateReportsEveryProblem(t *testing.T) {
	p := stagedPlan()
	p.Fragments[4].Env[1].Separator = ""
	p.Fragments[2].Mount.Path = ""
	p.Fragments[4].Anchor.Container = ""

	err := p.Validate()
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := strings.Count(err.Error(), "\n  - "); got < 3 {
		t.Errorf("expected all three problems reported at once, got %d:\n%v", got, err)
	}
}

func TestSortIsDependencyOrder(t *testing.T) {
	p := stagedPlan()
	// Reverse it: a customer pasting env before the volume exists still needs the rendering to be applicable
	// top to bottom.
	for i, j := 0, len(p.Fragments)-1; i < j; i, j = i+1, j-1 {
		p.Fragments[i], p.Fragments[j] = p.Fragments[j], p.Fragments[i]
	}
	p.Sort()

	var order []Kind
	for _, f := range p.Fragments {
		order = append(order, f.Kind)
	}
	want := []Kind{KindVolume, KindHelperContainer, KindVolumeMount, KindStartupDependency, KindEnv}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("sorted kinds = %v, want %v", order, want)
		}
	}
}

// TestSortStabilizesEnvOrder: providers build static env from maps (recipe.staticEnv), whose iteration order
// is randomized per process. Without this, every render differs and no golden test is possible.
func TestSortStabilizesEnvOrder(t *testing.T) {
	p := stagedPlan()
	p.Fragments[4].Env = []EnvVar{
		{Name: "ZZZ", Value: "1", Merge: MergeSet},
		{Name: "AAA", Value: "2", Merge: MergeSet},
		{Name: "MMM", Value: "3", Merge: MergeSet},
	}
	p.Sort()
	var names []string
	for _, e := range p.Fragments[len(p.Fragments)-1].Env {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "AAA,MMM,ZZZ" {
		t.Errorf("env order = %v, want sorted by name", names)
	}
}

func TestAnchorString(t *testing.T) {
	got := Anchor{Scope: ScopeContainer, Field: FieldEnv, Container: "api"}.String()
	if got != "containers[api].env" {
		t.Errorf("container anchor = %q, want containers[api].env", got)
	}
	if got := (Anchor{Scope: ScopeWorkload, Field: FieldVolumes}).String(); got != "volumes" {
		t.Errorf("workload anchor = %q, want volumes", got)
	}
}

// TestValidateRejectsMalformedPlaceholder: a lowercase or spaced token still reads as "replace this" to a
// human, so nothing about the artifact looks wrong — but no test that looks for the canonical form can see
// it, and neither can a customer scanning for the shape they were told to substitute.
func TestValidateRejectsMalformedPlaceholder(t *testing.T) {
	for _, bad := range []string{"<your_container>", "<YOUR CONTAINER>", "<Region>"} {
		p := stagedPlan()
		p.Fragments[4].Env[0].Value = bad
		err := p.Validate()
		if err == nil {
			t.Errorf("malformed placeholder %q was accepted", bad)
			continue
		}
		if !strings.Contains(err.Error(), PlaceholderContainer) {
			t.Errorf("error for %q should show the expected form, got: %v", bad, err)
		}
	}

	p := stagedPlan()
	p.Fragments[4].Env[0].Value = PlaceholderServiceName
	p.Fragments[4].Anchor.Container = PlaceholderContainer
	p.Fragments[2].Anchor.Container = PlaceholderContainer
	p.Fragments[3].Anchor.Container = PlaceholderContainer
	p.Placeholders = []Placeholder{
		{Token: PlaceholderServiceName, Description: "the workload's name in Odigos"},
		{Token: PlaceholderContainer, Description: "your application container"},
	}
	if err := p.Validate(); err != nil {
		t.Errorf("canonical placeholders were rejected: %v", err)
	}
}

// TestValidateRequiresEveryPlaceholderToBeDocumented covers the direction that reaches a customer: a token
// they are told to replace, with nothing saying what belongs there.
func TestValidateRequiresEveryPlaceholderToBeDocumented(t *testing.T) {
	p := stagedPlan()
	p.Fragments[4].Env[0].Value = PlaceholderServiceName

	err := p.Validate()
	if err == nil {
		t.Fatal("an undocumented placeholder was accepted")
	}
	if !strings.Contains(err.Error(), PlaceholderServiceName) {
		t.Errorf("error should name the token, got: %v", err)
	}

	p.Placeholders = []Placeholder{{Token: PlaceholderServiceName}}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "no description") {
		t.Errorf("a placeholder with no description was accepted: %v", err)
	}
}

// TestValidateRejectsAPlaceholderThatAppearsNowhere covers the other direction: a fill-in field for a value
// nothing in the plan substitutes, which a client would render as a required input that does nothing.
func TestValidateRejectsAPlaceholderThatAppearsNowhere(t *testing.T) {
	p := stagedPlan()
	p.Placeholders = []Placeholder{{Token: "<OTLP_ENDPOINT>", Description: "the destination endpoint"}}
	err := p.Validate()
	if err == nil {
		t.Fatal("a placeholder that appears nowhere in the plan was accepted")
	}
	if !strings.Contains(err.Error(), "appears nowhere") {
		t.Errorf("error should say the token is unused, got: %v", err)
	}
}
