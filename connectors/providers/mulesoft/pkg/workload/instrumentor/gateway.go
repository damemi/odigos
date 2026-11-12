package instrumentor

import (
	"context"
	"fmt"
	"strings"

	"github.com/odigos-io/odigos/connectors/anypoint"
	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	domaindest "github.com/odigos-io/odigos/connectors/base/domain/destination"
	store "github.com/odigos-io/odigos/connectors/providers/mulesoft/pkg/workload/store"
)

// Omni Gateway instrumentation. A gateway is instrumented by turning on its own distributed tracing with an
// OpenTelemetry provider pointed at the connector's destination — the gateway exports its spans itself, the
// way a Mule runtime's Direct Telemetry Stream does. Only a Managed Omni Gateway's configuration is writable
// through Gateway Manager.
//
// The field names under tracing.provider follow the documented Configuration resource (type, and properties
// serviceType, address, serviceName, timeout); Gateway Manager's own JSON for them is not published
// (unverified). Every write is therefore read back, and one Anypoint did not keep is undone and reported.

// gatewayTraceTimeout is the export timeout, in seconds, the provider is given.
const gatewayTraceTimeout = 10

// GatewayConflictError is returned for a gateway that already traces somewhere Odigos did not set up. Turning
// Odigos's provider on would take that tracing away from wherever the customer sends it.
type GatewayConflictError struct{ Provider string }

func (e *GatewayConflictError) Error() string {
	where := "Anypoint Monitoring"
	if e.Provider != "" && e.Provider != "anypoint" {
		where = "a " + e.Provider + " provider"
	}
	return fmt.Sprintf("the gateway already has distributed tracing turned on (to %s); Odigos does not change tracing it did not set up. Turn it off, or use manual mode", where)
}

// desiredGatewayTracing is the tracing configuration Odigos wants on a gateway: the current one, with tracing
// on, the OpenTelemetry provider pointed at the destination and Odigos's label added. Sampling and the
// customer's own labels are left as they are.
func desiredGatewayTracing(current map[string]any, dest domaindest.OTLPTarget, serviceName string) map[string]any {
	t := map[string]any{}
	for k, v := range current {
		t[k] = v
	}
	t["enabled"] = true
	serviceType, address := gatewayExporter(dest)
	t["provider"] = map[string]any{
		"type": "opentelemetry",
		"properties": map[string]any{
			"serviceType": serviceType,
			"address":     address,
			"serviceName": serviceName,
			"timeout":     gatewayTraceTimeout,
		},
	}
	t["labels"] = withGatewayLabel(current, store.GatewayManagedLabel, "true")
	return t
}

// gatewayExporter is the provider's service type and address for a destination. An OTLP/HTTP address carries
// the traces path, as the gateway's HTTP exporter expects.
func gatewayExporter(dest domaindest.OTLPTarget) (serviceType, address string) {
	typ, endpoint := exporterFor(dest)
	if typ == "HTTP" {
		return "http", endpoint + "/v1/traces"
	}
	return "grpc", endpoint
}

func withGatewayLabel(tracing map[string]any, name, value string) []any {
	out := []any{}
	labels, _ := tracing["labels"].([]any)
	for _, l := range labels {
		if m, ok := l.(map[string]any); ok && m["name"] == name {
			continue
		}
		out = append(out, l)
	}
	return append(out, map[string]any{"name": name, "type": "literal", "defaultValue": value})
}

func withoutGatewayLabel(tracing map[string]any, name string) []any {
	out := []any{}
	labels, _ := tracing["labels"].([]any)
	for _, l := range labels {
		if m, ok := l.(map[string]any); ok && m["name"] == name {
			continue
		}
		out = append(out, l)
	}
	return out
}

// gatewayTracingMatches reports whether a gateway's tracing already does what want asks: on, exporting with
// the same provider, service type, address and service name, and carrying Odigos's label. Fields Gateway
// Manager may normalize (the timeout, its units) are not compared.
func gatewayTracingMatches(got, want map[string]any) bool {
	if enabled, _ := got["enabled"].(bool); !enabled {
		return false
	}
	if !store.GatewayHasLabel(got, store.GatewayManagedLabel) {
		return false
	}
	gp, wp := gatewayProvider(got), gatewayProvider(want)
	if gp.typ != wp.typ {
		return false
	}
	for _, k := range []string{"serviceType", "address", "serviceName"} {
		if !strings.EqualFold(fmt.Sprint(gp.props[k]), fmt.Sprint(wp.props[k])) {
			return false
		}
	}
	return true
}

