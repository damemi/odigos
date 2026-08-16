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
