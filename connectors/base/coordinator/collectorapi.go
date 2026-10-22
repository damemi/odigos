package coordinator

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectoraction"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcutover"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectordestination"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectoringress"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectormetrics"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorownership"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorreport"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorsettings"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	"github.com/odigos-io/odigos-enterprise/connectors/base/store"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// The managed-Collector API the GraphQL resolvers delegate to: read the fleet, declare a Collector,
// change one, destroy one, and configure the pipeline that runs on it.
//
// Create and Update write CONFIGURATION and return. They do not build infrastructure on the calling
// goroutine, and that is not a limitation to work around: converging a Collector takes provider calls
// measured in minutes, and a mutation that blocked on them would time out at every layer between the
// customer and here while the work carried on regardless. The reconcile loop owns convergence and is
// nudged awake at the end of each write, so the only difference from doing it inline is that the caller
// gets an answer.

// storedCollectorSettingsTree reads the CONFIGURED `collector` subtree — what somebody set, not what
// the defaults resolve to.
//
// Stored rather than effective, because whatever comes back here is written straight back by
// mutateCollectorSettings, and a patch replaces a top-level key wholesale. Starting from the effective
// tree would persist today's code defaults into the database on the first create, freezing them: a
// later release raising the default memory would never reach this connector, and nothing would say why.
// Reading the stored tree writes back only what was actually configured, and collectorsettings.Read
// supplies the code defaults for everything absent — from the code, every time.
func (c *Coordinator) storedCollectorSettingsTree() map[string]any {
	tree, _ := c.cfgMgr.StoredValue(collectorsettings.Key).(map[string]any)
	return tree
}

// mutateCollectorSettings serializes one read-modify-write of the `collector` subtree.
//
// The lock is the whole point. config.Manager merges at the TOP-LEVEL key only, so every one of these
// mutations rewrites the entire subtree — and two concurrent creates for different regions would both
// read the tree before either wrote, leaving whichever finished second to erase the other's region
// while its own mutation had already returned success. gqlgen serves each request on its own goroutine,
// so "one connector replica" does not prevent this; only this lock does.
//
// Held across the read, the mutation and the persist, and released before any provider call.
func (c *Coordinator) mutateCollectorSettings(ctx context.Context, mutate func(tree map[string]any) (map[string]any, error)) error {
	c.collectorSettingsMu.Lock()
	defer c.collectorSettingsMu.Unlock()

	next, err := mutate(c.storedCollectorSettingsTree())
	if err != nil {
		return err
	}
	return c.cfgMgr.Update(ctx, map[string]any{collectorsettings.Key: next})
}

// ManagedCollectorReports returns one report per configured or recorded Collector.
//
// Configuration is desired state and appears before the provider has produced a managed_collectors row.
// Omitting those regions makes a successful create disappear from the next list query until AWS happens
// to finish its first write, and makes a placement refusal disappear forever. Recorded state wins once
// it exists; configured-only regions render honestly as Pending.
func (c *Coordinator) ManagedCollectorReports(ctx context.Context) ([]collectorreport.ModelInput, error) {
	apiLog.Debug("list managed collectors")
	records, err := c.collectorResourceStore().ListManagedCollectors(ctx)
	if err != nil {
		apiLog.Error("list managed collectors failed", "error", err)
		return nil, err
	}
	settings := c.CollectorSettings()
	now := time.Now()
	states, err := c.collectorResourceStore().ListCollectorResourceStates(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]collectorreport.ModelInput, 0, len(records))
	for _, rec := range records {
		in := c.collectorModelInput(rec, settings, now)
		in.Deletable = in.Deletable || c.canDiscardCollectorState(rec.Key.Region, &rec, states[rec.Key.Region], states[rec.Key.Region] != "")
		if err := c.addCollectorResourceStateProblem(ctx, &in, states[rec.Key.Region]); err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	out = includeConfiguredCollectorReports(out, collectorsettings.ConfiguredRegions(c.storedCollectorSettingsTree()), settings, now)
	for i := range out {
		if out[i].Deletable {
			continue
		}
		if _, recorded := findCollectorRecord(records, out[i].Region); !recorded {
			out[i].Deletable = c.canDiscardCollectorState(out[i].Region, nil, states[out[i].Region], states[out[i].Region] != "")
			if err := c.addCollectorResourceStateProblem(ctx, &out[i], states[out[i].Region]); err != nil {
				return nil, err
			}
		}
	}
	seen := make(map[string]bool, len(out))
	for _, in := range out {
		seen[in.Region] = true
	}
	for region, state := range states {
		if seen[region] || state == store.CollectorResourcesDestroyed || state == store.CollectorResourcesNotCreated {
			continue
		}
		in := pendingCollectorModelInput(region, settings, now)
		if err := c.addCollectorResourceStateProblem(ctx, &in, state); err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	routing := c.collectorRoutingReportContext(ctx)
	for i := range out {
		routing.annotate(&out[i])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Region < out[j].Region })
	return out, nil
}

// ManagedCollectorReport returns one region's report input, or nil when none is recorded there.
//
// nil rather than an error for a region with no Collector: the schema's query is nullable precisely
// because "this connector has none there" is a normal answer a client renders, not a failure.
func (c *Coordinator) ManagedCollectorReport(ctx context.Context, region string) (*collectorreport.ModelInput, error) {
	apiLog.Debug("get managed collector", "region", region)
	routing := c.collectorRoutingReportContext(ctx)
	state, known, err := c.collectorResourceStore().GetCollectorResourceState(ctx, region)
	if err != nil {
		return nil, err
	}
	rec, err := c.collectorResourceStore().GetManagedCollector(ctx, store.CollectorKey{Region: region})
	if err != nil {
		if errors.Is(err, store.ErrCollectorNotFound) {
			if collectorsettings.HasRegion(c.storedCollectorSettingsTree(), region) {
				in := pendingCollectorModelInput(region, c.CollectorSettings(), time.Now())
				in.Deletable = c.canDiscardCollectorState(region, nil, state, known)
				if err := c.addCollectorResourceStateProblem(ctx, &in, state); err != nil {
					return nil, err
				}
				routing.annotate(&in)
				return &in, nil
			}
			if known && state != store.CollectorResourcesDestroyed && state != store.CollectorResourcesNotCreated {
				in := pendingCollectorModelInput(region, c.CollectorSettings(), time.Now())
				if err := c.addCollectorResourceStateProblem(ctx, &in, state); err != nil {
					return nil, err
				}
				routing.annotate(&in)
				return &in, nil
			}
			return nil, nil
		}
		apiLog.Error("get managed collector failed", "region", region, "error", err)
		return nil, err
	}
	in := c.collectorModelInput(rec, c.CollectorSettings(), time.Now())
	in.Deletable = in.Deletable || c.canDiscardCollectorState(region, &rec, state, known)
	if err := c.addCollectorResourceStateProblem(ctx, &in, state); err != nil {
		return nil, err
	}
	routing.annotate(&in)
	return &in, nil
}

