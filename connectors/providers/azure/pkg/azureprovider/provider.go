// Package azureprovider implements the base.Provider seam for Azure across two domains: WORKLOADS
// (Container Apps — instrumented directly via a Source once instrumentation lands) and COMPUTE
// PLATFORMS (AKS clusters, Virtual Machines — managed infra Odigos is installed on). Each domain has
// its own detector and store package; the base runs them on independent discovery + reconcile loops.
// Which types run is resolved by the base from the spec + the connector's capabilities, so the
// provider never hardcodes its resource list.
package azureprovider

import (
	"context"
	"fmt"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	baseconfig "github.com/odigos-io/odigos-enterprise/connectors/base/config"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/azure/pkg/azureclient"
	computeplatformdetector "github.com/odigos-io/odigos-enterprise/connectors/providers/azure/pkg/computeplatform/detector"
	computeplatformreconciler "github.com/odigos-io/odigos-enterprise/connectors/providers/azure/pkg/computeplatform/reconciler"
	computeplatformstore "github.com/odigos-io/odigos-enterprise/connectors/providers/azure/pkg/computeplatform/store"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/azure/pkg/config"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/azure/pkg/types"
	workloaddetector "github.com/odigos-io/odigos-enterprise/connectors/providers/azure/pkg/workload/detector"
	workloadinstrumentor "github.com/odigos-io/odigos-enterprise/connectors/providers/azure/pkg/workload/instrumentor"
	workloadstore "github.com/odigos-io/odigos-enterprise/connectors/providers/azure/pkg/workload/store"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// Compile-time checks: the Azure provider satisfies base.Provider and the optional startup-verification
// seam (so base.Run enforces the subscription boundary before starting the loops).
var (
	_ baseapi.Provider        = (*Provider)(nil)
	_ baseapi.StartupVerifier = (*Provider)(nil)
)

var azurelog = connlog.Subsystem("azure-provider")

// Provider is the Azure implementation of base.Provider. It holds a workload store and a compute-platform
// store (two store types over one shared base pool) and the shared Azure client factory.
type Provider struct {
	cfg                 config.Config
	cfgMgr              *baseconfig.Manager
	workloadStdb        *workloadstore.Store
	computePlatformStdb *computeplatformstore.Store
	clients             *azureclient.Clients
}

// New builds the Azure provider over the two domain stores and the live config Manager.
func New(cfg config.Config, cfgMgr *baseconfig.Manager, workloadStore *workloadstore.Store, computePlatformStore *computeplatformstore.Store) (*Provider, error) {
	clients, err := azureclient.New(cfg.SubscriptionID, cfg.Credentials)
	if err != nil {
		return nil, fmt.Errorf("azure client factory: %w", err)
	}
	return &Provider{
		cfg:                 cfg,
		cfgMgr:              cfgMgr,
		workloadStdb:        workloadStore,
		computePlatformStdb: computePlatformStore,
		clients:             clients,
	}, nil
}

func (p *Provider) Spec() spec.CloudConnectorSpec { return spec.MustGet("azure") }

// VerifyStartup implements base.StartupVerifier: it enforces the connector's Azure subscription boundary
// at startup (the configured subscription must be readable) so a misconfigured connector cannot discover
// the wrong subscription. It then reconciles connector-name→subscription ownership so a name reused for
// a different subscription starts clean. The base aborts startup if this returns an error.
func (p *Provider) VerifyStartup(ctx context.Context) error {
	if err := p.clients.VerifySubscription(ctx); err != nil {
		return err
	}
	return p.reconcileOwnerSubscription(ctx)
}

// reconcileOwnerSubscription purges the connector's persisted sources/inventory when its name is found
// to be bound to a different Azure subscription than before (a CR deleted and recreated for another
// subscription). Without this, stale enabled sources from the old subscription could match — via the
// service-name fallback — a look-alike workload in the new subscription and instrument it unintentionally.
func (p *Provider) reconcileOwnerSubscription(ctx context.Context) error {
	prev, ok, err := p.workloadStdb.OwnerSubscription(ctx)
	if err != nil {
		return fmt.Errorf("read connector owner subscription: %w", err)
	}
	if ok && prev != p.cfg.SubscriptionID {
		azurelog.Warn("connector name rebound to a different Azure subscription; purging prior-subscription state",
			"connector", p.cfg.ConnectorName, "previous_subscription", prev, "current_subscription", p.cfg.SubscriptionID)
		if err := p.workloadStdb.PurgeConnectorInventory(ctx); err != nil {
			return fmt.Errorf("purge prior-subscription inventory: %w", err)
		}
		if err := p.workloadStdb.PurgeIntent(ctx); err != nil {
			return fmt.Errorf("purge prior-subscription intent: %w", err)
		}
	}
	return p.workloadStdb.SetOwnerSubscription(ctx, p.cfg.SubscriptionID)
}

// SupportedKinds is the union of workload and compute-platform kinds (reported at registration).
func (p *Provider) SupportedKinds() []string {
	return append(p.SupportedWorkloadKinds(), computeplatformstore.SupportedKinds()...)
}

// SupportedWorkloadKinds are directly-instrumentable workload kinds (WorkloadReports + createSource).
func (p *Provider) SupportedWorkloadKinds() []string {
	return workloadstore.SupportedKinds()
}

// SupportedComputePlatformKinds are compute-platform kinds assignable via createSource. Azure
// installation is not supported yet, so this is empty (AKS/VM are discovered but not assignable).
func (p *Provider) SupportedComputePlatformKinds() []string {
	return computeplatformstore.AssignableKinds()
}

// WorkloadDetectors maps workload discovery type names to detectors.
func (p *Provider) WorkloadDetectors() map[string]baseapi.Detector {
	return map[string]baseapi.Detector{
		types.ContainerApps: workloaddetector.NewContainerAppsDetector(p.workloadStdb, p.clients),
	}
}

// ComputePlatformDetectors maps compute-platform discovery type names to detectors.
func (p *Provider) ComputePlatformDetectors() map[string]baseapi.Detector {
	return map[string]baseapi.Detector{
		types.AKS: computeplatformdetector.NewAKSClusterDetector(p.computePlatformStdb, p.clients),
		types.VM:  computeplatformdetector.NewVMDetector(p.computePlatformStdb, p.clients),
	}
}

func (p *Provider) debugTelemetry() bool { return p.cfgMgr.Bool(config.KeyDebugTelemetry, false) }

// Reconcilers maps instrumentation type names to Azure instrumentors. Container Apps injects the
// OpenTelemetry agent into the app revision (destination-gated); the base runs it on the reconcile loop.
func (p *Provider) Reconcilers() map[string]baseapi.Reconciler {
	return map[string]baseapi.Reconciler{
		types.ContainerApps: workloadinstrumentor.NewContainerApps(p.workloadStdb, p.clients, p.cfg.AgentImage, p.debugTelemetry),
	}
}

// WorkloadPlanners implements the optional base.WorkloadPlannerRegistry: what a customer has to apply to
// instrument a Container App themselves, served on the read path.
//
// It is built from the same languageSpecs and agent image the reconciler above uses, so the published
// instructions and the desired state Odigos would apply cannot diverge.
func (p *Provider) WorkloadPlanners() map[string]baseapi.InstrumentationPlanner {
	return map[string]baseapi.InstrumentationPlanner{
		types.ContainerApps: workloadinstrumentor.NewContainerAppsPlanner(p.cfg.AgentImage),
	}
}

// ComputePlatformReconcilers maps compute-platform type names to Azure compute-platform reconcilers.
// Both are inert no-op seams (installation not supported yet; the base never runs them).
func (p *Provider) ComputePlatformReconcilers() map[string]baseapi.ComputePlatformReconciler {
	return map[string]baseapi.ComputePlatformReconciler{
		types.AKS: computeplatformreconciler.NewStubComputePlatform(types.AKS),
		types.VM:  computeplatformreconciler.NewStubComputePlatform(types.VM),
	}
}

func (p *Provider) ListWorkloads(ctx context.Context) ([]baseapi.DiscoveredWorkload, error) {
	return p.workloadStdb.ListWorkloads(ctx)
}

func (p *Provider) ListComputePlatforms(ctx context.Context) ([]baseapi.DiscoveredComputePlatform, error) {
	return p.computePlatformStdb.ListComputePlatforms(ctx)
}

func (p *Provider) LookupWorkload(ctx context.Context, serviceName, kind, region string) (*baseapi.DiscoveredWorkload, error) {
	externalID, foundRegion, found, err := p.workloadStdb.LookupDiscoveredWorkload(ctx, serviceName, kind, region)
	if err != nil || !found {
		return nil, err
	}
	return &baseapi.DiscoveredWorkload{
		ServiceName: serviceName,
		Kind:        kind,
		ExternalID:  externalID,
		Region:      foundRegion,
	}, nil
}

func (p *Provider) LookupComputePlatform(ctx context.Context, name, kind, region string) (*baseapi.DiscoveredComputePlatform, error) {
	externalID, foundRegion, found, err := p.computePlatformStdb.LookupDiscoveredComputePlatform(ctx, name, kind, region)
	if err != nil || !found {
		return nil, err
	}
	return &baseapi.DiscoveredComputePlatform{
		Name:       name,
		Kind:       kind,
		ExternalID: externalID,
		Region:     foundRegion,
	}, nil
}
