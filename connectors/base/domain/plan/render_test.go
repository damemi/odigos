package plan

import (
	"strings"
	"testing"
)

// TestStepsNameTheMergeDirection is the rendering half of the rule Validate enforces structurally: a
// customer reading the prose must be able to tell "put ours after yours" from "put ours before yours".
// Rendering both as "set X to Y" would be a correct-looking instruction that breaks one of the two.
func TestStepsNameTheMergeDirection(t *testing.T) {
	p := Plan{
		Type: "x", Mode: ModeStagedAgent,
		Fragments: []Fragment{{
			Anchor: Anchor{Scope: ScopeContainer, Field: FieldEnv, Container: "api"},
			Kind:   KindEnv,
			Env: []EnvVar{
				{Name: "JAVA_TOOL_OPTIONS", Value: "-javaagent:/var/odigos/java/javaagent.jar", Merge: MergeAppend, Separator: " "},
				{Name: "PYTHONPATH", Value: "/var/odigos/python", Merge: MergePrepend, Separator: ":"},
				{Name: "OTEL_SERVICE_NAME", Value: "api", Merge: MergeSet},
			},
		}},
	}
	steps := Steps(p)
	if len(steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(steps))
	}
	joined := strings.Join(steps[0].Detail, "\n")

	for _, want := range []string{
		`JAVA_TOOL_OPTIONS: append "-javaagent:/var/odigos/java/javaagent.jar" to the existing value`,
		`PYTHONPATH: prepend "/var/odigos/python" to the existing value`,
		`OTEL_SERVICE_NAME: set to "api"`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("detail is missing %q:\n%s", want, joined)
		}
	}
	// The separator has to be visible: "append to the existing value" without saying what joins them is not
	// a followable instruction for PYTHONPATH, where a space instead of a colon breaks the path.
	if !strings.Contains(joined, `separated by ":"`) || !strings.Contains(joined, `separated by " "`) {
		t.Errorf("detail should name each separator:\n%s", joined)
	}
	// And it must not read as a plain assignment, which is the mistake the whole model guards against.
	if strings.Contains(joined, `JAVA_TOOL_OPTIONS: set to`) {
		t.Errorf("a loader variable rendered as a plain set:\n%s", joined)
	}
}

func TestStepsCoverEveryFragmentKind(t *testing.T) {
	p := stagedPlan()
	p.Fragments = append(p.Fragments, Fragment{
		Anchor: Anchor{Scope: ScopeWorkload, Field: FieldLayers},
		Kind:   KindLayer,
		Layer:  &Layer{Placeholder: "<OTEL_LAYER_ARN>"},
	})
	steps := Steps(p)
	if len(steps) != len(p.Fragments) {
		t.Fatalf("steps = %d, want one per fragment (%d)", len(steps), len(p.Fragments))
	}
	for i, s := range steps {
		if s.Instruction == "" {
			t.Errorf("step %d (%s) has no instruction", i, s.Anchor)
		}
		if s.Anchor == "" {
			t.Errorf("step %d has no anchor; a fragment with nowhere to go is not applicable", i)
		}
	}
}

// TestStepsDoesNotMutateCaller: Steps sorts, and a caller that renders a plan twice — once for prose, once
// for a literal check — must get the same plan back both times.
func TestStepsDoesNotMutateCaller(t *testing.T) {
	p := stagedPlan()
	for i, j := 0, len(p.Fragments)-1; i < j; i, j = i+1, j-1 {
		p.Fragments[i], p.Fragments[j] = p.Fragments[j], p.Fragments[i]
	}
	before := p.Fragments[0].Kind
	_ = Steps(p)
	if p.Fragments[0].Kind != before {
		t.Errorf("Steps reordered the caller's plan: first fragment kind %q -> %q", before, p.Fragments[0].Kind)
	}
}

