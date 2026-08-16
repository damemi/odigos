package instrumentor

import (
	"fmt"
	"sort"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/plan"
)

// Container Apps' contribution to Wave 5: the instrumentation this connector would apply, expressed as
// anchored fragments a customer can put in their own Terraform / Bicep / az-cli definition.
//
// Built from the same package constants and languageSpecs the injector uses, so an agent directory or
// activation variable cannot change there without changing here. Container Apps revisions are immutable and
// nothing converges a manual-mode app, so instructions that have drifted from the injector leave a customer
// applying the previous release's configuration and reading a Partial conformance verdict they cannot
// attribute.
//
// The one structural difference from Cloud Run: Container Apps HAS run-to-completion init containers, so the
// stager exits when it is done and the platform's own ordering guarantees the agent files exist before the
// application container starts. There is no startup probe and no explicit dependency to declare.

// DesiredPlan returns the instrumentation plan for one Container Apps language. image is the odigos-agents
// image the init container stages from (config.AgentImage).
func DesiredPlan(lang, image string) (plan.Plan, error) {
	spec, ok := languageSpecs[lang]
	if !ok {
		return plan.Plan{}, fmt.Errorf("no Odigos Container Apps agent for language %q", lang)
	}
	if image == "" {
		return plan.Plan{}, fmt.Errorf("container-apps plan for %q needs the odigos-agents image (config.AgentImage)", lang)
	}

	p := plan.Plan{
		Type:     "azure.container-apps",
		Language: lang,
		Mode:     plan.ModeStagedAgent,
		Fragments: []plan.Fragment{
			{
				Anchor: plan.Anchor{Scope: plan.ScopeWorkload, Field: plan.FieldVolumes},
				Kind:   plan.KindVolume,
				// StorageType EmptyDir — Container Apps accepts no size limit on it, so the plan declares none
				// rather than inventing a value a customer would paste and have rejected.
				Volume: &plan.Volume{Name: agentVolumeName, Medium: "ephemeral"},
			},
			{
				Anchor: plan.Anchor{Scope: plan.ScopeWorkload, Field: plan.FieldContainers},
				Kind:   plan.KindHelperContainer,
				Helper: &plan.HelperContainer{
					Name:    initContainerName,
					Image:   image,
					Command: []string{"sh", "-c", initCommand(spec.agentDirs)},
					Mount:   plan.Mount{Volume: agentVolumeName, Path: agentsDir},
					// A real init container: it exits when staging is done and the platform orders the
					// application container after it, so no probe and no dependsOn fragment are needed.
					Init: true,
				},
			},
			{
				Anchor: plan.Anchor{Scope: plan.ScopeContainer, Field: plan.FieldVolumeMounts, Container: plan.PlaceholderContainer},
				Kind:   plan.KindVolumeMount,
				Mount:  &plan.Mount{Volume: agentVolumeName, Path: agentsDir},
			},
			{
				Anchor: plan.Anchor{Scope: plan.ScopeContainer, Field: plan.FieldEnv, Container: plan.PlaceholderContainer},
				Kind:   plan.KindEnv,
				Env:    desiredEnv(lang, spec),
			},
		},
		Notes: planNotes(lang),
		Placeholders: []plan.Placeholder{
			plan.ContainerPlaceholder,
			plan.ServiceNamePlaceholder,
			plan.RegionPlaceholder,
			plan.OTLPEndpointPlaceholder,
			plan.OTLPEndpointURLPlaceholder,
		},
	}
	p.Sort()
	if err := p.Validate(); err != nil {
		return plan.Plan{}, err
	}
	return p, nil
}

