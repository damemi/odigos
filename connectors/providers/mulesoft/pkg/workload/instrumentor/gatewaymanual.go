package instrumentor

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/odigos-io/odigos/connectors/anypoint"
	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	domaindest "github.com/odigos-io/odigos/connectors/base/domain/destination"
	"github.com/odigos-io/odigos/connectors/base/domain/plan"
	store "github.com/odigos-io/odigos/connectors/providers/mulesoft/pkg/workload/store"
)

// Manual (IaC) mode for an Omni Gateway: the customer turns the gateway's tracing on themselves — in Runtime
// Manager for a managed gateway, with a Configuration resource on the host for a self-managed one — and adds
// the odigos.iac label. This is the single definition of what they set; the plan entries name the settings by
// their path in the gateway's tracing configuration.

// GatewayIaCLabelValue is the value the instructions give the odigos.iac label.
const GatewayIaCLabelValue = "omni-gateway"

// PlaceholderGatewayServiceType is the provider's service type: grpc or http, matching the destination.
const PlaceholderGatewayServiceType = "<OTLP_SERVICE_TYPE>"

// Gateway tracing settings, named by their path in the gateway's tracing configuration.
const (
	GatewayTracingEnabled     = "tracing.enabled"
	GatewayProviderType       = "tracing.provider.type"
	GatewayProviderService    = "tracing.provider.properties.serviceType"
	GatewayProviderAddress    = "tracing.provider.properties.address"
	GatewayProviderSvcName    = "tracing.provider.properties.serviceName"
	GatewayTracingIaCLabelKey = "tracing.labels." + store.GatewayIaCLabel
)

var gatewayPlaceholders = []plan.Placeholder{
	plan.ServiceNamePlaceholder,
	{Token: PlaceholderGatewayServiceType, Description: "grpc for an OTLP gRPC destination, http for an OTLP/HTTP one — matching how the destination is configured on this connector."},
	{Token: PlaceholderTracesEndpoint, Description: "The destination's OTLP endpoint as a full URL. For http, the URL ends in /v1/traces."},
}

// GatewayManualPlan is the manual-mode tracing configuration for an Omni Gateway, with the per-workload values
// left as placeholders.
func GatewayManualPlan(typeName string) (plan.Plan, error) {
	env := []plan.EnvVar{
		setProp(GatewayTracingEnabled, "true"),
		setProp(GatewayProviderType, "opentelemetry"),
		setProp(GatewayProviderService, PlaceholderGatewayServiceType),
		setProp(GatewayProviderAddress, PlaceholderTracesEndpoint),
		setProp(GatewayProviderSvcName, plan.PlaceholderServiceName),
		setProp(GatewayTracingIaCLabelKey, GatewayIaCLabelValue),
	}
	p := plan.Plan{
		Type: typeName,
		Mode: plan.ModeRuntimeConfig,
		Fragments: []plan.Fragment{{
			Anchor: plan.Anchor{Scope: plan.ScopeWorkload, Field: plan.FieldProperties},
			Kind:   plan.KindEnv,
			Env:    env,
		}},
		Placeholders: gatewayPlaceholders,
		Notes: []string{
			"Omni Gateway exports traces only. Its spans join the Mule application's trace when the application continues the W3C traceparent the gateway sends, which Mule does from an HTTP Listener.",
		},
	}
	p.Sort()
	return p, p.Validate()
}

// GatewayManualValues resolves the gateway placeholders for the connector's destination. With none they stay
// unresolved, which is what the report also says: there is nothing to export to yet.
func GatewayManualValues(dest domaindest.OTLPTarget, haveDest bool) map[string]string {
	if !haveDest {
		return map[string]string{}
	}
	serviceType, address := gatewayExporter(dest)
	return map[string]string{PlaceholderGatewayServiceType: serviceType, PlaceholderTracesEndpoint: address}
}

// observeGateway judges a manual-mode gateway's tracing against the published configuration. A self-managed
// gateway's configuration is on its host and cannot be read, so it is reported as unobservable rather than
// guessed at.
func (o *Observer) observeGateway(ctx context.Context, app store.Application, serviceName string, dest domaindest.OTLPTarget, haveDest bool) (store.Observation, string, error) {
	prev := app.Observation
	keep := func(msg string) store.Observation {
		return store.Observation{Conformance: prev.Conformance, Missing: prev.Missing, Since: prev.Since, Error: msg}
	}
	if app.TargetType != store.GatewayManaged {
		return keep("a self-managed Omni Gateway keeps its tracing configuration on its host, which Anypoint does not expose, so Odigos cannot confirm it"), "", nil
	}
	d, err := o.client.GetGateway(ctx, o.orgID, app.EnvironmentID, app.ExternalID)
	if err != nil {
		return keep("read gateway: " + anypoint.Summarize(err)), "", fmt.Errorf("observe %s: %s", app.AppName, anypoint.Summarize(err))
	}
	if !haveDest {
		return keep("no OTLP destination is configured, so the export settings cannot be checked"), "", nil
	}
	tracing := d.Tracing()
	verdict, missing := JudgeGateway(tracing, dest, serviceName)
	since := time.Now().UTC()
	if prev.Conformance == verdict && !prev.Since.IsZero() {
		since = prev.Since
	}
	summary := d.Summary()
	return store.Observation{
		Conformance: verdict,
		Missing:     missing,
		Since:       since,
		Settling:    gatewayInFlight(summary) && !gatewayFailed(summary.Status),
	}, store.GatewayOrigin(tracing), nil
}

// JudgeGateway compares a gateway's tracing configuration with what the instructions publish for it. No
// Odigos label on tracing that is turned on is Absent: nothing says the customer applied them.
func JudgeGateway(tracing map[string]any, dest domaindest.OTLPTarget, serviceName string) (baseapi.Conformance, []string) {
	if store.GatewayOrigin(tracing) == "" {
		return baseapi.ConformanceAbsent, nil
	}
	serviceType, address := gatewayExporter(dest)
	p := gatewayProvider(tracing)
	var missing []string
	if p.typ != "opentelemetry" || !strings.EqualFold(fmt.Sprint(p.props["serviceType"]), serviceType) ||
		fmt.Sprint(p.props["address"]) != address {
		missing = append(missing, baseapi.ComponentCollectorExport)
	}
	if fmt.Sprint(p.props["serviceName"]) != serviceName {
		missing = append(missing, baseapi.ComponentServiceName)
	}
	if len(missing) == 0 {
		return baseapi.ConformanceConformant, nil
	}
	slices.Sort(missing)
	return baseapi.ConformancePartial, missing
}
