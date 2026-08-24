package instrumentor

import (
	"fmt"
	"sort"
	"strings"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/plan"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
)

// ECS Fargate instrumentation plan: anchored fragments a customer can put in their Terraform / CloudFormation
// task definition. Built from the same recipes as the injector (OSS community distros) so upstream activation
// changes fail coverage tests instead of shipping broken instructions. Workload-specific values are placeholders.

// planRecipes is a separate lazy loader from the instrumentor's so plan builds in tests are not coupled to
// whether an instrumentor has already run (the cache is process-wide behind sync.Once).
var planRecipes recipes

// DesiredFargatePlan returns the instrumentation plan for one Fargate language. image is the
// odigos-fargate-agents image the staging container copies from (config.FargateAgentsImage).
//
// runtimeVersion is only consulted for the version-templated agents (php, ruby); pass
// plan.PlaceholderRuntimeVersion to build the account-wide plan that names it as a value to substitute.
func DesiredFargatePlan(lang, image, runtimeVersion string) (plan.Plan, error) {
	if image == "" {
		return plan.Plan{}, fmt.Errorf("fargate plan for %q needs the odigos-fargate-agents image (config.FargateAgentsImage)", lang)
	}
	recipe, err := planRecipes.recipeFor(lang, runtimeVersion)
	if err != nil {
		return plan.Plan{}, err
	}

	p := plan.Plan{
		Type:     "aws.fargate-task",
		Language: lang,
		Mode:     plan.ModeStagedAgent,
		Fragments: []plan.Fragment{
			{
				Anchor: plan.Anchor{Scope: plan.ScopeWorkload, Field: plan.FieldVolumes},
				Kind:   plan.KindVolume,
				// Bare-name task volume: Fargate ephemeral storage shared across the task (no tmpfs option).
				Volume: &plan.Volume{Name: agentVolumeName, Medium: "ephemeral"},
			},
			{
				Anchor: plan.Anchor{Scope: plan.ScopeWorkload, Field: plan.FieldContainers},
				Kind:   plan.KindHelperContainer,
				Helper: &plan.HelperContainer{
					Name:    sidecarContainerName,
					Image:   image,
					Command: sidecarCommand(recipe.agentDirs),
					Mount:   plan.Mount{Volume: agentVolumeName, Path: agentsDir},
					// Run-to-completion stager (non-essential); app depends on SUCCESS. Still a normal
					// containerDefinition — ECS has no separate init list.
					Init: true,
				},
			},
			{
				Anchor: plan.Anchor{Scope: plan.ScopeContainer, Field: plan.FieldVolumeMounts, Container: plan.PlaceholderContainer},
				Kind:   plan.KindVolumeMount,
				// Agents are read-only in the app container; the stager owns writes.
				Mount: &plan.Mount{Volume: agentVolumeName, Path: agentsDir, ReadOnly: true},
			},
			{
				Anchor: plan.Anchor{Scope: plan.ScopeContainer, Field: plan.FieldDependsOn, Container: plan.PlaceholderContainer},
				Kind:   plan.KindStartupDependency,
				// SUCCESS is only valid against a non-essential container — same decision as Init above.
				StartupDependency: &plan.StartupDependency{On: sidecarContainerName, Condition: plan.ConditionSuccess},
			},
			{
				Anchor: plan.Anchor{Scope: plan.ScopeContainer, Field: plan.FieldEnv, Container: plan.PlaceholderContainer},
				Kind:   plan.KindEnv,
				Env:    desiredFargateEnv(recipe),
			},
		},
		Notes: fargatePlanNotes(recipe),
	}
	// From the plan contents, not a hardcoded list of version-templated languages (that list lives upstream).
	p.Placeholders = fargatePlaceholders(p)
	p.Sort()
	if err := p.Validate(); err != nil {
		return plan.Plan{}, err
	}
	return p, nil
}