type providerView struct {
	typ   string
	props map[string]any
}

func gatewayProvider(tracing map[string]any) providerView {
	p, _ := tracing["provider"].(map[string]any)
	props, _ := p["properties"].(map[string]any)
	typ, _ := p["type"].(string)
	return providerView{typ: strings.ToLower(typ), props: props}
}

// rollbackGatewayTracing is the configuration a rollback writes: the snapshot taken before Odigos's first
// change, or, without one (the connector's database was lost), the live configuration with tracing turned off
// and Odigos's provider and label taken out.
func rollbackGatewayTracing(current map[string]any, snap *store.GatewaySnapshot) map[string]any {
	if snap != nil {
		return snap.Tracing
	}
	t := map[string]any{}
	for k, v := range current {
		t[k] = v
	}
	t["enabled"] = false
	delete(t, "provider")
	t["labels"] = withoutGatewayLabel(current, store.GatewayManagedLabel)
	return t
}

// gatewayTransitions are the Gateway Manager statuses of a gateway in the middle of a change. A healthy managed
// gateway reports RUNNING with ready and running both true (verified against a live organization), so a
// status counts as a change only when it names one; a guess from its spelling would read RUNNING as one too.
var gatewayTransitions = map[string]bool{
	"APPLYING": true, "DEPLOYING": true, "UPDATING": true, "STARTING": true, "RESTARTING": true, "PENDING": true,
	"PROVISIONING": true, "CREATING": true, "STOPPING": true, "DELETING": true, "SCALING": true,
}

// gatewayInFlight reports whether a gateway is still changing: a transitional status, or one Gateway Manager
// says is not ready yet.
func gatewayInFlight(g anypoint.Gateway) bool {
	if gatewayTransitions[strings.ToUpper(strings.TrimSpace(g.Status))] {
		return true
	}
	return g.Ready != nil && !*g.Ready && !gatewayFailed(g.Status)
}

func gatewayFailed(status string) bool { return strings.Contains(strings.ToUpper(status), "FAIL") }

