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

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectormetrics"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorreport"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorsettings"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	domainsrc "github.com/odigos-io/odigos-enterprise/connectors/base/domain/source"
)

// SourceOverrides carries matched-source configuration overrides surfaced on WorkloadReports.
// ServiceName overrides OTel service.name when set; it does not change workload identity.
type SourceOverrides struct {
	ServiceName     string
	RuntimeLanguage string
	RuntimeVersion  string
	OtelDistroName  string
	// Containers narrows the overrides above to individual containers of a container-shaped
	// workload. Empty for single-container platforms (Lambda) and for workloads the customer has
	// not narrowed; see domainsrc.ContainerOverride for why containers are keyed by name.
	Containers []domainsrc.ContainerOverride
}

// HasRuntimeOverride reports whether a runtime language or version override is configured.
func (o SourceOverrides) HasRuntimeOverride() bool {
	return o.RuntimeLanguage != "" || o.RuntimeVersion != ""
}

// ContainerOverrideFor returns the override for a named container, or nil when none is configured.
func (o SourceOverrides) ContainerOverrideFor(name string) *domainsrc.ContainerOverride {
	for i := range o.Containers {
		if o.Containers[i].ContainerName == name {
			return &o.Containers[i]
		}
	}
	return nil
}

// WorkloadContainer is one container of a container-shaped workload, as the connector sees it from
// the provider's control plane.
//
// A connector rewrites a task definition or a service spec, so it knows every container, its image
// and its detected runtime — but it never observes a process inside one. That is why there are no
// PIDs here: the wire field exists for agents that can see processes, and a connector leaving it
// empty says "no process visibility", not "no processes".
type WorkloadContainer struct {
	Name  string
	Image string
	// RuntimeLanguage/RuntimeVersion are this container's own detection, not the workload's.
	RuntimeLanguage string
	RuntimeVersion  string
	// OverrideLanguage/OverrideVersion mirror a per-container source override when one applies.
	OverrideLanguage string
	OverrideVersion  string
	Instrumented     bool
	// Eligible plus ExcludedReason are the pair that travels: whether Odigos can instrument this
	// container, and — when it cannot — why. ExcludedReason is never left empty on an ineligible
	// container, because it is the only explanation a client ever receives.
	Eligible       bool
	ExcludedReason string
	Distro         string
	// Essential and OdigosManaged are CONNECTOR-INTERNAL and deliberately absent from the report:
	// they are provider-specific (an ECS container-definition flag, an Odigos ownership marker) and a
	// client would need each platform's rules to make sense of them. The provider applies those rules
	// and reports the outcome instead — see ExcludedReason above, which is where both end up
	// ("non-essential container", "Odigos agents sidecar").
	Essential     bool
	OdigosManaged bool
}

// WorkloadDetails carries the kind-specific facts a connector knows about a workload beyond the
// common report. Kind selects which fields are meaningful, and the GraphQL boundary narrows it to
// the matching wire type; nil means the workload has nothing beyond the common report.
//
// It is a flat superset rather than a Go interface because a connector stores and passes it as
// plain data. Every field is named — that is the whole point, since these facts used to have
// nowhere to live except an untyped metadata map.
type WorkloadDetails struct {
	Kind string

	// Container-shaped kinds (ECS, Cloud Run, Container Apps).
	Containers []WorkloadContainer

	// ECS only.
	ClusterArn           string
	TaskDefinition       string
	LaunchType           string
	DeploymentController string

	// Lambda only.
	FunctionArn  string
	Runtime      string
	Architecture string
	LayerArn     string

	// Revisioned-service kinds — Cloud Run and Container Apps, which share one wire type because these two
	// facts mean the same thing on both: Service is the platform resource identity (a Cloud Run service name,
	// a Container App resource id).
	Service string
	// LatestReadyRevision is the revision the platform last reported READY — deliberately not "the revision
	// serving traffic", which is a different fact. Revisions are immutable on both platforms, so
	// instrumentation always produces a new one and a traffic split can keep weighting an older,
	// uninstrumented one indefinitely: a customer can apply instrumentation correctly and see no telemetry.
	// Naming the field for what it holds is what keeps the two providers from filling it with different facts,
	// which is exactly what the two wire types it feeds used to do.
	LatestReadyRevision string
}

