// Package coordinator orchestrates a connector runtime: it runs the heartbeat, discovery and (optional)
// instrumentation-reconcile loops, and implements the GraphQL-agnostic api.CentralInterface the
// resolvers delegate to. It is provider-agnostic — which resource types are detected/reconciled is
// resolved from the provider's spec (connectors/spec) for the active permission mode, and the per-type
// work is delegated to the provider's Detectors/Reconcilers.
package coordinator

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/config"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
	"github.com/odigos-io/odigos-enterprise/connectors/base/store"
	cpg "github.com/odigos-io/odigos-enterprise/connectors/postgres"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

var coordLog = connlog.Subsystem("coordinator")

var errNotImplemented = api.ErrUnsupported

type Coordinator struct {
	cfg      config.Config
	cfgMgr   *config.Manager
	store    *store.Store
	provider api.Provider
	// workloadKinds: directly-instrumentable kinds (supportedSourceKinds + workload createSource).
	workloadKinds []string
	// computePlatformKinds: compute-platform assignment kinds (createSource for agent install on managed environments).
	computePlatformKinds []string
	destCatalog  domaindest.Catalog

	discoveryReady atomic.Bool
	authenticated  atomic.Bool
	// domainErrs holds the last error string per discovery domain ("workload"/"computePlatform"). Keyed per domain
	// so a healthy cycle in one domain does not erase a persistent failure in the other (a single shared
	// error field let the compute-platform loop's success mask a broken workload loop and vice-versa).
	domainErrs sync.Map // domain string -> string
	// draining is set once a drain request is observed (CR-delete cleanup or manual RequestCleanup) so the
	// normal reconcile loops stop instrumenting while the drain rolls everything back — they must not race
	// the rollback by re-instrumenting a source the drain just disabled.
	draining atomic.Bool
	// automaticDrainDone is set after a successful drainInstrumentation during CR-delete cleanup. Later
	// heartbeat ticks skip re-running that mutate pass and only re-count manual-mode leftovers — discovery
	// must keep running so the customer removing IaC instrumentation can be observed.
	automaticDrainDone atomic.Bool

	// awaitingManual holds the live count/sample of manual-mode workloads still instrumented after the
	// automatic drain. Heartbeat merges these into health so connector-runtime can surface
	// ManualCleanupRequired while withholding the finalizer. Guarded by awaitingMu.
	awaitingMu              sync.Mutex
	awaitingManualRetained  int
	awaitingManualWorkloads []string

	// reconcileMu serializes every reconcile pass (workload loop, compute-platform loop, shutdown drain and manual
	// cleanup) so at most one runs at a time. The runtime is not leader-elected; serializing passes keeps
	// a manual RequestCleanup from mutating provider resources concurrently with a loop tick.
	reconcileMu sync.Mutex

	workloadDiscoveryTrigger chan struct{}
	computePlatformDiscoveryTrigger  chan struct{}
	reconcileTrigger         chan struct{}
	computePlatformTrigger           chan struct{}
	observeTrigger           chan struct{}
}

func New(cfg config.Config, cfgMgr *config.Manager, st *store.Store, p api.Provider) *Coordinator {
	return &Coordinator{
		cfg:           cfg,
		cfgMgr:        cfgMgr,
		store:         st,
		provider:      p,
		workloadKinds: p.SupportedWorkloadKinds(),
		computePlatformKinds:  p.SupportedComputePlatformKinds(),
		destCatalog:   domaindest.NewCatalog(api.SupportedDestinationTypes(p)),
		// buffered + coalescing: a burst queues at most one extra pass per loop.
		workloadDiscoveryTrigger: make(chan struct{}, 1),
		computePlatformDiscoveryTrigger:  make(chan struct{}, 1),
		reconcileTrigger:         make(chan struct{}, 1),
		computePlatformTrigger:           make(chan struct{}, 1),
		observeTrigger:           make(chan struct{}, 1),
	}
}

// SupportedKinds returns all workload + assignable compute-platform kinds (registration / internal use).
func (c *Coordinator) SupportedKinds() []string {
	return append(append([]string{}, c.workloadKinds...), c.computePlatformKinds...)
}

// SupportedWorkloadKinds returns directly-instrumentable workload kinds.
func (c *Coordinator) SupportedWorkloadKinds() []string { return c.workloadKinds }

