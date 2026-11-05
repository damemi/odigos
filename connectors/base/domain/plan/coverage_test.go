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

// TestCoverageProblemsAcceptsAnEscapedJSONValue: a marker that is itself JSON is written escaped inside an HCL or
// JSON string and bare in YAML; both evaluate to the same value. A changed value is still caught.
func TestCoverageProblemsAcceptsAnEscapedJSONValue(t *testing.T) {
	p := pythonPlan()
	p.Fragments[4].Env = append(p.Fragments[4].Env, EnvVar{Name: "ODIGOS_IAC", Value: `{"v":1,"lang":"python"}`, Merge: MergeSet})
	artifacts := map[string]string{
		"terraform":      fullArtifact() + "\n" + `ODIGOS_IAC = "{\"v\":1,\"lang\":\"python\"}"`,
		"cloudformation": fullArtifact() + "\n" + `ODIGOS_IAC: '{"v":1,"lang":"python"}'`,
	}
	if got := CoverageProblems(artifacts, []Plan{p}); len(got) != 0 {
		t.Fatalf("escaped and bare forms of one value were rejected:\n%s", strings.Join(got, "\n"))
	}
	artifacts["terraform"] = fullArtifact() + "\n" + `ODIGOS_IAC = "{\"v\":1,\"lang\":\"java\"}"`
	if got := CoverageProblems(artifacts, []Plan{p}); len(got) == 0 {
		t.Fatal("an escaped marker with the wrong value was accepted")
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

// ---------------------------------------------------------------------------------------------------------
// CoverageProblemsByLanguage
// ---------------------------------------------------------------------------------------------------------

// javaArtifact / pythonArtifact are the per-language shape the providers publish: each carries its OWN
// language's staging command and loader variable, and neither documents the other's.
func javaArtifact() string {
	return strings.Join([]string{
		`volume "odigos-agent" size 500Mi mounted at /var/odigos`,
		`container odigos-agent image odigos/agents:v1 probe 2400`,
		`command: sh -c "cp -r /instrumentations/java /var/odigos/java"`,
		`OTEL_SERVICE_NAME = "api"`,
		`JAVA_TOOL_OPTIONS: append "-javaagent:/var/odigos/java/javaagent.jar" to the current value`,
	}, "\n")
}

func pythonArtifact() string {
	return strings.Join([]string{
		`volume "odigos-agent" size 500Mi mounted at /var/odigos`,
		`container odigos-agent image odigos/agents:v1 probe 2400`,
		`command: sh -c "cp -r /instrumentations/python /var/odigos/python"`,
		`OTEL_SERVICE_NAME = "api"`,
		`PYTHONPATH: prepend "/var/odigos/python" to the current value`,
	}, "\n")
}

func perLanguageArtifacts() map[string]Artifact {
	return map[string]Artifact{
		"tf-java":   {Language: "java", Body: javaArtifact()},
		"tf-python": {Language: "python", Body: pythonArtifact()},
	}
}

// TestCoverageProblemsByLanguagePassesPerLanguageArtifacts is the whole point of grouping: a Java document
// that says nothing about Python is complete, where the flat check would demand Python's agent path in it.
func TestCoverageProblemsByLanguagePassesPerLanguageArtifacts(t *testing.T) {
	got := CoverageProblemsByLanguage(perLanguageArtifacts(), []Plan{javaPlan(), pythonPlan()})
	if len(got) != 0 {
		t.Fatalf("per-language artifacts reported problems:\n%s", strings.Join(got, "\n"))
	}
}

// TestCoverageProblemsByLanguageCatchesALanguageMissingItsOwnValue is the failure a flat check cannot see:
// python's document loses python's agent path, and the java document never mentioned it, so the union check
// would still find the literal nowhere — but only because the java plan does not need it. Grouped, the
// omission lands on the artifact a reader has to go fix.
func TestCoverageProblemsByLanguageCatchesALanguageMissingItsOwnValue(t *testing.T) {
	arts := perLanguageArtifacts()
	arts["tf-python"] = Artifact{Language: "python", Body: strings.ReplaceAll(pythonArtifact(), "/var/odigos/python", "/var/odigos/py")}
	got := CoverageProblemsByLanguage(arts, []Plan{javaPlan(), pythonPlan()})
	if len(got) == 0 {
		t.Fatal("a stale python agent path in python's own artifact was accepted")
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "python") {
		t.Errorf("the problem should name the language, got:\n%s", joined)
	}
	if strings.Contains(joined, "tf-java") {
		t.Errorf("java's artifact was blamed for python's omission:\n%s", joined)
	}
}

// A language with no artifact of its own and no language-neutral fallback is served nothing, so the customer
// gets prose for a language the connector can fully instrument.
func TestCoverageProblemsByLanguageCatchesALanguageWithNoArtifact(t *testing.T) {
	arts := map[string]Artifact{"tf-java": {Language: "java", Body: javaArtifact()}}
	got := CoverageProblemsByLanguage(arts, []Plan{javaPlan(), pythonPlan()})
	if len(got) == 0 {
		t.Fatal("a language with no artifact at all was accepted")
	}
	if !strings.Contains(strings.Join(got, "\n"), "python") {
		t.Errorf("the problem should name the unserved language, got:\n%s", strings.Join(got, "\n"))
	}
}

// TestCoverageProblemsByLanguageCatchesAnUnreachableLanguage: a misspelled language key fails no other
// check — the selection asks for "python", finds nothing, falls back to a neutral set that does not exist,
// and the customer gets prose while a correct template sits unused in the spec.
func TestCoverageProblemsByLanguageCatchesAnUnreachableLanguage(t *testing.T) {
	arts := perLanguageArtifacts()
	arts["tf-python"] = Artifact{Language: "py", Body: pythonArtifact()}
	got := CoverageProblemsByLanguage(arts, []Plan{javaPlan(), pythonPlan()})
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, `"py"`) {
		t.Errorf("an artifact tagged with a language no plan builds was not reported, got:\n%s", joined)
	}
}

// Language-neutral artifacts serve every language, which is what a type whose instrumentation does not depend
// on the language declares — so the fallback must be checked against every plan rather than skipped.
func TestCoverageProblemsByLanguageChecksTheNeutralFallbackAgainstEveryLanguage(t *testing.T) {
	arts := map[string]Artifact{"reference": {Body: fullArtifact()}}
	if got := CoverageProblemsByLanguage(arts, []Plan{javaPlan(), pythonPlan()}); len(got) != 0 {
		t.Fatalf("a complete language-neutral artifact reported problems:\n%s", strings.Join(got, "\n"))
	}

	arts["reference"] = Artifact{Body: javaArtifact()}
	if got := CoverageProblemsByLanguage(arts, []Plan{javaPlan(), pythonPlan()}); len(got) == 0 {
		t.Fatal("a language-neutral artifact that only documents java was accepted for both languages")
	}
}

// ---------------------------------------------------------------------------------------------------------
// MarkerAttachmentProblems
// ---------------------------------------------------------------------------------------------------------

const (
	testMarkerPayload  = `{"v":1,"lang":"java"}`
	testWantMarkerVar  = "ODIGOS_IAC"
	testOtherMarkerVar = "ODIGOS_MANAGED"
)

func markerArtifact(varName string) string {
	return strings.Join([]string{
		"env:",
		"  - name: " + varName,
		"    value: '" + testMarkerPayload + "'",
		"",
		"NOTES",
		"- Set ODIGOS_IAC exactly as shown. Do NOT set ODIGOS_MANAGED.",
	}, "\n")
}

func TestMarkerAttachmentProblemsPassesTheRightVariable(t *testing.T) {
	arts := map[string]Artifact{"az-cli": {Language: "java", Body: markerArtifact(testWantMarkerVar)}}
	if got := MarkerAttachmentProblems(arts, testMarkerPayload, testWantMarkerVar, testOtherMarkerVar); len(got) != 0 {
		t.Fatalf("a correctly wired marker reported problems:\n%s", strings.Join(got, "\n"))
	}
}

// TestMarkerAttachmentProblemsCatchesTheWrongVariable is the exact failure this guard was added for: both
// variable names appear in the document, so every substring check passes, and the snippet a customer copies
// still writes the ownership marker.
func TestMarkerAttachmentProblemsCatchesTheWrongVariable(t *testing.T) {
	body := markerArtifact(testOtherMarkerVar)
	// Prove the substring sweep cannot see it: the wanted variable IS present, in the notes.
	if !strings.Contains(body, testWantMarkerVar) {
		t.Fatal("fixture is wrong: the wanted variable must be present so a literal check would pass")
	}
	arts := map[string]Artifact{"az-cli": {Language: "java", Body: body}}
	got := MarkerAttachmentProblems(arts, testMarkerPayload, testWantMarkerVar, testOtherMarkerVar)
	if len(got) == 0 {
		t.Fatal("a payload wired to the ownership marker was accepted")
	}
	if !strings.Contains(got[0], "az-cli") || !strings.Contains(got[0], testOtherMarkerVar) {
		t.Errorf("the problem should name the artifact and the wrong variable, got: %s", got[0])
	}
}

// An escaped payload (HCL, a shell heredoc) has to be recognized too, or the guard silently skips exactly the
// artifacts whose syntax makes the mistake hardest to spot.
func TestMarkerAttachmentProblemsSeesTheEscapedPayload(t *testing.T) {
	escaped := strings.ReplaceAll(testMarkerPayload, `"`, `\"`)
	body := "{ name = \"" + testOtherMarkerVar + "\", value = \"" + escaped + "\" },\n# Set ODIGOS_IAC, not this one."
	arts := map[string]Artifact{"terraform": {Language: "java", Body: body}}
	if got := MarkerAttachmentProblems(arts, testMarkerPayload, testWantMarkerVar, testOtherMarkerVar); len(got) == 0 {
		t.Fatal("an escaped payload wired to the ownership marker was accepted")
	}
}

// A payload sitting next to neither variable names no variable at all, which is its own bug: the customer is
// shown a value with nowhere to put it.
func TestMarkerAttachmentProblemsCatchesAnUnattachedPayload(t *testing.T) {
	arts := map[string]Artifact{"reference": {Language: "java", Body: "the marker value is " + testMarkerPayload}}
	if got := MarkerAttachmentProblems(arts, testMarkerPayload, testWantMarkerVar, testOtherMarkerVar); len(got) == 0 {
		t.Fatal("a payload with no variable beside it was accepted")
	}
}
