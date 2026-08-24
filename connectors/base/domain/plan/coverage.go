package plan

import (
	"fmt"
	"sort"
	"strings"
)

// CoverageProblems reports every way a provider's rendered artifact templates fail to say what its plans
// mean. It is the anti-drift guard Wave 5 turns on, and the reason it lives in the base rather than being
// written three times: the check is identical for four platforms, and a provider that wrote its own would
// write the weaker version of it.
//
// The problem it solves is specific. Terraform HCL, CloudFormation YAML, ARM JSON and three CLIs share no
// syntax, so the artifacts a customer copies have to be hand-authored per platform — there is no honest way
// for the base to generate them. Hand-authored copy-paste instructions are then exactly the thing that rots
// invisibly: the injector moves an agent directory or flips a loader variable's merge direction, every
// existing test still passes, and customers keep applying the previous release's configuration. In automatic
// mode that would self-correct on the next reconcile; in manual mode nothing converges it, so the customer
// gets a Partial conformance verdict with no way to tell which side is stale.
//
// Two checks, because the failures are different:
//
//   - Every literal ANY plan requires must appear in at least one artifact. This is what catches a language
//     whose recipe changed, or one nobody documented.
//   - Every literal EVERY plan requires — the language-independent core: the volume, the mount path, the
//     marker key, the export variables — must appear in EVERY artifact. This is what catches a variant that
//     was added later and quietly omits a step, which a union-only check would let through as long as some
//     other variant mentioned it.
//
// It also checks the one thing a substring search cannot see: an artifact naming a loader variable has to
// say which side our value goes on. A template that hardcodes JAVA_TOOL_OPTIONS to the agent option
// satisfies every literal check and is still the bug — it discards whatever the customer had set.
//
// artifacts is keyed by artifact id, so a problem names the artifact a reader has to go fix.
//
// It takes ONE language's artifacts and that language's plans. Use CoverageProblemsByLanguage for a provider's
// whole published set: the instructions are authored per language, and checking a Java template against a
// Python plan's literals would demand every language's values in every document — which is the shape this
// feature moved away from.
func CoverageProblems(artifacts map[string]string, plans []Plan) []string {
	if len(plans) == 0 {
		return []string{"no plans supplied, so the artifacts are trivially covered"}
	}
	if len(artifacts) == 0 {
		return []string{"no artifacts supplied, so nothing publishes these plans to a customer"}
	}

	ids := make([]string, 0, len(artifacts))
	for id := range artifacts {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	// needBy: literal -> the plans that need it, by Key so a failure names the language AND the activation.
	// A type with two activations per language (Lambda) would otherwise report "java" for a literal only one
	// of the two java plans needs, and send a reader to the wrong artifact.
	needBy := map[string][]string{}
	for _, p := range plans {
		for _, lit := range RequiredLiterals(p) {
			needBy[lit] = append(needBy[lit], p.Key())
		}
	}

	var problems []string
	lits := make([]string, 0, len(needBy))
	for lit := range needBy {
		lits = append(lits, lit)
	}
	sort.Strings(lits)

	for _, lit := range lits {
		needers := needBy[lit]
		// Common = every plan needs it. RequiredLiterals dedupes within a plan, so the count is the number of
		// plans and not an artifact of one plan mentioning a value twice.
		common := len(needers) == len(plans)

		var missing []string
		for _, id := range ids {
			if !strings.Contains(artifacts[id], lit) {
				missing = append(missing, id)
			}
		}
		switch {
		case len(missing) == len(ids):
			problems = append(problems, fmt.Sprintf("no artifact carries %q (needed by %s)", lit, describePlans(needers)))
		case common && len(missing) > 0:
			problems = append(problems, fmt.Sprintf("every plan needs %q but these artifacts omit it: %s",
				lit, strings.Join(missing, ", ")))
		}
	}

	problems = append(problems, loaderDirectionProblems(artifacts, ids, plans)...)
	return problems
}

// loaderDirectionProblems checks that some artifact naming each loader variable also states the direction.
// Kept separate from the literal sweep because it is asserting the presence of PROSE, not of a value: the
// value being present is precisely what the buggy hardcoded rendering also achieves.
func loaderDirectionProblems(artifacts map[string]string, ids []string, plans []Plan) []string {
	// One entry per (variable, direction) pair across all languages. The same variable can legitimately take
	// different directions on different platforms (PYTHONPATH is prepended on Lambda, appended on the
	// staged-agent platforms), so the pair is the unit.
	type loader struct{ name, verb, lang string }
	var want []loader
	seen := map[string]bool{}
	for _, p := range plans {
		for _, e := range LoaderVars(p) {
			verb := "append"
			if e.Merge == MergePrepend {
				verb = "prepend"
			}
			k := e.Name + "/" + verb
			if seen[k] {
				continue
			}
			seen[k] = true
			want = append(want, loader{name: e.Name, verb: verb, lang: p.Key()})
		}
	}
	sort.Slice(want, func(i, j int) bool { return want[i].name+want[i].verb < want[j].name+want[j].verb })

	var problems []string
	for _, l := range want {
		stated := false
		for _, id := range ids {
			if statesDirection(artifacts[id], l.name, l.verb) {
				stated = true
				break
			}
		}
		if !stated {
			problems = append(problems, fmt.Sprintf("no artifact says that %s must be %sed to the value the customer already has (%s); a template that just assigns it discards their value",
				l.name, l.verb, l.lang))
		}
	}
	return problems
}

// statesDirection reports whether the artifact says the direction AT the variable — on the line naming it, or
// on the line immediately above or below.
//
// Proximity is the whole point. Searching the document as a whole passes on any artifact that happens to use
// the word "append" anywhere — a note about a different variable, a Terraform function name — while the line
// that actually assigns the loader variable hardcodes it. That is precisely the bug this check exists for, so a
// document-wide search would report it as fine.
func statesDirection(body, name, verb string) bool {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if !strings.Contains(line, name) {
			continue
		}
		// The comment explaining the merge conventionally sits directly above the assignment, and a trailing
		// parenthetical directly below, so the window is one line either side and no wider.
		for j := max(0, i-1); j <= min(len(lines)-1, i+1); j++ {
			if strings.Contains(strings.ToLower(lines[j]), verb) {
				return true
			}
		}
	}
	return false
}