// resetTicker resets t to want when it differs from the current interval (and want is valid), returning
// the new current interval. Lets a loop pick up a live config-interval change without a restart.
func resetTicker(t *time.Ticker, current, want time.Duration) time.Duration {
	if want > 0 && want != current {
		coordLog.Info("config interval changed; resetting ticker", "from", current, "to", want)
		t.Reset(want)
		return want
	}
	return current
}

// Config returns the effective operational config (defaults + stored overrides) for the agents-api config
// query. ConfigDefaults returns just the code defaults. UpdateConfig merges a patch and applies it live.
func (c *Coordinator) Config(_ context.Context) (map[string]any, error) {
	return c.cfgMgr.Effective(), nil
}

func (c *Coordinator) ConfigDefaults(_ context.Context) (map[string]any, error) {
	return c.cfgMgr.Defaults(), nil
}

func (c *Coordinator) UpdateConfig(ctx context.Context, patch map[string]any) (map[string]any, error) {
	if err := c.cfgMgr.Update(ctx, patch); err != nil {
		return nil, err
	}
	// Discovery tags / intervals may have changed — converge now rather than waiting for the next tick.
	c.triggerReconcile()
	_ = c.RefreshDiscovery(ctx)
	return c.cfgMgr.Effective(), nil
}

// Start runs the heartbeat, reconcile and discovery loops until ctx is cancelled. It blocks on the
// discovery loop.
func (c *Coordinator) Start(ctx context.Context) {
	s := c.cfgMgr.Settings()
	coordLog.Info("coordinator loops starting",
		"heartbeat_interval", s.HeartbeatInterval,
		"discovery_interval", s.DiscoveryInterval,
		"reconcile_interval", s.ReconcileInterval,
	)
	// loopCtx lets a completed drain stop every loop — including the heartbeat — so the recorded
	// drain-complete status is not overwritten by a later heartbeat write. After it fires the process
	// idles (serving GraphQL) until the controller removes the finalizer and the pod is GC'd.
	loopCtx, cancelLoops := context.WithCancel(ctx)
	defer cancelLoops()
	go c.heartbeatLoop(loopCtx, cancelLoops)
	go c.reconcileLoop(loopCtx)
	go c.observeLoop(loopCtx)
	go c.computePlatformReconcileLoop(loopCtx)
	// Workloads and compute platforms discover on independent loops over their own detector sets + stores.
	go c.discoveryLoop(loopCtx, "computePlatform", c.activeComputePlatformDetectors, c.computePlatformDiscoveryTrigger)
	c.discoveryLoop(loopCtx, "workload", c.activeWorkloadDetectors, c.workloadDiscoveryTrigger)
}

func (c *Coordinator) heartbeatLoop(ctx context.Context, cancelLoops context.CancelFunc) {
	interval := c.cfgMgr.Settings().HeartbeatInterval
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// Emit an initial heartbeat immediately so the controller observes liveness within seconds of startup
	// rather than only after a full HeartbeatInterval.
	if c.tickHeartbeat(ctx, cancelLoops) {
		coordLog.Info("drain complete; stopping runtime loops")
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if c.tickHeartbeat(ctx, cancelLoops) {
				coordLog.Info("drain complete; stopping runtime loops")
				return
			}
			interval = resetTicker(ticker, interval, c.cfgMgr.Settings().HeartbeatInterval)
		}
	}
}

// tickHeartbeat refreshes the live config, observes any pending drain (draining in-loop while the pod is
// running, so a transient provider error just retries next tick with nothing force-killed mid-rollback),
// and writes one heartbeat. Returns true when a drain has completed and the caller should stop the loops.
func (c *Coordinator) tickHeartbeat(ctx context.Context, cancelLoops context.CancelFunc) bool {
	// Refresh operational config from the DB so config/updateConfig changes (intervals, tags, toggles)
	// apply without a pod restart. The heartbeat loop is the single refresher; the other loops read the
	// refreshed settings via cfgMgr.Settings().
	if err := c.cfgMgr.Refresh(ctx); err != nil {
		coordLog.Warn("config refresh failed; using last-known config", "error", err)
	}
	if c.observeDrain(ctx, cancelLoops) {
		return true
	}
	status := cpg.HeartbeatStatusConnected
	if !c.discoveryReady.Load() || !c.authenticated.Load() {
		status = cpg.HeartbeatStatusStarting
	}
	if c.lastErrString() != "" {
		status = cpg.HeartbeatStatusDegraded
	}
	health := map[string]any{
		"discoveryReady":      c.discoveryReady.Load(),
		"authenticated":       c.authenticated.Load(),
		"workloadReportKinds": c.workloadKinds,
	}
	c.mergeAwaitingManual(health)
	if err := c.store.Heartbeat(ctx, status, health); err != nil {
		coordLog.Error("heartbeat write failed", "status", status, "error", err)
	} else {
		coordLog.Debug("heartbeat written", "status", status)
	}
	return false
}

