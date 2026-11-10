// Package gcpprovider implements the base.Provider seam for GCP across two domains: WORKLOADS (Cloud Run
// services — instrumented directly via a Source) and COMPUTE PLATFORMS (GKE clusters, Compute Engine
// instances — managed infra Odigos is installed on). Each domain has its own detector and store package;
// the base runs them on independent discovery + reconcile loops. Which types run is resolved by the base
// from the spec + the connector's capabilities, so the provider never hardcodes its resource list.
package gcpprovider

import (
	"context"
	"fmt"

	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	baseconfig "github.com/odigos-io/odigos/connectors/base/config"
	connlog "github.com/odigos-io/odigos/connectors/base/log"
	computeplatformdetector "github.com/odigos-io/odigos/connectors/providers/gcp/pkg/computeplatform/detector"
	computeplatformreconciler "github.com/odigos-io/odigos/connectors/providers/gcp/pkg/computeplatform/reconciler"
	computeplatformstore "github.com/odigos-io/odigos/connectors/providers/gcp/pkg/computeplatform/store"
	"github.com/odigos-io/odigos/connectors/providers/gcp/pkg/config"
	"github.com/odigos-io/odigos/connectors/providers/gcp/pkg/gcpclient"
	"github.com/odigos-io/odigos/connectors/providers/gcp/pkg/types"
	workloaddetector "github.com/odigos-io/odigos/connectors/providers/gcp/pkg/workload/detector"
	workloadinstrumentor "github.com/odigos-io/odigos/connectors/providers/gcp/pkg/workload/instrumentor"
	workloadstore "github.com/odigos-io/odigos/connectors/providers/gcp/pkg/workload/store"
	"github.com/odigos-io/odigos/connectors/spec"
)

// Compile-time checks: the GCP provider satisfies base.Provider and the optional startup-verification
// seam (so base.Run enforces the project boundary before starting the loops).
var (
	_ baseapi.Provider        = (*Provider)(nil)
	_ baseapi.StartupVerifier = (*Provider)(nil)
)

var gcplog = connlog.Subsystem("gcp-provider")

// Provider is the GCP implementation of base.Provider. It holds a workload store and a compute-platform
// store (two store types over one shared base pool) and the shared GCP client factory.
type Provider struct {
	cfg                 config.Config
	cfgMgr              *baseconfig.Manager
	workloadStdb        *workloadstore.Store
	computePlatformStdb *computeplatformstore.Store
	clients             *gcpclient.Clients
	// images reads container image config labels (CNB buildpack metadata) for Cloud Run language
	// detection. Built once from the connector credentials; nil disables the buildpack detection layer.
	images workloadinstrumentor.ImageInspector
}

// New builds the GCP provider over the two domain stores and the live config Manager.
func New(cfg config.Config, cfgMgr *baseconfig.Manager, workloadStore *workloadstore.Store, computePlatformStore *computeplatformstore.Store) *Provider {
	// Build the image inspector up front (the token source is lazy, so this does no network I/O). A failure
	// here only disables the buildpack language-detection layer; the static heuristics still apply.
	images, err := workloadinstrumentor.NewImageInspector(context.Background(), cfg.Credentials)
	if err != nil {
		gcplog.Warn("image inspector init failed; buildpack language detection disabled", "error", err)
		images = nil
	}
	return &Provider{
		cfg:                 cfg,
		cfgMgr:              cfgMgr,
		workloadStdb:        workloadStore,
		computePlatformStdb: computePlatformStore,
		clients:             gcpclient.New(cfg.ProjectID, cfg.Credentials),
		images:              images,
	}
}

func (p *Provider) Spec() spec.CloudConnectorSpec { return spec.MustGet("gcp") }

// debugTelemetry returns the current Cloud Run debug-telemetry toggle from the live config (re-read each
// call so a config change applies without a restart).
func (p *Provider) debugTelemetry() bool { return p.cfgMgr.Bool(config.KeyDebugTelemetry, false) }

// VerifyStartup implements base.StartupVerifier: it enforces the connector's GCP project boundary at
// startup (the configured project must be readable) so a misconfigured connector cannot discover the
// wrong project. It then reconciles connector-name→project ownership so a name reused for a different
// project starts clean. The base aborts startup if this returns an error.
func (p *Provider) VerifyStartup(ctx context.Context) error {
	if err := p.clients.VerifyProject(ctx); err != nil {
		return err
	}
	return p.reconcileOwnerProject(ctx)
}

