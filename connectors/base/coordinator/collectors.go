package coordinator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorownership"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorsettings"
	"github.com/odigos-io/odigos-enterprise/connectors/base/opamp"
	"github.com/odigos-io/odigos-enterprise/connectors/base/store"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// The managed-Collector reconcile loop.
//
// Beside the workload and compute-platform loops rather than inside either, because a Collector is not
// a workload: it is not discovered, it is not instrumented, and — unlike workload instrumentation — it
// is NOT gated on having a destination. A Collector with no destination renders an export-less pipeline
// and keeps running, which is what lets a destination be added later as a configuration push instead of
// a redeployment.

// collectorTarget is one regional Collector to reconcile. NetworkID is retained
// as the physical host VPC passed to providers; Region is the lifecycle identity.
type collectorTarget struct {
	network  api.CollectorNetwork
	observer api.CollectorObserver
	// deployer is nil for a manual-mode type. Not "a deployer that refuses" — nil, so there is no
	// mutating call to make and no check to forget.
	deployer api.CollectorDeployer
	mode     string
}

type collectorResourceStore interface {
	GetCollectorResourceState(context.Context, string) (store.CollectorResourceState, bool, error)
	CollectorResourceStateDetail(context.Context, string) (string, error)
	CollectorResourceStateUpdatedAt(context.Context, string) (time.Time, error)
	ListCollectorResourceStates(context.Context) (map[string]store.CollectorResourceState, error)
	SetCollectorResourceState(context.Context, string, store.CollectorResourceState, string) error
	GetManagedCollector(context.Context, store.CollectorKey) (store.CollectorRecord, error)
	UpsertManagedCollector(context.Context, store.CollectorKey, string, string, string, string, *time.Time) error
	ListManagedCollectors(context.Context) ([]store.CollectorRecord, error)
	DeleteManagedCollector(context.Context, store.CollectorKey) error
	DeleteCollectorDestinationsForRegion(context.Context, string) error
}

func (c *Coordinator) collectorResourceStore() collectorResourceStore {
	if c.collectorResources != nil {
		return c.collectorResources
	}
	return c.store
}

// collectorReconcileLoop converges and observes managed Collectors until ctx is done.
func (c *Coordinator) collectorReconcileLoop(ctx context.Context) {
	provider, ok := c.provider.(api.CollectorProvider)
	if !ok {
		// Not silent. A provider that declares a collectors type the customer then enabled, and
		// implements no CollectorProvider, has a capability with nothing behind it — the same
		// mismatch base.Run says out loud when the config plane is missing, and worth the same
		// noise here rather than a loop that starts, finds no interface and returns.
		if types := c.provider.Spec().CollectorTypes(c.cfg.Capabilities); len(types) > 0 {
			coordLog.Warn("a managed-Collector capability is enabled but this provider implements "+
				"no Collector observation or deployment, so no Collector is reconciled",
				"types", len(types))
		}
		return
	}
	observers, deployers := c.activeCollectorHandlers(provider)
	// Keep the loop running with no active observers: a teardown already in
	// progress still needs retries after a capability change or restart.

	coordLog.Info("managed-Collector reconcile loop starting",
		"observed_types", len(observers), "deployed_types", len(deployers))

	interval := c.cfgMgr.Settings().ReconcileInterval
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	c.runCollectorReconcile(ctx, observers, deployers)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.runCollectorReconcile(ctx, observers, deployers)
			interval = resetTicker(ticker, interval, c.cfgMgr.Settings().ReconcileInterval)
		case <-c.collectorTrigger:
			coordLog.Info("managed-Collector reconcile triggered")
			c.runCollectorReconcile(ctx, observers, deployers)
			interval = deferNextTick(ticker, interval, c.cfgMgr.Settings().ReconcileInterval)
		}
	}
}

