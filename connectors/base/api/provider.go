package api

import (
	"context"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorsettings"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// Provider is what a cloud provider implements to get a full connector runtime from the base framework.
// The base owns the loops, the agents-api server, the Central client, and source/destination management;
// the provider supplies the connector definition (spec), per-type discovery (Detectors) and, optionally,
// per-type instrumentation (Reconcilers). The base resolves WHICH types run from the spec + the
// connector's capabilities (base config), so a provider never hardcodes its resource list.
type Provider interface {
	// Spec is the embedded connector definition (connectors/spec); source of truth for supported types.
	Spec() spec.CloudConnectorSpec
	// SupportedKinds are all workload + compute-platform kinds this connector publishes (reported at registration).
	SupportedKinds() []string
	// SupportedWorkloadKinds are directly-instrumentable kinds (WorkloadReports + createSource for workloads).
	SupportedWorkloadKinds() []string
	// SupportedComputePlatformKinds are compute-platform kinds assignable via createSource (agent install on managed infra).
	// Coverage-only compute-platform kinds are discovered but not assignable.
	SupportedComputePlatformKinds() []string

	// WorkloadDetectors maps a workload discovery type name to its detector. Workloads are directly
	// instrumentable; the base runs these in the WORKLOAD discovery loop for the types enabled by the
	// capabilities. One detector instance may be registered under several type names (the base de-dups by
	// identity so it runs once per cycle).
	WorkloadDetectors() map[string]Detector
	// ComputePlatformDetectors maps a compute-platform discovery type name to its detector. Compute platforms are managed infra
	// Odigos is installed on (not instrumented); the base runs these in the separate COMPUTE-PLATFORM discovery
	// loop. May be nil/empty (a compute-platform type may be a declared seam with no detector yet).
	ComputePlatformDetectors() map[string]Detector
	// Reconcilers maps a spec instrumentation type name to its reconciler. May be nil/empty; the base runs
	// only the reconcilers whose type is enabled by the capabilities.
	Reconcilers() map[string]Reconciler
	// ComputePlatformReconcilers maps a spec compute-platform type name to its reconciler. Compute-platform reconcilers manage a
	// separate Odigos agent installed in a discovered environment (assign-to-agent), rather than
	// instrumenting workloads directly; they run in their own loop and are NOT gated on a destination.
	// May be nil/empty.
	ComputePlatformReconcilers() map[string]ComputePlatformReconciler

	// ListWorkloads returns the raw discovered WORKLOAD inventory (the base enriches it with source
	// intent and reports it as WorkloadReports).
	ListWorkloads(ctx context.Context) ([]DiscoveredWorkload, error)
	// ListComputePlatforms returns the discovered COMPUTE-PLATFORM inventory (managed infra), reported as ComputePlatformReports.
	// Compute platforms have no source/destination intent, so the base does not enrich them.
	ListComputePlatforms(ctx context.Context) ([]DiscoveredComputePlatform, error)
	// LookupWorkload finds a single discovered workload by reported service name + kind + region (nil if
	// absent). region may be empty to match by serviceName + kind alone (a deterministic pick when the
	// same name exists in multiple regions). Used to validate a prospective Source against real inventory.
	LookupWorkload(ctx context.Context, serviceName, kind, region string) (*DiscoveredWorkload, error)
	// LookupComputePlatform finds a single discovered compute platform by name + kind + region (nil if absent). Used to
	// validate a prospective Source for compute-platform assignment.
	LookupComputePlatform(ctx context.Context, name, kind, region string) (*DiscoveredComputePlatform, error)
}

// CollectorRoutingProgressReporter is an optional, read-only provider view of deployed workload
// routes. The Collector report uses it to show rollout progress without treating desired routing
// as an applied workload update.
type CollectorRoutingProgressReporter interface {
	CollectorRoutingProgress(context.Context) (map[string]CollectorRoutingProgress, error)
}

type CollectorRoutingProgress struct {
	Instrumented int
	Routed       int
	Failed       int
	FailureRefs  []string
}

// ReconcilePassContextProvider optionally adds provider-owned pass state to the context shared
// by every workload reconciler in one serialized pass. Prerequisites and observers use the
// original context because they do not apply workload changes.
type ReconcilePassContextProvider interface {
	ReconcilePassContext(context.Context) context.Context
}

// Detector enumerates, persists and prunes ONE resource type across the provider's scope (e.g. regions)
// in a single discovery cycle. Keyed by spec discovery type name.
type Detector interface {
	Detect(ctx context.Context) (DetectResult, error)
}

// DetectResult reports per-cycle outcomes the base folds into runtime health.
type DetectResult struct {
	// Authenticated is true when the provider proved its credentials work this cycle.
	Authenticated bool
}

// Reconciler instruments / reconciles ONE instrumentation type in a single reconcile cycle. Keyed by
// spec instrumentation type name. Optional — providers without instrumentation return no Reconcilers.
type Reconciler interface {
	Reconcile(ctx context.Context) error
}

// ComputePlatformReconciler reconciles ONE compute-platform type in a single cycle of the (separate) compute-platform loop: it
// converges the Odigos agent installed in each enabled managed environment and assigns instrumentation
// to it. Keyed by spec compute-platform type name. Optional — providers without compute platforms return no
// ComputePlatformReconcilers. Unlike Reconciler it is not gated on a destination (the installed agent owns OTel
// export).
type ComputePlatformReconciler interface {
	Reconcile(ctx context.Context) error
}

// StartupVerifier is an optional capability a Provider may implement to run a one-time, fail-fast check
// before the runtime starts its loops (e.g. an AWS sts:GetCallerIdentity to enforce the account
// boundary). The base calls it after the schema check and aborts startup if it returns an error, so the
// connector never discovers or mutates resources when its provider preconditions are not satisfied.
type StartupVerifier interface {
	VerifyStartup(ctx context.Context) error
}

// Prerequisite is an optional capability a Detector or Reconciler may implement when its step depends
// on a one-time / idempotent setup that must be in place first. The base ensures it immediately before
// running the step (discovery or instrumentation); if EnsurePrerequisite returns an error the step is
// skipped for that cycle (and the reason recorded) rather than failing partway through. Implementations
// must be idempotent — they run every cycle and should no-op once satisfied.
type Prerequisite interface {
	EnsurePrerequisite(ctx context.Context) error
}

// Managed Collectors. Two interfaces, and the split IS the mutation gate.
//
// CollectorObserver reads and never writes; CollectorDeployer is the only thing that can change a
// customer's infrastructure. The coordinator wires a deployer only for a type returned by
// spec.ActiveCollectorDeploymentTypes — automatic mode — so a manual-mode Collector has no mutating
// interface anywhere in the runtime, including during the shutdown drain. That is a stronger guarantee
// than a mode check inside a deployer, because there is no code path to forget the check in.

// CollectorNetwork describes a regional managed Collector and its host network.
type CollectorNetwork struct {
	// Region keys the managed_collectors row. NetworkID is the VPC/VNet hosting the regional Collector;
	// it is metadata and remains part of the credential binding, but is not row identity.
	Region    string
	NetworkID string
	// ClusterRef is set when an existing ECS service was discovered before its host region was
	// configured. Observers use it to read that service; placement never uses it to deploy.
	ClusterRef string
	// Kind is the collectors type (e.g. "aws.fargate-collector"), which decides the mode.
	Kind string
}

// CollectorObserver reads back what is actually deployed for a collectors type, in BOTH modes.
//
// Optional. A provider that implements it is observed and configured; one that does not is absent from
// the loop. Nothing here may mutate: in manual mode this is the only interface a Collector is reached
// through, and Odigos never touches infrastructure the customer owns.
type CollectorObserver interface {
	// CollectorNetworks lists regional Collectors and their selected host networks, from discovery and
	// placement. Called every pass because regional enablement or host placement can change live.
	CollectorNetworks(ctx context.Context) ([]CollectorNetwork, error)
	// ObserveCollector records one network's actual state. Read-only.
	ObserveCollector(ctx context.Context, network CollectorNetwork) error
}

// CollectorDeployer converges a managed Collector's infrastructure. Wired ONLY for automatic mode.
//
// Per region rather than per pass, so one region's failure is isolated: a VPC with no egress route, or
// a name conflict with an untagged Collector, must not stop every other region from
// being converged.
type CollectorDeployer interface {
	// ConvergeCollector brings one regional Collector in its host network to the desired state.
	ConvergeCollector(ctx context.Context, network CollectorNetwork) error
	// TeardownCollector removes what Odigos created for one regional Collector. Gated per object by
	// collectorownership.MayDelete, so an adopted or customer-named object survives it.
	TeardownCollector(ctx context.Context, network CollectorNetwork) error
}

// CollectorProvider is the optional provider surface for managed Collectors: observers for every
// declared type, and deployers only for the types that can be built.
//
// Keyed by type name, like Reconcilers, so the coordinator can intersect them with the customer's
// capability instead of the provider deciding what is active.
type CollectorProvider interface {
	CollectorObservers() map[string]CollectorObserver
	CollectorDeployers() map[string]CollectorDeployer
}

// CollectorRoutingRefresher refreshes read-only routing evidence after a Collector pass.
// A provider that caches ingress during placement must read it back after convergence
// before workloads use that snapshot. Implementations must not mutate cloud resources.
type CollectorRoutingRefresher interface {
	RefreshCollectorRouting(context.Context) error
}

// CollectorRegionalSettingsValidator checks provider resources before a regional
// declaration is persisted. The patch is sparse on update; providers combine it
// with current settings before validating.
type CollectorRegionalSettingsValidator interface {
	ValidateCollectorRegionalSettings(context.Context, string, collectorsettings.Patch, bool) error
}

// CollectorDisabledRetirer applies a configured retention policy to a region
// that remains declared but has deployment disabled.
type CollectorDisabledRetirer interface {
	RetireDisabledCollector(ctx context.Context, network CollectorNetwork) error
}
