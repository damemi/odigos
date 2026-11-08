package instrumentor

import (
	"strings"

	"github.com/odigos-io/odigos-enterprise/connectors/anypoint"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/plan"
	store "github.com/odigos-io/odigos-enterprise/connectors/providers/mulesoft/pkg/workload/store"
)

// Manual (IaC) mode: the customer applies the property set themselves, from the instructions the connector
// publishes, and marks the application with ODIGOS_IAC. This file is the single definition of that set. The
// planner renders it as instructions and the observer judges a deployment against it, so the two cannot
// disagree; the automatic reconciler writes the same values through DesiredProperties.

// IaCMarkerValue is the value the published instructions give the ODIGOS_IAC property.
const IaCMarkerValue = `{"v":1,"lang":"mule"}`

// Placeholders in the published instructions (spec/data/mulesoft.yaml) that this provider resolves per
// workload. The service name is the base's own placeholder and the base fills it.
const (
	PlaceholderProtocol        = "<OTLP_PROTOCOL>"
	PlaceholderTracesEndpoint  = "<OTLP_TRACES_ENDPOINT>"
	PlaceholderLogsEndpoint    = "<OTLP_LOGS_ENDPOINT>"
	PlaceholderMetricsEndpoint = "<OTLP_METRICS_ENDPOINT>"
	PlaceholderSampleRatio     = "<SAMPLE_RATIO>"
	PlaceholderEnvironment     = "<ENVIRONMENT>"
	PlaceholderOrganization    = "<ORGANIZATION_ID>"
	PlaceholderTargetType      = "<TARGET_TYPE>"
	// PlaceholderApplicationID appears only in the anypoint-cli artifact, so it is resolved but not part of the
	// plan's fragments.
	PlaceholderApplicationID = "<APPLICATION_ID>"
)

var manualPlaceholders = []plan.Placeholder{
	plan.ServiceNamePlaceholder,
	{Token: PlaceholderProtocol, Description: "GRPC for an OTLP gRPC destination, HTTP for an OTLP/HTTP one — matching how the destination is configured on this connector."},
	{Token: PlaceholderTracesEndpoint, Description: "The destination's OTLP endpoint as a full URL. For HTTP, the URL ends in /v1/traces."},
	{Token: PlaceholderLogsEndpoint, Description: "The destination's OTLP endpoint as a full URL. For HTTP, the URL ends in /v1/logs."},
	{Token: PlaceholderMetricsEndpoint, Description: "The destination's OTLP endpoint as a full URL. For HTTP, the URL ends in /v1/metrics."},
	{Token: PlaceholderSampleRatio, Description: "The head-sampling ratio, 0.0 to 1.0. 1.0 sends every trace and lets the Odigos collector sample."},
	{Token: PlaceholderEnvironment, Description: "The Anypoint environment the application runs in, e.g. Production."},
	{Token: PlaceholderOrganization, Description: "Your Anypoint organization (business group) ID."},
	{Token: PlaceholderTargetType, Description: "shared-space, private-space or runtime-fabric — where the application runs."},
}

func setProp(name, value string) plan.EnvVar {
	return plan.EnvVar{Name: name, Value: value, Merge: plan.MergeSet}
}

// Signals is which telemetry a manual plan exports.
type Signals struct{ Traces, Logs, Metrics bool }

// AllSignals is what the instructions show while no destination is configured; the export placeholders then
// stay unresolved anyway.
var AllSignals = Signals{Traces: true, Logs: true, Metrics: true}

// SignalsFor is the signal set automatic mode writes for a destination (see DesiredProperties). The planner and
// the observer both derive the manual plan from it, so a customer is told to export exactly the signals the
// destination takes and is judged against the same set.
func SignalsFor(dest domaindest.OTLPTarget, haveDest bool) Signals {
	if !haveDest {
		return AllSignals
	}
	return Signals{Traces: dest.Traces, Logs: dest.Logs, Metrics: dest.Metrics}
}

