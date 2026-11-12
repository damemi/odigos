package plan

import (
	"fmt"
	"sort"
	"strings"
)

// Step is one thing a customer does, as prose. Anchor says where, Instruction says what, and Detail carries
// the values they need verbatim.
//
// Steps are the base's ONLY complete renderer, and deliberately so. Per-variant artifacts (Terraform,
// CloudFormation, Bicep, CLI) are syntax the four platforms genuinely do not share, so they are declared as
// templates in each provider's spec YAML. Prose is the one rendering that is identical across providers, is
// the one every provider can always offer, and is the fallback a customer on an unsupported toolchain
// still gets — so it is the one the base owns outright rather than leaving to per-provider copy.
type Step struct {
	Anchor      string
	Instruction string
	Detail      []string
}

// Steps renders the plan as ordered instructions. It sorts a copy of the fragments, so calling it does not
// reorder the caller's plan.
func Steps(p Plan) []Step {
	sorted := Plan{Type: p.Type, Language: p.Language, Mode: p.Mode, Activation: p.Activation, Notes: p.Notes, Placeholders: p.Placeholders}
	sorted.Fragments = append([]Fragment(nil), p.Fragments...)
	sorted.Sort()

	out := make([]Step, 0, len(sorted.Fragments))
	for _, f := range sorted.Fragments {
		if s, ok := stepFor(f); ok {
			out = append(out, s)
		}
	}
	return out
}

func stepFor(f Fragment) (Step, bool) {
	s := Step{Anchor: f.Anchor.String()}
	switch f.Kind {
	case KindEnv:
		// Named by anchor, not assumed: a Lambda function's environment is the function's own, and telling a
		// customer to configure "the application container" of a function is an instruction with no referent.
		switch {
		case f.Anchor.Field == FieldProperties:
			s.Instruction = "Set these application properties on the application."
		case f.Anchor.Scope == ScopeContainer:
			s.Instruction = fmt.Sprintf("Configure these environment variables on container %q.", f.Anchor.Container)
		default:
			s.Instruction = "Configure these environment variables on the workload itself."
		}
		for _, e := range f.Env {
			s.Detail = append(s.Detail, envLine(e))
		}
	case KindVolume:
		if f.Volume == nil {
			return Step{}, false
		}
		s.Instruction = fmt.Sprintf("Declare a shared %s volume named %q for the agent files.", mediumWord(f.Volume.Medium), f.Volume.Name)
		if f.Volume.SizeLimit != "" {
			s.Detail = append(s.Detail, "size limit: "+f.Volume.SizeLimit)
		}
	case KindVolumeMount:
		if f.Mount == nil {
			return Step{}, false
		}
		access := "read-write"
		if f.Mount.ReadOnly {
			access = "read-only"
		}
		s.Instruction = fmt.Sprintf("Mount volume %q at %s (%s).", f.Mount.Volume, f.Mount.Path, access)
	case KindHelperContainer:
		if f.Helper == nil {
			return Step{}, false
		}
		// "run-to-completion" / "long-running" rather than "init container" / "sidecar": the three staged-agent
		// platforms spell the same two shapes differently (Container Apps initContainers, an ECS non-essential
		// container in containerDefinitions, a Cloud Run container that cannot exit at all), so naming the
		// behavior is accurate everywhere while naming the construct is accurate on one platform and
		// misleading on the others.
		role := "long-running agent-staging container"
		if f.Helper.Init {
			role = "run-to-completion agent-staging container"
		}
		s.Instruction = fmt.Sprintf("Add the %s %q.", role, f.Helper.Name)
		s.Detail = append(s.Detail,
			"image: "+f.Helper.Image,
			"command: "+strings.Join(f.Helper.Command, " "),
			fmt.Sprintf("mounts %q at %s", f.Helper.Mount.Volume, f.Helper.Mount.Path),
		)
		if f.Helper.StartupProbePort != 0 {
			s.Detail = append(s.Detail, fmt.Sprintf("TCP startup probe on port %d — it is what tells the platform staging has finished", f.Helper.StartupProbePort))
		}
	case KindStartupDependency:
		if f.StartupDependency == nil {
			return Step{}, false
		}
		s.Instruction = fmt.Sprintf("Make this container start only after %q reports %s, so the agent files exist before the application loads them.",
			f.StartupDependency.On, f.StartupDependency.Condition)
	case KindLayer:
		if f.Layer == nil {
			return Step{}, false
		}
		s.Instruction = "Attach the OpenTelemetry Lambda layer, keeping any layers the function already has."
		s.Detail = append(s.Detail, "layer: "+layerValue(*f.Layer))
	default:
		return Step{}, false
	}
	return s, true
}