// discoveryLoop runs one discovery domain (workload or compute-platform) on its own ticker + refresh trigger,
// over the detector set returned by resolve. The two domains run as independent loops.
func (c *Coordinator) discoveryLoop(ctx context.Context, domain string, resolve func() []namedDetector, trigger <-chan struct{}) {
	interval := c.cfgMgr.Settings().DiscoveryInterval
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	c.runDiscovery(ctx, domain, resolve())
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.runDiscovery(ctx, domain, resolve())
			interval = resetTicker(ticker, interval, c.cfgMgr.Settings().DiscoveryInterval)
		case <-trigger:
			coordLog.Info("discovery refresh triggered", "domain", domain)
			c.runDiscovery(ctx, domain, resolve())
		}
	}
}

// namedDetector pairs a spec type name with its detector for logging.
type namedDetector struct {
	name string
	det  api.Detector
}

// activeWorkloadDetectors resolves the WORKLOAD discovery detectors: the spec's active workload discovery
// types intersected with the provider's workload detectors.
func (c *Coordinator) activeWorkloadDetectors() []namedDetector {
	dets := c.provider.WorkloadDetectors()
	var out []namedDetector
	for _, t := range c.provider.Spec().ActiveWorkloadDiscoveryTypes(c.cfg.Capabilities) {
		if d, ok := dets[t.Name]; ok {
			out = append(out, namedDetector{name: t.Name, det: d})
		} else {
			coordLog.Warn("workload discovery type enabled but no detector registered", "type", t.Name)
		}
	}
	return out
}

// activeComputePlatformDetectors resolves the COMPUTE-PLATFORM discovery detectors: the spec's active compute-platform discovery
// types intersected with the provider's compute-platform detectors.
func (c *Coordinator) activeComputePlatformDetectors() []namedDetector {
	dets := c.provider.ComputePlatformDetectors()
	var out []namedDetector
	for _, t := range c.provider.Spec().ActiveComputePlatformDiscoveryTypes(c.cfg.Capabilities) {
		if d, ok := dets[t.Name]; ok {
			out = append(out, namedDetector{name: t.Name, det: d})
		} else {
			coordLog.Warn("compute-platform discovery type enabled but no detector registered", "type", t.Name)
		}
	}
	return out
}

func (c *Coordinator) runDiscovery(ctx context.Context, domain string, dets []namedDetector) {
	if len(dets) == 0 {
		coordLog.Debug("no active discovery detectors", "domain", domain)
		c.discoveryReady.Store(true)
		return
	}
	coordLog.Info("discovery cycle starting", "domain", domain, "types", len(dets))
	start := time.Now()
	var anyErr error
	authed := false
	// A provider may register one detector under several type names; run each distinct detector instance
	// at most once per cycle.
	ran := make(map[api.Detector]bool, len(dets))
	for _, nd := range dets {
		if ran[nd.det] {
			continue
		}
		ran[nd.det] = true
		if err := ensurePrerequisite(ctx, nd.det); err != nil {
			anyErr = err
			coordLog.Warn("discovery prerequisite not met; skipping detector", "type", nd.name, "error", err)
			continue
		}
		res, err := nd.det.Detect(ctx)
		if err != nil {
			anyErr = err
			coordLog.Error("discovery detector failed", "type", nd.name, "error", err)
			continue
		}
		if res.Authenticated {
			authed = true
		}
	}
	// Reflect the cycle's auth result so revoked credentials flip health back to unauthenticated (the flag
	// was previously only ever set true). Only update when detectors actually ran, so a cycle with no
	// active detectors doesn't spuriously clear it.
	if len(ran) > 0 {
		c.authenticated.Store(authed)
	}
	if anyErr != nil {
		c.domainErrs.Store(domain, anyErr.Error())
		coordLog.Warn("discovery cycle finished with errors",
			"domain", domain,
			"types", len(dets),
			"duration", time.Since(start).Round(time.Millisecond),
			"error", anyErr,
		)
		// Do NOT mark discoveryReady on a failed cycle: a cycle where every detector failed has not
		// produced a usable inventory, so the connector stays "Starting" until a cycle succeeds.
		return
	}
	c.domainErrs.Store(domain, "")
	coordLog.Info("discovery cycle complete",
		"domain", domain,
		"types", len(dets),
		"duration", time.Since(start).Round(time.Millisecond),
	)
	c.discoveryReady.Store(true)
	// Converge immediately on what discovery just observed — a newly discovered workload matching an
	// enabled source, a workload that drifted (lost instrumentation), or one that disappeared — rather than
	// waiting for the next reconcile ticker. Coalesced, so back-to-back cycles don't pile up.
	c.triggerReconcile()
	// Manual-mode types have no reconciler to converge, so their equivalent of that is an observation pass:
	// a workload discovered just now should have its conformance read in this cycle, not in the next one.
	c.triggerObserve()
}