// DesiredFargatePlans is every coverable language's plan, ordered by language so a rendering is stable. The
// version-templated agents (php, ruby) are built against plan.PlaceholderRuntimeVersion.
func DesiredFargatePlans(image string) ([]plan.Plan, error) {
	langs := []string{LangJava, LangPython, LangNodeJS, LangDotNet, LangPHP, LangRuby}
	sort.Strings(langs)

	out := make([]plan.Plan, 0, len(langs))
	for _, l := range langs {
		p, err := DesiredFargatePlan(l, image, plan.PlaceholderRuntimeVersion)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// desiredFargateEnv mirrors instrumentContainer's env, with per-workload values as placeholders.
// Every entry has a counterpart in containerDiff so the plan matches what the observer judges.
func desiredFargateEnv(recipe agentRecipe) []plan.EnvVar {
	env := []plan.EnvVar{
		// Attribution marker first: without it the container reads as untouched. IaC (not ownership) —
		// manual mode has no rollback snapshot to author; see api.EnvOdigosIaC.
		{Name: store.EnvOdigosIaC, Value: fargateIaCMarkerValue(recipe.lang), Merge: plan.MergeSet},
		// Remove leftover ownership markers from earlier automatic runs (or hand-authored fakes).
		{Name: store.EnvOdigosManaged, Merge: plan.MergeRemove},

		{Name: EnvServiceName, Value: plan.PlaceholderServiceName, Merge: plan.MergeSet},
		{Name: envResourceAttributes, Value: ecsResourceAttributes(plan.PlaceholderRegion), Merge: plan.MergeSet},
		// Endpoint + protocol are one decision; mismatched pairs fail as opaque connection resets.
		{Name: envOTLPEndpoint, Value: plan.PlaceholderOTLPEndpointURL, Merge: plan.MergeSet},
		{Name: EnvOtlpProtocol, Value: recipe.otlpProtocol, Merge: plan.MergeSet},
		{Name: EnvTracesExporter, Value: exporterOTLP, Merge: plan.MergeSet},
		{Name: EnvMetricsExporter, Value: exporterOTLP, Merge: plan.MergeSet},
		{Name: EnvLogsExporter, Value: exporterOTLP, Merge: plan.MergeSet},
		// Explicit removal: containerDiff expects this gone unless debug-telemetry is on.
		{Name: EnvOtelLogLevel, Merge: plan.MergeRemove},
	}
	for k, v := range recipe.staticEnv {
		env = append(env, plan.EnvVar{Name: k, Value: v, Merge: plan.MergeSet})
	}
	for _, a := range recipe.appendEnv {
		// evaluate("") → our fragment alone (pattern strips the leading delimiter); MergeAppend keeps theirs.
		// Separator comes from the pattern (space for JAVA_TOOL_OPTIONS/NODE_OPTIONS, colon for PYTHONPATH).
		env = append(env, plan.EnvVar{
			Name:      a.name,
			Value:     a.evaluate(""),
			Merge:     plan.MergeAppend,
			Separator: a.separator(),
		})
	}
	return env
}

// fargateIaCMarkerValue is the manual-mode attribution marker (api.ParseIaCMarker wire format).
// Records schema version and language only — language is what the observer reconstructs activation against.
// source/version are for IaC modules; customers pasting by hand leave them off. No prevEnv: manual mode has
// no rollback snapshot, and loaderActivated checks fragment presence/position instead.
func fargateIaCMarkerValue(lang string) string {
	return fmt.Sprintf(`{"v":%d,"lang":%q}`, baseapi.IaCMarkerVersion, lang)
}

// loaderMergeNote tells the customer how to join our fragment to an existing loader value, and what is checked
// (presence + position, not exact match). Empty for php/ruby — they activate via static vars, nothing to merge.
func loaderMergeNote(recipe agentRecipe) string {
	if len(recipe.appendEnv) == 0 {
		return ""
	}
	names := make([]string, 0, len(recipe.appendEnv))
	for _, a := range recipe.appendEnv {
		names = append(names, a.name)
	}
	sort.Strings(names)
	return fmt.Sprintf("If your container ALREADY sets %s, keep its value and put the Odigos value AFTER it, joined by the separator shown — never assign ours over it. Odigos checks that its value is present and comes last, so whatever your application needs in front of it is yours to keep and is not reported as drift.",
		strings.Join(names, " / "))
}

// fargatePlaceholders is what this plan asks the customer to substitute.
// Runtime version only when the plan actually carries the token (otherwise nothing would substitute it).
func fargatePlaceholders(p plan.Plan) []plan.Placeholder {
	out := []plan.Placeholder{
		plan.ContainerPlaceholder,
		plan.ServiceNamePlaceholder,
		plan.RegionPlaceholder,
		plan.OTLPEndpointURLPlaceholder,
	}
	if plan.UsesPlaceholder(p, plan.PlaceholderRuntimeVersion) {
		out = append(out, plan.RuntimeVersionPlaceholder)
	}
	return out
}

// replaceableStaticEnv names static activation vars the instrumentor overwrites exactly (PHP_INI_SCAN_DIR,
// RUBYOPT). The plan must say replace, not merge — merging would be reported as drift.
func replaceableStaticEnv(recipe agentRecipe) []string {
	var out []string
	for _, k := range []string{"PHP_INI_SCAN_DIR", "RUBYOPT"} {
		if _, ok := recipe.staticEnv[k]; ok {
			out = append(out, k)
		}
	}
	return out
}

func fargatePlanNotes(recipe agentRecipe) []string {
	notes := []string{
		fmt.Sprintf("Set %s on each container you are instrumenting, exactly as shown including the language. Odigos attributes the container to this variable alone, per container: without it the container is reported as awaiting your deployment however completely the rest is applied, and the recorded language is the agent its activation environment is checked against.",
			store.EnvOdigosIaC),
		fmt.Sprintf("Do NOT set %s. That variable is the connector's own record of a mutation it performed, including the snapshot it restores from on rollback, and it is written only when you hand instrumentation over to Odigos by enabling the instrumentation capability for this type. Setting it by hand claims a rollback that was never recorded, and Odigos would then judge the container against values nobody has.",
			store.EnvOdigosManaged),
		fmt.Sprintf("%s must match the workload's name in Odigos. A different value is reported as instrumentation drift, not as a naming preference.", EnvServiceName),
		fmt.Sprintf("Set %s / %s / %s to \"%s\" for any signal your destination does not accept, so the agent never produces it.",
			EnvTracesExporter, EnvMetricsExporter, EnvLogsExporter, exporterNone),
		fmt.Sprintf("%s and %s are one decision and are both compared exactly, against the transport Odigos resolved for the destination on this connector — not against the value shown here in isolation. This agent prefers %s, and that is what the plan renders; if the destination offers only the other transport, Odigos resolves that one instead and you must apply its endpoint and protocol. Use %s for gRPC and %s for http/protobuf, and never pair an endpoint with the other protocol: it fails at runtime as a connection reset that points nowhere near the cause.",
			envOTLPEndpoint, EnvOtlpProtocol, recipe.otlpProtocol, otlpDefaultGRPCPort, otlpDefaultHTTPPort),
		fmt.Sprintf("Declare %q as NON-essential. It stages the agent and exits 0, and ECS only accepts a %s container dependency against a non-essential container — the two settings are one decision.",
			sidecarContainerName, plan.ConditionSuccess),
		fmt.Sprintf("A task definition may hold several applications in different languages. Apply the container-scoped fragments to each one separately, and MERGE the staging container's command: there is one %q for the whole task definition, and its command is compared against the union of the agents every marked container needs, in ascending order of the agent directory name. Chain a `&& cp -r /instrumentations/<lang> /var/odigos/<lang>` clause per language — a single-language command in a two-language task is reported as drift and leaves the other container with no agent staged.", sidecarContainerName),
		// Revision is inert until the service rolls onto it.
		"Registering the revision is not enough: update the service to it so the new revision actually runs.",
		fmt.Sprintf("AWS caps a task definition at %d containers. A definition already at the limit has no room for the staging container.", maxTaskDefContainers),
		"The agent image is pulled by your own task's execution role. If it cannot pull it, mirror the image into a registry the task can reach and use that reference.",
		fmt.Sprintf("The Odigos Fargate agents bundle is Linux-only and %s-based; a Windows task definition cannot be instrumented at all.", fargateLibcType),
	}
	if n := loaderMergeNote(recipe); n != "" {
		notes = append(notes, n)
	}
	// Exact replace (same as the instrumentor) for static activation vars apps may already set.
	if replaced := replaceableStaticEnv(recipe); len(replaced) > 0 {
		notes = append(notes, fmt.Sprintf("%s is set to the value shown, replacing anything already there. If your application relies on its own value, fold it into the value above before applying — Odigos compares this variable exactly, so it cannot merge the two for you.",
			strings.Join(replaced, " / ")))
	}
	if recipe.lang == LangPHP || recipe.lang == LangRuby {
		notes = append(notes, fmt.Sprintf("The %s agent's paths are per runtime version: replace %s above with your application's major.minor (for example 8.3).",
			recipe.lang, plan.PlaceholderRuntimeVersion))
	}
	if recipe.protocolFixed {
		notes = append(notes, fmt.Sprintf("The %s community SDK speaks %s only — it cannot be switched to the other transport, so the destination must offer that one.",
			recipe.lang, recipe.otlpProtocol))
	}
	return notes
}
