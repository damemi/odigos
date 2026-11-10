package coordinator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/base/store"
	cpg "github.com/odigos-io/odigos/connectors/postgres"
	"github.com/odigos-io/odigos/connectors/spec"
)

// shutdownCompleteWriteTimeout bounds the drain-complete write. It runs on a context independent of the
// (possibly exhausted) drain context so a rollback that consumed its whole budget can still record
// completion — the pod's termination grace period is sized to exceed the drain budget by more than this.
const shutdownCompleteWriteTimeout = 30 * time.Second

// retainedCountTimeout bounds the inventory read that counts manual-mode instrumentation still applied.
// Deliberately short: the count decides whether to withhold shutdownComplete, but a hung inventory read
// must not stall the heartbeat forever — the next tick retries.
const retainedCountTimeout = 15 * time.Second

// maxRetainedWorkloadNames caps how many retained workload identities travel in the awaiting/completion
// record. They land in a status blob and a Kubernetes event message, both size-bounded; the count is the
// actionable number and the names are a sample to start from.
const maxRetainedWorkloadNames = 20

// Shutdown rolls back all instrumentation by disabling every source intent and running one final
// reconcile pass for workload and compute-platform reconcilers. Provider reconcilers already roll back when no
// source is enabled, so no provider-specific shutdown code is required.
//
// It only marks the shutdown complete if every shutdown reconcile succeeded AND no manual-mode workloads
// remain instrumented. A failed automatic rollback must NOT be reported as complete: MarkShutdownComplete
// clears the drain request and sets shutdownComplete, which lets connector-runtime remove the CR finalizer —
// doing that while instrumentation is still live would leave orphaned agents. Manual-mode leftovers also
// withhold completion so the customer can remove IaC instrumentation while discovery keeps observing.
// On failure it returns the error so the process exits non-zero with the drain request intact; the
// controller retries on its next delete reconcile (and force-removes the finalizer only once its cleanup
// timeout elapses).
func (c *Coordinator) Shutdown(ctx context.Context) error {
	coordLog.Info("shutdown drain starting")
	c.draining.Store(true)
	if err := c.drainInstrumentation(ctx); err != nil {
		coordLog.Error("shutdown reconcile incomplete; not marking shutdown complete", "error", err)
		return fmt.Errorf("shutdown reconcile failed: %w", err)
	}
	// After the workloads and before completion. The ordering is not arbitrary: instrumentation is
	// rolled back first so nothing is still exporting into a Collector while it is being removed, and
	// completion is withheld until this succeeds so the finalizer is never dropped over a Collector that
	// is still running in the customer's account.
	if err := c.drainCollectors(ctx); err != nil {
		coordLog.Error("managed Collector teardown incomplete; not marking shutdown complete", "error", err)
		return fmt.Errorf("collector teardown failed: %w", err)
	}
	c.automaticDrainDone.Store(true)

	retained, names, err := c.countRetainedWithTimeout(ctx)
	if err != nil {
		return fmt.Errorf("count retained manual instrumentation: %w", err)
	}
	if !drainShouldComplete(retained) {
		c.setAwaitingManual(retained, names)
		coordLog.Warn("shutdown withheld: manual-mode instrumentation still applied; customer must remove it",
			"retained", retained, "workloads", names)
		return fmt.Errorf("manual-mode instrumentation still applied on %d workload(s); not marking shutdown complete", retained)
	}
	c.clearAwaitingManual()

	if err := c.markShutdownComplete(ctx, "shutdown"); err != nil {
		return err
	}
	coordLog.Info("shutdown drain complete")
	return nil
}