func TestProseIsOrderedAndCarriesNotes(t *testing.T) {
	p := stagedPlan()
	p.Notes = []string{"Cloud Run has no run-to-completion containers."}
	got := Prose(p)

	if !strings.HasPrefix(got, "1. Declare a shared in-memory volume") {
		t.Errorf("prose should start with the volume step:\n%s", got)
	}
	if !strings.Contains(got, "Note: Cloud Run has no run-to-completion containers.") {
		t.Errorf("prose dropped the note:\n%s", got)
	}
	// The staging command is the one thing a customer cannot reconstruct, so it must appear verbatim.
	if !strings.Contains(got, "cp -r /instrumentations/java /var/odigos/java") {
		t.Errorf("prose dropped the staging command:\n%s", got)
	}
	if strings.HasSuffix(got, "\n") {
		t.Error("prose should not end with a trailing newline (it is embedded in a YAML block scalar)")
	}
}

// TestProseIsStable catches the failure mode that makes a golden test worthless: providers build static env
// from Go maps, so an unsorted render differs between processes.
func TestProseIsStable(t *testing.T) {
	p := stagedPlan()
	p.Fragments[4].Env = []EnvVar{
		{Name: "CORECLR_PROFILER_PATH", Value: "/var/odigos/dotnet/x.so", Merge: MergeSet},
		{Name: "OTEL_DOTNET_AUTO_HOME", Value: "/var/odigos/dotnet", Merge: MergeSet},
		{Name: "DOTNET_STARTUP_HOOKS", Value: "/var/odigos/dotnet/y.dll", Merge: MergeSet},
	}
	first := Prose(p)
	for i := 0; i < 20; i++ {
		if got := Prose(p); got != first {
			t.Fatalf("prose is not stable across renders:\nfirst:\n%s\ngot:\n%s", first, got)
		}
	}
}

func TestRequiredLiteralsCoversEveryValueACustomerMustPaste(t *testing.T) {
	p := stagedPlan()
	lits := RequiredLiterals(p)
	index := map[string]bool{}
	for _, l := range lits {
		index[l] = true
	}

	for _, want := range []string{
		"odigos-agent",     // volume + helper name
		"odigos/agents:v1", // agent image
		"/var/odigos",      // mount path
		"cp -r /instrumentations/java /var/odigos/java", // staging command element
		"2400", // startup probe port
		"OTEL_SERVICE_NAME", "api",
		"JAVA_TOOL_OPTIONS", "-javaagent:/var/odigos/java/javaagent.jar",
	} {
		if !index[want] {
			t.Errorf("required literals is missing %q; a template could drop it and still pass\ngot: %v", want, lits)
		}
	}
	if index[""] {
		t.Error("required literals contains the empty string, which every template trivially satisfies")
	}
}

func TestRequiredLiteralsIncludesLayerPlaceholderWhenARNIsUnresolved(t *testing.T) {
	p := Plan{
		Type: "aws.lambda", Mode: ModeLayer,
		Fragments: []Fragment{{
			Anchor: Anchor{Scope: ScopeWorkload, Field: FieldLayers},
			Kind:   KindLayer,
			Layer:  &Layer{Placeholder: "<OTEL_LAYER_ARN>"},
		}},
	}
	if got := RequiredLiterals(p); len(got) != 1 || got[0] != "<OTEL_LAYER_ARN>" {
		t.Errorf("required literals = %v, want the placeholder a customer has to substitute", got)
	}
}

// TestLoaderVarsIsWhatASubstringCheckCannotSee: a template hardcoding the loader value satisfies
// RequiredLiterals and is still the bug, so the direction needs its own accessor.
func TestLoaderVarsExcludesPlainSets(t *testing.T) {
	p := stagedPlan()
	got := LoaderVars(p)
	if len(got) != 1 || got[0].Name != "JAVA_TOOL_OPTIONS" {
		t.Fatalf("loader vars = %+v, want only the append-style variable", got)
	}
	if got[0].Separator != " " {
		t.Errorf("loader var separator = %q, want the join a template has to honor", got[0].Separator)
	}
}