// activeCollectorHandlers intersects what the provider implements with what the customer enabled.
//
// The two sets come from different spec accessors, and the difference is the mutation gate:
//
//   - CollectorTypes is either mode. An observer is wired for every type the customer enabled, because
//     Odigos configures a manual-mode Collector too — the customer deployed it, Odigos owns its
//     configuration.
//   - ActiveCollectorDeploymentTypes is automatic only. A deployer is wired for nothing else, so a
//     manual-mode type reaches no mutating code at all.
func (c *Coordinator) activeCollectorHandlers(provider api.CollectorProvider) (map[string]api.CollectorObserver, map[string]api.CollectorDeployer) {
	declaredObservers := provider.CollectorObservers()
	declaredDeployers := provider.CollectorDeployers()

	observers := map[string]api.CollectorObserver{}
	for _, t := range c.provider.Spec().CollectorTypes(c.cfg.Capabilities) {
		if o, ok := declaredObservers[t.Name]; ok {
			observers[t.Name] = o
		}
	}

	deployers := map[string]api.CollectorDeployer{}
	for _, t := range c.provider.Spec().ActiveCollectorDeploymentTypes(c.cfg.Capabilities) {
		d, ok := declaredDeployers[t.Name]
		if !ok {
			continue
		}
		if _, observed := observers[t.Name]; !observed {
			// A type that can be built but not read back. Refused rather than deployed blind: the
			// observer is how a converge pass learns what already exists, and without it every pass
			// would be a first pass.
			coordLog.Warn("a Collector type can be deployed but not observed; skipping it",
				"type", t.Name)
			continue
		}
		deployers[t.Name] = d
	}

	if _, configPlane := c.provider.(opamp.Desired); !configPlane && len(deployers) > 0 {
		// Deployment without a config plane is the one combination that spends the customer's money
		// for nothing: this loop would build Collectors in their account and, with no DesiredConfig
		// to serve them, each would boot on the inert bootstrap fallback and stay there — running,
		// billed, exporting nothing.
		//
		// This is the provider-level form of the contradiction Capabilities.Validate already refuses
		// at the capability level ("Odigos would create a Collector it never configures, which serves
		// an inert fallback for ever"). It has to be caught here as well, because a provider that
		// implements half of this feature declares a perfectly valid capability: the spec check sees
		// management enabled and cannot see that DesiredConfig is missing.
		//
		// Refused rather than degraded: there is no useful partial behaviour to fall back to.
		coordLog.Error("refusing to deploy managed Collectors: this provider renders no Collector "+
			"configuration, so anything deployed here would never be configured",
			"deployable_types", len(deployers))
		deployers = map[string]api.CollectorDeployer{}
	}

	return observers, deployers
}

// runCollectorReconcile is one pass over every regional Collector.
func (c *Coordinator) runCollectorReconcile(ctx context.Context, observers map[string]api.CollectorObserver, deployers map[string]api.CollectorDeployer) {
	if c.draining.Load() {
		return
	}
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()

	c.retryCollectorTeardowns(ctx)
	c.revokeSupersededCollectorCredentials(ctx)
	c.revokeRemovedCapabilityCredentials(ctx)

	settings := collectorsettings.Read(c.cfgMgr.Effective())
	if !settings.Enabled {
		// The feature switch, read live: flipping it off stops convergence on the next pass with no
		// restart, and deletes nothing. A Collector already running keeps running and keeps
		// exporting under its last configuration.
		return
	}

	targets := c.collectorTargets(ctx, observers, deployers)
	if len(targets) == 0 {
		return
	}

	for _, target := range targets {
		c.reconcileCollector(ctx, target, settings)
	}
}

func (c *Coordinator) revokeRemovedCapabilityCredentials(ctx context.Context) {
	if c.store == nil {
		return
	}
	records, err := c.collectorResourceStore().ListManagedCollectors(ctx)
	if err != nil {
		coordLog.Error("listing Collector capabilities for credential cleanup failed", "error", err)
		return
	}
	for _, rec := range records {
		if rec.CollectorID == "" || c.cfg.Capabilities.CollectorMode(rec.Kind) != "" {
			continue
		}
		if _, err := c.store.RevokeCollectorCredentials(ctx, rec.CollectorID, collectorcredential.ReasonCapabilityRemoved, collectorcredential.ActorConnector); err != nil {
			coordLog.Error("revoking Collector credentials after capability removal failed", "region", rec.Key.Region, "error", err)
			continue
		}
		if _, err := c.store.ClearCollectorCredential(ctx, rec.CollectorID); err != nil {
			coordLog.Error("clearing Collector credential pointer after capability removal failed", "region", rec.Key.Region, "error", err)
		}
	}
}