// DiscoveredWorkload is a discovered cloud resource, reported to Central as a WorkloadReport.
type DiscoveredWorkload struct {
	AvailableDistros       []InstrumentationDistro
	EffectiveDistro        string
	InstrumentationVersion string
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
	// InstrumentationOrigin identifies who applied the live instrumentation when the provider can prove
	// it. "external" means a customer deployment definition applied it; "connector" means the connector
	// owns it and can normally roll it back. Empty means the provider has no attribution evidence.
	InstrumentationOrigin string
	// InstrumentationError is set when instrumentation or rollback last failed for this workload; empty
	// when healthy or not yet attempted.
	InstrumentationError string
	// RoutingNote is a non-fatal explanation of the selected telemetry path. Regional routing uses it
	// when a workload cannot reach the regional Collector and safely falls back to a global OTLP
	// terminal. It does not change the workload's instrumentation state.
	RoutingNote *WorkloadRoutingNote
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
	// ManagementMode is WHO instruments this workload: spec.ModeAutomatic (Odigos applies and repairs it) or
	// spec.ModeManual (the customer applies it themselves; Odigos observes and reports). Empty when this
	// workload's type is not enabled for instrumentation in any mode. Set by the coordinator, which resolves
	// it from the connector's capabilities — it is a property of the connector, not of the workload.
	ManagementMode string
	// SourceIntentAt is when the matched source's intent last changed; zero when no source is matched. It is
	// what lets a report say how long this state has held — how long a manual-mode workload has been awaiting
	// the customer's deployment, or how long a disabled source has had live instrumentation on it — rather
	// than only that it currently holds.
	SourceIntentAt time.Time
	// InstrumentationPhase is the live progress phase the instrumentor last recorded for this workload
	// (e.g. "Verifying", "LayerReady", "Instrumenting", "Instrumented"); empty when no phase has been
	// recorded yet. Set by the provider so a WorkloadReport reflects instrumentation progress on the fly.
	InstrumentationPhase string
	// Conformance is the verdict of the last successful read-only observation: does the instrumentation
	// actually on this workload match what Odigos wants for it (see api.Conformance). Written by the
	// provider's InstrumentationObserver, ConformanceUnknown when the type has no observer or none has run.
	//
	// Instrumented answers a strictly weaker question — is an Odigos ownership marker present — which is
	// enough in automatic mode (the reconciler converges any gap it finds) but a false green in manual
	// mode, where nothing converges anything. The two are deliberately separate fields: a Partial workload
	// is BOTH instrumented and wrong, and hiding either half would mislead whoever has to fix it.
	Conformance Conformance
	// MissingComponents names the parts of the desired instrumentation that are absent or stale, in
	// provider-neutral terms (e.g. "instrumentationLayer", "serviceName", "collectorExport"). Non-empty
	// only for ConformancePartial — it is the entire explanation a customer gets for what to fix, so a
	// Partial verdict must never travel without it.
	MissingComponents []string
	// ConformanceSince is when the current Conformance verdict was first observed, so a report can say how
	// long a drift has stood rather than only that it stands now. Zero when never observed.
	ConformanceSince time.Time
	// Settling is true when the provider observed a change to this workload still being applied — a Lambda
	// configuration update mid-flight, a revision rolling out. It is why an observation that looks
	// non-conformant must not be reported as drift yet: the customer's deployment may be seconds from
	// completing, and a "drifted" report in that window is simply wrong.
	Settling bool
	// ObservationError is set when the last observation could not be completed (an API error, or a desired
	// value that could not be resolved without mutating the account). It is NOT drift: the base keeps the
	// last confirmed Conformance and raises a transient issue, because "we could not look" and "your
	// instrumentation is broken" are different things to tell a customer.
	ObservationError string
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
	// ConformanceObservable is true when this connector actually has an InstrumentationObserver registered
	// for this workload's instrumentation type, so a Conformance verdict is something that WILL arrive. Set by
	// the coordinator, which is the only thing that knows (see activeObservers).
	//
	// It is what separates the two very different meanings of ConformanceUnknown. On an observable type it is
	// temporary — no pass has run yet, or the last one failed — and the report can honestly say the check is
	// pending. On a type with no observer it is PERMANENT: nothing will ever set it, and a report promising a
	// verification that is never coming would be worse than saying nothing. That gap is a connector capability
	// limit, already warned about once at startup rather than per workload forever.
	ConformanceObservable bool
	// WaitingFor names the prerequisite blocking a workload the customer HAS marked for
	// instrumentation, when the connector is deliberately not attempting it yet. It is not a failure:
	// nothing has gone wrong and nothing needs fixing on the workload — a precondition simply is not
	// met, so the reconcile skips it and will proceed on its own once the precondition is satisfied.
	//
	// Without this the skip is invisible: the workload reports as enabled-but-not-instrumented with no
	// explanation, and the only trace is a connector log line the customer cannot see.
	// Empty when nothing is blocking. Set by the coordinator, which knows the connector-wide state.
	WaitingFor string
	// Details carries kind-specific facts (an ECS task's containers, a Lambda's ARN and layer).
	// Nil when the provider has nothing to add beyond the common report.
	Details    *WorkloadDetails
	LastSeenAt time.Time
}

