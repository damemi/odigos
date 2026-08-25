package instrumentor

import (
	"fmt"
	"sort"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/plan"
)

// Cloud Run's contribution to Wave 5: the instrumentation this connector would apply, expressed as anchored
// fragments a customer can put in their own Terraform / gcloud definition.
//
// It is built from the SAME package-level constants and languageSpecs the injector uses — not from a copy —
// which is the whole point. Cloud Run revisions are immutable and nothing converges a manual-mode service,
// so instructions that have drifted from the injector are not a stale doc: they are a customer applying last
// release's agent path, getting a Partial conformance verdict, and having no way to tell which of the two
// sides is wrong. Reading the live recipe means an agent directory or activation variable cannot change here
// without changing there.
//
// The values only the customer's deployment supplies — their container's name, the workload's Odigos source
// name, the OTLP endpoint, the region — are rendered as plan placeholders.

// DesiredPlan returns the instrumentation plan for one Cloud Run language. image is the odigos-agents image
// the stager copies from (config.AgentImage); passing it in rather than reading the default keeps the plan
// honest for a connector configured with an override.
func DesiredPlan(lang, image string) (plan.Plan, error) {
	spec, ok := languageSpecs[lang]
	if !ok {
		return plan.Plan{}, fmt.Errorf("no Odigos Cloud Run agent for language %q", lang)
	}
	// Required, not defaulted. The image is the one value in the plan a customer cannot derive or guess, and
	// a plan that silently substituted a placeholder would publish instructions whose stager cannot start.
	if image == "" {
		return plan.Plan{}, fmt.Errorf("cloud-run plan for %q needs the odigos-agents image (config.AgentImage)", lang)
	}

	p := plan.Plan{
		Type:     "gcp.cloud-run",
		Language: lang,
		Mode:     plan.ModeStagedAgent,
		Fragments: []plan.Fragment{
			{
				Anchor: plan.Anchor{Scope: plan.ScopeWorkload, Field: plan.FieldVolumes},
				Kind:   plan.KindVolume,
				Volume: &plan.Volume{Name: agentVolumeName, Medium: "memory", SizeLimit: agentVolumeSizeLimit},
			},
			{
				Anchor: plan.Anchor{Scope: plan.ScopeWorkload, Field: plan.FieldContainers},
				Kind:   plan.KindHelperContainer,
				Helper: &plan.HelperContainer{
					Name:    sidecarContainerName,
					Image:   image,
					Command: sidecarCommand(spec.agentDirs),
					Mount:   plan.Mount{Volume: agentVolumeName, Path: agentsDir},
					// Cloud Run has no run-to-completion containers, so the stager cannot exit: it holds a TCP
					// port and the ingress container's startup dependency waits on that probe instead.
					Init:             false,
					StartupProbePort: startupProbePort,
				},
			},
			{
				Anchor: plan.Anchor{Scope: plan.ScopeContainer, Field: plan.FieldVolumeMounts, Container: plan.PlaceholderContainer},
				Kind:   plan.KindVolumeMount,
				Mount:  &plan.Mount{Volume: agentVolumeName, Path: agentsDir},
			},
			{
				Anchor:            plan.Anchor{Scope: plan.ScopeContainer, Field: plan.FieldDependsOn, Container: plan.PlaceholderContainer},
				Kind:              plan.KindStartupDependency,
				StartupDependency: &plan.StartupDependency{On: sidecarContainerName, Condition: plan.ConditionStarted},
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

// desiredEnv mirrors applyInstrumentation's env exactly, in the same order, with the per-workload values as
// placeholders. Every entry here has a counterpart in alreadyInstrumented's comparison — that is what makes
// the plan the same desired state the observer judges against.
func desiredEnv(lang string, spec languageSpec) []plan.EnvVar {
	env := []plan.EnvVar{
		// The marker is FIRST because it is the one a customer skips and the one that makes everything else
		// invisible: attribution is derived from this variable alone, so a service with every other fragment
		// applied and no marker reports as "waiting for your deployment" forever, and the connector cannot
		// tell it from an untouched service.
		//
		// The IaC marker, NOT the ownership marker, and that is the whole difference between the modes. The
		// ownership marker's payload is a rollback snapshot of what the connector changed; in manual mode
		// nothing converges the service and nothing will ever roll it back, so asking a customer to hand-author
		// one asks them to describe a rollback that will never run — and the connector would then have had to
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
		// exports nowhere, so the two endpoints are separate placeholders on purpose.
		{Name: EnvOTLPEndpoint, Value: plan.PlaceholderOTLPEndpointURL, Merge: plan.MergeSet},
		{Name: EnvOTLPProtocol, Value: otlpProtocolGRPC, Merge: plan.MergeSet},
		// Per-signal exporters: the connector sets these from what the destination actually accepts, so a
		// signal the backend does not implement is never produced. A plan cannot know the destination, so it
		// names the enabled value; set a signal your destination rejects to "none" instead.
		{Name: EnvTracesExporter, Value: exporterOTLP, Merge: plan.MergeSet},
		{Name: EnvMetricsExporter, Value: exporterOTLP, Merge: plan.MergeSet},
		{Name: EnvLogsExporter, Value: exporterOTLP, Merge: plan.MergeSet},
		// A removal, not an omission: the connector deletes this variable unless its debug-telemetry toggle is
		// on, and alreadyInstrumented compares it against exactly that. A service carrying its own value would
		// be reported as drifted, so the plan has to say so rather than leaving it unmentioned.
		{Name: EnvOtelLogLevel, Merge: plan.MergeRemove},
	}
	for k, v := range spec.staticEnv {
		env = append(env, plan.EnvVar{Name: k, Value: v, Merge: plan.MergeSet})
	}
	for _, a := range spec.appendEnv {
		// APPEND, matching addLoaderValue: the customer's original value stays first so their own settings
		// keep precedence, and ours is added after. Rendering this as a literal would silently drop whatever
		// they had — which is why it is expressed as a merge instruction and not a value.
		env = append(env, plan.EnvVar{Name: a.name, Value: a.value, Merge: plan.MergeAppend, Separator: a.sep})
	}
	return env
}

// iacMarkerValue is the attribution marker a manual-mode customer applies: the wire format
// api.ParseIaCMarker reads, carrying the schema version and the staged agent's language.
//
// The language is load-bearing rather than informational — it is what the desired activation environment is
// derived from, so a service whose image later changed runtime would otherwise be judged against an agent its
// definition never named. "source"/"version" are attribution an Odigos IaC module fills in about itself; a
// customer pasting a fragment by hand has nothing true to write there.
//
// prevEnv is deliberately absent. It is the rollback snapshot — the pre-Odigos value of every key the
// INSTRUMENTOR changed — and in manual mode there is nothing for Odigos to roll back: the customer's own
// deployment definition is the record of what they had, and reverting it is a git operation. Asking them to
// hand-write a nested JSON snapshot of their own previous env would be busywork with a real chance of getting
// it wrong, and the marker that carries such a snapshot is the one they must not write at all.
func iacMarkerValue(lang string) string {
	return fmt.Sprintf(`{"v":%d,"lang":%q}`, baseapi.IaCMarkerVersion, lang)
}
