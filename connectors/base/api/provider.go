package api

import (
	"context"

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