func (c *Coordinator) revokeSupersededCollectorCredentials(ctx context.Context) {
	if c.store == nil {
		return
	}
	records, err := c.collectorResourceStore().ListManagedCollectors(ctx)
	if err != nil {
		coordLog.Error("listing Collector credentials for rotation cleanup failed", "error", err)
		return
	}
	const rotationGrace = 5 * time.Minute
	for _, rec := range records {
		if rec.CollectorID == "" {
			continue
		}
		if err := c.store.RevokeSupersededCollectorCredentials(ctx, rec.CollectorID, time.Now().UTC(), rotationGrace); err != nil {
			coordLog.Error("revoking superseded Collector credentials failed", "region", rec.Key.Region, "error", err)
		}
	}
}

// retryCollectorTeardowns resumes a request that encountered AWS release lag.
// It runs before placement because disabled declarations no longer produce
// deployment targets, while their recorded refs remain the authority to delete.
func (c *Coordinator) retryCollectorTeardowns(ctx context.Context) {
	states, err := c.collectorResourceStore().ListCollectorResourceStates(ctx)
	if err != nil {
		coordLog.Error("listing pending Collector teardowns failed", "error", err)
		return
	}
	provider, ok := c.provider.(api.CollectorProvider)
	if !ok {
		return
	}
	deployers := provider.CollectorDeployers()
	for region, state := range states {
		if state != store.CollectorResourcesTearingDown && state != store.CollectorResourcesMoving {
			continue
		}
		migrationTarget := ""
		if state == store.CollectorResourcesMoving {
			migrationTarget, err = c.collectorResourceStore().CollectorResourceStateDetail(ctx, region)
			if err != nil || migrationTarget == "" {
				coordLog.Error("reading Collector migration target failed", "region", region, "error", err)
				continue
			}
		}
		rec, err := c.collectorResourceStore().GetManagedCollector(ctx, store.CollectorKey{Region: region})
		if err != nil {
			if errors.Is(err, store.ErrCollectorNotFound) {
				if state == store.CollectorResourcesMoving {
					c.finishCollectorMigration(ctx, region, migrationTarget)
					continue
				}
				if err := c.collectorResourceStore().SetCollectorResourceState(ctx, region, store.CollectorResourcesDestroyed, "Cloud teardown completed"); err == nil {
					_ = c.removeCollectorLocalState(ctx, region, false)
				}
				continue
			}
			coordLog.Error("reading pending Collector teardown failed", "region", region, "error", err)
			continue
		}
		deployer, ok := deployers[rec.Kind]
		if !ok || rec.Mode != spec.ModeAutomatic || rec.Origin != string(collectorownership.OriginOdigos) {
			coordLog.Error("pending Collector teardown has no automatic deployer", "region", region, "kind", rec.Kind)
			continue
		}
		if err := c.teardownCollector(ctx, deployer, rec); err != nil {
			if state != store.CollectorResourcesMoving && !errors.Is(err, api.ErrCollectorTeardownRetry) {
				_ = c.collectorResourceStore().SetCollectorResourceState(ctx, region, store.CollectorResourcesOrphaned, err.Error())
			}
			continue
		}
		if _, err := c.collectorResourceStore().GetManagedCollector(ctx, rec.Key); !errors.Is(err, store.ErrCollectorNotFound) {
			if err == nil && state != store.CollectorResourcesMoving {
				_ = c.collectorResourceStore().SetCollectorResourceState(ctx, region, store.CollectorResourcesOrphaned, "Cloud teardown retained its infrastructure record")
			}
			continue
		}
		if state == store.CollectorResourcesMoving {
			c.finishCollectorMigration(ctx, region, migrationTarget)
			continue
		}
		if err := c.collectorResourceStore().SetCollectorResourceState(ctx, region, store.CollectorResourcesDestroyed, "Cloud teardown completed"); err != nil {
			coordLog.Error("recording completed Collector teardown failed", "region", region, "error", err)
			continue
		}
		if err := c.removeCollectorLocalState(ctx, region, false); err != nil {
			coordLog.Error("removing completed Collector declaration failed", "region", region, "error", err)
		}
	}
}