// namedReconciler pairs a spec instrumentation type name with its reconciler.
type namedReconciler struct {
	name string
	rec  api.Reconciler
}

// activeReconcilers resolves the instrumentation reconcilers to run from the spec's instrumentation
// types enabled by the connector's capabilities, intersected with the provider's reconcilers.
func (c *Coordinator) activeReconcilers() []namedReconciler {
	recs := c.provider.Reconcilers()
	if len(recs) == 0 {
		return nil
	}
	var out []namedReconciler
	for _, t := range c.provider.Spec().ActiveInstrumentationTypes(c.cfg.Capabilities) {
		// ActiveInstrumentationTypes resolves AUTOMATIC-mode types only, which is what keeps Odigos out of a
		// workload the customer owns: a manual-mode type gets no reconciler here, so no mutation path exists for
		// it anywhere in the runtime — including the shutdown drain, which re-runs these same reconcilers.
		// TestReconcilersNeverIncludeManualModeTypes pins that agreement between this resolver and
		// Capabilities.WorkloadInstrumentationMode across every capability combination.
		if r, ok := recs[t.Name]; ok {
			out = append(out, namedReconciler{name: t.Name, rec: r})
		}
	}
	return out
}

// namedComputePlatformReconciler pairs a spec compute-platform type name with its reconciler for logging.
type namedComputePlatformReconciler struct {
	name string
	rec  api.ComputePlatformReconciler
}

// activeComputePlatformReconcilers resolves the compute-platform reconcilers to run from the spec's compute-platform types enabled
// by the connector's capabilities, intersected with the provider's compute-platform reconcilers.
func (c *Coordinator) activeComputePlatformReconcilers() []namedComputePlatformReconciler {
	recs := c.provider.ComputePlatformReconcilers()
	if len(recs) == 0 {
		return nil
	}
	var out []namedComputePlatformReconciler
	for _, t := range c.provider.Spec().ActiveComputePlatformTypes(c.cfg.Capabilities) {
		if r, ok := recs[t.Name]; ok {
			out = append(out, namedComputePlatformReconciler{name: t.Name, rec: r})
		}
	}
	return out
}

// ensurePrerequisite runs a step's optional api.Prerequisite before the step executes. Steps (detectors
// or reconcilers) that don't implement it always pass. Used to gate discovery/instrumentation on an
// idempotent setup that must exist before discovery or instrumentation can proceed.
func ensurePrerequisite(ctx context.Context, step any) error {
	if p, ok := step.(api.Prerequisite); ok {
		return p.EnsurePrerequisite(ctx)
	}
	return nil
}

func (c *Coordinator) reconcileLoop(ctx context.Context) {
	recs := c.activeReconcilers()
	if len(recs) == 0 {
		return
	}
	coordLog.Info("reconcile loop starting", "types", len(recs))
	interval := c.cfgMgr.Settings().ReconcileInterval
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	c.runReconcile(ctx, recs)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.runReconcile(ctx, recs)
			interval = resetTicker(ticker, interval, c.cfgMgr.Settings().ReconcileInterval)
		case <-c.reconcileTrigger:
			coordLog.Info("reconcile triggered by source change")
			c.runReconcile(ctx, recs)
		}
	}
}

// runReconcile runs one reconcile pass over the active reconcilers, ensuring each one's prerequisite
// before its step.
func (c *Coordinator) runReconcile(ctx context.Context, recs []namedReconciler) {
	// While a drain is in progress the drain owns rollback; skip normal reconcile so we never re-instrument
	// a source the drain just disabled.
	if c.draining.Load() {
		return
	}
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()
	for _, nr := range recs {
		if err := ensurePrerequisite(ctx, nr.rec); err != nil {
			coordLog.Warn("instrumentation prerequisite not met; skipping reconcile", "type", nr.name, "error", err)
			continue
		}
		if err := nr.rec.Reconcile(ctx); err != nil {
			coordLog.Error("reconcile failed", "type", nr.name, "error", err)
		}
	}
}