// ManualPlan is the manual-mode property set for one instrumentation type and signal set, with every
// per-workload value left as a placeholder. metricsRuntime is false for Mule 4.11, which cannot export metrics:
// the meter properties are then left out even when the destination takes metrics.
func ManualPlan(typeName string, s Signals, metricsRuntime bool) (plan.Plan, error) {
	env := []plan.EnvVar{setProp(store.PropOdigosIaC, IaCMarkerValue)}
	if s.Traces {
		env = append(env,
			setProp(PropTracesEnabled, "true"),
			setProp(PropTracesType, PlaceholderProtocol),
			setProp(PropTracesEndpoint, PlaceholderTracesEndpoint),
			setProp(PropSampler, samplerParentBasedRatio),
			setProp(PropSamplerArg, PlaceholderSampleRatio),
		)
	}
	if s.Logs {
		env = append(env,
			setProp(PropLogsEnabled, "true"),
			setProp(PropLogsType, PlaceholderProtocol),
			setProp(PropLogsEndpoint, PlaceholderLogsEndpoint),
		)
	}
	if s.Metrics && metricsRuntime {
		env = append(env,
			setProp(PropMetricsEnabled, "true"),
			setProp(PropMetricsType, PlaceholderProtocol),
			setProp(PropMetricsEnd, PlaceholderMetricsEndpoint),
		)
	}
	env = append(env,
		setProp(PropServiceName, plan.PlaceholderServiceName),
		plan.EnvVar{
			Name:      PropResourceAttributes,
			Value:     manualResourceAttributes,
			Merge:     plan.MergeAppend,
			Separator: ",",
		},
		setProp(PropLogCorrelation, "true"),
	)
	p := plan.Plan{
		Type:     typeName,
		Mode:     plan.ModeRuntimeConfig,
		Language: "",
		Fragments: []plan.Fragment{{
			Anchor: plan.Anchor{Scope: plan.ScopeWorkload, Field: plan.FieldProperties},
			Kind:   plan.KindEnv,
			Env:    env,
		}},
		Placeholders: usedPlaceholders(env),
	}
	if s.Metrics && !metricsRuntime {
		p.Notes = []string{"Mule 4.11 cannot export metrics; upgrade to 4.12 or later to add the meter properties."}
	}
	p.Sort()
	return p, p.Validate()
}

const manualResourceAttributes = "deployment.environment.name=" + PlaceholderEnvironment +
	",mulesoft.org.id=" + PlaceholderOrganization + ",mulesoft.target.type=" + PlaceholderTargetType

// usedPlaceholders keeps the placeholders the property set refers to, so a plan without a signal does not
// describe that signal's endpoint.
func usedPlaceholders(env []plan.EnvVar) []plan.Placeholder {
	out := make([]plan.Placeholder, 0, len(manualPlaceholders))
	for _, p := range manualPlaceholders {
		for _, e := range env {
			if strings.Contains(e.Value, p.Token) {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

// ManualValues resolves this provider's placeholders for one workload. The export values come from
// exporterFor — the function the automatic reconciler uses — so a customer following the instructions ends up
// with exactly what automatic mode would have written. With no destination the export placeholders stay
// unresolved, which is what the report also says: there is nothing to export to yet.
func ManualValues(app store.Application, orgID string, dest domaindest.OTLPTarget, haveDest bool, ratio float64) map[string]string {
	out := map[string]string{
		PlaceholderSampleRatio:   formatRatio(ratio),
		PlaceholderEnvironment:   app.EnvironmentName,
		PlaceholderOrganization:  orgID,
		PlaceholderTargetType:    app.TargetType,
		PlaceholderApplicationID: app.ExternalID,
	}
	if haveDest {
		typ, endpoint := exporterFor(dest)
		out[PlaceholderProtocol] = typ
		if typ == "HTTP" {
			out[PlaceholderTracesEndpoint] = endpoint + "/v1/traces"
			out[PlaceholderLogsEndpoint] = endpoint + "/v1/logs"
			out[PlaceholderMetricsEndpoint] = endpoint + "/v1/metrics"
		} else {
			out[PlaceholderTracesEndpoint] = endpoint
			out[PlaceholderLogsEndpoint] = endpoint
			out[PlaceholderMetricsEndpoint] = endpoint
		}
	}
	for k, v := range out {
		if v == "" {
			delete(out, k)
		}
	}
	return out
}

// metricsSupported reports whether the workload's Mule runtime can export metrics (4.12+). An unparseable
// version publishes the full set, since the meter lines carry their own "drop on 4.11" step.
func metricsSupported(runtimeVersion string) bool {
	v, ok := anypoint.ParseMuleVersion(runtimeVersion)
	return !ok || v.AtLeast(4, metricsMinMinor)
}