type collectorRoutingReportContext struct {
	markedRegions   map[string]bool
	markerUpdatedAt map[string]time.Time
	announcements   map[string]*store.CutoverAnnouncement
	directFound     bool
	progress        map[string]api.CollectorRoutingProgress
}

func (c *Coordinator) collectorRoutingReportContext(ctx context.Context) collectorRoutingReportContext {
	out := collectorRoutingReportContext{}
	if c.store == nil {
		return out
	}
	destinations, err := c.store.ListDestinations(ctx)
	if err != nil {
		apiLog.Warn("cannot read Collector routing markers for report", "error", err)
		return out
	}
	out.markedRegions = make(map[string]bool)
	out.markerUpdatedAt = make(map[string]time.Time)
	for _, destination := range destinations {
		if !destination.Disabled && domaindest.IsManagedCollectorType(destination.Type) {
			out.markedRegions[destination.Region] = true
			out.markerUpdatedAt[destination.Region] = destination.UpdatedAt
		}
	}
	if len(out.markedRegions) == 0 {
		return out
	}
	direct, err := c.store.OTLPRouting(ctx, "")
	if err != nil {
		apiLog.Warn("cannot read global direct OTLP route for Collector report", "error", err)
		out.markedRegions = nil
		return out
	}
	out.directFound = direct.Found
	out.announcements = make(map[string]*store.CutoverAnnouncement, len(out.markedRegions))
	for region := range out.markedRegions {
		announcement, readErr := c.store.GetCollectorCutoverAnnouncement(ctx, region)
		if readErr != nil {
			apiLog.Warn("cannot read Collector cutover announcement for report", "region", region, "error", readErr)
			continue
		}
		out.announcements[region] = announcement
	}
	if reporter, ok := c.provider.(api.CollectorRoutingProgressReporter); ok {
		out.progress, err = reporter.CollectorRoutingProgress(ctx)
		if err != nil {
			apiLog.Warn("cannot read Collector workload routing progress for report", "error", err)
			out.progress = nil
		}
	}
	return out
}

func (r collectorRoutingReportContext) annotate(in *collectorreport.ModelInput) {
	if in.Teardown {
		return
	}
	if !r.markedRegions[in.Region] {
		return
	}
	if in.FirstReadyAt == nil {
		addCollectorRoutingIssue(in, collectorstatus.Problem{
			Code: "CollectorCutoverAwaitingReadiness", Severity: collectorstatus.SeverityInfo,
			Message: "No workload routes to this Collector yet. The first cutover waits until it has reported Ready.",
		})
	} else if announcement := r.announcements[in.Region]; r.directFound && (announcement == nil || !announcement.Matches(r.markerUpdatedAt[in.Region], *in.FirstReadyAt)) {
		addCollectorRoutingIssue(in, collectorstatus.Problem{
			Code: "CollectorCutoverAwaitingWorkload", Severity: collectorstatus.SeverityInfo,
			Message: "The Collector is ready. The first eligible instrumented workload will start an announced one-minute warning window before cutover begins.",
		})
	} else if r.directFound && r.announcements[in.Region].Announcing {
		addCollectorRoutingIssue(in, collectorstatus.Problem{
			Code: collectorcutover.ProblemRolloutStarting, Severity: collectorstatus.SeverityInfo,
			Message: "The regional Collector is ready. New workload cutovers are announced and will begin after the one-minute warning window, in bounded batches.",
		})
	} else if progress := r.progress[in.Region]; progress.Instrumented > 0 {
		code := collectorcutover.ProblemRolloutInProgress
		if progress.Routed == progress.Instrumented {
			code = collectorcutover.ProblemRolloutComplete
		}
		message := fmt.Sprintf("%d of %d instrumented workload(s) in %s currently export through the regional Collector.",
			progress.Routed, progress.Instrumented, in.Region)
		if r.directFound {
			message += " Others still use direct export while rollout or network eligibility is evaluated."
		} else if progress.Routed < progress.Instrumented {
			message += " Workloads without a Collector route have no global export fallback."
		}
		addCollectorRoutingIssue(in, collectorstatus.Problem{
			Code: code, Severity: collectorstatus.SeverityInfo,
			Message: message,
		})
		if progress.Failed > 0 {
			addCollectorRoutingIssue(in, collectorstatus.Problem{
				Code: collectorcutover.ProblemRolloutWorkloadFailed, Severity: collectorstatus.SeverityDegraded,
				Message: fmt.Sprintf("%d workload cutover(s) in %s failed and will be retried. Examples: %s.",
					progress.Failed, in.Region, strings.Join(progress.FailureRefs, ", ")),
			})
		}
	}
}

// Routing describes workload rollout, not Collector health. Keep its issues visible on the
// Collector report without escalating an otherwise Ready Collector or appending routing text to
// the Collector's health message.
func addCollectorRoutingIssue(in *collectorreport.ModelInput, problem collectorstatus.Problem) {
	in.ExtraIssues = append(in.ExtraIssues, collectorreport.IssueFromProblem(problem))
}

// AWS may keep load-balancer network interfaces for several minutes. Keep retrying for as long as
// the recorded refs exist, but distinguish routine release lag from a teardown that needs attention.
const collectorTeardownGracePeriod = 30 * time.Minute

func (c *Coordinator) addCollectorResourceStateProblem(ctx context.Context, in *collectorreport.ModelInput, state store.CollectorResourceState) error {
	switch state {
	case store.CollectorResourcesNotCreated:
		in.Problems = append(in.Problems, collectorstatus.Problem{Code: "CollectorCloudResourcesNotCreated", Severity: collectorstatus.SeverityInfo, Message: "No cloud resources have been created for this Collector."})
	case store.CollectorResourcesCreated:
		in.Problems = append(in.Problems, collectorstatus.Problem{Code: "CollectorCloudResourcesCreated", Severity: collectorstatus.SeverityInfo, Message: "Cloud resources have been recorded for this Collector."})
	case store.CollectorResourcesTearingDown:
		startedAt, err := c.collectorResourceStore().CollectorResourceStateUpdatedAt(ctx, in.Region)
		if err != nil {
			return err
		}
		in.Teardown = true
		in.RecordedMessage = ""
		in.Problems = nil // Old provider status describes a partially removed stack, not current health.
		if in.Now.Sub(startedAt) < collectorTeardownGracePeriod {
			in.Status = collectorstatus.StatusDeleting
			in.Problems = append(in.Problems, collectorstatus.Problem{Code: "CollectorTeardownInProgress", Severity: collectorstatus.SeverityPending, Message: "Cloud teardown is in progress; the connector is retrying resources still being released by AWS."})
		} else {
			in.Status = collectorstatus.StatusError
			in.Problems = append(in.Problems, collectorstatus.Problem{Code: "CollectorTeardownTimedOut", Severity: collectorstatus.SeverityError, Message: "Cloud teardown has been blocked for over 30 minutes. The connector will keep retrying; inspect the remaining AWS dependencies."})
		}
	case store.CollectorResourcesProvisioning:
		in.Problems = append(in.Problems, collectorstatus.Problem{Code: "CollectorCloudResourcesProvisioning", Severity: collectorstatus.SeverityPending, Message: "Cloud provisioning started; its outcome has not been recorded yet."})
	case store.CollectorResourcesMoving:
		in.Problems = append(in.Problems, collectorstatus.Problem{Code: "CollectorCloudResourcesMoving", Severity: collectorstatus.SeverityPending, Message: "The old VPC stack is being removed. The Collector will be provisioned in the selected VPC after teardown completes."})
	case store.CollectorResourcesOrphaned:
		in.Problems = append(in.Problems, collectorstatus.Problem{Code: "CollectorCloudResourcesOrphaned", Severity: collectorstatus.SeverityError, Message: "Cloud provisioning or teardown did not complete. Resources may remain and need investigation."})
	case store.CollectorResourcesDestroyed:
		in.Problems = append(in.Problems, collectorstatus.Problem{Code: "CollectorCloudResourcesDestroyed", Severity: collectorstatus.SeverityInfo, Message: "Cloud resources were destroyed; local Collector cleanup is finishing."})
	}
	return nil
}