// computePlatformReconcileLoop is the compute-platform counterpart to reconcileLoop, run as a separate loop: service
// types converge a separately-installed Odigos agent rather than instrumenting workloads directly, and
// are not gated on a destination.
func (c *Coordinator) computePlatformReconcileLoop(ctx context.Context) {
	recs := c.activeComputePlatformReconcilers()
	if len(recs) == 0 {
		return
	}
	coordLog.Info("compute-platform reconcile loop starting", "types", len(recs))
	interval := c.cfgMgr.Settings().ReconcileInterval
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	c.runComputePlatformReconcile(ctx, recs)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.runComputePlatformReconcile(ctx, recs)
			interval = resetTicker(ticker, interval, c.cfgMgr.Settings().ReconcileInterval)
		case <-c.computePlatformTrigger:
			coordLog.Info("compute-platform reconcile triggered by source change")
			c.runComputePlatformReconcile(ctx, recs)
		}
	}
}

// runComputePlatformReconcile runs one pass over the active compute-platform reconcilers, ensuring each one's
// prerequisite before its step.
func (c *Coordinator) runComputePlatformReconcile(ctx context.Context, recs []namedComputePlatformReconciler) {
	if c.draining.Load() {
		return
	}
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()
	for _, nr := range recs {
		if err := ensurePrerequisite(ctx, nr.rec); err != nil {
			coordLog.Warn("service prerequisite not met; skipping reconcile", "type", nr.name, "error", err)
			continue
		}
		if err := nr.rec.Reconcile(ctx); err != nil {
			coordLog.Error("compute-platform reconcile failed", "type", nr.name, "error", err)
		}
	}
}

// --- api.CentralInterface ---

func (c *Coordinator) SupportedSourceKinds(_ context.Context) ([]string, error) {
	return c.workloadKinds, nil
}

func (c *Coordinator) WorkloadReports(ctx context.Context) ([]api.DiscoveredWorkload, error) {
	coordLog.Debug("listing workload reports")
	raw, err := c.provider.ListWorkloads(ctx)
	if err != nil {
		coordLog.Error("list workloads failed", "error", err)
		return nil, err
	}
	out, err := c.enrichWithSources(ctx, raw)
	if err != nil {
		coordLog.Error("enrich workloads failed", "error", err)
		return nil, err
	}
	c.markInstrumentationAvailability(out)
	c.markBlockedOnPrerequisites(ctx, out)
	// Ordered after the prerequisite check so a missing destination wins over "waiting for the customer".
	c.markAwaitingCustomer(out)
	coordLog.Debug("list workload reports complete", "count", len(out))
	return out, nil
}

// markBlockedOnPrerequisites flags workloads the customer marked for instrumentation that the
// reconcile is deliberately skipping because a connector-wide precondition is unmet.
//
// Today that is the OTLP destination: every connector instrumentation exports directly to it, so
// without one the instrumentors log a warning and skip. That skip was invisible to the customer —
// the workload showed as enabled and not instrumented, with nothing saying why or that it will
// resolve itself once a destination exists.
//
// It is marked here rather than in each instrumentor because the condition is a property of the
// connector, not of a workload: one check covers Lambda, Fargate and Cloud Run alike, needs no
// per-workload persistence, and cannot go stale the way a written flag would.
func (c *Coordinator) markBlockedOnPrerequisites(ctx context.Context, workloads []api.DiscoveredWorkload) {
	if len(workloads) == 0 {
		return
	}
	// Only workloads that would otherwise be instrumented can be blocked, so skip the lookup entirely
	// when none is waiting on anything.
	wanted := false
	for i := range workloads {
		if blockableForInstrumentation(workloads[i]) {
			wanted = true
			break
		}
	}
	if !wanted {
		return
	}

	_, haveDest, err := c.store.OTLPExportTarget(ctx)
	if err != nil {
		// The destination state is unknown, not absent. Reporting "waiting for a destination" on a
		// transient store error would be a guess, and a wrong one blames the customer's configuration
		// for our failure.
		coordLog.Warn("resolve otlp export target for workload reports failed; not reporting a blocked state", "error", err)
		return
	}
	if haveDest {
		return
	}
	for i := range workloads {
		if blockableForInstrumentation(workloads[i]) {
			workloads[i].WaitingFor = api.WaitingForDestination
		}
	}
}