func describePlans(keys []string) string {
	uniq := map[string]bool{}
	var out []string
	for _, k := range keys {
		if !uniq[k] {
			uniq[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// Artifact is one published instruction template plus the language it serves. Language is "" for a
// language-neutral artifact, matching spec.SetupArtifact.Language.
type Artifact struct {
	Language string
	Body     string
}

// CoverageProblemsByLanguage is CoverageProblems over a provider's WHOLE published set for one instrumentation
// type: it groups the plans and the artifacts by language and checks each language against its own artifacts.
//
// Grouping is what makes the guard mean anything now that the instructions are per language. A Ruby Fargate
// task's Terraform must carry Ruby's agent path and RUBYOPT; demanding that it also carry .NET's profiler
// GUID — which a single flat check does, because some plan needs that literal — is what forced every artifact
// to document every language in the first place. Conversely a flat check cannot notice that Ruby's artifact
// omits Ruby's own values, as long as some other language's document happens to mention them.
//
// The grouping mirrors spec.InstrumentationArtifactsFor exactly, including the fallback to the
// language-neutral artifacts, because the point is to check what a customer is actually SERVED. A guard that
// checked a different selection than the connector serves would pass on instructions nobody sees and fail on
// ones nobody gets.
func CoverageProblemsByLanguage(artifacts map[string]Artifact, plans []Plan) []string {
	if len(plans) == 0 {
		return []string{"no plans supplied, so the artifacts are trivially covered"}
	}
	if len(artifacts) == 0 {
		return []string{"no artifacts supplied, so nothing publishes these plans to a customer"}
	}

	plansByLang := map[string][]Plan{}
	for _, p := range plans {
		plansByLang[p.Language] = append(plansByLang[p.Language], p)
	}

	var problems []string
	for _, lang := range sortedKeys(plansByLang) {
		bodies := bodiesForLanguage(artifacts, lang)
		if len(bodies) == 0 {
			problems = append(problems, fmt.Sprintf(
				"no artifact serves language %s, so a customer running it falls back to the base's prose rendering and is never offered a snippet in their own toolchain",
				describeLanguage(lang)))
			continue
		}
		for _, prob := range CoverageProblems(bodies, plansByLang[lang]) {
			problems = append(problems, describeLanguage(lang)+": "+prob)
		}
	}

	// An artifact tagged with a language no plan builds is unreachable: the selection asks for the plan's
	// language, finds nothing, and falls back to the neutral set — so a customer gets prose while a perfectly
	// good template sits in the spec. A misspelled language key fails no other check.
	for _, id := range sortedKeys(artifacts) {
		lang := artifacts[id].Language
		if lang == "" {
			continue
		}
		if _, ok := plansByLang[lang]; !ok {
			problems = append(problems, fmt.Sprintf(
				"artifact %s declares language %q, which no plan builds, so nothing ever serves it (misspelled language key?)", id, lang))
		}
	}
	return problems
}

// bodiesForLanguage selects the artifact bodies served for one language: the ones declaring it, or the
// language-neutral ones when it declares none.
func bodiesForLanguage(artifacts map[string]Artifact, lang string) map[string]string {
	pick := func(want string) map[string]string {
		out := map[string]string{}
		for id, a := range artifacts {
			if a.Language == want {
				out[id] = a.Body
			}
		}
		return out
	}
	if lang != "" {
		if scoped := pick(lang); len(scoped) > 0 {
			return scoped
		}
	}
	return pick("")
}

func describeLanguage(lang string) string {
	if lang == "" {
		return "(language-independent)"
	}
	return lang
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// MarkerAttachmentProblems reports artifacts that attach a marker PAYLOAD to the wrong variable.
//
// It exists because the literal sweep in CoverageProblems cannot see this failure, and the failure is a real
// one that happened: an artifact's snippet still said
//
//   - name: ODIGOS_MANAGED
//     value: '{"v":1,"lang":"java"}'
//
// while its notes said "set ODIGOS_IAC". Both variable names were present in the document, so every substring
// check passed — and a customer copying the snippet writes the ownership marker, which claims a rollback
// snapshot that was never taken and, on the staged-agent platforms, is also what authorizes Odigos to mutate
// their workload. The two markers are not interchangeable (see api.EnvOdigosIaC), so which one the payload is
// wired to is the whole meaning of the fragment.
//
// Proximity, for the same reason statesDirection uses it: the assignment and the variable name are adjacent in
// every syntax these artifacts are written in (same line in HCL and Bicep, the line below in a YAML `- name:`
// pair), while a prose mention of either variable is paragraphs away from any payload.
func MarkerAttachmentProblems(artifacts map[string]Artifact, payload, wantVar, forbidVar string) []string {
	if payload == "" {
		return []string{"no marker payload supplied, so nothing was checked"}
	}
	var problems []string
	for _, id := range sortedKeys(artifacts) {
		body := artifacts[id].Body
		hits := payloadLines(body, payload)
		if len(hits) == 0 {
			// Whether the payload has to be present at all is CoverageProblems' question, not this one.
			continue
		}
		for _, i := range hits {
			if nearLine(body, i, forbidVar) {
				problems = append(problems, fmt.Sprintf(
					"artifact %s attaches the marker payload to %s; it belongs to %s, and a customer copying this writes the wrong marker",
					id, forbidVar, wantVar))
				break
			}
			if !nearLine(body, i, wantVar) {
				problems = append(problems, fmt.Sprintf(
					"artifact %s carries the marker payload with no %s beside it, so nothing says which variable it goes in", id, wantVar))
				break
			}
		}
	}
	return problems
}

// payloadLines returns the indexes of the lines carrying the payload, in either its bare or its
// backslash-escaped form — HCL and shell heredocs carry the escaped one, YAML and Bicep the bare one.
func payloadLines(body, payload string) []int {
	escaped := strings.ReplaceAll(payload, `"`, `\"`)
	var out []int
	for i, line := range strings.Split(body, "\n") {
		if strings.Contains(line, payload) || strings.Contains(line, escaped) {
			out = append(out, i)
		}
	}
	return out
}

// nearLine reports whether needle appears on line i or immediately either side of it.
func nearLine(body string, i int, needle string) bool {
	lines := strings.Split(body, "\n")
	for j := max(0, i-1); j <= min(len(lines)-1, i+1); j++ {
		if strings.Contains(lines[j], needle) {
			return true
		}
	}
	return false
}