// WorkloadRoutingNote is provider-neutral routing context surfaced as a WorkloadIssue. Code is the
// stable routing reason; Message is the human explanation produced by the shared routing resolver.
type WorkloadRoutingNote struct {
	Code      string
	Message   string
	Transient bool
}

// Prerequisites a workload's instrumentation can be waiting on. These are values of
// DiscoveredWorkload.WaitingFor and travel to the UI as WorkloadReport.progress.waitingFor.
const (
	// WaitingForDestination: the customer marked the workload for instrumentation but the connector
	// has no OTLP destination. Every connector instrumentation exports directly to that destination
	// (a Fargate task or Lambda has no node-local collector), so there is nowhere to point an agent
	// and instrumentation is deferred rather than applied half-configured.
	WaitingForDestination = "destination"
	// WaitingForCustomer: the workload's type is in manual (IaC) mode, so the customer applies the
	// instrumentation themselves — through Terraform, CloudFormation, Bicep, a CLI or by hand — and the
	// connector is waiting for that deployment to land. It resolves on its own the moment the customer
	// applies it: discovery observes the Odigos ownership marker and the workload reports Instrumented.
	//
	// This is the normal, healthy resting state of a marked manual-mode workload, not a failure. It takes
	// lower precedence than WaitingForDestination: without a destination there is no endpoint to put in the
	// instructions, so there is nothing for the customer to apply yet.
	WaitingForCustomer = "customer"
)

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
	Metadata   map[string]any
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
	// InstrumentationPlan returns what a customer has to apply to instrument one workload, keyed the same way
	// as WorkloadReport. nil (with no error) when there is nothing to publish for it — a type with no
	// instrumentation defined, a provider with no planner, a language it cannot instrument.
	InstrumentationPlan(ctx context.Context, serviceName, kind, region string) (*InstrumentationPlanResult, error)
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

	// --- Managed Collectors ---
	//
	// Every operation is keyed by REGION, because that is a Collector's identity (§5.1). The host
	// network it runs in is placement metadata and is never how one is addressed.
	//
	// Create and Update write CONFIGURATION and return; they do not build infrastructure synchronously.
	// The reconcile loop converges it on its next pass, which is why both return the report — normally
	// still Pending — rather than a success boolean. A caller that wants to know when it is running
	// watches the report's status.

	// CollectorSettings returns the resolved managed-Collector configuration, read live. It is what
	// renders a Collector that is configured but not yet built — after a create, before the reconcile
	// loop has produced a row — so that mutation can answer with the settings it just wrote rather than
	// with nothing.
	CollectorSettings() collectorsettings.Settings
	// ManagedCollectorReports returns one report input per recorded Collector, for every mode.
	ManagedCollectorReports(ctx context.Context) ([]collectorreport.ModelInput, error)
	// ManagedCollectorReport returns one region's report input, or nil when none is recorded there.
	ManagedCollectorReport(ctx context.Context, region string) (*collectorreport.ModelInput, error)
	// CreateManagedCollector declares that a region should have a Collector. It refuses a region that
	// already has one — changing an existing Collector is UpdateManagedCollector, and letting create
	// overwrite would turn a mistaken create into a silent reconfiguration of a running Collector.
	CreateManagedCollector(ctx context.Context, region string, patch collectorsettings.Patch) error
	// UpdateManagedCollector merges settings into a region that already has a Collector.
	UpdateManagedCollector(ctx context.Context, region string, patch collectorsettings.Patch) error
	// TeardownManagedCollector removes one region's Collector. A refusal (authority did not permit it)
	// is reported in the result, not as an error: silently leaked infrastructure is the failure mode
	// worth avoiding, so a declined teardown must still be visible to whoever asked.
	TeardownManagedCollector(ctx context.Context, region string) (CollectorTeardownResult, error)
	// Destinations one region's Collector owns and exports to — not the workload destinations above,
	// which are configured independently. Identity is (region, name); a destination's Region is its
	// Collector. The same rows are what the configuration renderer reads, so what a reader sees here and
	// what the Collector actually forwards to cannot drift apart.
	ManagedCollectorDestinations(ctx context.Context, region string) ([]domaindest.Destination, error)
	ManagedCollectorDestination(ctx context.Context, region, name string) (*domaindest.Destination, error)
	CreateManagedCollectorDestination(ctx context.Context, dest domaindest.Destination) error
	UpdateManagedCollectorDestination(ctx context.Context, dest domaindest.Destination) error
	DeleteManagedCollectorDestination(ctx context.Context, region, name string) (bool, error)
	// ManagedCollectorMetrics returns the Collector's counters. A refresh waits briefly for a new
	// periodic own-metrics report; found=false means it has never reported any.
	ManagedCollectorMetrics(ctx context.Context, region string, refresh bool) (collectormetrics.Record, bool, error)
	IssueManagedCollectorCredential(ctx context.Context, input ManualCollectorCredentialInput) (ManualCollectorCredentialIssue, error)
	RevokeManagedCollectorCredential(ctx context.Context, region, credentialID string) error
	// ManagedCollectorInstructions renders provider-owned deployment documents for one region. A nil
	// result means this provider has no collector artifact for the requested region.
	ManagedCollectorInstructions(ctx context.Context, region string) (*ManagedCollectorInstructions, error)
}