// blockableForInstrumentation reports whether a workload is one the reconcile would try to
// instrument: marked and enabled by the customer, not already instrumented, and of a type this
// connector actually implements. A workload failing any of those has a more specific story to tell
// (Unknown, NotImplemented, already working) and must not be relabelled as merely waiting.
func blockableForInstrumentation(w api.DiscoveredWorkload) bool {
	return w.SourceMatched && w.SourceEnabled && !w.Instrumented &&
		w.InstrumentationAvailable && w.InstrumentationImplemented
}

// markInstrumentationAvailability flags each workload whose instrumentation type this connector handles,
// and in which mode.
//
// Availability spans BOTH modes deliberately. "Available" answers "does this connector have instrumentation
// for this workload at all", which is true of a manual-mode type — the customer applies it, but there is a
// defined instrumentation and a plan to publish. Resolving it from the automatic-only set instead would
// report every manual-mode workload as Unknown / "no instrumentation defined", which is the opposite of the
// truth and hides the state the customer is actually in. The genuinely-unavailable case survives: a kind
// with no instrumentation type declared at all (InstrumentationType == "") has nothing to offer in any mode.
//
// This is also why availability must not be used to authorize a mutation — ManagementMode is what says
// whether Odigos may act, and the mutating reconcilers are separately gated by ActiveInstrumentationTypes.
//
// It also flags whether the type's instrumentation is implemented in this release: a declared-but-
// unimplemented type (implemented:false) is reported as NotImplemented rather than as working.
func (c *Coordinator) markInstrumentationAvailability(workloads []api.DiscoveredWorkload) {
	handled := make(map[string]bool)
	for _, t := range c.provider.Spec().InstrumentationTypes(c.cfg.Capabilities) {
		handled[t.Name] = true
	}
	unimplemented := c.provider.Spec().UnimplementedInstrumentationKinds()
	for i := range workloads {
		t := workloads[i].InstrumentationType
		workloads[i].InstrumentationAvailable = t != "" && handled[t]
		workloads[i].InstrumentationImplemented = !unimplemented[t]
		if t != "" {
			workloads[i].ManagementMode = c.cfg.Capabilities.WorkloadInstrumentationMode(t)
		}
	}
}

// markAwaitingCustomer flags manual-mode workloads the customer has marked but not yet instrumented, so the
// report says "waiting for your deployment" instead of leaving an enabled-but-uninstrumented workload
// unexplained.
//
// It runs after markBlockedOnPrerequisites and only fills an empty WaitingFor, so a missing destination keeps
// precedence: the instructions the customer needs must carry an OTLP endpoint, so until a destination exists
// there is nothing for them to apply and telling them to act would be wrong.
//
// Like the destination check this belongs in the base rather than in a provider: the mode is a property of
// the connector's capabilities, so one check covers Lambda, Fargate, Cloud Run and Container Apps alike.
func (c *Coordinator) markAwaitingCustomer(workloads []api.DiscoveredWorkload) {
	for i := range workloads {
		w := &workloads[i]
		if w.WaitingFor != "" || w.ManagementMode != spec.ModeManual {
			continue
		}
		if awaitingCustomerAction(*w) {
			w.WaitingFor = api.WaitingForCustomer
		}
	}
}

// awaitingCustomerAction reports whether a manual-mode workload is marked for instrumentation and the
// customer's deployment has not landed yet. A workload already carrying Odigos instrumentation, one whose
// source is disabled or absent, and one whose type this connector cannot instrument each have a more
// specific story to tell and must not be relabelled as merely waiting.
func awaitingCustomerAction(w api.DiscoveredWorkload) bool {
	return w.SourceMatched && w.SourceEnabled && !w.Instrumented &&
		w.InstrumentationAvailable && w.InstrumentationImplemented
}

// markComputePlatformSupport overrides the reported status of discovered compute platforms whose compute-platform type is declared
// but not implemented in this release (a stub assign/install flow), so they surface as NotImplemented
// rather than Assigned/Discovered — an unsupported managed compute-platform capability is never shown as working.
func (c *Coordinator) markComputePlatformSupport(services []api.DiscoveredComputePlatform) {
	unimplemented := c.provider.Spec().UnimplementedComputePlatformKinds()
	if len(unimplemented) == 0 {
		return
	}
	for i := range services {
		if unimplemented[services[i].Kind] {
			services[i].Status = api.ComputePlatformStatusNotImplemented
		}
	}
}