func (r *AMC) instrumentGateway(ctx context.Context, app store.Application, serviceName string, dest domaindest.OTLPTarget, _ float64, budget *int) error {
	if app.TargetType != store.GatewayManaged {
		return fmt.Errorf("%s is a self-managed Omni Gateway: its tracing is configured on the gateway's host, which Odigos cannot reach. Switch this type to manual mode and apply the published Configuration resource", app.AppName)
	}
	d, err := r.client.GetGateway(ctx, r.orgID, app.EnvironmentID, app.ExternalID)
	if err != nil {
		return fmt.Errorf("read gateway: %s", anypoint.Summarize(err))
	}
	current := d.Tracing()
	owned := store.GatewayHasLabel(current, store.GatewayManagedLabel)
	if enabled, _ := current["enabled"].(bool); enabled && !owned {
		return &GatewayConflictError{Provider: gatewayProvider(current).typ}
	}
	want := desiredGatewayTracing(current, dest, serviceName)
	summary := d.Summary()
	status := summary.Status
	if gatewayTracingMatches(current, want) {
		r.setInstrumented(ctx, app, store.OriginConnector)
		if gatewayInFlight(summary) {
			r.setPhase(ctx, app, baseapi.PhaseVerifying)
		} else {
			r.clearPhase(ctx, app)
		}
		return nil
	}
	switch {
	case gatewayFailed(status):
		return fmt.Errorf("the gateway is in a failed state; Odigos changes it once it runs again")
	case gatewayInFlight(summary):
		return fmt.Errorf("%w: gateway update in progress (status %s)", errDeferred, status)
	case *budget <= 0:
		return fmt.Errorf("%w: redeploy budget for this pass is spent", errDeferred)
	}

	r.markDirty(app.ExternalID)
	// The snapshot is written before the gateway is: a crash between the two must still leave a rollback that
	// restores what the customer had.
	wroteSnapshot := false
	if !owned && app.GatewaySnapshot == nil {
		if err := r.store.SetGatewaySnapshot(ctx, app.ExternalID, &store.GatewaySnapshot{Tracing: current}); err != nil {
			return fmt.Errorf("record the gateway's tracing before changing it: %w", err)
		}
		wroteSnapshot = true
	}
	// A write that did not stick changed nothing, so the snapshot taken for it describes nothing Odigos owns. Left
	// behind, it would be what a later rollback restores, however the customer changed the gateway since.
	dropSnapshot := func() {
		if wroteSnapshot {
			if err := r.store.SetGatewaySnapshot(ctx, app.ExternalID, nil); err != nil {
				log.Warn("clear gateway snapshot failed", "gateway", app.AppName, "error", err)
			}
		}
	}
	log.Info("turning on Omni Gateway tracing", "gateway", app.AppName, "environment", app.EnvironmentName)
	r.setPhase(ctx, app, baseapi.PhaseInstrumenting)
	// undo forgets this attempt: its phase (set just above, so the row in hand does not have it) and its snapshot.
	undo := func() {
		if err := r.store.ClearInstrumentationPhase(ctx, app.ExternalID); err != nil {
			log.Warn("clear instrumentation phase failed", "gateway", app.AppName, "error", err)
		}
		dropSnapshot()
	}
	if err := r.client.UpdateGateway(ctx, r.orgID, app.EnvironmentID, app.ExternalID, d.UpdateBody(want)); err != nil {
		undo()
		return fmt.Errorf("update gateway: %s", anypoint.Summarize(err))
	}
	*budget--

	// Read it back: Gateway Manager's JSON for a custom provider is not published, and one that dropped it would
	// leave tracing turned on toward Anypoint Monitoring instead of Odigos.
	after, err := r.client.GetGateway(ctx, r.orgID, app.EnvironmentID, app.ExternalID)
	if err != nil {
		return fmt.Errorf("read gateway after update: %s", anypoint.Summarize(err))
	}
	if !gatewayTracingMatches(after.Tracing(), want) {
		if err := r.client.UpdateGateway(ctx, r.orgID, app.EnvironmentID, app.ExternalID, after.UpdateBody(current)); err != nil {
			return fmt.Errorf("Anypoint did not keep the OpenTelemetry tracing provider Odigos set, and putting the gateway's tracing back failed: %s", anypoint.Summarize(err))
		}
		undo()
		return fmt.Errorf("Anypoint did not keep the OpenTelemetry tracing provider Odigos set (Gateway Manager may not accept a custom provider through its API for this organization yet); the gateway's tracing was put back")
	}
	r.setInstrumented(ctx, app, store.OriginConnector)
	return nil
}

func (r *AMC) rollbackGateway(ctx context.Context, app store.Application) error {
	d, err := r.client.GetGateway(ctx, r.orgID, app.EnvironmentID, app.ExternalID)
	if err != nil {
		if anypoint.IsNotFound(err) {
			r.setInstrumented(ctx, app, "")
			r.clearGatewaySnapshot(ctx, app)
			return nil
		}
		return fmt.Errorf("read gateway: %s", anypoint.Summarize(err))
	}
	current := d.Tracing()
	if !store.GatewayHasLabel(current, store.GatewayManagedLabel) {
		r.setInstrumented(ctx, app, store.GatewayOrigin(current))
		r.markClean(app.ExternalID)
		return nil
	}
	if summary := d.Summary(); gatewayInFlight(summary) {
		return fmt.Errorf("%w: gateway update in progress (status %s)", errDeferred, summary.Status)
	}
	log.Info("rolling back Omni Gateway tracing", "gateway", app.AppName, "environment", app.EnvironmentName)
	if err := r.client.UpdateGateway(ctx, r.orgID, app.EnvironmentID, app.ExternalID, d.UpdateBody(rollbackGatewayTracing(current, app.GatewaySnapshot))); err != nil {
		return fmt.Errorf("update gateway: %s", anypoint.Summarize(err))
	}
	r.clearGatewaySnapshot(ctx, app)
	r.setInstrumented(ctx, app, "")
	r.clearPhase(ctx, app)
	r.markClean(app.ExternalID)
	return nil
}

func (r *AMC) clearGatewaySnapshot(ctx context.Context, app store.Application) {
	if app.GatewaySnapshot == nil {
		return
	}
	if err := r.store.SetGatewaySnapshot(ctx, app.ExternalID, nil); err != nil {
		log.Warn("clear gateway snapshot failed", "gateway", app.AppName, "error", err)
	}
}
