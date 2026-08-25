package instrumentor

import (
	"fmt"
	"sort"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
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
		// invisible: attribution is derived from this variable alone, so an app with every other fragment
		// applied and no marker is indistinguishable from an untouched app.
		//
		// The IaC marker, NOT the ownership marker, and that is the whole difference between the modes. The
		// ownership marker's payload is a rollback snapshot of what the connector changed; in manual mode
		// nothing converges the app and nothing will ever roll it back, so asking a customer to hand-author one
		// asks them to describe a rollback that will never run — and the connector would then have had to
		// honour it. What their definition can honestly state is that Odigos instrumentation is here and which
		// agent it stages, which is exactly this marker.
		{Name: EnvOdigosIaC, Value: iacMarkerValue(lang), Merge: plan.MergeSet},
		// A removal, not an omission: the ownership marker is the connector's own record of a mutation IT
		// performed. A hand-authored one claims a snapshot that was never taken, and one left over from an
		// earlier automatic-mode run would have Odigos restore values nobody has any more.
		{Name: EnvOdigosManaged, Merge: plan.MergeRemove},

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

// iacMarkerValue is the attribution marker a manual-mode customer applies, in the wire format
// api.ParseIaCMarker reads: the schema version and the staged agent's language.
//
// The language is load-bearing rather than informational — it is what the desired activation environment is
// derived from, so an app whose image later changed runtime would otherwise be judged against an agent its
// definition never named. "source"/"version" are attribution an Odigos IaC module fills in about itself; a
// customer pasting a fragment by hand has nothing true to write there.
//
// prevEnv is absent: it is the rollback snapshot of what the INSTRUMENTOR overwrote, and in manual mode the
// customer's own deployment definition is that record — reverting is a git operation, not something Odigos
// performs. The marker that carries such a snapshot is the one they must not write at all.
func iacMarkerValue(lang string) string {
	return fmt.Sprintf(`{"v":%d,"lang":%q}`, baseapi.IaCMarkerVersion, lang)
}
