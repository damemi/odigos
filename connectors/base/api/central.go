// Package api defines the GraphQL-agnostic domain contract the connector runtime serves. The gql
// package converts between these domain types and the agents-api generated model; the coordinator
// implements CentralInterface. This mirrors the vm-agent's central.CentralInterface split.
//
// Discovered cloud resources are surfaced as WorkloadReports (the same object the k8s/VM runtimes
// publish), so Central renders instrumentation-applicable workloads uniformly — there is no bespoke
// "candidate" type.
package api

import (
	"context"
	"time"

	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	domainsrc "github.com/odigos-io/odigos-enterprise/connectors/base/domain/source"
)

// SourceOverrides carries matched-source configuration overrides surfaced on WorkloadReports.
// ServiceName overrides OTel service.name when set; it does not change workload identity.
type SourceOverrides struct {
	ServiceName       string
	RuntimeLanguage   string
	RuntimeVersion    string
	OtelDistroName    string
}

// HasRuntimeOverride reports whether a runtime language or version override is configured.
func (o SourceOverrides) HasRuntimeOverride() bool {
	return o.RuntimeLanguage != "" || o.RuntimeVersion != ""
}

// DiscoveredWorkload is a discovered cloud resource, reported to Central as a WorkloadReport.
type DiscoveredWorkload struct {
	// ServiceName + Kind + Region form the WorkloadReport composite key (Region is empty for
	// region-less platforms).
	ServiceName string
	Kind        string
	Region      string
	// ExternalID is the provider resource id (e.g. a cloud resource ARN); used later to create a Source.
	ExternalID string
	// RuntimeLanguage is the detected runtime language (e.g. "java", "python", "nodejs"); empty when unknown.
	RuntimeLanguage string
	// RuntimeVersion is the detected runtime version (e.g. "17", "3.11", "20.x"); empty when unknown.
	RuntimeVersion string
	// Instrumented reports whether Odigos instrumentation is currently applied to the workload.
	Instrumented bool
	// InstrumentationError is set when instrumentation or rollback last failed for this workload; empty
	// when healthy or not yet attempted.
	InstrumentationError string
	// SourceMatched is true when a source intent row exists for this workload.
	SourceMatched bool
	// SourceEnabled mirrors the linked source's enabled flag when SourceMatched is true.
	SourceEnabled bool
	// Overrides carries matched-source config (service name, runtime, distro) for reporting and
	// instrumentation; zero when no source is matched.
	Overrides SourceOverrides
	// InstrumentationType is the connector-spec instrumentation type for this workload; empty when the
	// workload's kind has no instrumentation type. Set by the provider.
	InstrumentationType string
	// InstrumentationPhase is the live progress phase the instrumentor last recorded for this workload
	// (e.g. "Verifying", "LayerReady", "Instrumenting", "Instrumented"); empty when no phase has been
	// recorded yet. Set by the provider so a WorkloadReport reflects instrumentation progress on the fly.
	InstrumentationPhase string
	// InstrumentationAvailable is true when InstrumentationType is an active instrumentation type for
	// this connector (spec feature enabled + capability allowed). Set by the coordinator. When a
	// workload is marked for instrumentation but this is false, it is reported as Unknown (not an
	// error): the connector has no instrumentation defined for it.
	InstrumentationAvailable bool
	// InstrumentationImplemented is false when InstrumentationType is declared in the spec but its
	// instrumentation is not built in this release (implemented:false). Set by the coordinator. A
	// source-matched workload of such a type is reported as NotImplemented rather than as instrumented,
	// so an unsupported capability is never shown as working.
	InstrumentationImplemented bool
	LastSeenAt                 time.Time
}

// ReportedServiceName returns the OTel service.name instrumentation should report: the source-level
// override when set, otherwise the discovered workload identity.
func (w DiscoveredWorkload) ReportedServiceName() string {
	if w.Overrides.ServiceName != "" {
		return w.Overrides.ServiceName
	}
	return w.ServiceName
}

// Instrumentation phases are the canonical DiscoveredWorkload.InstrumentationPhase values a provider
// records as it instruments a workload; they surface as the WorkloadReport progress stage so central-ui
// and odictl can monitor live progress. Every value except PhaseInstrumented is "in flight"; a workload
// with a non-empty, non-terminal phase is actively being instrumented and reports WorkloadStateInstrumenting.
const (
	PhaseVerifying     = "Verifying"     // validating the workload and resolving/installing the agent/layer
	PhaseLayerReady    = "LayerReady"    // the instrumentation artifact (e.g. OTel Lambda layer) is ready
	PhaseInstrumenting = "Instrumenting" // applying instrumentation to the workload (may settle async)
	PhaseInstrumented  = "Instrumented"  // instrumentation applied and confirmed (terminal)
)

// Workload current-state values surfaced to Central (WorkloadReport.current.state). WorkloadStateInstrumenting
// is a distinct in-progress state reported while a source-enabled workload has a non-terminal phase, so the
// UI/CLI show it as progressing rather than as NotInstrumented ("disabled").
const (
	WorkloadStateNotInstrumented = "NotInstrumented"
	WorkloadStateInstrumenting   = "Instrumenting"
	WorkloadStateInstrumented    = "Instrumented"
)

// Compute-platform report status values surfaced to Central. NotImplemented marks a discovered managed
// environment whose compute-platform type is declared but whose assign/install flow is not built in this release.
const (
	ComputePlatformStatusNotImplemented = "NotImplemented"
)