// ComputePlatformReports returns the discovered compute platforms (managed install targets) from the compute-platform store,
// enriched with assignment state from enabled service-assignment sources where applicable.
func (c *Coordinator) ComputePlatformReports(ctx context.Context) ([]api.DiscoveredComputePlatform, error) {
	coordLog.Debug("listing compute-platform reports")
	raw, err := c.provider.ListComputePlatforms(ctx)
	if err != nil {
		coordLog.Error("list compute platforms failed", "error", err)
		return nil, err
	}
	out, err := c.enrichComputePlatformsWithSources(ctx, raw)
	if err != nil {
		coordLog.Error("enrich services failed", "error", err)
		return nil, err
	}
	c.markComputePlatformSupport(out)
	coordLog.Debug("list compute-platform reports complete", "count", len(out))
	return out, nil
}

// ComputePlatformReport returns a single discovered compute platform by composite key (name + kind + region); an empty
// region matches by name + kind alone.
func (c *Coordinator) ComputePlatformReport(ctx context.Context, name, kind, region string) (*api.DiscoveredComputePlatform, error) {
	coordLog.Debug("get compute-platform report", "name", name, "kind", kind, "region", region)
	all, err := c.ComputePlatformReports(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Name == name && all[i].Kind == kind && (region == "" || all[i].Region == region) {
			return &all[i], nil
		}
	}
	return nil, nil
}

func (c *Coordinator) WorkloadReport(ctx context.Context, serviceName, kind, region string) (*api.DiscoveredWorkload, error) {
	coordLog.Debug("get workload report", "service", serviceName, "kind", kind, "region", region)
	all, err := c.WorkloadReports(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		// region is part of the composite key; an empty region matches by serviceName + kind alone
		// (region-less callers, e.g. a UI that hasn't adopted the region field yet).
		if all[i].ServiceName == serviceName && all[i].Kind == kind && (region == "" || all[i].Region == region) {
			return &all[i], nil
		}
	}
	return nil, nil
}

func (c *Coordinator) RefreshDiscovery(_ context.Context) error {
	// Refresh both discovery domains (each loop has its own coalescing trigger).
	for _, t := range []chan struct{}{c.workloadDiscoveryTrigger, c.computePlatformDiscoveryTrigger} {
		select {
		case t <- struct{}{}:
		default:
		}
	}
	coordLog.Debug("discovery refresh queued")
	return nil
}

// triggerReconcile requests an out-of-band reconcile pass (e.g. after a source create/update/delete) so
// instrumentation converges immediately rather than waiting for the next ticker. Coalesces with any
// already-queued pass; a no-op when no reconcilers are active (the loop isn't draining the channel).
func (c *Coordinator) triggerReconcile() {
	select {
	case c.reconcileTrigger <- struct{}{}:
		coordLog.Debug("reconcile queued")
	default:
		coordLog.Debug("reconcile already queued")
	}
	// Source mutations also affect compute-platform assignment, so kick the separate compute-platform loop too.
	select {
	case c.computePlatformTrigger <- struct{}{}:
		coordLog.Debug("compute-platform reconcile queued")
	default:
		coordLog.Debug("compute-platform reconcile already queued")
	}
}

func (c *Coordinator) RuntimeHealth(ctx context.Context) (api.Health, error) {
	last, ok, _ := c.store.LastHeartbeat(ctx)
	h := api.Health{
		Registered:      ok,
		SchemaVersionOK: true, // the runtime would have exited at startup on mismatch
		Authenticated:   c.authenticated.Load(),
		DiscoveryReady:  c.discoveryReady.Load(),
		Message:         c.lastErrString(),
	}
	if ok {
		lh := last
		h.LastHeartbeatAt = &lh
	}
	return h, nil
}

func (c *Coordinator) CollectorStatuses(_ context.Context) ([]api.CollectorStatus, error) {
	return nil, nil // managed-otelcol integration is a later phase
}

func (c *Coordinator) TriggerRollback(_ context.Context, _ string) error { return errNotImplemented }

// lastErrString returns a combined error across all discovery domains (empty when every domain is
// healthy), so a failure in any domain surfaces as Degraded even if another domain is fine.
func (c *Coordinator) lastErrString() string {
	var errs []string
	c.domainErrs.Range(func(k, v any) bool {
		if s, _ := v.(string); s != "" {
			errs = append(errs, k.(string)+": "+s)
		}
		return true
	})
	return strings.Join(errs, "; ")
}