func (c *Coordinator) finishCollectorMigration(ctx context.Context, region, networkID string) {
	if err := c.mutateCollectorSettings(ctx, func(tree map[string]any) (map[string]any, error) {
		return collectorsettings.WriteRegion(tree, region, collectorsettings.Patch{HostNetworkID: &networkID})
	}); err != nil {
		coordLog.Error("writing target VPC after Collector teardown failed", "region", region, "network", networkID, "error", err)
		return
	}
	if err := c.collectorResourceStore().SetCollectorResourceState(ctx, region, store.CollectorResourcesNotCreated, "Old VPC stack removed; target VPC is ready for provisioning"); err != nil {
		coordLog.Error("recording completed Collector migration failed", "region", region, "error", err)
		return
	}
	c.TriggerCollectorReconcile()
}

// collectorTargets asks every observer which networks it has, collapses duplicate
// networks in a region to one host, and pairs each regional Collector with the
// authority for its type. Multiple candidates require an explicit regional host;
// the coordinator refuses to guess because that determines reachability for the
// whole region.
//
// A listing failure costs that type's regions for this pass and nothing else — the same isolation the
// per-region loop below gives, one level up.
func (c *Coordinator) collectorTargets(ctx context.Context, observers map[string]api.CollectorObserver, deployers map[string]api.CollectorDeployer) []collectorTarget {
	var targets []collectorTarget
	for _, name := range sortedKeys(observers) {
		observer := observers[name]
		networks, err := observer.CollectorNetworks(ctx)
		if err != nil {
			coordLog.Error("listing the networks that need a Collector failed",
				"type", name, "error", err)
			continue
		}
		byRegion := map[string][]api.CollectorNetwork{}
		for _, network := range networks {
			if network.Region == "" || network.NetworkID == "" {
				coordLog.Warn("ignoring a managed Collector without region or host network", "type", name,
					"region", network.Region, "network", network.NetworkID)
				continue
			}
			if network.Kind == "" {
				network.Kind = name
			}
			byRegion[network.Region] = append(byRegion[network.Region], network)
		}
		for _, region := range sortedKeys(byRegion) {
			candidates := byRegion[region]
			host := regionalCollectorHost(region, candidates, collectorsettings.Read(c.cfgMgr.Effective()))
			if host.NetworkID == "" {
				coordLog.Error("refusing to reconcile a regional Collector without an unambiguous host network",
					"type", name, "region", region, "candidates", collectorNetworkIDs(candidates))
				continue
			}
			host.Kind = name
			targets = append(targets, collectorTarget{network: host, observer: observer,
				deployer: deployers[name], mode: c.cfg.Capabilities.CollectorMode(name)})
		}
	}
	return targets
}