// ErrPrefix* are the contract between a provider (which records a workload's InstrumentationError) and
// the workload report mapping (which classifies the error into a state). An error whose message begins
// with ErrPrefixUnsupported is a terminal "this workload cannot be instrumented" condition (unsupported
// runtime/language, or a handler the agent cannot wrap such as the AWS Lambda Web Adapter) and is
// reported as an Unsupported state rather than a retryable failure. ErrPrefixUnsupportedRuntime is the
// runtime-specific message (still matches the broader ErrPrefixUnsupported).
const (
	ErrPrefixUnsupported        = "unsupported"
	ErrPrefixUnsupportedRuntime = "unsupported runtime"
)

// DiscoveredComputePlatform is a discovered managed environment Odigos can be installed on (not instrumented),
// reported to Central as a ComputePlatformReport. It carries no source/destination intent — installing/updating
// Odigos on it is driven over the central↔odigos GraphQL API.
type DiscoveredComputePlatform struct {
	// Name + Kind + Region form the ComputePlatformReport composite key (Region is empty for region-less platforms).
	Name   string
	Kind   string
	Region string
	// ExternalID is the provider resource id (e.g. cluster ARN, instance id).
	ExternalID string
	// Status is the lifecycle of Odigos on this service (e.g. "Discovered"); install status is populated
	// once the central↔odigos install flow lands.
	Status string
	// OdigosVersion is the installed Odigos version, when known.
	OdigosVersion string
	// Metadata holds kind-specific attributes reported by the provider.
	Metadata map[string]any
	LastSeenAt time.Time
}

// Health is the computed runtime health surfaced to Central.
type Health struct {
	Registered      bool
	LastHeartbeatAt *time.Time
	SchemaVersionOK bool
	Authenticated   bool
	DiscoveryReady  bool
	Message         string
}

// CollectorStatus is a generic per-region collector status (empty until managed-otelcol integration).
type CollectorStatus struct {
	Name    string
	Healthy bool
	Message string
}

// CentralInterface is the set of connector operations the GraphQL resolvers delegate to.
type CentralInterface interface {
	SupportedSourceKinds(ctx context.Context) ([]string, error)
	// WorkloadReports returns all discovered workloads as WorkloadReport-shaped domain objects.
	WorkloadReports(ctx context.Context) ([]DiscoveredWorkload, error)
	// WorkloadReport returns a single discovered workload by composite key (serviceName + kind +
	// region), or nil if absent. region may be empty to match by serviceName + kind alone.
	WorkloadReport(ctx context.Context, serviceName, kind, region string) (*DiscoveredWorkload, error)
	// ComputePlatformReports returns all discovered compute platforms (managed install targets) as ComputePlatformReport-shaped
	// domain objects.
	ComputePlatformReports(ctx context.Context) ([]DiscoveredComputePlatform, error)
	// ComputePlatformReport returns a single discovered compute platform by composite key (name + kind + region), or nil
	// if absent. region may be empty to match by name + kind alone.
	ComputePlatformReport(ctx context.Context, name, kind, region string) (*DiscoveredComputePlatform, error)
	RefreshDiscovery(ctx context.Context) error
	// Config returns the effective operational config (code defaults + DB-stored overrides); ConfigDefaults
	// returns just the defaults; UpdateConfig merges a patch of overrides, applies it live (no restart), and
	// returns the new effective config. These back the agents-api config/configDefaults/updateConfig ops.
	Config(ctx context.Context) (map[string]any, error)
	ConfigDefaults(ctx context.Context) (map[string]any, error)
	UpdateConfig(ctx context.Context, patch map[string]any) (map[string]any, error)
	RuntimeHealth(ctx context.Context) (Health, error)
	CollectorStatuses(ctx context.Context) ([]CollectorStatus, error)
	TriggerRollback(ctx context.Context, id string) error
	RequestCleanup(ctx context.Context, id string) error

	GetSources(ctx context.Context) ([]domainsrc.Source, error)
	// GetSource resolves a single source by its name handle, narrowed by kind and region when provided
	// (empty = unconstrained) to disambiguate the same name across regions/kinds. Returns an error when
	// the name alone is ambiguous rather than picking a row arbitrarily.
	GetSource(ctx context.Context, name, kind, region string) (*domainsrc.Source, error)
	CreateSource(ctx context.Context, src domainsrc.Source) error
	UpdateSource(ctx context.Context, src domainsrc.Source) error
	// DeleteSource removes the exact source addressed by name (+ kind/region to disambiguate). It resolves
	// the row's true identity (kind, service_name, region) before deleting, so it never deletes the wrong
	// region's row or misses a row whose service_name was overridden. Ambiguous names error; a name that
	// matches nothing is a no-op.
	DeleteSource(ctx context.Context, name, kind, region string) error

	GetDestinations(ctx context.Context) ([]domaindest.Destination, error)
	GetDestination(ctx context.Context, name string) (*domaindest.Destination, error)
	CreateDestination(ctx context.Context, dest domaindest.Destination) error
	UpdateDestination(ctx context.Context, dest domaindest.Destination) error
	DeleteDestination(ctx context.Context, name string) error
	// DestinationCatalog returns the Odigos destination types this connector accepts.
	DestinationCatalog() domaindest.Catalog
}
