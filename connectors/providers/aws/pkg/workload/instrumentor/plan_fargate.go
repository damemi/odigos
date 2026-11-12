package instrumentor

import (
	"fmt"
	domaindest "github.com/odigos-io/odigos/connectors/base/domain/destination"
	"sort"

	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/base/domain/plan"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/workload/store"
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
	for key, value := range (domaindest.OTLPTarget{Traces: true, Metrics: true, Logs: true}).SignalActivation(recipe.lang) {
		if key != EnvTracesExporter && key != EnvMetricsExporter && key != EnvLogsExporter {
			env = append(env, plan.EnvVar{Name: key, Value: value, Merge: plan.MergeSet})
		}
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
	for _, key := range domaindest.SignalTransportOverrides() {
		env = append(env, plan.EnvVar{Name: key, Merge: plan.MergeRemove})
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

// fargateServiceNamePlaceholder narrows the base's description for the one type where the workload's name in
// Odigos and its reported service.name are not the same string. A Fargate source is named
// "<cluster>-<service>" to keep same-named services in two clusters distinct; the value that goes here is the
// ECS SERVICE name, which is also the only one a task definition can express — it cannot reference the service
// that will run it. See fargateSourceIntent for the resolution rule this describes.
var fargateServiceNamePlaceholder = plan.Placeholder{
	Token: plan.PlaceholderServiceName,
	Description: "Your ECS service's name. It must match exactly: a different value is reported as " +
		"instrumentation drift, not as a naming preference. Note this is the ECS service name, not the " +
		"cluster-prefixed name the source is listed under in Odigos — unless you set a reported service name " +
		"on the source, in which case use that.",
}

// fargatePlaceholders is what this plan asks the customer to substitute.
// Runtime version only when the plan actually carries the token (otherwise nothing would substitute it).
func fargatePlaceholders(p plan.Plan) []plan.Placeholder {
	out := []plan.Placeholder{
		plan.ContainerPlaceholder,
		fargateServiceNamePlaceholder,
		plan.RegionPlaceholder,
		plan.OTLPEndpointURLPlaceholder,
	}
	if plan.UsesPlaceholder(p, plan.PlaceholderRuntimeVersion) {
		out = append(out, plan.RuntimeVersionPlaceholder)
	}
	return out
}