// DesiredPlans is every supported language's plan, ordered by language so a rendering is stable.
func DesiredPlans(image string) ([]plan.Plan, error) {
	langs := make([]string, 0, len(languageSpecs))
	for l := range languageSpecs {
		langs = append(langs, l)
	}
	sort.Strings(langs)

	out := make([]plan.Plan, 0, len(langs))
	for _, l := range langs {
		p, err := DesiredPlan(l, image)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// desiredEnv mirrors applyInstrumentation's env, with the per-workload values as placeholders. Every entry
// has a counterpart in alreadyInstrumented's comparison, which is what makes the plan the same desired state
// an observer judges against.
func desiredEnv(lang string, spec languageSpec) []plan.EnvVar {
	env := []plan.EnvVar{
		// First because it is the one a customer skips and the one whose absence makes everything else
		// invisible: ownership is derived from this variable alone, so an app with every other fragment
		// applied and no marker is indistinguishable from an untouched app.
		{Name: EnvOdigosManaged, Value: markerValue(lang), Merge: plan.MergeSet},

		{Name: EnvServiceName, Value: plan.PlaceholderServiceName, Merge: plan.MergeSet},
		{Name: EnvResourceAttributes, Value: resourceAttributes(plan.PlaceholderRegion), Merge: plan.MergeSet},
		{Name: EnvCollectorEndpoint, Value: plan.PlaceholderOTLPEndpoint, Merge: plan.MergeSet},
		// The scheme-qualified form, not the bare host:port above: the SDK accepts a bare authority and then
		// exports nowhere.
		{Name: EnvOTLPEndpoint, Value: plan.PlaceholderOTLPEndpointURL, Merge: plan.MergeSet},
		{Name: EnvOTLPProtocol, Value: otlpProtocolGRPC, Merge: plan.MergeSet},
		{Name: EnvTracesExporter, Value: exporterOTLP, Merge: plan.MergeSet},
		{Name: EnvMetricsExporter, Value: exporterOTLP, Merge: plan.MergeSet},
		{Name: EnvLogsExporter, Value: exporterOTLP, Merge: plan.MergeSet},
		// A removal, not an omission: the connector deletes this variable unless its debug-telemetry toggle is
		// on, and alreadyInstrumented compares it against exactly that — so an app carrying its own value
		// would be reported as drifted.
		{Name: EnvOtelLogLevel, Merge: plan.MergeRemove},
	}
	for k, v := range spec.staticEnv {
		env = append(env, plan.EnvVar{Name: k, Value: v, Merge: plan.MergeSet})
	}
	for _, a := range spec.appendEnv {
		// APPEND, matching addLoaderValue: the customer's value stays first so their settings keep precedence.
		env = append(env, plan.EnvVar{Name: a.name, Value: a.value, Merge: plan.MergeAppend, Separator: a.sep})
	}
	return env
}

// markerValue is the ownership marker a customer applies, in the wire format the connector parses.
//
// prevEnv is omitted: it is the rollback snapshot of what the INSTRUMENTOR overwrote, and in manual mode the
// customer's own deployment definition is that record — reverting is a git operation, not something Odigos
// performs. See the note for the one case where it matters.
func markerValue(lang string) string {
	return fmt.Sprintf(`{"v":%d,"lang":%q}`, managedManifestVersion, lang)
}

func planNotes(lang string) []string {
	notes := []string{
		fmt.Sprintf("Set %s exactly as shown, including the language. It is the only thing that tells Odigos this app is instrumented: without it the app is reported as awaiting your deployment however completely the rest is applied, and the recorded language is what Odigos compares the live configuration against.",
			EnvOdigosManaged),
		fmt.Sprintf("%s must match the workload's name in Odigos. A different value is reported as instrumentation drift, not as a naming preference.", EnvServiceName),
		fmt.Sprintf("Set %s / %s / %s to \"%s\" for any signal your destination does not accept, so the agent never produces it.",
			EnvTracesExporter, EnvMetricsExporter, EnvLogsExporter, exporterNone),
		fmt.Sprintf("%q is a real init container: it stages the agent and exits, and Container Apps starts your container only after it succeeds. No startup probe or explicit dependency is needed.", initContainerName),
		// Revisions are immutable and, in Multiple-revision mode, an ingress traffic split can keep weighting
		// an older one. A customer who applies this and leaves traffic where it was has done everything right
		// and will still see nothing.
		"Container Apps revisions are immutable, so this creates a new revision. In Multiple-revision mode, move ingress traffic onto it — until you do, the app keeps serving the previous, uninstrumented revision.",
		fmt.Sprintf("If you later hand instrumentation over to Odigos (enable the instrumentation capability for this type), add a \"prevEnv\" object to %s recording your pre-Odigos value for each variable above, so Odigos can restore them on rollback.", EnvOdigosManaged),
		"The agent image is pulled by your own Container App. If your environment cannot pull it, mirror it into a registry the app can reach and use that reference.",
		"If this connector is configured with an agent-image override (ODIGOS_CONTAINERAPPS_AGENT_IMAGE), use that image instead of the one shown here.",
	}
	if lang == LangDotNet {
		notes = append(notes, "The .NET agent bundled here is glibc-only; a musl (Alpine) based image is not supported.")
	}
	return notes
}
