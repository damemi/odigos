// Package mulesoftprovider implements the base.Provider seam for MuleSoft. It has one domain, WORKLOADS: Mule
// applications on CloudHub 2.0, Runtime Fabric and hybrid servers. There are no compute platforms — the
// platforms Mule apps run on are MuleSoft-managed or reached only through Runtime Manager.
package mulesoftprovider

import (
	"context"
	"errors"
	"fmt"

	"github.com/odigos-io/odigos/connectors/anypoint"
	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	baseconfig "github.com/odigos-io/odigos/connectors/base/config"
	connlog "github.com/odigos-io/odigos/connectors/base/log"
	"github.com/odigos-io/odigos/connectors/providers/mulesoft/pkg/config"
	"github.com/odigos-io/odigos/connectors/providers/mulesoft/pkg/types"
	workloaddetector "github.com/odigos-io/odigos/connectors/providers/mulesoft/pkg/workload/detector"
	workloadinstrumentor "github.com/odigos-io/odigos/connectors/providers/mulesoft/pkg/workload/instrumentor"
	workloadstore "github.com/odigos-io/odigos/connectors/providers/mulesoft/pkg/workload/store"
	"github.com/odigos-io/odigos/connectors/spec"
)

var (
	_ baseapi.Provider                 = (*Provider)(nil)
	_ baseapi.StartupVerifier          = (*Provider)(nil)
	_ baseapi.DestinationTypesProvider = (*Provider)(nil)
	_ baseapi.WorkloadPlannerRegistry  = (*Provider)(nil)
	_ baseapi.WorkloadObserverRegistry = (*Provider)(nil)
)

var mlog = connlog.Subsystem("mulesoft-provider")

// Provider is the MuleSoft implementation of base.Provider.
type Provider struct {
	cfg    config.Config
	cfgMgr *baseconfig.Manager
	stdb   *workloadstore.Store
	client *anypoint.Client
	// scanner is shared by the CloudHub 2.0 and Runtime Fabric detectors so one cycle reads each deployment once.
	scanner *workloaddetector.AMCScanner
}

// New builds the provider over the workload store and the live config Manager.
func New(cfg config.Config, cfgMgr *baseconfig.Manager, st *workloadstore.Store) *Provider {
	client := anypoint.New(anypoint.Config{BaseURL: cfg.BaseURL, ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret})
	return &Provider{
		cfg:     cfg,
		cfgMgr:  cfgMgr,
		stdb:    st,
		client:  client,
		scanner: workloaddetector.NewAMCScanner(client, cfg.OrganizationID),
	}
}

func (p *Provider) Spec() spec.CloudConnectorSpec { return spec.MustGet("mulesoft") }

// SupportedDestinationTypes implements base.DestinationTypesProvider. Mule's exporter speaks both OTLP gRPC and
// OTLP/HTTP, and HTTP is the one that survives what sits between CloudHub and a collector most often — an HTTPS
// load balancer, or a tunnel that carries no gRPC.
func (p *Provider) SupportedDestinationTypes() []string {
	return []string{baseapi.DefaultDestinationType, "otlphttp"}
}

// VerifyStartup enforces the organization boundary: the Connected App must be able to list the configured
// organization's environments, so a connector pointed at an organization its credentials cannot see fails at
// startup instead of discovering nothing. It then purges state left by a previous organization under the same
// connector name.
func (p *Provider) VerifyStartup(ctx context.Context) error {
	if _, err := p.client.Environments(ctx, p.cfg.OrganizationID); err != nil && !errors.Is(err, anypoint.ErrIncomplete) {
		return fmt.Errorf("verify Anypoint organization %s: %s", p.cfg.OrganizationID, anypoint.Summarize(err))
	}
	prev, ok, err := p.stdb.OwnerOrganization(ctx)
	if err != nil {
		return fmt.Errorf("read connector owner organization: %w", err)
	}
	if ok && prev != p.cfg.OrganizationID {
		mlog.Warn("connector name rebound to a different Anypoint organization; purging prior state",
			"connector", p.cfg.ConnectorName, "previous_org", prev, "current_org", p.cfg.OrganizationID)
		if err := p.stdb.PurgeConnectorInventory(ctx); err != nil {
			return fmt.Errorf("purge prior-organization inventory: %w", err)
		}
		if err := p.stdb.PurgeIntent(ctx); err != nil {
			return fmt.Errorf("purge prior-organization intent: %w", err)
		}
	}
	return p.stdb.SetOwnerOrganization(ctx, p.cfg.OrganizationID)
}

func (p *Provider) SupportedKinds() []string                { return workloadstore.SupportedKinds() }
func (p *Provider) SupportedWorkloadKinds() []string        { return workloadstore.SupportedKinds() }
func (p *Provider) SupportedComputePlatformKinds() []string { return nil }