// observeDrain rolls back instrumentation when a drain has been requested (CR-delete cleanup), running in
// the heartbeat goroutine while the pod is still up.
//
// Two-phase completion:
//  1. Automatic rollback — disable sources and run mutating reconcilers (once; retried on error).
//  2. Manual wait — if any manual-mode workload is still instrumented, withhold shutdownComplete and keep
//     discovery running so the customer's IaC removal can be observed. When the retained count hits zero,
//     record shutdownComplete and cancel the loops.
//
// Returns true only once both phases succeed AND shutdownComplete has been recorded — the caller then
// cancels the runtime loops. A failed automatic rollback returns false with the drain request left set, so
// the next heartbeat tick retries; no pod termination is involved, so nothing is force-killed mid-rollback.
func (c *Coordinator) observeDrain(ctx context.Context, cancelLoops context.CancelFunc) bool {
	drain, err := c.store.DrainRequested(ctx)
	if err != nil {
		coordLog.Error("read drain state failed; will retry", "error", err)
		return false
	}
	if !drain.Requested {
		c.clearAwaitingManual()
		c.endDrain()
		return false
	}
	if !endsConnector(drain.Reason) {
		c.resumeManualCleanup(ctx)
		return false
	}
	c.draining.Store(true)

	if !c.automaticDrainDone.Load() {
		coordLog.Info("drain requested; rolling back automatic-mode instrumentation", "reason", drain.Reason)
		if err := c.drainInstrumentation(ctx); err != nil {
			coordLog.Error("drain reconcile incomplete; will retry on next tick", "error", err)
			return false
		}
		if err := c.drainCollectors(ctx); err != nil {
			// Retried on the next tick rather than failed, and a teardown is built to be resumed: every
			// step treats what a previous pass removed as success, and the row survives until nothing
			// deletable is left. A load balancer takes minutes to release the network interfaces its
			// security group holds, so at least one tick reporting this is the expected path rather than
			// an error condition.
			coordLog.Error("managed Collector teardown incomplete; will retry on next tick", "error", err)
			return false
		}
		c.automaticDrainDone.Store(true)
	}

	retained, names, err := c.countRetainedWithTimeout(ctx)
	if err != nil {
		// Never treat a read failure as "nothing retained" — that would drop the finalizer while agents
		// may still be live. Keep any prior awaiting state and retry next tick.
		coordLog.Warn("could not count retained manual instrumentation; withholding completion", "error", err)
		return false
	}
	if !drainShouldComplete(retained) {
		c.setAwaitingManual(retained, names)
		coordLog.Info("awaiting customer to remove manual-mode instrumentation before finishing delete",
			"retained", retained, "workloads", names)
		return false
	}
	c.clearAwaitingManual()

	if err := c.markShutdownComplete(ctx, drain.Reason); err != nil {
		coordLog.Error("mark shutdown complete failed; will retry on next tick", "error", err)
		return false
	}
	cancelLoops()
	return true
}

// endsConnector reports whether a drain with this reason is a deletion, which tears down the managed
// Collectors, records shutdownComplete and stops the loops. Only a manual cleanup is not. A reason this
// runtime does not recognize (or none, from an older controller) counts as a deletion, which is what every
// drain meant before the reasons were told apart.
func endsConnector(reason string) bool {
	return reason != cpg.DrainReasonManualCleanup
}

// endDrain resumes normal operation once the drain request is gone: a manual cleanup finished, or the request
// was withdrawn. draining otherwise stays set for the life of the pod and the reconcile loops never run again.
// A RequestCleanup still in flight owns the flag itself and is left alone.
func (c *Coordinator) endDrain() {
	if c.cleanupInFlight.Load() {
		return
	}
	c.draining.Store(false)
	c.automaticDrainDone.Store(false)
}

// resumeManualCleanup finishes a manual cleanup whose inline rollback did not complete: RequestCleanup leaves
// the request set when it fails, so the rollback is retried here on every heartbeat until it succeeds, then
// the request is cleared and the loops resume.
//
// It never tears down Collectors, records shutdownComplete or stops the loops. The connector is not being
// deleted: recording completion would mark it Deleting and stop discovery and reconcile for good while its
// custom resource still exists.
func (c *Coordinator) resumeManualCleanup(ctx context.Context) {
	if c.cleanupInFlight.Load() {
		return
	}
	if err := c.finishManualCleanup(ctx); err != nil {
		coordLog.Error("manual cleanup incomplete; will retry on next tick", "error", err)
	}
}

// finishManualCleanup rolls back instrumentation and clears the manual-cleanup request. Shared by the
// heartbeat and by a pod shutdown that finds the request still pending.
func (c *Coordinator) finishManualCleanup(ctx context.Context) error {
	c.draining.Store(true)
	if err := c.drainInstrumentation(ctx); err != nil {
		return fmt.Errorf("manual cleanup rollback failed: %w", err)
	}
	if err := c.store.ClearDrain(ctx, cpg.DrainReasonManualCleanup); err != nil {
		return fmt.Errorf("clear drain request after cleanup: %w", err)
	}
	c.draining.Store(false)
	coordLog.Info("manual cleanup complete")
	return nil
}

