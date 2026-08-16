package coordinator

import (
	"context"
	"strings"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/plan"
)

// InstrumentationPlan returns what a customer has to apply to instrument one workload, or nil when there is
// nothing to publish for it.
//
// It is served in BOTH modes deliberately. Manual mode is the reason it exists — the connector reports the
// workload as awaiting a deployment, so it had better be able to describe that deployment. But the same plan
// in automatic mode answers "what exactly did Odigos change here", which today can only be answered by
// reading a provider console. Restricting it to manual mode would withhold the desired state from the mode
// where Odigos is the one applying it.
//
// nil (with no error) is the answer for every "nothing to say" case, and they are all legitimate: a workload
// whose type has no instrumentation defined, a provider with no planner for the type, a language the planner
// cannot instrument. The report already explains each of those; a plan invented for one would be instructions
// that cannot work.
func (c *Coordinator) InstrumentationPlan(ctx context.Context, serviceName, kind, region string) (*api.InstrumentationPlanResult, error) {
	coordLog.Debug("instrumentation plan", "service", serviceName, "kind", kind, "region", region)

	// The enriched report, not the raw inventory: the instrumentation type, the derived mode and the reported
	// service name are all decided by the enrichment, and the plan has to agree with the report a client is
	// looking at when it asks for this.
	w, err := c.WorkloadReport(ctx, serviceName, kind, region)
	if err != nil || w == nil {
		return nil, err
	}
	if w.InstrumentationType == "" || !w.InstrumentationAvailable {
		return nil, nil
	}

	planner, ok := c.workloadPlanner(w.InstrumentationType)
	if !ok {
		return nil, nil
	}
	planned, err := planner.PlanFor(ctx, *w)
	if err != nil {
		// A planner failure is not a query failure. The plan is a read-path convenience; returning an error
		// would blank the whole response for a client that is otherwise showing a healthy report.
		coordLog.Warn("computing instrumentation plan failed", "service", serviceName, "kind", kind,
			"type", w.InstrumentationType, "error", err)
		return nil, nil
	}
	if planned == nil {
		return nil, nil
	}

	res := &api.InstrumentationPlanResult{
		ServiceName:         w.ServiceName,
		Kind:                w.Kind,
		Region:              w.Region,
		InstrumentationType: w.InstrumentationType,
		Mode:                w.ManagementMode,
		Plan:                planned.Plan,
		Artifacts:           c.instrumentationArtifacts(w.InstrumentationType),
		Resolved:            c.resolvePlaceholders(ctx, *w, planned.Resolved),
	}
	return res, nil
}

// workloadPlanner resolves the provider's planner for one instrumentation type. The registry is asserted here
// rather than being a Provider method, matching activeObservers: planning lands per provider, and one that
// has not implemented it yet contributes nothing rather than breaking every provider module at once.
func (c *Coordinator) workloadPlanner(typeName string) (api.InstrumentationPlanner, bool) {
	reg, ok := c.provider.(api.WorkloadPlannerRegistry)
	if !ok {
		return nil, false
	}
	p, ok := reg.WorkloadPlanners()[typeName]
	return p, ok && p != nil
}

// instrumentationArtifacts is the provider's spec-declared delivery variants for one instrumentation type.
//
// They come from the spec rather than from the planner because they are per TYPE, not per workload: one set of
// templates serves a whole account, which is what lets them be authored once and checked against the injector
// by a provider test. The planner supplies the values that make them workload-specific.
func (c *Coordinator) instrumentationArtifacts(typeName string) []api.InstrumentationArtifact {
	arts := c.provider.Spec().InstrumentationArtifacts(typeName)
	if len(arts) == 0 {
		return nil
	}
	out := make([]api.InstrumentationArtifact, 0, len(arts))
	for _, a := range arts {
		out = append(out, api.InstrumentationArtifact{
			ID:           a.ID,
			Variant:      a.Variant,
			VariantLabel: a.VariantLabel,
			Title:        a.Title,
			Description:  a.Description,
			Steps:        a.Steps,
			Format:       a.Format,
			Template:     a.Template,
		})
	}
	return out
}

// resolvePlaceholders fills the placeholder values that are properties of the CONNECTOR rather than of the
// workload's own definition: the reported service name, the region, and the destination endpoint in both the
// raw host:port and scheme-qualified forms.
//
// These are resolved here rather than in each planner for one reason: the two endpoint forms are not
// interchangeable — the OpenTelemetry SDK variables need a scheme and silently export nowhere without one,
// while the Odigos-namespaced collector variables take a bare authority — and four providers deriving that
// independently is four chances to hand a customer an endpoint that looks right and drops their telemetry.
//
// A placeholder this cannot resolve is simply absent from the map. The client renders it as a field to fill
// in, which is the truth: a container name lives in a definition the connector may never have read, and no
// Odigos Lambda layer is published yet at all.
func (c *Coordinator) resolvePlaceholders(ctx context.Context, w api.DiscoveredWorkload, fromProvider map[string]string) map[string]string {
	// The provider's resolutions first, then the connector-owned ones on top: the base is authoritative for
	// those, so a provider that also filled one does not get to disagree.
	out := make(map[string]string, len(fromProvider)+4)
	for k, v := range fromProvider {
		if v != "" {
			out[k] = v
		}
	}

	// The REPORTED name, which is what the observer compares the workload's OTEL_SERVICE_NAME against — a
	// source override makes that different from the discovered name, and handing over the discovered one
	// would produce instructions whose result is immediately reported as drift.
	if name := reportedServiceName(w); name != "" {
		out[plan.PlaceholderServiceName] = name
	}
	if w.Region != "" {
		out[plan.PlaceholderRegion] = w.Region
	}

	target, ok, err := c.OTLPExportTarget(ctx)
	if err != nil {
		coordLog.Warn("resolving the OTLP endpoint for an instrumentation plan failed", "error", err)
		return out
	}
	if !ok || target.Endpoint == "" {
		// No destination configured yet. Leaving both endpoint placeholders unresolved is correct and
		// self-explanatory: there is nothing to export to, which is the same reason the report says the
		// workload is waiting for a destination rather than for the customer.
		return out
	}
	out[plan.PlaceholderOTLPEndpoint] = target.Endpoint
	out[plan.PlaceholderOTLPEndpointURL] = otlpEndpointURL(target.Endpoint, target.Insecure)
	out[plan.PlaceholderOTLPInsecure] = boolString(target.Insecure)
	return out
}

// reportedServiceName is the name the workload reports as its OTel service.name: a source override when one
// is set, else the discovered name.
func reportedServiceName(w api.DiscoveredWorkload) string {
	if w.Overrides.ServiceName != "" {
		return w.Overrides.ServiceName
	}
	return w.ServiceName
}

// otlpEndpointURL turns a host:port endpoint into the scheme-qualified URL the OpenTelemetry SDK variables
// require, choosing the scheme from the destination's TLS setting. A value that is already a URL passes
// through unchanged.
func otlpEndpointURL(endpoint string, insecure bool) string {
	if endpoint == "" || strings.Contains(endpoint, "://") {
		return endpoint
	}
	if insecure {
		return "http://" + endpoint
	}
	return "https://" + endpoint
}

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
