package instrumentor

import (
	"fmt"
	"sort"

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

// desiredEnv mirrors applyInstrumentation's env exactly, in the same order, with the per-workload values as
// placeholders. Every entry here has a counterpart in alreadyInstrumented's comparison — that is what makes
// the plan the same desired state the observer judges against.
func desiredEnv(lang string, spec languageSpec) []plan.EnvVar {
	env := []plan.EnvVar{
		// The ownership marker is FIRST because it is the one a customer skips and the one that makes
		// everything else invisible. Ownership is derived from this variable alone (Wave 0 made it the sole
		// authority), so a service with every other fragment applied and no marker reports as
		// "waiting for your deployment" forever, and the connector cannot tell it from an untouched service.
		{Name: EnvOdigosManaged, Value: markerValue(lang), Merge: plan.MergeSet},

		{Name: EnvServiceName, Value: plan.PlaceholderServiceName, Merge: plan.MergeSet},
		{Name: EnvResourceAttributes, Value: resourceAttributes(plan.PlaceholderRegion), Merge: plan.MergeSet},
		{Name: EnvCollectorEndpoint, Value: plan.PlaceholderOTLPEndpoint, Merge: plan.MergeSet},
		// The scheme-qualified form, not the bare host:port above: the SDK accepts a bare authority and then
		// exports nowhere, so the two endpoints are separate placeholders on purpose.
		{Name: EnvOTLPEndpoint, Value: plan.PlaceholderOTLPEndpointURL, Merge: plan.MergeSet},
		{Name: EnvOTLPProtocol, Value: otlpProtocolGRPC, Merge: plan.MergeSet},
		// Per-signal exporters: the connector sets these from what the destination actually accepts, so a
		// signal the backend does not implement is never produced. A plan cannot know the destination, so it
		// names the enabled value and the note below explains the "none" case.
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

// markerValue is the ownership marker a customer applies: the wire format the observer parses, carrying the
// version and the language.
//
// prevEnv is deliberately omitted. It is the rollback snapshot — the pre-Odigos value of every key the
// INSTRUMENTOR changed — and in manual mode there is nothing for Odigos to roll back: the customer's own
// deployment definition is the record of what they had, and reverting it is a git operation. Asking them to
// hand-write a nested JSON snapshot of their own previous env would be busywork with a real chance of
// getting it wrong. The note explains the one case where it matters.
func markerValue(lang string) string {
	return fmt.Sprintf(`{"v":%d,"lang":%q}`, managedManifestVersion, lang)
}

func planNotes(lang string) []string {
	notes := []string{
		fmt.Sprintf("Set %s exactly as shown, including the language. It is the only thing that tells Odigos this service is instrumented: without it the service is reported as awaiting your deployment however completely the rest is applied, and the recorded language is what Odigos compares the live configuration against.",
			EnvOdigosManaged),
		fmt.Sprintf("%s must match the workload's name in Odigos. A different value is reported as instrumentation drift, not as a naming preference.", EnvServiceName),
		fmt.Sprintf("Set %s / %s / %s to \"%s\" for any signal your destination does not accept, so the agent never produces it.",
			EnvTracesExporter, EnvMetricsExporter, EnvLogsExporter, exporterNone),
		fmt.Sprintf("Cloud Run has no run-to-completion containers, so %q stays running and holds TCP port %d once it has staged the agent. The ingress container's dependency on it is what keeps the application from starting before the agent files exist.",
			sidecarContainerName, startupProbePort),
		fmt.Sprintf("If you later hand instrumentation over to Odigos (enable the instrumentation capability for this type), add a \"prevEnv\" object to %s recording your pre-Odigos value for each variable above, so Odigos can restore them on rollback.", EnvOdigosManaged),
		// The image is pulled by the CUSTOMER's Cloud Run service, not by the connector, so its availability
		// is their account's problem to hit and ours to warn about. Cloud Run also restricts image hosts to
		// *.gcr.io, *-docker.pkg.dev and docker.io, so a mirror has to be one of those.
		"The agent image is pulled by your own Cloud Run service. If your project cannot pull it, mirror it into your own Artifact Registry and use that reference — Cloud Run only accepts images from *.gcr.io, *-docker.pkg.dev or docker.io.",
		"If this connector is configured with an agent-image override (ODIGOS_CLOUDRUN_AGENT_IMAGE), use that image instead of the one shown here.",
	}
	if lang == LangDotNet {
		notes = append(notes, "The .NET agent bundled for Cloud Run is glibc-only; a musl (Alpine) based image is not supported.")
	}
	return notes
}