// reconcileCollector handles ONE regional Collector. The network on the target
// is its selected host VPC, not a second Collector identity.
//
// Every failure is contained here: a panic or an error converging one region must not stop the next.
// Regions are independent by construction, so a host VPC with no egress route or a name collision must
// not hold the whole fleet un-converged.
func (c *Coordinator) reconcileCollector(ctx context.Context, target collectorTarget, settings collectorsettings.Settings) {
	regional := settings.Region(target.network.Region)
	if state, _, err := c.collectorResourceStore().GetCollectorResourceState(ctx, target.network.Region); err != nil ||
		state == store.CollectorResourcesMoving || state == store.CollectorResourcesTearingDown {
		if err != nil {
			coordLog.Error("reading Collector resource state before reconciliation failed", "region", target.network.Region, "error", err)
		}
		return
	}

	defer func() {
		// A provider panic is one network's failure too. Without this a nil map in one deployer takes
		// down the coordinator, and with it the workload instrumentation that has nothing to do with
		// Collectors.
		if r := recover(); r != nil {
			coordLog.Error("reconciling a regional Collector panicked",
				"network", target.network.NetworkID, "region", target.network.Region, "panic", r)
		}
	}()

	// Observation first, and unconditionally. It is the only thing that happens in manual mode, and in
	// automatic mode it is how the converge below learns what already exists.
	if err := target.observer.ObserveCollector(ctx, target.network); err != nil {
		coordLog.Error("observing a Collector failed",
			"network", target.network.NetworkID, "region", target.network.Region,
			"type", target.network.Kind, "error", err)
		// Not returned on: a converge pass that has to create everything from scratch is still better
		// than no pass, and the deployer reads its own state anyway.
	}

	if target.deployer == nil {
		// Manual mode. There is no mutating call to make — see api.CollectorDeployer.
		return
	}
	if !regional.Deploys() {
		// The default retention policy keeps a disabled Collector running. A
		// configured idle policy may scale it to zero or retire its owned stack;
		// this path never enters provisioning.
		if retirer, ok := target.deployer.(api.CollectorDisabledRetirer); ok {
			if err := retirer.RetireDisabledCollector(ctx, target.network); err != nil {
				coordLog.Error("applying disabled Collector retention failed", "region", target.network.Region, "error", err)
			} else if _, err := c.collectorResourceStore().GetManagedCollector(ctx, store.CollectorKey{Region: target.network.Region}); errors.Is(err, store.ErrCollectorNotFound) {
				_ = c.collectorResourceStore().SetCollectorResourceState(ctx, target.network.Region, store.CollectorResourcesDestroyed, "Disabled Collector retired by retention policy")
			}
		}
		return
	}

	state, known, err := c.collectorResourceStore().GetCollectorResourceState(ctx, target.network.Region)
	if err != nil {
		coordLog.Error("reading Collector cloud-resource state failed; skipping deployment", "region", target.network.Region, "error", err)
		return
	}
	if !known || state == store.CollectorResourcesNotCreated {
		if err := c.collectorResourceStore().SetCollectorResourceState(ctx, target.network.Region, store.CollectorResourcesProvisioning, "Cloud provisioning started"); err != nil {
			coordLog.Error("recording Collector cloud-resource state failed; skipping deployment", "region", target.network.Region, "error", err)
			return
		}
	}
	convergeErr := target.deployer.ConvergeCollector(ctx, target.network)
	rec, recordErr := c.collectorResourceStore().GetManagedCollector(ctx, store.CollectorKey{Region: target.network.Region})
	nextState := store.CollectorResourcesProvisioning
	switch {
	case convergeErr != nil && recordErr == nil && hasCloudResourceRefs(rec) && state == store.CollectorResourcesCreated:
		// A transient AWS failure does not turn an existing deployment into an orphan.
		nextState = store.CollectorResourcesCreated
	case convergeErr != nil && errors.Is(recordErr, store.ErrCollectorNotFound) && state == store.CollectorResourcesCreated:
		// Resources were previously recorded, but their row has vanished.
		nextState = store.CollectorResourcesOrphaned
	case convergeErr != nil:
		// The next reconcile may succeed after throttling or an IAM correction.
		// Neither a first failure nor partially recorded refs proves an orphan.
		nextState = store.CollectorResourcesProvisioning
	case recordErr == nil && hasCloudResourceRefs(rec) && convergeErr == nil:
		nextState = store.CollectorResourcesCreated
	case recordErr == nil && !hasCloudResourceRefs(rec) && convergeErr == nil:
		// The provider may have created the derived-name log group before it
		// reached a resource-ref column. Keep the provisioning history so
		// teardown uses its ownership gate instead of discarding the row.
		nextState = store.CollectorResourcesProvisioning
	case errors.Is(recordErr, store.ErrCollectorNotFound) && convergeErr == nil && state == store.CollectorResourcesCreated:
		// Retention teardown can run inside provider convergence and remove the
		// row after successfully destroying its cloud resources.
		nextState = store.CollectorResourcesDestroyed
	case errors.Is(recordErr, store.ErrCollectorNotFound) && convergeErr == nil && state == store.CollectorResourcesDestroyed:
		nextState = store.CollectorResourcesDestroyed
	case errors.Is(recordErr, store.ErrCollectorNotFound) && convergeErr == nil && (!known || state == store.CollectorResourcesNotCreated || state == store.CollectorResourcesProvisioning):
		// Placement can refuse before the provider creates anything.
		nextState = store.CollectorResourcesNotCreated
	case recordErr != nil && known:
		nextState = state
	}
	if err := c.collectorResourceStore().SetCollectorResourceState(ctx, target.network.Region, nextState, collectorResourceStateDetail(nextState)); err != nil {
		coordLog.Error("recording Collector cloud-resource state after deployment failed", "region", target.network.Region, "error", err)
	}
	if recordErr != nil && !errors.Is(recordErr, store.ErrCollectorNotFound) {
		coordLog.Error("reading Collector infrastructure after deployment failed", "region", target.network.Region, "error", recordErr)
	}
	if convergeErr != nil {
		coordLog.Error("converging a Collector failed",
			"network", target.network.NetworkID, "region", target.network.Region,
			"type", target.network.Kind, "error", convergeErr)
	}
}