// ManualCollectorCredentialInput names the immutable binding and the customer's secret location.
// Rotate explicitly replaces an existing credential; retrying a lost one-time reveal requires it.
type ManualCollectorCredentialInput struct {
	Region, NetworkID, SecretRef string
	Rotate                       bool
}

// ManualCollectorCredentialIssue is returned once by the authenticated mutation. The secret wrapper
// prevents accidental logging or serialization; only the GraphQL adapter may reveal the header.
type ManualCollectorCredentialIssue struct {
	CollectorID, CredentialID, SecretRef string
	AuthorizationHeader                  collectorcredential.Secret
}

// ManualCollectorCredentialIssuer is implemented only by providers that support manual Collector
// credentials. It may write connector state, but must not mutate customer cloud infrastructure.
type ManualCollectorCredentialIssuer interface {
	IssueManualCollectorCredential(ctx context.Context, input ManualCollectorCredentialInput) (ManualCollectorCredentialIssue, error)
}

// ManagedCollectorInstructions is the provider-neutral shape used by GraphQL. The documents remain
// provider-owned because CloudFormation and Terraform are AWS syntax, not a base concern.
type ManagedCollectorInstructions struct {
	Region            string
	HostNetworkID     string
	DeploymentRoleRef string
	Mode              string
	Documents         []InstructionDocument
	Notes             []string
	UpdatedAt         time.Time
}

// InstructionDocument is one copy-pasteable delivery variant.
type InstructionDocument struct {
	ID, Variant, VariantLabel, Title, Description, Format, Content string
	Steps                                                          []string
	Unresolved                                                     []InstructionInput
}

// InstructionInput describes a value the customer must still provide.
type InstructionInput struct{ Placeholder, Description string }

// ManagedCollectorInstructionProvider is implemented by a cloud provider that can render its own
// collector artifacts. It is optional so providers without a collector implementation continue to
// return the schema's normal null result.
type ManagedCollectorInstructionProvider interface {
	ManagedCollectorInstructions(ctx context.Context, region string) (*ManagedCollectorInstructions, error)
}

// CollectorTeardownResult is the outcome of a teardown request.
//
// Deleted=false is a refusal rather than a failure: the Collector is manual-mode, or customer-created,
// or its objects are not recorded as Odigos-created, and Message names which. Retained lists what was
// deliberately left standing so a customer is never told a teardown completed while their own load
// balancer is still there.
type CollectorTeardownResult struct {
	Deleted  bool
	Message  string
	Retained []string
}