// enrichWithSources annotates the provider's raw discovered workloads with source-intent state (matched
// + enabled), joining on external resource id first and then service name + kind. Generic across
// providers; the inventory itself is provider-specific.
func (c *Coordinator) enrichWithSources(ctx context.Context, workloads []api.DiscoveredWorkload) ([]api.DiscoveredWorkload, error) {
	sources, err := c.store.ListSources(ctx)
	if err != nil {
		return nil, err
	}
	return enrichWorkloads(workloads, sources), nil
}

// enrichComputePlatformsWithSources annotates discovered compute platforms with assignment state from source-intent rows.
func (c *Coordinator) enrichComputePlatformsWithSources(ctx context.Context, services []api.DiscoveredComputePlatform) ([]api.DiscoveredComputePlatform, error) {
	sources, err := c.store.ListSources(ctx)
	if err != nil {
		return nil, err
	}
	return enrichComputePlatforms(services, sources), nil
}

// enrichComputePlatforms is the pure join of discovered compute platforms against assignment sources (matched on
// external resource id first, then name + kind + region).
func enrichComputePlatforms(services []api.DiscoveredComputePlatform, sources []store.SourceRecord) []api.DiscoveredComputePlatform {
	if len(sources) == 0 {
		return services
	}
	byKey := make(map[string]store.SourceRecord, len(sources))
	byExternal := make(map[string]store.SourceRecord, len(sources))
	for _, src := range sources {
		byKey[sourceMatchKey(src.Kind, src.Name, src.Region)] = src
		if src.ExternalResourceID != "" {
			byExternal[src.ExternalResourceID] = src
		}
	}
	for i := range services {
		s := &services[i]
		if src, ok := byExternal[s.ExternalID]; ok && src.Kind == s.Kind {
			applyComputePlatformAssignment(s, src)
			continue
		}
		if src, ok := byKey[sourceMatchKey(s.Kind, s.Name, s.Region)]; ok {
			applyComputePlatformAssignment(s, src)
		}
	}
	return services
}

func applyComputePlatformAssignment(s *api.DiscoveredComputePlatform, src store.SourceRecord) {
	if src.Enabled {
		s.Status = "Assigned"
	} else {
		s.Status = "Disabled"
	}
}

// enrichWorkloads is the pure join of discovered workloads against source-intent rows (matched on
// external resource id first, then service name + kind). Separated from store access for testing.
func enrichWorkloads(workloads []api.DiscoveredWorkload, sources []store.SourceRecord) []api.DiscoveredWorkload {
	if len(sources) == 0 {
		return workloads
	}
	byKey := make(map[string]store.SourceRecord, len(sources))
	byExternal := make(map[string]store.SourceRecord, len(sources))
	for _, src := range sources {
		byKey[sourceMatchKey(src.Kind, src.Name, src.Region)] = src
		if src.ExternalResourceID != "" {
			byExternal[src.ExternalResourceID] = src
		}
	}
	for i := range workloads {
		w := &workloads[i]
		if src, ok := byExternal[w.ExternalID]; ok && src.Kind == w.Kind {
			applySource(w, src)
			continue
		}
		if src, ok := byKey[sourceMatchKey(w.Kind, w.ServiceName, w.Region)]; ok {
			applySource(w, src)
		}
	}
	return workloads
}

// sourceMatchKey is the connector source identity used to join a discovered workload to a source
// intent when no external resource id is available: kind + service name + provider region.
func sourceMatchKey(kind, serviceName, region string) string {
	return kind + "\x00" + serviceName + "\x00" + region
}

func applySource(w *api.DiscoveredWorkload, src store.SourceRecord) {
	w.SourceMatched = true
	w.SourceEnabled = src.Enabled
	w.SourceIntentAt = src.UpdatedAt
	w.Overrides = sourceOverridesFromRecord(src)
}

func sourceOverridesFromRecord(src store.SourceRecord) api.SourceOverrides {
	dom := store.SourceRecordToDomain(src)
	return api.SourceOverrides{
		ServiceName:     dom.ServiceName,
		RuntimeLanguage: dom.Config.RuntimeOverride.Language,
		RuntimeVersion:  dom.Config.RuntimeOverride.Version,
		OtelDistroName:  dom.Config.DistroOverride.OtelDistroName,
		Containers:      dom.Config.ContainerOverrides,
	}
}