func findCollectorRecord(records []store.CollectorRecord, region string) (store.CollectorRecord, bool) {
	for _, rec := range records {
		if rec.Key.Region == region {
			return rec, true
		}
	}
	return store.CollectorRecord{}, false
}

// A declaration that never created cloud resources can always be discarded locally.
// Legacy declarations without a resource-state row qualify only when they have no
// cloud deployment parameters. Ambiguous records fail closed.
func (c *Coordinator) canDiscardCollectorState(region string, rec *store.CollectorRecord, state store.CollectorResourceState, known bool) bool {
	if rec != nil {
		if rec.Mode == spec.ModeManual {
			return true // manual infrastructure is never a connector deletion candidate
		}
		if rec.Mode != spec.ModeAutomatic || rec.Origin != string(collectorownership.OriginOdigos) || hasCloudResourceRefs(*rec) {
			return false
		}
	} else if len(c.provider.Spec().ActiveCollectorDeploymentTypes(c.cfg.Capabilities)) == 0 {
		// A manual declaration cannot have started a deployer. A legacy automatic
		// provisioning state is still ambiguous and must fail closed.
		if !known || state == store.CollectorResourcesNotCreated || state == store.CollectorResourcesDestroyed {
			for _, collectorType := range c.provider.Spec().CollectorTypes(c.cfg.Capabilities) {
				if c.cfg.Capabilities.CollectorMode(collectorType.Name) == spec.ModeManual {
					return true
				}
			}
		}
		return false
	}
	if state == store.CollectorResourcesDestroyed {
		return true
	}
	if rec != nil && rec.Mode == spec.ModeAutomatic && rec.NetworkID != "" {
		// A row written before the first cloud call records the derived-name
		// log group even when no opaque infrastructure ref exists yet.
		return false
	}
	if state == store.CollectorResourcesNotCreated {
		return true
	}
	if known {
		return false
	}
	desired := c.CollectorSettings().Region(region)
	return desired.HostNetworkID == "" && len(desired.Provider) == 0
}

func hasCloudResourceRefs(rec store.CollectorRecord) bool {
	return hasRecordedRefs(rec) || rec.CollectorID != "" || rec.CredentialRef != "" ||
		rec.ActiveCredentialID != "" || rec.LicenseSecretRef != ""
}

// ManagedCollectorInstructions delegates syntax-specific artifacts to the selected provider. The
// coordinator owns configuration and lifecycle, while the provider owns the ECS, Terraform and
// CloudFormation contract that the artifacts describe.
func (c *Coordinator) ManagedCollectorInstructions(ctx context.Context, region string) (*api.ManagedCollectorInstructions, error) {
	renderer, ok := c.provider.(api.ManagedCollectorInstructionProvider)
	if !ok {
		return nil, nil
	}
	return renderer.ManagedCollectorInstructions(ctx, region)
}

func pendingCollectorModelInput(region string, settings collectorsettings.Settings, now time.Time) collectorreport.ModelInput {
	desired := settings.Region(region)
	return collectorreport.ModelInput{
		Region:        region,
		HostNetworkID: desired.HostNetworkID,
		Desired:       desired,
		UpdatedAt:     now,
		Now:           now,
	}
}

func includeConfiguredCollectorReports(recorded []collectorreport.ModelInput, configured []string, settings collectorsettings.Settings, now time.Time) []collectorreport.ModelInput {
	out := append([]collectorreport.ModelInput(nil), recorded...)
	seen := make(map[string]bool, len(recorded))
	for _, in := range recorded {
		seen[in.Region] = true
	}
	for _, region := range configured {
		if !seen[region] {
			out = append(out, pendingCollectorModelInput(region, settings, now))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Region < out[j].Region })
	return out
}

// collectorModelInput flattens one row plus its resolved settings into what the report renders.
//
// Authority comes from the ROW (mode, origin, whether infrastructure refs are recorded) rather than
// from live cloud tags: the observer has already read those and written its conclusion here, and
// re-reading them would put a provider call behind every report.
func (c *Coordinator) collectorModelInput(rec store.CollectorRecord, settings collectorsettings.Settings, now time.Time) collectorreport.ModelInput {
	class := collectorownership.ClassifyRecorded(rec.Mode, rec.Origin,
		hasCloudResourceRefs(rec) || (rec.Mode == spec.ModeAutomatic && rec.NetworkID != ""))
	recordedMessage, problems := recordedCollectorProblems(rec.Status, rec.Message)
	if selected := c.cfg.Capabilities.CollectorMode(rec.Kind); rec.Kind != "" && selected != rec.Mode {
		transition := collectorownership.TransitionFor(rec.Mode, selected)
		problems = append(problems, collectorstatus.Problem{
			Code: "CollectorCapabilityTransition", Severity: collectorstatus.SeverityInfo,
			Message: transition.Note,
		})
	}

	return collectorreport.ModelInput{
		Region:        rec.Key.Region,
		HostNetworkID: rec.NetworkID,
		Kind:          rec.Kind,

		Status:            rec.Status,
		RecordedMessage:   recordedMessage,
		Mode:              rec.Mode,
		Origin:            rec.Origin,
		Adopted:           rec.AdoptedAt != nil,
		OpAMPLastSeenAt:   rec.OpAMPLastSeenAt,
		OpAMPInstanceUID:  rec.OpAMPInstanceUID,
		AgentHealth:       rec.AgentHealth,
		HeartbeatInterval: c.cfgMgr.Settings().HeartbeatInterval,
		Problems:          problems,

		Desired: settings.Region(rec.Key.Region),

		EndpointGRPC:   rec.EndpointGRPC,
		EndpointHTTP:   rec.EndpointHTTP,
		EndpointSource: rec.EndpointSource,
		TLSMode:        collectoringress.TLSMode(rec.TLSMode),
		TLSTermination: rec.TLSTermination,
		IngressTier:    collectoringress.Tier(rec.IngressTier),

		ScalingMode:  rec.ScalingMode,
		DesiredCount: rec.DesiredCount,
		RunningCount: rec.RunningCount,
		MinCount:     rec.MinCount,
		MaxCount:     rec.MaxCount,

		ImageDigest:      rec.ImageDigest,
		ImageTag:         rec.ImageTag,
		ImageArch:        rec.ImageArch,
		LicenseExpiresAt: rec.LicenseExpiresAt,

		ConfigHash:     rec.ConfigHash,
		ConfigPushedAt: rec.ConfigPushedAt,
		FirstReadyAt:   rec.FirstReadyAt,

		Attribution: string(class.Attribution),
		Mutable:     class.Mutable,
		Deletable:   class.Deletable,

		Provider:        c.cfg.Provider,
		WorkloadRef:     rec.WorkloadRef,
		RevisionRef:     rec.RevisionRef,
		ClusterRef:      rec.RuntimeRef,
		LoadBalancerRef: rec.FrontendRef,
		TargetGroupRefs: rec.BackendRefs,
		SecurityGroupID: rec.IngressRef,
		SubnetIDs:       rec.PlacementRefs,

		UpdatedAt: rec.UpdatedAt,
		Now:       now,
	}
}