func collectorResourceStateDetail(state store.CollectorResourceState) string {
	switch state {
	case store.CollectorResourcesNotCreated:
		return "Provisioning completed without recorded cloud resources"
	case store.CollectorResourcesCreated:
		return "Cloud infrastructure is recorded"
	default:
		return "Provisioning may have left cloud resources; inspect the Collector record before teardown"
	}
}

func regionalCollectorHost(region string, candidates []api.CollectorNetwork, settings collectorsettings.Settings) api.CollectorNetwork {
	configured := settings.Region(region).HostNetworkID
	if configured != "" {
		for _, n := range candidates {
			if n.NetworkID == configured {
				return n
			}
		}
		// An explicit host that discovery cannot see is unsafe: the provider may
		// otherwise create resources in an unknown VPC. Leave the region refused
		// until discovery can verify the configured physical location.
		return api.CollectorNetwork{}
	}
	ids := map[string]api.CollectorNetwork{}
	for _, n := range candidates {
		ids[n.NetworkID] = n
	}
	if len(ids) != 1 {
		return api.CollectorNetwork{}
	}
	for _, n := range ids {
		return n
	}
	return api.CollectorNetwork{}
}

func collectorNetworkIDs(candidates []api.CollectorNetwork) []string {
	ids := make([]string, 0, len(candidates))
	seen := map[string]bool{}
	for _, n := range candidates {
		if n.NetworkID != "" && !seen[n.NetworkID] {
			ids = append(ids, n.NetworkID)
			seen[n.NetworkID] = true
		}
	}
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
	return ids
}

// CollectorSettings exposes the resolved managed-Collector configuration, for a provider deployer and
// for reporting. Read live, so a change through updateConfig is visible on the next read.
func (c *Coordinator) CollectorSettings() collectorsettings.Settings {
	return collectorsettings.Read(c.cfgMgr.Effective())
}

// TriggerCollectorReconcile asks for an out-of-band pass. Coalescing: a burst queues at most one.
func (c *Coordinator) TriggerCollectorReconcile() {
	select {
	case c.collectorTrigger <- struct{}{}:
	default:
	}
}