// WorkloadDetectors maps workload discovery types to detectors. CloudHub 2.0 shared spaces, private spaces and
// Runtime Fabric get separate detectors over one shared scan, so each prunes only its own kind.
func (p *Provider) WorkloadDetectors() map[string]baseapi.Detector {
	return map[string]baseapi.Detector{
		types.CloudHub2:    workloaddetector.NewAMCDetector(types.CloudHub2, p.scanner, p.stdb),
		types.PrivateSpace: workloaddetector.NewAMCDetector(types.PrivateSpace, p.scanner, p.stdb),
		types.RTF:          workloaddetector.NewAMCDetector(types.RTF, p.scanner, p.stdb),
		types.Hybrid:       workloaddetector.NewHybridDetector(p.client, p.cfg.OrganizationID, p.stdb),
		types.OmniGateway:  workloaddetector.NewGatewayDetector(p.client, p.cfg.OrganizationID, p.stdb),
	}
}

func (p *Provider) ComputePlatformDetectors() map[string]baseapi.Detector { return nil }

// Reconcilers maps instrumentation types to reconcilers (automatic mode only; the base wires none for a
// manual-mode type). Hybrid has none: Direct Telemetry Stream is configured per server there.
//
// Private spaces and Runtime Fabric are registered here, in the planners and in the observers, but the spec
// declares their instrumentation implemented: false until it is verified on a real organization, and the base
// wires nothing for an unimplemented type. Enabling them is a spec change.
func (p *Provider) Reconcilers() map[string]baseapi.Reconciler {
	return map[string]baseapi.Reconciler{
		types.CloudHub2:    workloadinstrumentor.NewAMC(types.CloudHub2, p.stdb, p.client, p.cfg.OrganizationID, p.settings),
		types.PrivateSpace: workloadinstrumentor.NewAMC(types.PrivateSpace, p.stdb, p.client, p.cfg.OrganizationID, p.settings),
		types.RTF:          workloadinstrumentor.NewAMC(types.RTF, p.stdb, p.client, p.cfg.OrganizationID, p.settings),
		// An Omni Gateway's tracing is changed through Gateway Manager, not Application Manager; the same
		// reconciler loop runs gateway steps for it (see instrumentor/gateway.go).
		types.OmniGateway: workloadinstrumentor.NewAMC(types.OmniGateway, p.stdb, p.client, p.cfg.OrganizationID, p.settings),
	}
}

// settings reads the live operational knobs, so a config change applies on the next pass without a restart.
func (p *Provider) settings() workloadinstrumentor.Settings {
	eff := p.cfgMgr.Effective()
	return workloadinstrumentor.Settings{
		SampleRatio:  config.Float(eff, config.KeyTraceSampleRatio, config.DefaultTraceSampleRatio, 0, 1),
		MaxRedeploys: int(config.Float(eff, config.KeyMaxRedeploysPerCycle, config.DefaultMaxRedeploysPerCycle, 1, 1000)),
	}
}

// WorkloadPlanners implements base.WorkloadPlannerRegistry: the manual-mode instructions for one application,
// rendered with its own values. Built from the same property set the observer judges against.
func (p *Provider) WorkloadPlanners() map[string]baseapi.InstrumentationPlanner {
	planner := workloadinstrumentor.NewPlanner(p.stdb, p.cfg.OrganizationID, p.settings)
	return map[string]baseapi.InstrumentationPlanner{
		types.CloudHub2:    planner,
		types.PrivateSpace: planner,
		types.RTF:          planner,
		types.OmniGateway:  planner,
	}
}

// WorkloadObservers implements base.WorkloadObserverRegistry. The base runs these only for a type in manual
// mode, where they turn "the customer set ODIGOS_IAC" into a Conformant or Partial verdict.
func (p *Provider) WorkloadObservers() map[string]baseapi.InstrumentationObserver {
	return map[string]baseapi.InstrumentationObserver{
		types.CloudHub2:    workloadinstrumentor.NewObserver(types.CloudHub2, p.stdb, p.client, p.cfg.OrganizationID, p.settings),
		types.PrivateSpace: workloadinstrumentor.NewObserver(types.PrivateSpace, p.stdb, p.client, p.cfg.OrganizationID, p.settings),
		types.RTF:          workloadinstrumentor.NewObserver(types.RTF, p.stdb, p.client, p.cfg.OrganizationID, p.settings),
		types.OmniGateway:  workloadinstrumentor.NewObserver(types.OmniGateway, p.stdb, p.client, p.cfg.OrganizationID, p.settings),
	}
}

func (p *Provider) ComputePlatformReconcilers() map[string]baseapi.ComputePlatformReconciler {
	return nil
}

func (p *Provider) ListWorkloads(ctx context.Context) ([]baseapi.DiscoveredWorkload, error) {
	return p.stdb.ListWorkloads(ctx)
}

func (p *Provider) ListComputePlatforms(context.Context) ([]baseapi.DiscoveredComputePlatform, error) {
	return nil, nil
}

func (p *Provider) LookupWorkload(ctx context.Context, serviceName, kind, region string) (*baseapi.DiscoveredWorkload, error) {
	externalID, env, found, err := p.stdb.LookupDiscoveredWorkload(ctx, serviceName, kind, region)
	if err != nil || !found {
		return nil, err
	}
	return &baseapi.DiscoveredWorkload{ServiceName: serviceName, Kind: kind, ExternalID: externalID, Region: env}, nil
}

func (p *Provider) LookupComputePlatform(context.Context, string, string, string) (*baseapi.DiscoveredComputePlatform, error) {
	return nil, nil
}