const collectorConvergenceFailurePrefix = "Collector convergence failed:"

// recordedCollectorProblems restores structured conditions that have to cross the database status
// channel. Provider convergence errors are discovered in the background reconcile goroutine, after the
// GraphQL mutation has returned, so the row's status/message pair is their durable handoff to readers.
// Recognizing the prefix written by the deployer gives API clients the stable issue code they branch on
// while retaining the provider's original message verbatim. The recognized message is removed from the
// prose-only channel so it is rendered once, through Problems, rather than duplicated.
func recordedCollectorProblems(status collectorstatus.Status, message string) (string, []collectorstatus.Problem) {
	if status == collectorstatus.StatusError && strings.HasPrefix(message, collectorConvergenceFailurePrefix) {
		return "", []collectorstatus.Problem{{
			Code:     "CollectorConvergenceFailed",
			Severity: collectorstatus.SeverityError,
			Message:  message,
		}}
	}
	return message, nil
}

// hasRecordedRefs reports whether this row holds any infrastructure ref, which is half the deletion
// gate (§10.4). A row with no refs grants no delete authority — the same rule CollectorRecord.Recorded
// applies per object, asked here of the whole row.
func hasRecordedRefs(rec store.CollectorRecord) bool {
	return rec.WorkloadRef != "" || rec.RevisionRef != "" || rec.RuntimeRef != "" ||
		rec.FrontendRef != "" || rec.IngressRef != "" ||
		len(rec.BackendRefs) > 0 || len(rec.PlacementRefs) > 0
}

// CreateManagedCollector declares that a region should have a Collector.
//
// Refuses a region that already has one. Create-as-overwrite would turn a mistaken create — the same
// mutation sent twice, a region name typed from memory — into a silent reconfiguration of a running
// Collector, with the defaults for every field the second call omitted.
func (c *Coordinator) CreateManagedCollector(ctx context.Context, region string, patch collectorsettings.Patch) error {
	apiLog.Info("create managed collector", "region", region)
	if !c.collectorsCapable() {
		return fmt.Errorf("createManagedCollector: this connector has no managed-Collector capability enabled")
	}
	if regionalValidator, ok := c.provider.(api.CollectorRegionalSettingsValidator); ok {
		if err := regionalValidator.ValidateCollectorRegionalSettings(ctx, region, patch, true); err != nil {
			return fmt.Errorf("createManagedCollector: %w", err)
		}
	}

	// Both sources, because either one alone misses a real Collector. Configuration misses one that was
	// discovered or adopted — it has a row and no config entry — and overwriting that with enabled:true
	// would point the reconcile loop at a Collector somebody else built. The row misses one created
	// seconds ago that no pass has converged yet.
	if _, err := c.collectorResourceStore().GetManagedCollector(ctx, store.CollectorKey{Region: region}); err == nil {
		return fmt.Errorf("createManagedCollector: region %s already has a Collector; use updateManagedCollector to change it", region)
	} else if !errors.Is(err, store.ErrCollectorNotFound) {
		return fmt.Errorf("createManagedCollector: %w", err)
	}

	// A create enables the region unless the caller explicitly said otherwise, because a create that
	// deploys nothing is not what anybody means by creating a Collector.
	if patch.Enabled == nil {
		enabled := true
		patch.Enabled = &enabled
	}
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()
	if _, err := c.collectorResourceStore().GetManagedCollector(ctx, store.CollectorKey{Region: region}); err == nil {
		return fmt.Errorf("createManagedCollector: region %s already has a Collector; use updateManagedCollector to change it", region)
	} else if !errors.Is(err, store.ErrCollectorNotFound) {
		return fmt.Errorf("createManagedCollector: %w", err)
	}
	state, known, err := c.collectorResourceStore().GetCollectorResourceState(ctx, region)
	if err != nil {
		return fmt.Errorf("createManagedCollector: %w", err)
	}
	if known && state != store.CollectorResourcesDestroyed && state != store.CollectorResourcesNotCreated {
		return fmt.Errorf("createManagedCollector: region %s has %s cloud-resource state; finish its teardown before creating it again", region, state)
	}

	if err := c.mutateCollectorSettings(ctx, func(tree map[string]any) (map[string]any, error) {
		// Re-checked inside the lock: without this, two creates for the same region both pass the check
		// above and the second silently overwrites the first.
		if collectorsettings.HasRegion(tree, region) {
			return nil, fmt.Errorf("region %s already has a Collector; use updateManagedCollector to change it", region)
		}
		return collectorsettings.WriteRegion(tree, region, patch)
	}); err != nil {
		apiLog.Error("create managed collector failed", "region", region, "error", err)
		return fmt.Errorf("createManagedCollector: %w", err)
	}
	if err := c.collectorResourceStore().SetCollectorResourceState(ctx, region, store.CollectorResourcesNotCreated, "Collector declared; no cloud provisioning has started"); err != nil {
		rollbackErr := c.mutateCollectorSettings(ctx, func(tree map[string]any) (map[string]any, error) {
			return collectorsettings.RemoveRegion(tree, region), nil
		})
		return fmt.Errorf("createManagedCollector: recording the unprovisioned state: %w", errors.Join(err, rollbackErr))
	}

	apiLog.Info("create managed collector complete; convergence is the reconcile loop's", "region", region)
	c.TriggerCollectorReconcile()
	return nil
}