// FinishManualCleanup is finishManualCleanup for a pod that is shutting down with a manual cleanup still
// pending, so a restart does not leave instrumentation the customer asked to remove.
func (c *Coordinator) FinishManualCleanup(ctx context.Context) error {
	return c.finishManualCleanup(ctx)
}

// markShutdownComplete records a finished drain on a fresh, bounded context, independent of ctx: the
// drain may have used all of ctx's budget, and if this write is starved the controller never sees
// shutdownComplete and stalls the delete to its cleanup timeout even though the rollback succeeded.
//
// Callers must only invoke this when automatic rollback succeeded and no manual-mode instrumentation
// remains observed — otherwise the controller would drop the finalizer while agents are still live.
func (c *Coordinator) markShutdownComplete(ctx context.Context, reason string) error {
	health := map[string]any{
		"discoveryReady": c.discoveryReady.Load(),
		"authenticated":  c.authenticated.Load(),
		"drainReason":    reason,
	}

	completeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownCompleteWriteTimeout)
	defer cancel()
	if err := c.store.MarkShutdownComplete(completeCtx, health); err != nil {
		return fmt.Errorf("mark shutdown complete: %w", err)
	}
	coordLog.Info("drain complete; shutdownComplete recorded", "reason", reason)
	return nil
}

func (c *Coordinator) countRetainedWithTimeout(ctx context.Context) (int, []string, error) {
	countCtx, cancelCount := context.WithTimeout(context.WithoutCancel(ctx), retainedCountTimeout)
	defer cancelCount()
	return c.retainedManualInstrumentation(countCtx)
}

// retainedManualInstrumentation counts the workloads that are still carrying Odigos instrumentation which
// this drain did not and cannot remove: manual-mode workloads, whose instrumentation the customer applied.
//
// It reads the connector's own inventory (no provider API calls), so it is cheap and safe to run inside the
// drain. Errors propagate: under the withhold-until-uninstrumented policy, a read failure must not be
// treated as retained=0 (that would complete the delete while agents may still be live). The names are
// capped because they ride in a status blob and an event message, not a report.
func (c *Coordinator) retainedManualInstrumentation(ctx context.Context) (int, []string, error) {
	workloads, err := c.WorkloadReports(ctx)
	if err != nil {
		return 0, nil, err
	}
	n, names := countRetainedManualInstrumentation(workloads)
	return n, names, nil
}

// countRetainedManualInstrumentation is the pure count over already-enriched workloads, separated from store
// access so the rule itself is testable without a database.
func countRetainedManualInstrumentation(workloads []api.DiscoveredWorkload) (int, []string) {
	var count int
	var names []string
	for _, w := range workloads {
		// Manual mode is what makes it retained: the customer applied this instrumentation, so the drain never
		// had a way to remove it. Source state is deliberately not consulted — the drain disables every source
		// before this runs, so an enabled/disabled check here would count nothing at all.
		if w.ManagementMode != spec.ModeManual || !w.Instrumented {
			continue
		}
		count++
		if len(names) < maxRetainedWorkloadNames {
			names = append(names, w.Kind+"/"+w.ServiceName)
		}
	}
	return count, names
}

// drainShouldComplete reports whether a drain that already finished automatic rollback may record
// shutdownComplete. Pure so the withhold rule is unit-tested without a coordinator.
func drainShouldComplete(retainedManual int) bool {
	return retainedManual == 0
}

func (c *Coordinator) setAwaitingManual(retained int, names []string) {
	c.awaitingMu.Lock()
	defer c.awaitingMu.Unlock()
	c.awaitingManualRetained = retained
	c.awaitingManualWorkloads = names
}

func (c *Coordinator) clearAwaitingManual() {
	c.awaitingMu.Lock()
	defer c.awaitingMu.Unlock()
	c.awaitingManualRetained = 0
	c.awaitingManualWorkloads = nil
}

// mergeAwaitingManual copies the live awaiting state into a heartbeat health map so connector-runtime can
// hold the finalizer and surface ManualCleanupRequired while discovery waits for the customer.
func (c *Coordinator) mergeAwaitingManual(health map[string]any) {
	c.awaitingMu.Lock()
	defer c.awaitingMu.Unlock()
	if c.awaitingManualRetained <= 0 {
		return
	}
	health["awaitingManualCleanup"] = true
	health["manualInstrumentationRetained"] = c.awaitingManualRetained
	if len(c.awaitingManualWorkloads) > 0 {
		health["manualInstrumentationWorkloads"] = append([]string(nil), c.awaitingManualWorkloads...)
	}
}

