package instrumentor

import (
	"context"

	"github.com/odigos-io/odigos-enterprise/connectors/anypoint"
	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	store "github.com/odigos-io/odigos-enterprise/connectors/providers/mulesoft/pkg/workload/store"
)

// plannerInventory is what the planner reads: the connector's own database, never Anypoint.
type plannerInventory interface {
	ApplicationByID(ctx context.Context, externalID string) (store.Application, bool, error)
	OTLPExportTarget(ctx context.Context) (domaindest.OTLPTarget, bool, error)
}

// Planner serves the manual-mode instructions for one discovered Mule application: the property set, with the
// destination's protocol and per-signal endpoints, the environment, organization and target type filled in.
//
// It runs on the GraphQL read path when a client opens a workload, so it reads only the inventory discovery
// wrote and makes no Anypoint calls.
type Planner struct {
	store    plannerInventory
	orgID    string
	settings func() Settings
}

// NewPlanner builds the planner.
func NewPlanner(st plannerInventory, orgID string, settings func() Settings) *Planner {
	return &Planner{store: st, orgID: orgID, settings: settings}
}

// PlanFor implements baseapi.InstrumentationPlanner.
func (p *Planner) PlanFor(ctx context.Context, w baseapi.DiscoveredWorkload) (*baseapi.PlannedInstrumentation, error) {
	if w.InstrumentationType == "" {
		return nil, nil
	}
	if w.InstrumentationType == store.KindOmniGateway {
		dest, haveDest, err := p.store.OTLPExportTarget(ctx)
		if err != nil {
			haveDest = false
		}
		pl, err := GatewayManualPlan(w.InstrumentationType)
		if err != nil {
			return nil, err
		}
		return &baseapi.PlannedInstrumentation{Plan: pl, Resolved: GatewayManualValues(dest, haveDest)}, nil
	}
	// No plan for a runtime that cannot run Direct Telemetry Stream: the report already says to upgrade, and
	// instructions for it would be properties the runtime ignores.
	if v, ok := anypoint.ParseMuleVersion(w.RuntimeVersion); ok && !Eligible(v) {
		return nil, nil
	}
	dest, haveDest, err := p.store.OTLPExportTarget(ctx)
	if err != nil {
		haveDest = false
	}
	pl, err := ManualPlan(w.InstrumentationType, SignalsFor(dest, haveDest), metricsSupported(w.RuntimeVersion))
	if err != nil {
		return nil, err
	}

	app, found, err := p.store.ApplicationByID(ctx, w.ExternalID)
	if err != nil {
		return nil, err
	}
	if !found {
		app = store.Application{ExternalID: w.ExternalID, EnvironmentName: w.Region}
	}
	return &baseapi.PlannedInstrumentation{
		Plan:     pl,
		Resolved: ManualValues(app, p.orgID, dest, haveDest, p.settings().SampleRatio),
	}, nil
}