// UpdateManagedCollector merges settings into a region that already has a Collector.
func (c *Coordinator) UpdateManagedCollector(ctx context.Context, region string, patch collectorsettings.Patch) error {
	apiLog.Info("update managed collector", "region", region)
	if regionalValidator, ok := c.provider.(api.CollectorRegionalSettingsValidator); ok {
		if err := regionalValidator.ValidateCollectorRegionalSettings(ctx, region, patch, false); err != nil {
			return fmt.Errorf("updateManagedCollector: %w", err)
		}
	}
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()
	var manualOriginal *store.CollectorRecord
	if patch.HostNetworkID != nil && *patch.HostNetworkID != c.CollectorSettings().Region(region).HostNetworkID {
		if !collectorsettings.HasRegion(c.storedCollectorSettingsTree(), region) {
			return fmt.Errorf("updateManagedCollector: region %s has no Collector configured; create one first", region)
		}
		state, known, err := c.collectorResourceStore().GetCollectorResourceState(ctx, region)
		if err != nil {
			return fmt.Errorf("updateManagedCollector: reading cloud-resource state: %w", err)
		}
		if state == store.CollectorResourcesMoving || state == store.CollectorResourcesTearingDown {
			return fmt.Errorf("updateManagedCollector: region %s teardown is in progress", region)
		}
		rec, err := c.collectorResourceStore().GetManagedCollector(ctx, store.CollectorKey{Region: region})
		if err != nil && !errors.Is(err, store.ErrCollectorNotFound) {
			return fmt.Errorf("updateManagedCollector: reading existing infrastructure: %w", err)
		}
		if errors.Is(err, store.ErrCollectorNotFound) && known && state != store.CollectorResourcesNotCreated && state != store.CollectorResourcesDestroyed {
			return fmt.Errorf("updateManagedCollector: networkId cannot change while unrecorded cloud resources may exist")
		}
		if err == nil {
			if rec.Mode == spec.ModeManual {
				if rec.CollectorID != "" {
					if c.store == nil {
						return fmt.Errorf("updateManagedCollector: credential store unavailable for manual network change")
					}
					if _, err := c.store.RevokeCollectorCredentials(ctx, rec.CollectorID, collectorcredential.ReasonNetworkChanged, collectorcredential.ActorCentral); err != nil {
						return fmt.Errorf("updateManagedCollector: revoking network-bound credential: %w", err)
					}
					if _, err := c.store.ClearCollectorCredential(ctx, rec.CollectorID); err != nil {
						return fmt.Errorf("updateManagedCollector: clearing network-bound credential: %w", err)
					}
				}
				if err := c.collectorResourceStore().UpsertManagedCollector(ctx, rec.Key, *patch.HostNetworkID, rec.Kind, rec.Mode, rec.Origin, rec.AdoptedAt); err != nil {
					return fmt.Errorf("updateManagedCollector: rebinding manual Collector to target network: %w", err)
				}
				manualOriginal = &rec
			} else if rec.Mode == spec.ModeAutomatic && rec.NetworkID != "" &&
				!hasCloudResourceRefs(rec) && state == store.CollectorResourcesProvisioning {
				return fmt.Errorf("updateManagedCollector: networkId cannot change while the first cloud provisioning pass may have created resources")
			} else if hasCloudResourceRefs(rec) && rec.Mode == spec.ModeAutomatic && rec.Origin == string(collectorownership.OriginOdigos) {
				other := patch
				other.HostNetworkID = nil
				if !reflect.DeepEqual(other, collectorsettings.Patch{}) {
					return fmt.Errorf("updateManagedCollector: change networkId separately from other settings so the migration can resume after a restart")
				}
				if err := c.collectorResourceStore().SetCollectorResourceState(ctx, region, store.CollectorResourcesMoving, *patch.HostNetworkID); err != nil {
					return fmt.Errorf("updateManagedCollector: recording target network: %w", err)
				}
				c.TriggerCollectorReconcile()
				return nil
			} else if hasCloudResourceRefs(rec) {
				return fmt.Errorf("updateManagedCollector: networkId cannot change because the existing Collector infrastructure is customer-owned")
			}
		}
	}

	if err := c.mutateCollectorSettings(ctx, func(tree map[string]any) (map[string]any, error) {
		if !collectorsettings.HasRegion(tree, region) {
			return nil, fmt.Errorf("region %s has no Collector configured; create one first", region)
		}
		return collectorsettings.WriteRegion(tree, region, patch)
	}); err != nil {
		if manualOriginal != nil {
			rollbackErr := c.collectorResourceStore().UpsertManagedCollector(ctx, manualOriginal.Key, manualOriginal.NetworkID,
				manualOriginal.Kind, manualOriginal.Mode, manualOriginal.Origin, manualOriginal.AdoptedAt)
			err = errors.Join(err, rollbackErr)
		}
		apiLog.Error("update managed collector failed", "region", region, "error", err)
		return fmt.Errorf("updateManagedCollector: %w", err)
	}
	if manualOriginal != nil && c.store != nil {
		if err := c.store.SetCollectorStatus(ctx, manualOriginal.Key, collectorstatus.StatusAwaitingCustomer,
			"Host VPC changed. Update the customer-owned IaC, issue a new network-bound OpAMP credential, write it to the secret and redeploy the ECS service."); err != nil {
			apiLog.Warn("recording manual Collector network-change guidance failed", "region", region, "error", err)
		}
	}

	apiLog.Info("update managed collector complete", "region", region)
	c.TriggerCollectorReconcile()
	return nil
}