// sortedKeys orders a handler map so a pass visits types in the same order every time. Map iteration
// would make the log of a failing pass differ from run to run.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// drainCollectors tears down every managed Collector this connector recorded, on CR delete.
//
// Called from the shutdown drain and nothing else. Notably NOT from RequestCleanup, which rolls
// instrumentation back while the pod keeps running: disabling every source is not a decision to destroy
// the egress tier, and a customer who re-enables a source a minute later would otherwise have paid for a
// full redeployment of every workload on the network. Sources coming off leaves each network IDLE, and
// the retention policy is what decides how long its Collector is kept — which defaults to
// keeping it.
//
// It enumerates the RECORDED rows rather than the placement plan, and that is the difference between a
// teardown and a converge pass. Placement answers "which networks should have a Collector", which during
// a drain is nobody and which depends on a topology scan that may not even succeed; the rows answer
// "which Collectors did this connector actually build", which is the only question teardown has. A
// network the plan forgot — a VPC deleted, a region that failed to scan — still has a row, and it is
// exactly the one that would otherwise be leaked.
//
// A failure here propagates. The caller withholds shutdownComplete on it, so connector-runtime keeps the
// finalizer and the delete is retried, because dropping it while a Collector is still running in the
// customer's account is precisely the silently-leaked infrastructure exists to prevent.
func (c *Coordinator) drainCollectors(ctx context.Context) error {
	provider, ok := c.provider.(api.CollectorProvider)
	if !ok {
		return nil
	}
	deployers := c.collectorTeardownHandlers(provider)
	if len(deployers) == 0 {
		// Every enabled Collector type is manual-mode, or this provider deploys none. Nothing Odigos
		// created, nothing to remove — "in manual mode teardown deletes nothing, whatever the
		// tags say", enforced by there being no deployer to call rather than by a check inside one.
		return nil
	}

	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()

	records, err := c.recordedCollectors().ListManagedCollectors(ctx)
	if err != nil {
		// Never treated as "nothing recorded". A read failure that returned success here would drop the
		// finalizer with the whole fleet still running, and the refs in those rows are half the authority
		// to remove any of it.
		return fmt.Errorf("listing the managed Collectors to tear down: %w", err)
	}
	coordLog.Info("tearing down managed Collectors", "collectors", len(records))
	var errs []error
	lifecycleStoreAvailable := c.store != nil || c.collectorResources != nil
	if lifecycleStoreAvailable {
		states, err := c.collectorResourceStore().ListCollectorResourceStates(ctx)
		if err != nil {
			return err
		}
		recorded := make(map[string]bool, len(records))
		for _, rec := range records {
			recorded[rec.Key.Region] = true
		}
		configured := make(map[string]bool)
		for _, region := range collectorsettings.ConfiguredRegions(c.storedCollectorSettingsTree()) {
			configured[region] = true
			if recorded[region] {
				continue
			}
			state, known := states[region]
			if c.canDiscardCollectorState(region, nil, state, known) {
				if _, err := c.removeUnprovisionedCollector(ctx, region, nil); err != nil {
					errs = append(errs, err)
				}
			} else {
				errs = append(errs, fmt.Errorf("Collector in %s has no infrastructure record but cloud state %q is unresolved", region, state))
			}
		}
		for region, state := range states {
			if !recorded[region] && !configured[region] && state != store.CollectorResourcesDestroyed && state != store.CollectorResourcesNotCreated {
				errs = append(errs, fmt.Errorf("Collector in %s has unresolved cloud state %q without an infrastructure record", region, state))
			}
		}
	}
	for _, record := range records {
		if lifecycleStoreAvailable {
			state, known, err := c.collectorResourceStore().GetCollectorResourceState(ctx, record.Key.Region)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if c.canDiscardCollectorState(record.Key.Region, &record, state, known) {
				if _, err := c.removeUnprovisionedCollector(ctx, record.Key.Region, &record); err != nil {
					errs = append(errs, err)
				}
				continue
			}
		}
		deployer, ok := deployers[record.Kind]
		if !ok {
			// A row whose type is not one this connector may deploy — a manual-mode Collector it only
			// observes and configures. Left exactly as it is, which is the whole of the manual-mode
			// rule, and reported so a drain does not look like it removed something it did not.
			coordLog.Info("leaving a Collector standing: its type has no deployer, so Odigos did not create it",
				"network", record.NetworkID, "region", record.Key.Region, "type", record.Kind)
			continue
		}
		if err := c.teardownCollector(ctx, deployer, record); err != nil {
			if lifecycleStoreAvailable {
				err = errors.Join(err, c.collectorResourceStore().SetCollectorResourceState(ctx, record.Key.Region, store.CollectorResourcesOrphaned, "Connector shutdown could not finish cloud teardown"))
			}
			errs = append(errs, err)
			continue
		}
		if !lifecycleStoreAvailable {
			continue
		}
		if _, err := c.collectorResourceStore().GetManagedCollector(ctx, record.Key); err == nil {
			errs = append(errs, errors.Join(fmt.Errorf("Collector infrastructure remains recorded in %s after cloud teardown", record.Key.Region),
				c.collectorResourceStore().SetCollectorResourceState(ctx, record.Key.Region, store.CollectorResourcesOrphaned, "Connector shutdown retained a cloud infrastructure record")))
			continue
		} else if !errors.Is(err, store.ErrCollectorNotFound) {
			errs = append(errs, err)
			continue
		}
		if err := c.collectorResourceStore().SetCollectorResourceState(ctx, record.Key.Region, store.CollectorResourcesDestroyed, "Connector shutdown completed cloud teardown"); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := c.removeCollectorLocalState(ctx, record.Key.Region, false); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// collectorRecords is the row listing a drain reads. See Coordinator.collectorRows.
type collectorRecords interface {
	ListManagedCollectors(ctx context.Context) ([]store.CollectorRecord, error)
}

func (c *Coordinator) recordedCollectors() collectorRecords {
	if c.collectorRows != nil {
		return c.collectorRows
	}
	return c.store
}

// teardownCollector removes one network's Collector, containing a provider panic the way the reconcile
// loop does: one network's failure must not abandon the rest of the fleet mid-drain, which would leave
// the remaining Collectors both running and unreported.
func (c *Coordinator) teardownCollector(ctx context.Context, deployer api.CollectorDeployer, record store.CollectorRecord) (err error) {
	defer func() {
		if r := recover(); r != nil {
			coordLog.Error("tearing down a Collector panicked",
				"network", record.NetworkID, "region", record.Key.Region, "panic", r)
			err = fmt.Errorf("tearing down the Collector on %s panicked: %v", record.NetworkID, r)
		}
	}()

	if err := deployer.TeardownCollector(ctx, api.CollectorNetwork{
		Region:    record.Key.Region,
		NetworkID: record.NetworkID,
		Kind:      record.Kind,
	}); err != nil {
		if errors.Is(err, api.ErrCollectorTeardownRetry) {
			coordLog.Info("Collector teardown is waiting for AWS dependencies; reconciliation will retry",
				"network", record.NetworkID, "region", record.Key.Region, "error", err)
		} else {
			coordLog.Error("tearing down a Collector failed; the recorded state permits a retry",
				"network", record.NetworkID, "region", record.Key.Region, "error", err)
		}
		return fmt.Errorf("%s/%s: %w", record.Key.Region, record.NetworkID, err)
	}
	return nil
}

// collectorTeardownHandlers are the deployers a drain may call, and the ONE gate it does not apply.
//
// activeCollectorHandlers refuses to hand out deployers when the provider renders no Collector
// configuration, because building a Collector nothing will ever configure spends the customer's money on
// something that boots inert and stays there. That refusal must not extend to teardown, and the reason
// is that the two fail in opposite directions:
//
//   - refusing to CREATE costs a feature, and is recoverable by fixing the connector;
//   - refusing to DESTROY leaks a running Collector into a customer's account, holding their destination
//     credential, after the connector that owned it is gone.
//
// A build whose config plane was removed — a downgrade to an image that renders nothing — would
// otherwise delete its CR and leave the entire fleet standing, silently, with the rows that authorized
// removing it deleted alongside. So the mode gate applies here (automatic types only) and the
// config-plane gate does not.
func (c *Coordinator) collectorTeardownHandlers(provider api.CollectorProvider) map[string]api.CollectorDeployer {
	declared := provider.CollectorDeployers()
	deployers := map[string]api.CollectorDeployer{}
	for _, t := range c.provider.Spec().ActiveCollectorDeploymentTypes(c.cfg.Capabilities) {
		if d, ok := declared[t.Name]; ok {
			deployers[t.Name] = d
		}
	}
	return deployers
}
