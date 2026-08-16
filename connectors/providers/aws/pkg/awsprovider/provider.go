// Package awsprovider implements the base.Provider seam for AWS across two domains: WORKLOADS (Lambda,
// Fargate tasks — instrumented directly via a Source) and COMPUTE PLATFORMS (ECS-EC2/Fargate clusters, EKS, EC2 —
// managed infra Odigos is installed on). Each domain has its own detector, store and reconciler packages;
// the base runs them on independent discovery + reconcile loops.
package awsprovider

import (
	"context"
	"fmt"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	baseconfig "github.com/odigos-io/odigos-enterprise/connectors/base/config"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
	cpg "github.com/odigos-io/odigos-enterprise/connectors/postgres"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/awsclient"
	computeplatformdetector "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/computeplatform/detector"
	computeplatformreconciler "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/computeplatform/reconciler"
	computeplatformstore "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/computeplatform/store"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/config"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/types"
	workloaddetector "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/detector"
	workloadinstrumentor "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/instrumentor"
	workloadstore "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// Compile-time checks: the AWS provider satisfies base.Provider and the optional startup-verification
// seam (so base.Run enforces the account boundary before starting the loops).
var (
	_ baseapi.Provider                 = (*Provider)(nil)
	_ baseapi.StartupVerifier          = (*Provider)(nil)
	_ baseapi.WorkloadObserverRegistry = (*Provider)(nil)
)

var awslog = connlog.Subsystem("aws-provider")

// Provider is the AWS implementation of base.Provider. It holds a workload store and a compute-platform store
// (two store types over one shared base pool) and the shared AWS config cache.
type Provider struct {
	cfg                 config.Config
	cfgMgr              *baseconfig.Manager
	workloadStdb        *workloadstore.Store
	computePlatformStdb *computeplatformstore.Store
	cache               *awsclient.Cache
	layers              workloadinstrumentor.LayerResolver
	// ecrImages reads container image configs (via ECR auth or anonymously) for Fargate language
	// detection. Optional layer: the task-definition heuristics and source overrides work without it.
	ecrImages workloadinstrumentor.ImageInspector
}

// New builds the AWS provider over the two domain stores and the live config Manager. By default Lambda
// instrumentation is self-sufficient (discover-or-publish a managed OTel layer); an explicit ARN table
// overrides it.
func New(cfg config.Config, cfgMgr *baseconfig.Manager, workloadStore *workloadstore.Store, computePlatformStore *computeplatformstore.Store) *Provider {
	cache := awsclient.NewCache(cfg.Credentials)
	var layers workloadinstrumentor.LayerResolver = workloadinstrumentor.NewManagedLayerResolver(cache, cfg.LambdaLayerDir)
	if len(cfg.LambdaLayerARNs) > 0 {
		layers = workloadinstrumentor.NewPublicLayerResolver(cfg.LambdaLayerARNs)
	}
	return &Provider{
		cfg: cfg, cfgMgr: cfgMgr, workloadStdb: workloadStore, computePlatformStdb: computePlatformStore,
		cache: cache, layers: layers,
		ecrImages: workloadinstrumentor.NewECRImageInspector(cache),
	}
}

// discoveryTags returns the current discovery tag selectors from the live config (re-read each call so a
// config change applies without a restart).
func (p *Provider) discoveryTags() map[string]string { return p.cfgMgr.Settings().DiscoveryTags }

// debugTelemetry returns the current Lambda debug-telemetry toggle from the live config.
func (p *Provider) debugTelemetry() bool { return p.cfgMgr.Bool(config.KeyDebugLambdaTelemetry, false) }

// debugEcsTelemetry returns the current ECS/Fargate debug-telemetry toggle from the live config.
func (p *Provider) debugEcsTelemetry() bool { return p.cfgMgr.Bool(config.KeyDebugEcsTelemetry, false) }

// SupportedDestinationTypes implements base.DestinationTypesProvider: alongside the default OTLP gRPC
// destination, the AWS connector accepts otlphttp — required by the http/protobuf-only Fargate agents
// (php/ruby) and usable as a fallback by the rest.
func (p *Provider) SupportedDestinationTypes() []string {
	return []string{baseapi.DefaultDestinationType, "otlphttp"}
}

func (p *Provider) Spec() spec.CloudConnectorSpec { return spec.MustGet("aws") }

// VerifyStartup implements base.StartupVerifier: it enforces the connector's AWS account boundary at
// startup (sts:GetCallerIdentity must resolve to the configured account) so a misconfigured connector
// cannot discover or mutate the wrong account. It then reconciles connector-name→account ownership so a
// name reused for a different account starts clean. The base aborts startup if this returns an error.
func (p *Provider) VerifyStartup(ctx context.Context) error {
	if err := p.cache.VerifyAccount(ctx, p.cfg.AccountID); err != nil {
		return err
	}
	return p.reconcileOwnerAccount(ctx)
}

// reconcileOwnerAccount purges the connector's persisted sources/inventory when its name is found to be
// bound to a different AWS account than before (a CR deleted and recreated for another account). Without
// this, stale enabled sources from the old account could match — via the service-name fallback — a
// look-alike workload in the new account and instrument it unintentionally (cross-account leakage).
func (p *Provider) reconcileOwnerAccount(ctx context.Context) error {
	prev, ok, err := p.workloadStdb.OwnerAccount(ctx)
	if err != nil {
		return fmt.Errorf("read connector owner account: %w", err)
	}
	if ok && prev != p.cfg.AccountID {
		awslog.Warn("connector name rebound to a different AWS account; purging prior-account state",
			"connector", p.cfg.ConnectorName, "previous_account", prev, "current_account", p.cfg.AccountID)
		if err := p.workloadStdb.PurgeConnectorInventory(ctx); err != nil {
			return fmt.Errorf("purge prior-account inventory: %w", err)
		}
		if err := p.workloadStdb.PurgeIntent(ctx); err != nil {
			return fmt.Errorf("purge prior-account intent: %w", err)
		}
	}
	return p.workloadStdb.SetOwnerAccount(ctx, p.cfg.AccountID)
}

// SupportedKinds is the union of workload and compute-platform kinds (reported at registration).
func (p *Provider) SupportedKinds() []string {
	return append(p.SupportedWorkloadKinds(), computeplatformstore.SupportedKinds()...)
}

// SupportedWorkloadKinds are directly-instrumentable workload kinds (WorkloadReports + createSource).
func (p *Provider) SupportedWorkloadKinds() []string {
	return workloadstore.SupportedKinds()
}

// SupportedComputePlatformKinds are compute-platform kinds assignable via createSource (agent install on managed infra).
func (p *Provider) SupportedComputePlatformKinds() []string {
	return computeplatformstore.AssignableKinds()
}

// WorkloadDetectors maps workload discovery type names to detectors.
func (p *Provider) WorkloadDetectors() map[string]baseapi.Detector {
	return map[string]baseapi.Detector{
		types.Lambda:      workloaddetector.NewLambdaDetector(p.workloadStdb, p.cache, p.discoveryTags),
		types.FargateTask: workloaddetector.NewECSTaskDetector(p.workloadStdb, p.cache, p.discoveryTags),
	}
}

// ComputePlatformDetectors maps compute-platform discovery type names to detectors. One ECS cluster detector serves both
// aws.ecs-cluster and aws.fargate-cluster (the base de-dups by identity). aws.eks/aws.ec2 are declared
// seams with no detector yet.
func (p *Provider) ComputePlatformDetectors() map[string]baseapi.Detector {
	ecs := computeplatformdetector.NewECSClusterDetector(p.computePlatformStdb, p.cache)
	return map[string]baseapi.Detector{
		types.ECSCluster:     ecs,
		types.FargateCluster: ecs,
	}
}

// Reconcilers maps instrumentation type names to AWS instrumentors (workloads the connector instruments
// directly; destination-gated). Lambda attaches an OTel layer to the function; Fargate registers an
// instrumented task-definition revision (agent sidecar + shared volume + activation/OTLP env) and rolls
// the service to it.
func (p *Provider) Reconcilers() map[string]baseapi.Reconciler {
	return map[string]baseapi.Reconciler{
		types.Lambda:      workloadinstrumentor.NewLambda(p.workloadStdb, p.cache, p.layers, p.debugTelemetry),
		types.FargateTask: workloadinstrumentor.NewECSFargate(p.workloadStdb, p.cache, p.cfg.FargateAgentsImage, p.debugEcsTelemetry, p.ecrImages),
	}
}

// WorkloadObservers implements the optional base.WorkloadObserverRegistry: read-only conformance observers,
// which the base runs ONLY for instrumentation types whose mode is manual (IaC). They are the manual-mode
// counterpart to Reconcilers above and share its comparator, so both modes judge "correctly instrumented"
// by one definition.
//
// Lambda only, for now. Fargate, Cloud Run and Container Apps each report marker presence but not conformance
// until their own observers land (plan waves 4.2-4.4); the base warns for a manual-mode type with no observer
// rather than letting an unobserved type look observed.
func (p *Provider) WorkloadObservers() map[string]baseapi.InstrumentationObserver {
	return map[string]baseapi.InstrumentationObserver{
		types.Lambda: workloadinstrumentor.NewLambdaObserver(p.workloadStdb, p.cache, p.layers, p.debugTelemetry),
	}
}

// ComputePlatformReconcilers maps compute-platform type names to AWS compute-platform reconcilers (NOT destination-gated).
func (p *Provider) ComputePlatformReconcilers() map[string]baseapi.ComputePlatformReconciler {
	central := computeplatformreconciler.CentralParams{
		BackendURL:    p.cfg.CentralBackendURL,
		ConnectorName: p.cfg.ConnectorName,
		Version:       p.cfg.Version,
		SchemaVersion: cpg.AWSExpectedVersion(),
	}
	return map[string]baseapi.ComputePlatformReconciler{
		types.ECSCluster: computeplatformreconciler.NewECSEC2Agent(p.computePlatformStdb, p.cache, central, p.cfg.ECSAgentImage),
		types.EKS:        computeplatformreconciler.NewStubComputePlatform(types.EKS),
		types.EC2:        computeplatformreconciler.NewStubComputePlatform(types.EC2),
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