// TeardownManagedCollector destroys one region's Collector.
//
// The region is disabled before infrastructure is removed. AWS releases some dependencies
// asynchronously — an NLB can keep a security-group ENI briefly after DeleteLoadBalancer returns — so
// a failed partial teardown must not let a concurrent reconcile recreate the resources it just removed.
// The row remains until teardown succeeds: its refs are half the authority to delete anything (§10.4).
//
// A refusal is a result, not an error. Manual mode, customer-created infrastructure and an unrecorded
// stack all reach here legitimately, and the caller has to be told which — an error would render as "the
// request failed, try again" for a state that will never change on its own.
func (c *Coordinator) TeardownManagedCollector(ctx context.Context, region string) (api.CollectorTeardownResult, error) {
	apiLog.Info("teardown managed collector", "region", region)
	key := store.CollectorKey{Region: region}
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()
	state, known, err := c.collectorResourceStore().GetCollectorResourceState(ctx, region)
	if err != nil {
		return api.CollectorTeardownResult{}, err
	}
	if state == store.CollectorResourcesTearingDown || state == store.CollectorResourcesMoving {
		return api.CollectorTeardownResult{Deleted: false, Message: "Collector teardown is in progress and will be retried automatically"}, nil
	}

	rec, err := c.collectorResourceStore().GetManagedCollector(ctx, key)
	if err != nil {
		if errors.Is(err, store.ErrCollectorNotFound) {
			if !collectorsettings.HasRegion(c.storedCollectorSettingsTree(), region) {
				if known && state != store.CollectorResourcesDestroyed && state != store.CollectorResourcesNotCreated {
					return api.CollectorTeardownResult{Deleted: false, Message: "cloud resources may be orphaned; provisioning history exists without a Collector record"}, nil
				}
				return api.CollectorTeardownResult{Deleted: true, Message: "no Collector is configured or recorded in this region"}, nil
			}
			if !c.canDiscardCollectorState(region, nil, state, known) {
				if err := c.collectorResourceStore().SetCollectorResourceState(ctx, region, store.CollectorResourcesOrphaned, "Collector declaration has no infrastructure record; cloud state is uncertain"); err != nil {
					return api.CollectorTeardownResult{}, err
				}
				return api.CollectorTeardownResult{Deleted: false, Message: "cloud provisioning may have started, but no Collector infrastructure is recorded; investigate possible orphaned resources before removing the declaration"}, nil
			}
			return c.removeUnprovisionedCollector(ctx, region, nil)
		}
		return api.CollectorTeardownResult{}, err
	}
	if c.canDiscardCollectorState(region, &rec, state, known) {
		if rec.Mode == spec.ModeManual {
			return c.removeManualCollector(ctx, region, rec)
		}
		return c.removeUnprovisionedCollector(ctx, region, &rec)
	}
	if !hasCloudResourceRefs(rec) && rec.NetworkID == "" {
		if err := c.collectorResourceStore().SetCollectorResourceState(ctx, region, store.CollectorResourcesOrphaned, "Collector row has no infrastructure references; cloud state is uncertain"); err != nil {
			return api.CollectorTeardownResult{}, err
		}
		return api.CollectorTeardownResult{Deleted: false, Message: "cloud resources may be orphaned; the Collector has no recorded infrastructure references, so Odigos cannot safely destroy them", Retained: recordedRefs(rec)}, nil
	}

	class := collectorownership.ClassifyRecorded(rec.Mode, rec.Origin,
		hasCloudResourceRefs(rec) || (rec.Mode == spec.ModeAutomatic && rec.NetworkID != ""))
	retained := recordedRefs(rec)
	if !class.Deletable {
		msg := teardownRefusal(class, rec)
		apiLog.Info("teardown declined", "region", region, "mode", rec.Mode, "origin", rec.Origin, "reason", msg)
		return api.CollectorTeardownResult{Deleted: false, Message: msg, Retained: retained}, nil
	}

	provider, ok := c.provider.(api.CollectorProvider)
	if !ok {
		return api.CollectorTeardownResult{
			Deleted: false, Retained: retained,
			Message: "this connector deploys no Collectors, so there is nothing it may remove",
		}, nil
	}
	deployer, ok := c.collectorTeardownHandlers(provider)[rec.Kind]
	if !ok {
		// A recorded type this connector may not deploy — an observed, manual-mode Collector. Left
		// exactly as it is, which is the whole of the manual-mode rule (§10.6).
		return api.CollectorTeardownResult{
			Deleted: false, Retained: retained,
			Message: fmt.Sprintf("Collector type %q is not one this connector deploys, so Odigos did not create it and will not remove it", rec.Kind),
		}, nil
	}

	// Stop convergence before the first destructive call. If AWS asks us to retry, this disabled entry
	// remains as an explicit teardown-in-progress marker; the row and its refs remain intact, so a retry
	// has the authority it needs without rebuilding the partially removed stack.
	disabled := false
	if err := c.mutateCollectorSettings(ctx, func(tree map[string]any) (map[string]any, error) {
		return collectorsettings.WriteRegion(tree, region, collectorsettings.Patch{Enabled: &disabled})
	}); err != nil {
		return api.CollectorTeardownResult{}, fmt.Errorf("disabling the Collector in region %s before teardown: %w", region, err)
	}

	if err := c.teardownCollector(ctx, deployer, rec); err != nil {
		if errors.Is(err, api.ErrCollectorTeardownRetry) {
			if stateErr := c.collectorResourceStore().SetCollectorResourceState(ctx, region, store.CollectorResourcesTearingDown, err.Error()); stateErr != nil {
				return api.CollectorTeardownResult{}, errors.Join(err, stateErr)
			}
			c.TriggerCollectorReconcile()
			return api.CollectorTeardownResult{Deleted: false, Message: "AWS is releasing Collector network interfaces; teardown will finish automatically", Retained: retained}, nil
		}
		if stateErr := c.collectorResourceStore().SetCollectorResourceState(ctx, region, store.CollectorResourcesOrphaned, "Cloud teardown did not complete; retry with the recorded references"); stateErr != nil {
			return api.CollectorTeardownResult{}, errors.Join(err, stateErr)
		}
		return api.CollectorTeardownResult{}, err
	}
	// A provider may complete its calls yet retain the row because a tagged
	// object lacks a recorded ref. That is an incomplete teardown, not success.
	if remaining, err := c.collectorResourceStore().GetManagedCollector(ctx, key); err == nil {
		if stateErr := c.collectorResourceStore().SetCollectorResourceState(ctx, region, store.CollectorResourcesOrphaned, "Cloud teardown retained its infrastructure record; investigate remaining resources"); stateErr != nil {
			return api.CollectorTeardownResult{}, stateErr
		}
		return api.CollectorTeardownResult{Deleted: false, Message: "cloud teardown is incomplete; the Collector infrastructure record and any remaining resources were retained", Retained: recordedRefs(remaining)}, nil
	} else if !errors.Is(err, store.ErrCollectorNotFound) {
		return api.CollectorTeardownResult{}, err
	}
	if err := c.collectorResourceStore().SetCollectorResourceState(ctx, region, store.CollectorResourcesDestroyed, "Cloud teardown completed"); err != nil {
		return api.CollectorTeardownResult{}, err
	}

	if err := c.removeCollectorLocalState(ctx, region, true); err != nil {
		return api.CollectorTeardownResult{}, err
	}

	apiLog.Info("teardown managed collector complete", "region", region)
	return api.CollectorTeardownResult{Deleted: true}, nil
}

func (c *Coordinator) removeUnprovisionedCollector(ctx context.Context, region string, rec *store.CollectorRecord) (api.CollectorTeardownResult, error) {
	// Freeze the declaration first. If any later database step fails, a retry can
	// finish cleanup without a reconcile creating resources in the meantime.
	disabled := false
	if err := c.mutateCollectorSettings(ctx, func(tree map[string]any) (map[string]any, error) {
		return collectorsettings.WriteRegion(tree, region, collectorsettings.Patch{Enabled: &disabled})
	}); err != nil {
		return api.CollectorTeardownResult{}, err
	}
	if err := c.collectorResourceStore().SetCollectorResourceState(ctx, region, store.CollectorResourcesDestroyed, "No Odigos cloud resources were created; local declaration removed"); err != nil {
		return api.CollectorTeardownResult{}, err
	}
	if err := c.removeCollectorLocalState(ctx, region, rec != nil); err != nil {
		return api.CollectorTeardownResult{}, err
	}
	return api.CollectorTeardownResult{Deleted: true, Message: "Collector declaration removed; no cloud resources required deletion"}, nil
}

