package coordinator

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// shutdownCompleteWriteTimeout bounds the drain-complete write. It runs on a context independent of the
// (possibly exhausted) drain context so a rollback that consumed its whole budget can still record
// completion — the pod's termination grace period is sized to exceed the drain budget by more than this.
const shutdownCompleteWriteTimeout = 30 * time.Second

// Shutdown rolls back all instrumentation by disabling every source intent and running one final
// reconcile pass for workload and compute-platform reconcilers. Provider reconcilers already roll back when no
// source is enabled, so no provider-specific shutdown code is required.
//
// It only marks the shutdown complete if every shutdown reconcile succeeded. A failed rollback must NOT
// be reported as complete: MarkShutdownComplete clears the drain request and sets shutdownComplete, which
// lets connector-runtime remove the CR finalizer — doing that while instrumentation is still live would
// leave orphaned agents. On failure it returns the error so the process exits non-zero with the drain
// request intact; the controller retries on its next delete reconcile (and force-removes the finalizer
// only once its cleanup timeout elapses).
func (c *Coordinator) Shutdown(ctx context.Context) error {
	coordLog.Info("shutdown drain starting")
	c.draining.Store(true)
	if err := c.drainInstrumentation(ctx); err != nil {
		coordLog.Error("shutdown reconcile incomplete; not marking shutdown complete", "error", err)
		return fmt.Errorf("shutdown reconcile failed: %w", err)
	}
	if err := c.markShutdownComplete(ctx, "shutdown"); err != nil {
		return err
	}
	coordLog.Info("shutdown drain complete")
	return nil
}

// observeDrain rolls back all instrumentation inline when a drain has been requested (CR-delete cleanup or
// manual RequestCleanup), running in the heartbeat goroutine while the pod is still up. It returns true
// only once the rollback has fully succeeded AND shutdownComplete has been recorded — the caller then
// cancels the runtime loops. A failed rollback (e.g. a transient AWS error) returns false with the drain
// request left set, so the next heartbeat tick retries; no pod termination is involved, so nothing is
// force-killed mid-rollback. Running in the heartbeat goroutine serializes the completion write with
// heartbeat writes, so a heartbeat can never overwrite the recorded shutdownComplete.
func (c *Coordinator) observeDrain(ctx context.Context, cancelLoops context.CancelFunc) bool {
	drain, err := c.store.DrainRequested(ctx)
	if err != nil {
		coordLog.Error("read drain state failed; will retry", "error", err)
		return false
	}
	if !drain.Requested {
		return false
	}
	c.draining.Store(true)
	coordLog.Info("drain requested; rolling back instrumentation", "reason", drain.Reason)
	if err := c.drainInstrumentation(ctx); err != nil {
		coordLog.Error("drain reconcile incomplete; will retry on next tick", "error", err)
		return false
	}
	if err := c.markShutdownComplete(ctx, drain.Reason); err != nil {
		coordLog.Error("mark shutdown complete failed; will retry on next tick", "error", err)
		return false
	}
	cancelLoops()
	return true
}

// markShutdownComplete records a successful drain on a fresh, bounded context, independent of ctx: the
// drain may have used all of ctx's budget, and if this write is starved the controller never sees
// shutdownComplete and stalls the delete to its cleanup timeout even though the rollback succeeded.
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
	return nil
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
// Postgres (so a pod restart before completion stays drained) and then rolls back inline — disabling all
// sources and running the rollback reconcile now — rather than only flagging the request and waiting for
// an external restart/delete. The inline pass takes the same reconcile lock as the loops, so it never
// races a loop tick. Returns an error if the rollback did not fully succeed.
//
// On success it clears the drain request: the pod keeps running (unlike shutdown-drain, this is not a
// termination) and every source is now disabled, so leaving the flag set would make a later, unrelated
// pod restart (an upgrade/reschedule) roll back instrumentation the user may have re-enabled since. If the
// rollback fails the flag is intentionally left set, so a restart before it succeeds still drains.
func (c *Coordinator) RequestCleanup(ctx context.Context, _ string) error {
	coordLog.Info("manual cleanup requested; draining instrumentation")
	// Gate source create/update for the duration of the rollback (mirrors Shutdown/observeDrain) so a
	// mutation racing the cleanup can't re-enable a source the rollback just disabled and leave it
	// un-reconciled. Unlike a termination drain the pod keeps running, so the flag is cleared once the
	// rollback fully succeeds and normal operation resumes.
	c.draining.Store(true)
	if err := c.store.RequestDrain(ctx, "manual_cleanup"); err != nil {
		// Nothing was drained; resume normal operation so a transient DB blip doesn't quiesce the connector.
		c.draining.Store(false)
		return fmt.Errorf("record drain request: %w", err)
	}
	if err := c.drainInstrumentation(ctx); err != nil {
		// Rollback incomplete: leave draining set (and the DB drain flag set, per below) so the connector
		// stays quiesced and a restart re-drains rather than accepting new sources over live instrumentation.
		return fmt.Errorf("manual cleanup rollback failed: %w", err)
	}
	if err := c.store.ClearDrain(ctx); err != nil {
		return fmt.Errorf("clear drain request after cleanup: %w", err)
	}
	c.draining.Store(false)
	coordLog.Info("manual cleanup complete")
	return nil
}
