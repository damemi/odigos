package coordinator

import (
	"context"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorsettings"
	"github.com/odigos-io/odigos-enterprise/connectors/base/opamp"
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
	if len(observers) == 0 {
		// No declared type the customer enabled. Nothing to observe and, by construction, nothing to
		// deploy — a deployer without an observer of the same type is unreachable from here.
		return
	}

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

	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()

	for _, target := range targets {
		c.reconcileCollector(ctx, target, settings)
	}
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
		// Disabled for this region. Nothing is deleted: turning a region off keeps whatever is running
		// until somebody explicitly removes it.
		coordLog.Debug("skipping a regional Collector deploy",
			"network", target.network.NetworkID, "region", target.network.Region,
			"enabled", regional.Enabled)
		return
	}

	if err := target.deployer.ConvergeCollector(ctx, target.network); err != nil {
		coordLog.Error("converging a Collector failed",
			"network", target.network.NetworkID, "region", target.network.Region,
			"type", target.network.Kind, "error", err)
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