// removeManualCollector removes only connector state. Customer-owned ECS, ELB,
// security group, secrets and logs remain for the customer's IaC to remove.
func (c *Coordinator) removeManualCollector(ctx context.Context, region string, rec store.CollectorRecord) (api.CollectorTeardownResult, error) {
	retained := recordedRefs(rec)
	disabled := false
	if err := c.mutateCollectorSettings(ctx, func(tree map[string]any) (map[string]any, error) {
		return collectorsettings.WriteRegion(tree, region, collectorsettings.Patch{Enabled: &disabled})
	}); err != nil {
		return api.CollectorTeardownResult{}, err
	}
	if rec.CollectorID != "" {
		if c.store == nil {
			return api.CollectorTeardownResult{}, fmt.Errorf("credential store unavailable for manual Collector teardown")
		}
		if _, err := c.store.RevokeCollectorCredentials(ctx, rec.CollectorID, collectorcredential.ReasonTeardown, collectorcredential.ActorCentral); err != nil {
			return api.CollectorTeardownResult{}, fmt.Errorf("revoking manual Collector credentials: %w", err)
		}
	}
	if err := c.collectorResourceStore().SetCollectorResourceState(ctx, region, store.CollectorResourcesDestroyed, "Manual declaration removed; customer infrastructure retained"); err != nil {
		return api.CollectorTeardownResult{}, err
	}
	if err := c.removeCollectorLocalState(ctx, region, true); err != nil {
		return api.CollectorTeardownResult{}, err
	}
	return api.CollectorTeardownResult{Deleted: true, Retained: retained, Message: "Collector declaration removed and credentials revoked; customer infrastructure retained"}, nil
}

func (c *Coordinator) removeCollectorLocalState(ctx context.Context, region string, recorded bool) error {
	// Desired configuration is removed first so no later reconcile can recreate the Collector.
	if err := c.mutateCollectorSettings(ctx, func(tree map[string]any) (map[string]any, error) {
		return collectorsettings.RemoveRegion(tree, region), nil
	}); err != nil {
		return fmt.Errorf("removing the configuration for the Collector in region %s: %w", region, err)
	}
	if err := c.collectorResourceStore().DeleteCollectorDestinationsForRegion(ctx, region); err != nil {
		return err
	}
	if err := c.collectorResourceStore().DeleteCollectorActionsForRegion(ctx, region); err != nil {
		return fmt.Errorf("deleting Collector actions: %w", err)
	}
	if recorded {
		if err := c.collectorResourceStore().DeleteManagedCollector(ctx, store.CollectorKey{Region: region}); err != nil {
			return err
		}
	}
	return nil
}

// teardownRefusal says which of the authority rules declined, in the customer's terms: what they would
// have to change to make it deletable, or that nothing will.
func teardownRefusal(class collectorownership.Class, rec store.CollectorRecord) string {
	switch {
	case !class.Mutable:
		return "this Collector is in manual mode: Odigos configures it but never changes or removes its infrastructure"
	case class.Origin != collectorownership.OriginOdigos:
		return "this Collector's infrastructure was created by you, not by Odigos, so Odigos converges it but will not delete it"
	case !hasRecordedRefs(rec):
		return "Odigos holds no record of creating this Collector's infrastructure, and a tag alone is not authority to delete it"
	default:
		return "Odigos may not remove this Collector's infrastructure"
	}
}

// recordedRefs lists what a refusal leaves standing, so a declined teardown names the objects rather
// than only the reason.
// It lists exactly what hasRecordedRefs counts, subnets included. The two disagreeing would produce the
// worst possible refusal: a row whose only recorded refs are subnets would pass the delete gate, and a
// refusal on it would report an EMPTY retained list — telling the customer nothing was left standing
// while something was.
func recordedRefs(rec store.CollectorRecord) []string {
	refs := make([]string, 0, 5+len(rec.BackendRefs)+len(rec.PlacementRefs))
	for _, ref := range append(append([]string{
		rec.WorkloadRef, rec.RevisionRef, rec.RuntimeRef, rec.FrontendRef, rec.IngressRef,
	}, rec.BackendRefs...), rec.PlacementRefs...) {
		if ref != "" {
			refs = append(refs, ref)
		}
	}
	return refs
}

// --- Collector destinations ---
//
// The destinations a Collector owns, not the workload destinations: GetDestinations and its siblings
// say where workloads send, and are configured independently. Every write ends in a Collector reconcile
// trigger, because a destination only takes effect when the rendered configuration reaches the Collector
// over OpAMP; without the nudge a customer would add one and watch nothing happen until the next tick,
// which reads as a broken mutation.

// ManagedCollectorDestinations returns one region's Collector destinations, ordered by name. Empty, not
// an error, for a region with none — including one with no Collector.
func (c *Coordinator) ManagedCollectorDestinations(ctx context.Context, region string) ([]domaindest.Destination, error) {
	return c.store.ListCollectorDestinations(ctx, region)
}

// ManagedCollectorDestination returns one Collector destination, or nil when that region has none by
// that name.
func (c *Coordinator) ManagedCollectorDestination(ctx context.Context, region, name string) (*domaindest.Destination, error) {
	return c.store.GetCollectorDestination(ctx, region, name)
}

// CreateManagedCollectorDestination adds a destination to one region's Collector. Refused for a region
// with no Collector configured, and for a name that region already has.
func (c *Coordinator) CreateManagedCollectorDestination(ctx context.Context, dest domaindest.Destination) error {
	apiLog.Info("create collector destination", "region", dest.Region, "name", dest.Name, "type", dest.Type)
	if err := collectordestination.Validate(dest); err != nil {
		return fmt.Errorf("createManagedCollectorDestination: %w", err)
	}
	if err := c.requireCollector(ctx, dest.Region, "createManagedCollectorDestination"); err != nil {
		return err
	}
	existing, err := c.store.GetCollectorDestination(ctx, dest.Region, dest.Name)
	if err != nil {
		return err
	}
	if existing != nil {
		return fmt.Errorf("createManagedCollectorDestination: region %s already has a destination named %q",
			dest.Region, dest.Name)
	}
	if err := c.store.UpsertCollectorDestination(ctx, dest); err != nil {
		return err
	}
	c.TriggerCollectorReconcile()
	return nil
}