// reconcileOwnerProject purges the connector's persisted sources/inventory when its name is found to be
// bound to a different GCP project than before (a CR deleted and recreated for another project). Without
// this, stale enabled sources from the old project could match — via the service-name fallback — a
// look-alike workload in the new project and instrument it unintentionally (cross-project leakage).
func (p *Provider) reconcileOwnerProject(ctx context.Context) error {
	prev, ok, err := p.workloadStdb.OwnerProject(ctx)
	if err != nil {
		return fmt.Errorf("read connector owner project: %w", err)
	}
	if ok && prev != p.cfg.ProjectID {
		gcplog.Warn("connector name rebound to a different GCP project; purging prior-project state",
			"connector", p.cfg.ConnectorName, "previous_project", prev, "current_project", p.cfg.ProjectID)
		if err := p.workloadStdb.PurgeConnectorInventory(ctx); err != nil {
			return fmt.Errorf("purge prior-project inventory: %w", err)
		}
		if err := p.workloadStdb.PurgeIntent(ctx); err != nil {
			return fmt.Errorf("purge prior-project intent: %w", err)
		}
	}
	return p.workloadStdb.SetOwnerProject(ctx, p.cfg.ProjectID)
}

// SupportedKinds is the union of workload and compute-platform kinds (reported at registration).
func (p *Provider) SupportedKinds() []string {
	return append(p.SupportedWorkloadKinds(), computeplatformstore.SupportedKinds()...)
}

// SupportedWorkloadKinds are directly-instrumentable workload kinds (WorkloadReports + createSource).
func (p *Provider) SupportedWorkloadKinds() []string {
	return workloadstore.SupportedKinds()
}

// SupportedComputePlatformKinds are compute-platform kinds assignable via createSource. GCP installation
// is not supported yet, so this is empty (GKE/GCE are discovered but not assignable).
func (p *Provider) SupportedComputePlatformKinds() []string {
	return computeplatformstore.AssignableKinds()
}

// WorkloadDetectors maps workload discovery type names to detectors.
func (p *Provider) WorkloadDetectors() map[string]baseapi.Detector {
	return map[string]baseapi.Detector{
		types.CloudRun: workloaddetector.NewCloudRunDetector(p.workloadStdb, p.clients),
	}
}

// ComputePlatformDetectors maps compute-platform discovery type names to detectors.
func (p *Provider) ComputePlatformDetectors() map[string]baseapi.Detector {
	return map[string]baseapi.Detector{
		types.GKE: computeplatformdetector.NewGKEClusterDetector(p.computePlatformStdb, p.clients),
		types.GCE: computeplatformdetector.NewGCEInstanceDetector(p.computePlatformStdb, p.clients),
	}
}

// Reconcilers maps instrumentation type names to GCP instrumentors. Cloud Run injects the OpenTelemetry
// agent into the service revision (destination-gated); the base runs it on the reconcile loop.
func (p *Provider) Reconcilers() map[string]baseapi.Reconciler {
	return map[string]baseapi.Reconciler{
		types.CloudRun: workloadinstrumentor.NewCloudRun(p.workloadStdb, p.clients, p.cfg.AgentImage, p.debugTelemetry, p.images),
	}
}

// WorkloadPlanners implements the optional base.WorkloadPlannerRegistry: what a customer has to apply to
// instrument a Cloud Run service themselves, served on the read path.
//
// It is built from the same languageSpecs and agent image the reconciler above uses. That agreement is the
// property the feature rests on: nothing converges a manual-mode service, so a plan that differs from what
// conformance is judged against produces a customer who applied the instructions correctly and is told their
// deployment has drifted.
func (p *Provider) WorkloadPlanners() map[string]baseapi.InstrumentationPlanner {
	return map[string]baseapi.InstrumentationPlanner{
		types.CloudRun: workloadinstrumentor.NewCloudRunPlanner(p.cfg.AgentImage),
	}
}

// ComputePlatformReconcilers maps compute-platform type names to GCP compute-platform reconcilers. Both
// are inert no-op seams (installation not supported yet; the base never runs them).
func (p *Provider) ComputePlatformReconcilers() map[string]baseapi.ComputePlatformReconciler {
	return map[string]baseapi.ComputePlatformReconciler{
		types.GKE: computeplatformreconciler.NewStubComputePlatform(types.GKE),
		types.GCE: computeplatformreconciler.NewStubComputePlatform(types.GCE),
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