// envLine is where the merge direction becomes words. "append X to Y" and "set Y to X" are different
// instructions with different consequences, and this is the only place a customer learns which one applies.
func envLine(e EnvVar) string {
	switch e.Merge {
	case MergeAppend:
		return fmt.Sprintf("%s: append %q to the existing value, separated by %q (keep the current value first)", e.Name, e.Value, e.Separator)
	case MergePrepend:
		return fmt.Sprintf("%s: prepend %q to the existing value, separated by %q (ours must come first)", e.Name, e.Value, e.Separator)
	case MergeRemove:
		return fmt.Sprintf("%s: remove it if your definition sets it — Odigos compares it against unset, so a leftover value is reported as drift", e.Name)
	default:
		return fmt.Sprintf("%s: set to %q", e.Name, e.Value)
	}
}

func mediumWord(medium string) string {
	switch medium {
	case "memory":
		return "in-memory"
	case "ephemeral":
		return "ephemeral"
	default:
		return "shared"
	}
}

func layerValue(l Layer) string {
	if l.ARN != "" {
		return l.ARN
	}
	return l.Placeholder
}

// Prose renders the plan as the body of a `manual` delivery variant: a numbered instruction list with the
// values inline. It is the checked-in content of each provider's manual artifact, and a provider test
// asserts the YAML still matches — so a change to an agent path, a mount, or a merge direction fails a test
// instead of silently leaving a customer following instructions for a previous release.
func Prose(p Plan) string {
	var b strings.Builder
	for i, s := range Steps(p) {
		fmt.Fprintf(&b, "%d. %s\n", i+1, s.Instruction)
		fmt.Fprintf(&b, "   at: %s\n", s.Anchor)
		for _, d := range s.Detail {
			fmt.Fprintf(&b, "   - %s\n", d)
		}
	}
	for _, n := range p.Notes {
		fmt.Fprintf(&b, "\nNote: %s\n", n)
	}
	return strings.TrimRight(b.String(), "\n")
}

// RequiredLiterals is every string a rendering of this plan MUST contain verbatim, whatever its syntax:
// environment variable names and values, the volume and helper container names, the agent image, the mount
// path, the staging command, the layer, and the separators that carry the merge semantics.
//
// It exists to make one test cover all thirteen (type, variant) artifact templates. The templates are
// hand-authored per platform — there is no way around that, since HCL, CloudFormation YAML, ARM JSON and
// three CLIs share no syntax — and hand-authored copy-paste instructions are exactly the thing that rots
// silently: the injector changes an agent directory, every test still passes, and customers keep applying
// last release's path. Asserting the templates contain what the plan means catches that without pretending
// the base can generate four platforms' syntax.
//
// It is deliberately a required-substring set, not an exact rendering: a template legitimately carries
// surrounding structure the plan knows nothing about, and asserting equality would make every artifact
// reword a test failure.
func RequiredLiterals(p Plan) []string {
	set := map[string]bool{}
	for _, f := range p.Fragments {
		for _, lit := range fragmentLiterals(f) {
			if lit != "" {
				set[lit] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// fragmentLiterals is every string a rendering of one fragment must carry verbatim. It is shared by
// RequiredLiterals and by Validate's placeholder check, so "what the artifact must contain" and "what is
// checked for a malformed placeholder" are the same list by construction rather than by two authors
// remembering to update both.
func fragmentLiterals(f Fragment) []string {
	switch f.Kind {
	case KindEnv:
		out := make([]string, 0, 2*len(f.Env))
		for _, e := range f.Env {
			out = append(out, e.Name, e.Value)
		}
		return out
	case KindVolume:
		if f.Volume == nil {
			return nil
		}
		return []string{f.Volume.Name, f.Volume.SizeLimit}
	case KindVolumeMount:
		if f.Mount == nil {
			return nil
		}
		return []string{f.Mount.Volume, f.Mount.Path}
	case KindHelperContainer:
		if f.Helper == nil {
			return nil
		}
		out := []string{f.Helper.Name, f.Helper.Image, f.Helper.Mount.Path}
		out = append(out, f.Helper.Command...)
		if f.Helper.StartupProbePort != 0 {
			out = append(out, fmt.Sprint(f.Helper.StartupProbePort))
		}
		return out
	case KindStartupDependency:
		if f.StartupDependency == nil {
			return nil
		}
		return []string{f.StartupDependency.On}
	case KindLayer:
		if f.Layer == nil {
			return nil
		}
		return []string{layerValue(*f.Layer)}
	}
	return nil
}

// LoaderVars are the plan's append/prepend environment variables — the ones whose rendering is a two-sided
// instruction rather than a value.
//
// A provider test uses it to assert the artifact templates say which side our value goes on. That check
// cannot be folded into RequiredLiterals: the literal being present is exactly what a template that
// hardcodes `JAVA_TOOL_OPTIONS = "-javaagent:/var/odigos/java/javaagent.jar"` already satisfies, and that
// template is the bug — it silently drops whatever the customer had set. The direction is the part a
// substring check cannot see.
func LoaderVars(p Plan) []EnvVar {
	var out []EnvVar
	for _, f := range p.Fragments {
		if f.Kind != KindEnv {
			continue
		}
		for _, e := range f.Env {
			if e.Merge == MergeAppend || e.Merge == MergePrepend {
				out = append(out, e)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