// UpdateManagedCollectorDestination replaces one Collector destination with its updated form. Refused
// when that region has no destination by that name: an update that created one would make a typo'd name
// a second destination, credentials and all.
func (c *Coordinator) UpdateManagedCollectorDestination(ctx context.Context, dest domaindest.Destination) error {
	apiLog.Info("update collector destination", "region", dest.Region, "name", dest.Name, "type", dest.Type)
	if err := collectordestination.Validate(dest); err != nil {
		return fmt.Errorf("updateManagedCollectorDestination: %w", err)
	}
	existing, err := c.store.GetCollectorDestination(ctx, dest.Region, dest.Name)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("updateManagedCollectorDestination: region %s has no destination named %q",
			dest.Region, dest.Name)
	}
	if err := c.store.UpsertCollectorDestination(ctx, dest); err != nil {
		return err
	}
	c.TriggerCollectorReconcile()
	return nil
}

// DeleteManagedCollectorDestination removes one Collector destination, reporting whether there was one.
func (c *Coordinator) DeleteManagedCollectorDestination(ctx context.Context, region, name string) (bool, error) {
	apiLog.Info("delete collector destination", "region", region, "name", name)
	deleted, err := c.store.DeleteCollectorDestination(ctx, region, name)
	if err != nil {
		return false, err
	}
	if deleted {
		c.TriggerCollectorReconcile()
	}
	return deleted, nil
}

// ManagedCollectorActions returns the actions owned by one regional Collector.
func (c *Coordinator) ManagedCollectorActions(ctx context.Context, region string) ([]collectoraction.Action, error) {
	return c.store.ListCollectorActions(ctx, region)
}

// AllManagedCollectorActions is the connector-wide read used by the shared
// Action query when Central has not selected a Collector region. Definitions
// remain region-owned; this is only a read projection for generic clients.
func (c *Coordinator) AllManagedCollectorActions(ctx context.Context) ([]collectoraction.Action, error) {
	return c.store.ListAllCollectorActions(ctx)
}

func (c *Coordinator) ManagedCollectorAction(ctx context.Context, region, name string) (*collectoraction.Action, error) {
	return c.store.GetCollectorAction(ctx, region, name)
}

func (c *Coordinator) CreateManagedCollectorAction(ctx context.Context, action collectoraction.Action) error {
	if err := collectoraction.Validate(action); err != nil {
		return fmt.Errorf("createAction: %w", err)
	}
	if err := c.requireCollector(ctx, action.Region, "createAction"); err != nil {
		return err
	}
	existing, err := c.store.GetCollectorAction(ctx, action.Region, action.Name)
	if err != nil {
		return err
	}
	if existing != nil {
		return fmt.Errorf("createAction: region %s already has an action named %q", action.Region, action.Name)
	}
	if err := c.store.UpsertCollectorAction(ctx, action); err != nil {
		return err
	}
	c.TriggerCollectorReconcile()
	return nil
}

func (c *Coordinator) UpdateManagedCollectorAction(ctx context.Context, action collectoraction.Action) error {
	if err := collectoraction.Validate(action); err != nil {
		return fmt.Errorf("updateAction: %w", err)
	}
	existing, err := c.store.GetCollectorAction(ctx, action.Region, action.Name)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("updateAction: region %s has no action named %q", action.Region, action.Name)
	}
	if err := c.store.UpsertCollectorAction(ctx, action); err != nil {
		return err
	}
	c.TriggerCollectorReconcile()
	return nil
}

func (c *Coordinator) DeleteManagedCollectorAction(ctx context.Context, region, name string) (bool, error) {
	deleted, err := c.store.DeleteCollectorAction(ctx, region, name)
	if err != nil {
		return false, err
	}
	if deleted {
		c.TriggerCollectorReconcile()
	}
	return deleted, nil
}

// ManagedCollectorMetrics returns the Collector's last reported counters. With refresh it waits briefly
// for the next authenticated own-metrics report. OpAMP offers a telemetry destination, but does not
// have a synchronous metrics request, so a fresh periodic report is the only live answer available.
// If none arrives promptly, the last stored snapshot remains available with Live=false.
func (c *Coordinator) ManagedCollectorMetrics(ctx context.Context, region string, refresh bool) (collectormetrics.Record, bool, error) {
	key := store.CollectorKey{Region: region}
	rec, found, err := c.store.CollectorMetrics(ctx, key)
	if err != nil {
		if errors.Is(err, store.ErrCollectorNotFound) {
			return collectormetrics.Record{}, false, nil
		}
		return collectormetrics.Record{}, false, err
	}
	if !refresh {
		return rec, found, nil
	}
	return waitForNextCollectorMetrics(ctx, rec, found, 5*time.Second, 200*time.Millisecond,
		func(readCtx context.Context) (collectormetrics.Record, bool, error) {
			return c.store.CollectorMetrics(readCtx, key)
		})
}

// A newer persisted timestamp proves that a metrics POST completed during this query. A timeout is a
// normal cached result, including when the Collector is disconnected or has never reported.
func waitForNextCollectorMetrics(ctx context.Context, rec collectormetrics.Record, found bool,
	maxWait, pollInterval time.Duration,
	read func(context.Context) (collectormetrics.Record, bool, error),
) (collectormetrics.Record, bool, error) {
	baseline := rec.ObservedAt
	waitCtx, cancel := context.WithTimeout(ctx, maxWait)
	defer cancel()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return collectormetrics.Record{}, false, ctx.Err()
			}
			return rec, found, nil
		case <-ticker.C:
			latest, latestFound, readErr := read(waitCtx)
			if readErr != nil {
				// A store read can be in flight when the five-second refresh window expires.
				// Its deadline error is the same normal cached-answer outcome as the timer
				// branch above; only cancellation of the caller's request is an error.
				if waitCtx.Err() != nil {
					if ctx.Err() != nil {
						return collectormetrics.Record{}, false, ctx.Err()
					}
					return rec, found, nil
				}
				if errors.Is(readErr, store.ErrCollectorNotFound) {
					return collectormetrics.Record{}, false, nil
				}
				return collectormetrics.Record{}, false, readErr
			}
			if latestFound {
				rec, found = latest, true
				if latest.ObservedAt.After(baseline) {
					rec.Live = true
					return rec, true, nil
				}
			}
		}
	}
}

// requireCollector refuses a per-region operation for a region that has no Collector configured.
//
// Checked against the CONFIGURATION rather than the recorded row, because a Collector declared a moment
// ago has no row until the first reconcile pass builds one — and refusing to configure a Collector the
// customer just created would be an arbitrary ordering requirement. What it does catch is a typo'd
// region.
func (c *Coordinator) requireCollector(ctx context.Context, region, op string) error {
	if collectorsettings.HasRegion(c.storedCollectorSettingsTree(), region) {
		return nil
	}
	if _, err := c.store.GetManagedCollector(ctx, store.CollectorKey{Region: region}); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrCollectorNotFound) {
		return err
	}
	return fmt.Errorf("%s: region %s has no Collector; create one first", op, region)
}

// collectorsCapable reports whether this connector has any Collector type enabled. Without one, a
// created Collector would be configuration nothing ever reads.
func (c *Coordinator) collectorsCapable() bool {
	return len(c.provider.Spec().CollectorTypes(c.cfg.Capabilities)) > 0
}
