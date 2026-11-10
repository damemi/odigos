package coordinator

import (
	"context"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// namedObserver pairs a spec instrumentation type name with its read-only observer.
type namedObserver struct {
	name string
	obs  api.InstrumentationObserver
}

// activeObservers resolves the read-only observers to run: the provider's observers for the instrumentation
// types whose derived mode is MANUAL.
//
// Manual-only is the whole point, and it is the exact complement of activeReconcilers: a type Odigos
// instruments itself needs no separate observation pass — the reconciler reads the same live state and then
// converges it, so observing would be a second read of the customer's control plane for an answer the
// connector is about to enforce anyway. A type the CUSTOMER instruments has no reconciler at all, so this
// pass is the only thing that can tell whether what they applied is actually correct.
//
// api.WorkloadObserverRegistry is asserted here rather than being a Provider method: observation lands one
// provider at a time, and a provider that has not implemented it yet simply contributes nothing.
func (c *Coordinator) activeObservers() []namedObserver {
	reg, ok := c.provider.(api.WorkloadObserverRegistry)
	if !ok {
		return nil
	}
	obs := reg.WorkloadObservers()
	if len(obs) == 0 {
		return nil
	}
	var out []namedObserver
	// InstrumentationTypes spans both modes; the mode filter below narrows it to manual. Reading it from
	// ActiveInstrumentationTypes instead would yield the automatic set — precisely inverted.
	for _, t := range c.provider.Spec().InstrumentationTypes(c.cfg.Capabilities) {
		if c.cfg.Capabilities.WorkloadInstrumentationMode(t.Name) != spec.ModeManual {
			continue
		}
		if o, ok := obs[t.Name]; ok {
			out = append(out, namedObserver{name: t.Name, obs: o})
		} else {
			// A manual-mode type with no observer cannot report conformance, so it can only report marker
			// presence — which is the false green this whole pass exists to prevent. Say so plainly rather
			// than letting it look observed.
			coordLog.Warn("instrumentation type is in manual mode but the provider registers no observer; conformance will not be reported", "type", t.Name)
		}
	}
	return out
}

// observeLoop runs the read-only observation pass for manual-mode instrumentation types.
//
// It is a separate loop from reconcileLoop, not a step inside it, because a pure manual-mode connector has
// no active reconcilers at all — reconcileLoop returns immediately — and that is exactly the connector
// that needs observing most.
//
// It ticks on the DISCOVERY interval rather than the (faster) reconcile interval: an observer reads the
// same provider control-plane listing discovery reads, so pacing it with discovery keeps observation from
// multiplying a customer's control-plane call volume for state that only changes when they deploy. A
// discovery cycle also kicks it directly, so a newly discovered workload is observed without waiting a
// full interval.
//
// Those two mechanisms share an interval, so the kick would otherwise DOUBLE exactly the call volume the
// paragraph above is about — a second full round of control-plane reads per cycle for state that had not
// moved since the first. A triggered pass therefore defers the tick a full interval (deferNextTick), making
// the interval mean "at most one pass per interval" whatever caused it.
func (c *Coordinator) observeLoop(ctx context.Context) {
	obs := c.activeObservers()
	if len(obs) == 0 {
		return
	}
	coordLog.Info("observe loop starting", "types", len(obs))
	interval := c.cfgMgr.Settings().DiscoveryInterval
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	c.runObserve(ctx, obs)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.runObserve(ctx, obs)
			interval = resetTicker(ticker, interval, c.cfgMgr.Settings().DiscoveryInterval)
		case <-c.observeTrigger:
			coordLog.Debug("observation triggered")
			c.runObserve(ctx, obs)
			interval = deferNextTick(ticker, interval, c.cfgMgr.Settings().DiscoveryInterval)
		}
	}
}

// runObserve runs one observation pass. An observer error is logged and does not abort the pass: each type
// is independent, and a failed read is recorded per-workload by the observer itself (as an observation
// error, never as drift) so the report stays honest about what could not be seen.
//
// It deliberately does NOT take reconcileMu. The pass performs no writes to provider resources, so it
// cannot race a reconcile into a conflicting mutation — and serializing it behind that lock would let a
// slow reconcile stall observation, which is the reporting path.
func (c *Coordinator) runObserve(ctx context.Context, obs []namedObserver) {
	// A drain disables every source and rolls automatic-mode instrumentation back; refreshing a conformance
	// verdict against intent that is being torn down would record drift that means nothing.
	if c.draining.Load() {
		return
	}
	start := time.Now()
	for _, no := range obs {
		if err := no.obs.Observe(ctx); err != nil {
			coordLog.Error("observation failed", "type", no.name, "error", err)
		}
	}
	coordLog.Debug("observation pass complete", "types", len(obs), "duration", time.Since(start).Round(time.Millisecond))
	// runDiscovery already fires this trigger too, but that one races this pass: for a type whose Instrumented
	// flag is observer-derived (ECS Fargate today) rather than discovery-derived (Lambda), adopt reading
	// ListWorkloads before THIS pass writes the flag sees a stale "not instrumented" and correctly concludes
	// there is nothing to adopt yet — the AWS calls above can easily outlast adopt's near-instant DB read.
	// Firing it again here, now that this pass's writes are visible, is what actually closes that window; the
	// discovery-triggered one still matters on its own for discovery-derived types, which have no such race.
	c.triggerAdopt()
}

// triggerObserve requests an out-of-band observation pass, coalescing with any already queued. A no-op when
// no observers are active (nothing drains the channel).
func (c *Coordinator) triggerObserve() {
	select {
	case c.observeTrigger <- struct{}{}:
		coordLog.Debug("observation queued")
	default:
		coordLog.Debug("observation already queued")
	}
}
