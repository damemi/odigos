package coordinator

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectordestination"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectoringress"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectormetrics"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorownership"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorreport"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorsettings"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	"github.com/odigos-io/odigos-enterprise/connectors/base/store"
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
	records, err := c.store.ListManagedCollectors(ctx)
	if err != nil {
		apiLog.Error("list managed collectors failed", "error", err)
		return nil, err
	}
	settings := c.CollectorSettings()
	now := time.Now()

	out := make([]collectorreport.ModelInput, 0, len(records))
	for _, rec := range records {
		out = append(out, c.collectorModelInput(rec, settings, now))
	}
	return includeConfiguredCollectorReports(out, collectorsettings.ConfiguredRegions(c.storedCollectorSettingsTree()), settings, now), nil
}

// ManagedCollectorReport returns one region's report input, or nil when none is recorded there.
//
// nil rather than an error for a region with no Collector: the schema's query is nullable precisely
// because "this connector has none there" is a normal answer a client renders, not a failure.
func (c *Coordinator) ManagedCollectorReport(ctx context.Context, region string) (*collectorreport.ModelInput, error) {
	apiLog.Debug("get managed collector", "region", region)
	rec, err := c.store.GetManagedCollector(ctx, store.CollectorKey{Region: region})
	if err != nil {
		if errors.Is(err, store.ErrCollectorNotFound) {
			if collectorsettings.HasRegion(c.storedCollectorSettingsTree(), region) {
				in := pendingCollectorModelInput(region, c.CollectorSettings(), time.Now())
				return &in, nil
			}
			return nil, nil
		}
		apiLog.Error("get managed collector failed", "region", region, "error", err)
		return nil, err
	}
	in := c.collectorModelInput(rec, c.CollectorSettings(), time.Now())
	return &in, nil
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
	class := collectorownership.ClassifyRecorded(rec.Mode, rec.Origin, hasRecordedRefs(rec))
	recordedMessage, problems := recordedCollectorProblems(rec.Status, rec.Message)

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

	// Both sources, because either one alone misses a real Collector. Configuration misses one that was
	// discovered or adopted — it has a row and no config entry — and overwriting that with enabled:true
	// would point the reconcile loop at a Collector somebody else built. The row misses one created
	// seconds ago that no pass has converged yet.
	if _, err := c.store.GetManagedCollector(ctx, store.CollectorKey{Region: region}); err == nil {
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

	apiLog.Info("create managed collector complete; convergence is the reconcile loop's", "region", region)
	c.TriggerCollectorReconcile()
	return nil
}

// UpdateManagedCollector merges settings into a region that already has a Collector.
func (c *Coordinator) UpdateManagedCollector(ctx context.Context, region string, patch collectorsettings.Patch) error {
	apiLog.Info("update managed collector", "region", region)

	if err := c.mutateCollectorSettings(ctx, func(tree map[string]any) (map[string]any, error) {
		if !collectorsettings.HasRegion(tree, region) {
			return nil, fmt.Errorf("region %s has no Collector configured; create one first", region)
		}
		return collectorsettings.WriteRegion(tree, region, patch)
	}); err != nil {
		apiLog.Error("update managed collector failed", "region", region, "error", err)
		return fmt.Errorf("updateManagedCollector: %w", err)
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

	rec, err := c.store.GetManagedCollector(ctx, key)
	if err != nil {
		if errors.Is(err, store.ErrCollectorNotFound) {
			// Idempotent: the requested end state already holds. Also the answer for a second call that
			// arrives while the first is still running, which is why it is a success and not a 404.
			return api.CollectorTeardownResult{Deleted: true, Message: "no Collector is recorded in this region"}, nil
		}
		return api.CollectorTeardownResult{}, err
	}

	class := collectorownership.ClassifyRecorded(rec.Mode, rec.Origin, hasRecordedRefs(rec))
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

	// The same lock every reconcile pass takes. Without it a converge pass could be rebuilding what this
	// is removing, and the two would fight until one of them lost.
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()

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
		return api.CollectorTeardownResult{}, err
	}

	// The destructive pass succeeded. Remove its disabled configuration entry, then its destinations and
	// record. Removal rather than leaving enabled:false ensures a later Collector is never silently
	// reconfigured from stale settings.
	if err := c.mutateCollectorSettings(ctx, func(tree map[string]any) (map[string]any, error) {
		return collectorsettings.RemoveRegion(tree, region), nil
	}); err != nil {
		return api.CollectorTeardownResult{}, fmt.Errorf("removing the configuration for the Collector in region %s: %w", region, err)
	}
	if err := c.store.DeleteCollectorDestinationsForRegion(ctx, region); err != nil {
		return api.CollectorTeardownResult{}, err
	}
	if err := c.store.DeleteManagedCollector(ctx, key); err != nil {
		return api.CollectorTeardownResult{}, err
	}

	apiLog.Info("teardown managed collector complete", "region", region)
	return api.CollectorTeardownResult{Deleted: true}, nil
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

// ManagedCollectorMetrics returns the Collector's last reported counters.
//
// refresh asks for a live round trip to the running Collector. The OpAMP session carries no
// request/response channel for that yet, so the cached snapshot is served and reported honestly as
// Live=false rather than being relabelled live to satisfy the argument. The alternative — failing the
// query — would make a client that always passes refresh unable to read metrics at all.
func (c *Coordinator) ManagedCollectorMetrics(ctx context.Context, region string, refresh bool) (collectormetrics.Record, bool, error) {
	if refresh {
		apiLog.Debug("live Collector metrics were requested; serving the cached snapshot", "region", region)
	}
	rec, found, err := c.store.CollectorMetrics(ctx, store.CollectorKey{Region: region})
	if err != nil {
		if errors.Is(err, store.ErrCollectorNotFound) {
			return collectormetrics.Record{}, false, nil
		}
		return collectormetrics.Record{}, false, err
	}
	return rec, found, nil
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
