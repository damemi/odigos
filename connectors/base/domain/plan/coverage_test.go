package plan

import (
	"strings"
	"testing"
)

// javaPlan / pythonPlan differ exactly the way two languages of one platform do: same volume, mount, marker
// key and export variables; different agent directory, loader variable and merge direction.
func javaPlan() Plan {
	p := stagedPlan()
	p.Fragments[2].Anchor.Container = PlaceholderContainer
	p.Fragments[3].Anchor.Container = PlaceholderContainer
	p.Fragments[4].Anchor.Container = PlaceholderContainer
	return p
}

func pythonPlan() Plan {
	p := javaPlan()
	p.Language = "python"
	p.Fragments[1].Helper.Command = []string{"sh", "-c", "cp -r /instrumentations/python /var/odigos/python"}
	p.Fragments[4].Env = []EnvVar{
		{Name: "OTEL_SERVICE_NAME", Value: "api", Merge: MergeSet},
		{Name: "PYTHONPATH", Value: "/var/odigos/python", Merge: MergePrepend, Separator: ":"},
	}
	return p
}

// fullArtifact carries everything both plans need, so a test can remove one thing at a time and see exactly
// which check fires.
func fullArtifact() string {
	return strings.Join([]string{
		`volume "odigos-agent" size 500Mi mounted at /var/odigos`,
		`container odigos-agent image odigos/agents:v1 probe 2400`,
		`command: sh -c "cp -r /instrumentations/java /var/odigos/java"`,
		`command: sh -c "cp -r /instrumentations/python /var/odigos/python"`,
		`OTEL_SERVICE_NAME = "api"`,
		`JAVA_TOOL_OPTIONS: append "-javaagent:/var/odigos/java/javaagent.jar" to the current value`,
		`PYTHONPATH: prepend "/var/odigos/python" to the current value`,
	}, "\n")
}

func TestCoverageProblemsPassesACompleteArtifact(t *testing.T) {
	got := CoverageProblems(map[string]string{"terraform": fullArtifact()}, []Plan{javaPlan(), pythonPlan()})
	if len(got) != 0 {
		t.Fatalf("a complete artifact reported problems:\n%s", strings.Join(got, "\n"))
	}
}

// TestCoverageProblemsCatchesADriftedAgentPath is the case the guard exists for: the injector's agent
// directory moved and nobody updated the instructions.
func TestCoverageProblemsCatchesADriftedAgentPath(t *testing.T) {
	stale := strings.ReplaceAll(fullArtifact(), "/instrumentations/python /var/odigos/python", "/instrumentations/py /var/odigos/py")
	got := CoverageProblems(map[string]string{"terraform": stale}, []Plan{javaPlan(), pythonPlan()})
	if len(got) == 0 {
		t.Fatal("a stale python agent path was accepted")
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "python") {
		t.Errorf("the problem should name the language that needs it:\n%s", joined)
	}
}

// TestCoverageProblemsCatchesAVariantThatSkipsAStep: a union-only check would pass this, because the other
// artifact still mentions the volume.
func TestCoverageProblemsCatchesAVariantThatSkipsAStep(t *testing.T) {
	complete := fullArtifact()
	partial := strings.ReplaceAll(complete, "/var/odigos", "/somewhere-else")

	got := CoverageProblems(map[string]string{"terraform": complete, "cli": partial}, []Plan{javaPlan(), pythonPlan()})
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "cli") {
		t.Fatalf("the incomplete artifact was not named:\n%s", joined)
	}
	if !strings.Contains(joined, "every plan needs") {
		t.Errorf("the problem should say the omitted literal is language-independent:\n%s", joined)
	}
}

// TestCoverageProblemsCatchesAHardcodedLoaderValue is the check no substring sweep can make: the value is
// present, so every literal check passes, and the instruction is still the one that destroys the customer's
// JAVA_TOOL_OPTIONS.
func TestCoverageProblemsCatchesAHardcodedLoaderValue(t *testing.T) {
	hardcoded := strings.ReplaceAll(fullArtifact(),
		`JAVA_TOOL_OPTIONS: append "-javaagent:/var/odigos/java/javaagent.jar" to the current value`,
		`JAVA_TOOL_OPTIONS = "-javaagent:/var/odigos/java/javaagent.jar"`)

	got := CoverageProblems(map[string]string{"terraform": hardcoded}, []Plan{javaPlan(), pythonPlan()})
	if len(got) == 0 {
		t.Fatal("a hardcoded loader value was accepted")
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "JAVA_TOOL_OPTIONS") || !strings.Contains(joined, "append") {
		t.Errorf("the problem should name the variable and the required direction:\n%s", joined)
	}
	if !strings.Contains(joined, "discards their value") {
		t.Errorf("the problem should say what goes wrong, or it reads as a wording nit:\n%s", joined)
	}
}

// TestCoverageProblemsDistinguishesAppendFromPrepend: PYTHONPATH is prepended on Lambda and appended on the
// staged-agent platforms, so the (variable, direction) pair is the unit — an artifact stating the wrong
// direction must not pass because the variable is mentioned somewhere.
func TestCoverageProblemsDistinguishesAppendFromPrepend(t *testing.T) {
	wrongWay := strings.ReplaceAll(fullArtifact(),
		`PYTHONPATH: prepend "/var/odigos/python" to the current value`,
		`PYTHONPATH: append "/var/odigos/python" to the current value`)

	got := CoverageProblems(map[string]string{"terraform": wrongWay}, []Plan{javaPlan(), pythonPlan()})
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "PYTHONPATH") || !strings.Contains(joined, "prepend") {
		t.Fatalf("appending a variable that must be prepended was accepted:\n%s", joined)
	}
}

func TestCoverageProblemsRejectsEmptyInputs(t *testing.T) {
	if got := CoverageProblems(map[string]string{"a": "x"}, nil); len(got) != 1 {
		t.Errorf("no plans should be reported as a problem, got %v", got)
	}
	if got := CoverageProblems(nil, []Plan{javaPlan()}); len(got) != 1 {
		t.Errorf("no artifacts should be reported as a problem, got %v", got)
	}
}
