package coordinator

import (
	"context"
	"strings"

	"github.com/odigos-io/odigos/connectors/base/api"
	domaindest "github.com/odigos-io/odigos/connectors/base/domain/destination"
	"github.com/odigos-io/odigos/connectors/base/domain/plan"
)

// workloadRoutingResolver is an optional provider seam for plans whose export target depends on the
// workload's own network. Providers that do not implement it retain the connector-wide destination path.
// It is deliberately local to the coordinator: this is a read-path enhancement, not a requirement every
// provider must add to the base Provider contract.
type workloadRoutingResolver interface {
	ResolveWorkloadRouting(context.Context, api.DiscoveredWorkload) (domaindest.Routing, bool, error)
}

// InstrumentationPlan returns what a customer must apply to instrument one workload, or nil when there is
// nothing to publish (unsupported type, no planner, uninstrumentable language — the report already explains).
//
// Served in both modes: manual needs it to describe the pending deployment; automatic uses it to show the
// desired state Odigos applied (otherwise only visible in the provider console).
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
		// Narrowed to the language the planner resolved for THIS workload — its detected runtime, or the
		// runtime override when the customer set one, whichever the plan was built from. A customer opening a
		// Java service gets the Java instructions and not a document covering six languages.
		Artifacts: c.instrumentationArtifacts(w.InstrumentationType, planned.Plan.Language),
		Resolved:  c.resolvePlaceholders(ctx, *w, planned.Resolved),
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

// instrumentationArtifacts returns the provider's spec-declared delivery variants for one (type, language).
// Spec-owned (not planner) so templates are shared across workloads and testable against the injector;
// the planner only supplies workload-specific values.
//
// language must be the plan's (detected runtime + override), not the report's — otherwise instructions and
// conformance disagree. Empty means no templates for that language; the caller falls back to prose.
func (c *Coordinator) instrumentationArtifacts(typeName, language string) []api.InstrumentationArtifact {
	arts := c.provider.Spec().InstrumentationArtifactsFor(typeName, language)
	if len(arts) == 0 {
		return nil
	}
	out := make([]api.InstrumentationArtifact, 0, len(arts))
	for _, a := range arts {
		out = append(out, api.InstrumentationArtifact{
			ID:            a.ID,
			Variant:       a.Variant,
			VariantLabel:  a.VariantLabel,
			Title:         a.Title,
			Description:   a.Description,
			Steps:         a.Steps,
			Format:        a.Format,
			Template:      a.Template,
			Prerequisites: a.Prerequisites,
		})
	}
	return out
}

// resolvePlaceholders fills connector-owned placeholders: service name, region, and the destination
// endpoint in both forms (scheme-qualified for OTel SDK vars; bare host:port for Odigos collector vars).
// Centralized so providers cannot disagree on those two forms. Unresolvable keys are omitted — the client
// shows them as fields to fill in.
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

	// A provider may resolve a workload-specific regional Collector route. The bool distinguishes an
	// unsupported workload kind from a supported kind whose route currently falls back to the global
	// destination; only the former skips this path. A successfully resolved provider route returns early so
	// the plan does not overwrite its Collector endpoint with the connector-wide endpoint.
	if router, ok := c.provider.(workloadRoutingResolver); ok {
		routing, handled, err := router.ResolveWorkloadRouting(ctx, w)
		if err != nil {
			coordLog.Warn("resolving workload-specific OTLP route for instrumentation plan failed", "error", err,
				"service", w.ServiceName, "kind", w.Kind, "region", w.Region)
			// The provider accepted responsibility for this workload's route but could not prove one.
			// Leave the placeholder unresolved; falling through to the global target would publish a plan
			// that disagrees with the automatic instrumentor's no-write decision.
			if handled {
				return out
			}
		} else if handled {
			if endpoint := routingEndpoint(routing.Target); routing.Found && endpoint != "" {
				out[plan.PlaceholderOTLPEndpoint] = endpoint
				out[plan.PlaceholderOTLPEndpointURL] = otlpEndpointURL(endpoint, routing.Target.Insecure)
				out[plan.PlaceholderOTLPInsecure] = boolString(routing.Target.Insecure)
				return out
			}
			return out
		}
	}

	target, ok, err := c.OTLPExportTarget(ctx)
	if err != nil {
		coordLog.Warn("resolving the OTLP endpoint for an instrumentation plan failed", "error", err)
		return out
	}
	if !ok || routingEndpoint(target) == "" {
		// No destination configured yet. Leaving both endpoint placeholders unresolved is correct and
		// self-explanatory: there is nothing to export to, which is the same reason the report says the
		// workload is waiting for a destination rather than for the customer.
		return out
	}
	endpoint := routingEndpoint(target)
	if endpoint == "" {
		return out
	}
	out[plan.PlaceholderOTLPEndpoint] = endpoint
	out[plan.PlaceholderOTLPEndpointURL] = otlpEndpointURL(endpoint, target.Insecure)
	out[plan.PlaceholderOTLPInsecure] = boolString(target.Insecure)
	return out
}

func routingEndpoint(target domaindest.OTLPTarget) string {
	if target.Endpoint != "" {
		return target.Endpoint
	}
	return target.HTTPEndpoint
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
