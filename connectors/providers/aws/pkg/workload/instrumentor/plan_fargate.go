package instrumentor

import (
	"fmt"
	"sort"
	"strings"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/plan"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
)

// ECS Fargate's contribution to Wave 5: the instrumentation this connector would apply, expressed as anchored
// fragments a customer can put in their own Terraform / CloudFormation task definition.
//
// It is built from the SAME recipes the injector uses — recipeFor, which reads the OSS Odigos community
// distros — not from a hand-copied table. That matters more here than on the other three platforms: the
// activation env for six languages is not knowledge this repo owns, it is loaded from an upstream module, so
// a hand-written instruction set would drift on somebody else's release. Reading the recipe means an upstream
// change to a distro's ReplacePattern shows up as a failing coverage test rather than as customers applying
// an activation string that no longer works.
//
// The values only the customer's own definition supplies — their container's name, the workload's Odigos
// source name, the OTLP endpoint, the region, and (for the version-templated agents) the runtime version —
// are rendered as plan placeholders.

// planRecipes is the plan builder's own recipe loader. It is a separate instance from the instrumentor's on
// purpose: the loader is lazy and process-wide-cached behind a sync.Once, so sharing one would couple a
// plan built in a test to whether some instrumentor had already run.
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
				// A bare-name task volume: no host, docker or EFS configuration. On Fargate that is an
				// ephemeral-storage bind mount scoped to the task, which is exactly the shared staging area
				// wanted — and Fargate has no tmpfs, so there is no in-memory option to declare.
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
					// Run-to-completion: ECS has real non-essential containers, so the stager exits 0 and the
					// application container's dependency below gates on that. It is still declared in
					// containerDefinitions like any other container — ECS has no separate init list.
					Init: true,
				},
			},
			{
				Anchor: plan.Anchor{Scope: plan.ScopeContainer, Field: plan.FieldVolumeMounts, Container: plan.PlaceholderContainer},
				Kind:   plan.KindVolumeMount,
				// Read-only in the application container: the agents are only read there, and the staging
				// container owns the writes.
				Mount: &plan.Mount{Volume: agentVolumeName, Path: agentsDir, ReadOnly: true},
			},
			{
				Anchor: plan.Anchor{Scope: plan.ScopeContainer, Field: plan.FieldDependsOn, Container: plan.PlaceholderContainer},
				Kind:   plan.KindStartupDependency,
				// SUCCESS, which ECS only accepts against a NON-essential container — the two settings are one
				// decision, and declaring the stager essential makes this dependency invalid.
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
	// Derived from the plan just built, not from a list of which languages are version-templated: that list
	// lives in an upstream distro and would rot here silently.
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

// desiredFargateEnv mirrors instrumentContainer's env exactly, with the per-workload values as placeholders.
// Every entry has a counterpart in containerDiff, which is what makes the plan the same desired state the
// observer judges a live task definition against.
func desiredFargateEnv(recipe agentRecipe) []plan.EnvVar {
	env := []plan.EnvVar{
		// First because it is the one a customer skips and the one whose absence makes everything else
		// invisible: ownership comes from this variable alone, per container, so a task definition with every
		// other fragment applied and no marker reads as untouched — and its recorded language is what the
		// observer compares the live activation env against.
		{Name: store.EnvOdigosManaged, Value: fargateMarkerValue(recipe.lang), Merge: plan.MergeSet},

		{Name: EnvServiceName, Value: plan.PlaceholderServiceName, Merge: plan.MergeSet},
		{Name: envResourceAttributes, Value: ecsResourceAttributes(plan.PlaceholderRegion), Merge: plan.MergeSet},
		// Endpoint and protocol are one decision, never two: an HTTP request to a gRPC listener fails as an
		// opaque connection reset, so the protocol below is the recipe's own and the note names the port that
		// has to match it.
		{Name: envOTLPEndpoint, Value: plan.PlaceholderOTLPEndpointURL, Merge: plan.MergeSet},
		{Name: EnvOtlpProtocol, Value: recipe.otlpProtocol, Merge: plan.MergeSet},
		{Name: EnvTracesExporter, Value: exporterOTLP, Merge: plan.MergeSet},
		{Name: EnvMetricsExporter, Value: exporterOTLP, Merge: plan.MergeSet},
		{Name: EnvLogsExporter, Value: exporterOTLP, Merge: plan.MergeSet},
		// A removal, not an omission: the connector deletes this variable unless its debug-telemetry toggle is
		// on, and containerDiff compares it against exactly that — so a container carrying its own value is
		// reported as drifted.
		{Name: EnvOtelLogLevel, Merge: plan.MergeRemove},
	}
	for k, v := range recipe.staticEnv {
		env = append(env, plan.EnvVar{Name: k, Value: v, Merge: plan.MergeSet})
	}
	for _, a := range recipe.appendEnv {
		// The distro's ReplacePattern evaluated against an EMPTY original is our value alone (it strips the
		// leading delimiter), and the merge direction carries "and keep whatever they had". Evaluating it
		// against a literal placeholder instead would embed the placeholder inside the value, where it reads
		// as part of the agent path.
		//
		// The separator comes from the pattern, not from an assumption: it is a space for
		// JAVA_TOOL_OPTIONS/NODE_OPTIONS and a colon for PYTHONPATH.
		env = append(env, plan.EnvVar{
			Name:      a.name,
			Value:     a.evaluate(""),
			Merge:     plan.MergeAppend,
			Separator: a.separator(),
		})
	}
	return env
}

// fargateMarkerValue is the ownership marker a customer applies, in the wire format parseECSManifest reads.
//
// The minimal form is deliberate, and so is the note that goes with it. prevEnv is the pre-Odigos value of
// every key the INSTRUMENTOR overwrote, and in manual mode nothing rolls back — the customer's own definition
// is that record. But it is not merely decorative here: containerDiff reconstructs the expected loader value
// as a.evaluate(ecsPriorEnv(manifest, name)), so with no prevEnv the expected value is OURS ALONE. A customer
// who already sets JAVA_TOOL_OPTIONS and correctly appends to it is then reported as drifted for doing exactly
// what the instructions said. Recording the original in prevEnv is what makes the two agree.
func fargateMarkerValue(lang string) string {
	return fmt.Sprintf(`{"v":%d,"lang":%q}`, ecsManagedManifestVersion, lang)
}

// loaderPrevEnvNote is the instruction that keeps a correctly-applied plan from being reported as drift. It is
// emitted only for the languages that actually have a loader variable — php and ruby activate through static
// variables, so there is nothing to merge and nothing to record.
func loaderPrevEnvNote(recipe agentRecipe) string {
	if len(recipe.appendEnv) == 0 {
		return ""
	}
	names := make([]string, 0, len(recipe.appendEnv))
	for _, a := range recipe.appendEnv {
		names = append(names, a.name)
	}
	sort.Strings(names)
	example := fmt.Sprintf(`{"v":%d,"lang":%q,"prevEnv":{%q:"<your original value>"}}`,
		ecsManagedManifestVersion, recipe.lang, names[0])
	return fmt.Sprintf("If your container ALREADY sets %s, record its original value in the marker: %s. Odigos reconstructs the expected value as your recorded original joined to the Odigos value, so appending to a value you did not record is reported as instrumentation drift even though you applied the plan correctly.",
		strings.Join(names, " / "), example)
}

// fargatePlaceholders is what this plan asks a customer to substitute.
//
// The runtime version is declared only when the plan actually carries the token: java/python/nodejs/dotnet have
// no per-version paths at all, and a plan built for a workload whose version IS known has the real version
// baked in. Declaring it otherwise renders a required fill-in field for a value nothing substitutes.
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

// replaceableStaticEnv names the recipe's static activation variables that a real application plausibly sets
// itself, and which the instrumentor therefore overwrites. PHP's scan dir and Ruby's RUBYOPT are loader
// variables in everything but their declaration — upstream expresses them as static values (PHP's leading
// colon means "the default scan dir, then ours"), so both the injector and any observer compare them exactly.
// The plan says so rather than quietly telling a customer to merge, which would be reported as drift.
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
		fmt.Sprintf("Set %s on each container you are instrumenting, exactly as shown including the language. Odigos derives ownership from this variable alone, per container: without it the container is reported as awaiting your deployment however completely the rest is applied.",
			store.EnvOdigosManaged),
		fmt.Sprintf("%s must match the workload's name in Odigos. A different value is reported as instrumentation drift, not as a naming preference.", EnvServiceName),
		fmt.Sprintf("Set %s / %s / %s to \"%s\" for any signal your destination does not accept, so the agent never produces it.",
			EnvTracesExporter, EnvMetricsExporter, EnvLogsExporter, exporterNone),
		fmt.Sprintf("This agent exports over %s, so %s must point at that transport's port — %s for gRPC, %s for http/protobuf. Pairing an endpoint with the other protocol fails at runtime as a connection reset that points nowhere near the cause.",
			recipe.otlpProtocol, envOTLPEndpoint, otlpDefaultGRPCPort, otlpDefaultHTTPPort),
		fmt.Sprintf("Declare %q as NON-essential. It stages the agent and exits 0, and ECS only accepts a %s container dependency against a non-essential container — the two settings are one decision.",
			sidecarContainerName, plan.ConditionSuccess),
		fmt.Sprintf("A task definition may hold several applications in different languages. Apply the container-scoped fragments to each one separately; %q stages every language's agent and serves them all.", sidecarContainerName),
		// A task definition revision is inert until the service rolls onto it — the customer has done
		// everything right at this point and will still see nothing.
		"Registering the revision is not enough: update the service to it so the new revision actually runs.",
		fmt.Sprintf("AWS caps a task definition at %d containers. A definition already at the limit has no room for the staging container.", maxTaskDefContainers),
		"The agent image is pulled by your own task's execution role. If it cannot pull it, mirror the image into a registry the task can reach and use that reference.",
		fmt.Sprintf("The Odigos Fargate agents bundle is Linux-only and %s-based; a Windows task definition cannot be instrumented at all.", fargateLibcType),
	}
	if n := loaderPrevEnvNote(recipe); n != "" {
		notes = append(notes, n)
	}
	// The static activation variables are applied as-is, which replaces a value the customer may already have.
	// PHP_INI_SCAN_DIR and RUBYOPT are the two that a real application plausibly sets, and this is what the
	// instrumentor does too — so the note describes the behavior rather than the plan diverging from it, which
	// would be reported as drift.
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
