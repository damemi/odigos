package coordinator

import (
	"context"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domainsrc "github.com/odigos-io/odigos-enterprise/connectors/base/domain/source"
	"github.com/odigos-io/odigos-enterprise/connectors/base/store"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// adoptLoop periodically creates a source for any discovered workload that already carries live Odigos
// instrumentation — an ODIGOS_MANAGED marker (automatic mode) or an ODIGOS_IAC marker (the customer's own
// IaC, manual mode) — but matches no source at all. That gap is otherwise invisible: one that was never
// created for infrastructure a customer instrumented themselves, or one lost along with the connector's own
// state, leaves a live agent with nothing in the connector tracking it and nothing to click on in a UI that
// lists sources.
//
// Whether the adopted row starts enabled depends on which marker it found — see adoptedSourceEnabled. It is
// never adoption's place to decide that a workload SHOULD be instrumented going forward; it only records the
// intent a workload's own live state already proves, one way or the other.
//
// A source a human DELETED is never re-adopted, which is what keeps this pass from overriding people. The
// deletion is a tombstone rather than a removed row (see store.DeleteSourceByKey) precisely so this pass can
// tell "deliberately untracked" from "never seen", and the distinction is load-bearing in both modes: a
// deleted manual-mode source would otherwise return on the very next pass, since deleting a source does not
// (and must not) strip the customer's own IaC marker, and a deleted automatic-mode source would return
// during the window between the delete and the reconcile that rolls the instrumentation back. Recreating the
// source explicitly clears the tombstone, so a human always outranks it in both directions.
//
// It is a separate loop from reconcileLoop and observeLoop because it depends on neither: it must adopt an
// orphaned automatic-mode marker even when nothing in that mode is being reconciled, and an orphaned IaC
// marker even when this provider has not implemented an InstrumentationObserver at all — unlike observeLoop
// (see activeObservers), this pass has nothing to gate on.
//
// It ticks on the discovery interval, same as observeLoop, and a discovery cycle kicks it directly (see
// runDiscovery) so a freshly discovered orphan is adopted without waiting a full interval.
func (c *Coordinator) adoptLoop(ctx context.Context) {
	coordLog.Info("orphaned-workload adoption loop starting")
	interval := c.cfgMgr.Settings().DiscoveryInterval
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	c.runAdopt(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.runAdopt(ctx)
			interval = resetTicker(ticker, interval, c.cfgMgr.Settings().DiscoveryInterval)
		case <-c.adoptTrigger:
			coordLog.Debug("orphan adoption triggered")
			c.runAdopt(ctx)
		}
	}
}

// runAdopt scans the current workload reports for live instrumentation with no matching source and creates
// one for each, enabled or not per adoptedSourceEnabled. Creation goes through CreateSource so an adopted
// row gets the exact same kind/region validation and external-id resolution a human's source-create request
// would.
func (c *Coordinator) runAdopt(ctx context.Context) {
	// A drain disables every source and rolls automatic-mode instrumentation back; adopting a new source
	// while sources are being torn down would recreate exactly what the drain is removing.
	if c.draining.Load() {
		return
	}
	raw, err := c.provider.ListWorkloads(ctx)
	if err != nil {
		coordLog.Warn("orphan adoption: list workloads failed", "error", err)
		return
	}
	enriched, err := c.enrichWithSources(ctx, raw)
	if err != nil {
		coordLog.Warn("orphan adoption: enrich workloads failed", "error", err)
		return
	}
	// Fetched once per pass rather than per candidate. On failure the pass is ABANDONED, never continued
	// without it: adopting while blind to the tombstones is exactly how a deliberately deleted source comes
	// back, so no adoption at all is the safe failure here — the next pass retries in seconds.
	deleted, err := c.store.DeletedSourceKeys(ctx)
	if err != nil {
		coordLog.Warn("orphan adoption: list deleted source keys failed; skipping this pass", "error", err)
		return
	}
	// ManagementMode is what adoptedSourceEnabled keys on; enrichWithSources does not set it (WorkloadReports
	// is the only other caller, and it does this itself).
	c.markInstrumentationAvailability(enriched)
	for _, w := range enriched {
		if !shouldAdopt(w, deleted) {
			continue
		}
		enabled := adoptedSourceEnabled(w)
		src := domainsrc.Source{
			Name:    w.ServiceName,
			Kind:    w.Kind,
			Region:  w.Region,
			Enabled: enabled,
		}
		if err := c.CreateSource(ctx, src); err != nil {
			coordLog.Warn("orphan adoption: create source failed",
				"name", w.ServiceName, "kind", w.Kind, "region", w.Region, "error", err)
			continue
		}
		coordLog.Info("adopted orphaned instrumented workload as a source",
			"name", w.ServiceName, "kind", w.Kind, "region", w.Region, "enabled", enabled)
	}
}

// shouldAdopt is the whole adoption decision for one workload, kept pure (no store, no provider) so every
// branch is testable on its own. deleted is the tombstoned-identity set from store.DeletedSourceKeys.
func shouldAdopt(w api.DiscoveredWorkload, deleted map[string]bool) bool {
	return orphanedInstrumentedWorkload(w) && !deleted[store.SourceIdentityKey(w.Kind, w.ExternalID)]
}

// orphanedInstrumentedWorkload reports whether a workload is live-instrumented with no source tracking it
// at all, regardless of which marker (ODIGOS_MANAGED or ODIGOS_IAC) attributes the instrumentation — both
// leave a connector with nothing recording intent for a workload that is nonetheless instrumented. A
// region-less workload is skipped because CreateSource requires one to pin a source to a single regional
// resource.
func orphanedInstrumentedWorkload(w api.DiscoveredWorkload) bool {
	return w.Instrumented && !w.SourceMatched && w.Region != ""
}

// adoptedSourceEnabled decides whether an adopted source starts enabled, from which marker attributes the
// workload's instrumentation:
//
//   - Manual (ODIGOS_IAC): the marker only exists because the customer wrote it into their own deployment
//     definition — it IS their intent, expressed outside Odigos entirely. Adopting it enabled just catches
//     Odigos's bookkeeping up to a reality the customer already fully created; there is no "disabled" state
//     to land in, because the customer never went through Odigos to get here in the first place, and nothing
//     about enabling a manual-mode source grants Odigos any ability to touch the workload it does not
//     already lack — manual mode is never mutated regardless.
//   - Automatic (ODIGOS_MANAGED): the marker exists only because ODIGOS ITSELF wrote it, under a source that
//     is now gone — deleted on purpose, or lost some other way. Automatic mode DOES grant Odigos mutation
//     rights once enabled, so re-enabling is a real commitment this pass must not make on the customer's
//     behalf without them asking; adopting it disabled surfaces the gap instead of guessing the answer.
func adoptedSourceEnabled(w api.DiscoveredWorkload) bool {
	return w.ManagementMode == spec.ModeManual
}

// triggerAdopt requests an out-of-band adoption pass, coalescing with any already queued.
func (c *Coordinator) triggerAdopt() {
	select {
	case c.adoptTrigger <- struct{}{}:
		coordLog.Debug("orphan adoption queued")
	default:
		coordLog.Debug("orphan adoption already queued")
	}
}