// drainInstrumentation disables every source intent and runs one final reconcile pass over the workload
// and compute-platform reconcilers, returning the joined error of any rollback failure. Shared by Shutdown (pod
// termination) and RequestCleanup (manual, while the pod keeps running) so both perform the identical
// rollback and surface failures the same way.
func (c *Coordinator) drainInstrumentation(ctx context.Context) error {
	if err := c.store.DisableAllSources(ctx); err != nil {
		return fmt.Errorf("disable sources: %w", err)
	}

	var errs []error
	recs := c.activeReconcilers()
	if len(recs) > 0 {
		coordLog.Info("drain reconcile starting", "types", len(recs))
		errs = append(errs, c.runReconcileShutdown(ctx, recs))
	}

	svcRecs := c.activeComputePlatformReconcilers()
	if len(svcRecs) > 0 {
		coordLog.Info("drain compute-platform reconcile starting", "types", len(svcRecs))
		errs = append(errs, c.runComputePlatformReconcileShutdown(ctx, svcRecs))
	}
	return errors.Join(errs...)
}

// runReconcileShutdown runs every reconciler's final rollback pass and returns the joined error. Unlike
// runReconcile it does not swallow failures: a reconcile error here means instrumentation may still be
// live, which must propagate so Shutdown withholds MarkShutdownComplete. Takes the reconcile lock so it
// never overlaps a loop tick.
func (c *Coordinator) runReconcileShutdown(ctx context.Context, recs []namedReconciler) error {
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()
	var errs []error
	for _, nr := range recs {
		if err := nr.rec.Reconcile(ctx); err != nil {
			coordLog.Error("shutdown reconcile failed", "type", nr.name, "error", err)
			errs = append(errs, fmt.Errorf("%s: %w", nr.name, err))
		}
	}
	return errors.Join(errs...)
}

func (c *Coordinator) runComputePlatformReconcileShutdown(ctx context.Context, recs []namedComputePlatformReconciler) error {
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()
	var errs []error
	for _, nr := range recs {
		if err := nr.rec.Reconcile(ctx); err != nil {
			coordLog.Error("shutdown compute-platform reconcile failed", "type", nr.name, "error", err)
			errs = append(errs, fmt.Errorf("%s: %w", nr.name, err))
		}
	}
	return errors.Join(errs...)
}

// RequestCleanup performs an immediate instrumentation rollback on demand. It records the drain intent in
// Postgres and then rolls back inline — disabling all sources and running the rollback reconcile now —
// rather than only flagging the request and waiting for an external restart/delete. The inline pass takes
// the same reconcile lock as the loops, so it never races a loop tick. Returns an error if the rollback did
// not fully succeed.
//
// RequestCleanup only rolls back what this connector owns (automatic-mode types). Manual-mode
// instrumentation is the customer's; this API does not wait for them to remove it (unlike CR-delete drain,
// which withholds shutdownComplete until discovery reports those workloads uninstrumented). It does not tear
// down managed Collectors either, and the pod keeps running.
//
// On success it clears the request, so a later, unrelated pod restart (an upgrade/reschedule) does not roll
// back instrumentation the user may have re-enabled since. On failure the request is left set and the loops
// stay quiesced: the heartbeat retries the rollback every tick (resumeManualCleanup), and a pod shutdown
// finishes it, until it succeeds. A cleanup asked for while the connector is being deleted is refused, since
// the delete drain already rolls back everything.
func (c *Coordinator) RequestCleanup(ctx context.Context, _ string) error {
	coordLog.Info("manual cleanup requested; draining instrumentation")
	c.cleanupInFlight.Store(true)
	defer c.cleanupInFlight.Store(false)
	if err := c.store.RequestDrain(ctx, cpg.DrainReasonManualCleanup); err != nil {
		if errors.Is(err, store.ErrOtherDrainPending) {
			return errors.New("the connector is being deleted, and its delete drain already rolls back all instrumentation")
		}
		return fmt.Errorf("record drain request: %w", err)
	}
	// Gate source create/update and the reconcile loops for the duration of the rollback (mirrors
	// Shutdown/observeDrain) so a mutation racing the cleanup can't re-enable a source the rollback just
	// disabled and leave it un-reconciled.
	if err := c.finishManualCleanup(ctx); err != nil {
		// Rollback incomplete: draining stays set and so does the request, so the connector stays quiesced
		// and the heartbeat retries rather than accepting new sources over live instrumentation.
		return err
	}
	return nil
}
