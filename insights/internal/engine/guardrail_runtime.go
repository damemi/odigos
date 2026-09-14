package engine

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

type guardrailRuntime struct {
	repo  repo.Repository
	clock func() time.Time
	warn  func(msg string, err error)
	keep  keepSampleFunc

	config   atomic.Pointer[GuardrailConfig]
	buf      *guardrailBuffer
	known    atomic.Pointer[map[string]map[string]struct{}]
	evidence sync.Map // groupKey string -> struct{}, write-once evidence gate
}

func newGuardrailRuntime(r repo.Repository, clock func() time.Time, warn func(string, error), keep keepSampleFunc) *guardrailRuntime {
	return &guardrailRuntime{repo: r, clock: clock, warn: warn, keep: keep, buf: newGuardrailBuffer()}
}

// setConfig installs (or replaces) the guardrail snapshot; a nil snapshot
// polices nothing (Resolve is nil-safe).
func (g *guardrailRuntime) setConfig(cfg *GuardrailConfig) { g.config.Store(cfg) }

// reload refreshes the snapshot from the DB in place. A no-op until a config is
// installed, so an engine wired without guardrails never errors.
func (g *guardrailRuntime) reload(ctx context.Context, r repo.Repository) error {
	cfg := g.config.Load()
	if cfg == nil {
		return nil
	}
	return cfg.Reload(ctx, r)
}

// evaluate polices this transaction's edges against its resolved guardrail and
// buffers one occurrence per broken (rule, offending item). The common
// "no guardrail" case is a single lock-free map miss. It never fires an anomaly
// or touches the baseline. full is the undecomposed trace, kept only for
// evidence (a forbidden caller sits above this sub-trace's root).
//
// system is the settings generation this trace was processed under: it decides
// which identity scheme the allowlist is checked against, and which inventory
// generation the buffered counts belong to.
func (g *guardrailRuntime) evaluate(ctx context.Context, t *Trace, tx repo.Transaction, full *Trace, system *systemSnapshot) {
	serviceKey := repo.ServiceScopeKey(tx.Namespace, tx.Service)
	now := g.clock()
	inventoryEpoch := system.settings.TransactionInventoryEpoch
	if guardrail, ok := g.config.Load().Resolve(tx.ID, serviceKey); ok {
		for _, violation := range guardrailViolations(t, tx, guardrail, g.inMeshServices(tx.Namespace), system.identityFingerprint) {
			g.buf.add(repo.ScopeService, serviceKey, violation.rule, violation.item, violation.score, t.TraceID, now, inventoryEpoch)
			g.captureEvidence(ctx, full, tx, serviceKey, violation.rule, violation.item)
		}
	}
	// Transaction-scoped guardrails are additive and independent of any service
	// allowlist, so they run even when this transaction has no service guardrail.
	g.evaluateTransactionGuardrail(ctx, tx, full, serviceKey, now, inventoryEpoch)
}

// evaluateTransactionGuardrail runs the transaction-scoped guardrail (if any)
// additively over the FULL trace. It resolves by transaction id, so it fires
// once per trace - on the sub-transaction whose id matches - and its cross-span
// rules can reference spans in other services' sub-traces. Violations are
// recorded under the entry service's scope key so they flow through the same
// Findings/evidence pipeline as the allowlist rules.
func (g *guardrailRuntime) evaluateTransactionGuardrail(ctx context.Context, tx repo.Transaction, full *Trace, serviceKey string, now time.Time, inventoryEpoch int) {
	if full == nil {
		return
	}
	txnGuardrail, ok := g.config.Load().ResolveTransactionGuardrail(tx.ID)
	if !ok {
		return
	}
	for _, violation := range correlationViolations(full, txnGuardrail) {
		g.buf.add(repo.ScopeService, serviceKey, violation.rule, violation.item, violation.score, full.TraceID, now, inventoryEpoch)
		g.captureEvidence(ctx, full, tx, serviceKey, violation.rule, violation.item)
	}
}

// flushViolations drains the buffered occurrences and records them as summed
// increments. Called on the flush cadence; a failure is logged, not fatal.
// Counts an inventory reset invalidated are dropped - see liveInventoryEpoch.
func (g *guardrailRuntime) flushViolations(ctx context.Context) {
	if g.buf.empty() {
		return
	}
	currentInventoryEpoch, err := liveInventoryEpoch(ctx, g.repo)
	if err != nil {
		// Without it there is no way to tell live counts from ones an inventory
		// reset invalidated. Keep them buffered for the next flush rather than
		// risk reporting transactions that no longer exist.
		g.warn("read transaction inventory epoch for guardrail flush", err)
		return
	}
	hits := g.buf.drain(currentInventoryEpoch)
	if len(hits) == 0 {
		return
	}
	if err := g.repo.RecordGuardrailViolations(ctx, hits); err != nil {
		g.warn("flush guardrail violations", err)
	}
}

// remember records an in-mesh service under its namespace. Fast path is a
// wait-free membership check on the published map; a new (namespace, name)
// publishes a copy via compare-and-swap, retrying if a racing writer won - so no
// writer lock is needed and readers never see a half-updated map.
func (g *guardrailRuntime) remember(namespace, name string) {
	if name == "" {
		return
	}
	key := strings.ToLower(name)
	for {
		cur := g.known.Load()
		if cur != nil {
			if _, ok := (*cur)[namespace][key]; ok {
				return
			}
		}
		next := cloneKnownServices(cur, namespace, key)
		if g.known.CompareAndSwap(cur, &next) {
			return
		}
	}
}

// inMeshServices returns the known-service set for one namespace, for read-only
// use on the per-trace path. The returned map must not be mutated (it is shared).
func (g *guardrailRuntime) inMeshServices(namespace string) map[string]struct{} {
	if m := g.known.Load(); m != nil {
		return (*m)[namespace]
	}
	return nil
}

// cloneKnownServices copies the namespace→names map with (namespace, name)
// added - the copy-on-write input for the CAS in remember. Only the outer map
// and the one inner set being extended are cloned; the other inner sets are
// shared (they are never mutated after publish).
func cloneKnownServices(cur *map[string]map[string]struct{}, namespace, name string) map[string]map[string]struct{} {
	var src map[string]map[string]struct{}
	if cur != nil {
		src = *cur
	}
	next := make(map[string]map[string]struct{}, len(src)+1)
	for ns, names := range src {
		next[ns] = names
	}
	names := make(map[string]struct{}, len(next[namespace])+1)
	for n := range next[namespace] {
		names[n] = struct{}{}
	}
	names[name] = struct{}{}
	next[namespace] = names
	return next
}

// captureEvidence stores one whole-trace UI example per violation group.
// Occurrences are counted separately. The FULL (undecomposed) trace is stored
// so a forbidden-caller violation - whose offending span sits above this
// sub-trace's root - is visible.
//
// The group gate makes "one per group" a single in-memory check for every
// repeat violation. The per-transaction cap behind keep is what makes it hold
// across restarts, and it queues the write instead of blocking the trace on it.
func (g *guardrailRuntime) captureEvidence(ctx context.Context, full *Trace, tx repo.Transaction, serviceKey string, rule repo.RuleKey, offending string) {
	if full == nil || len(full.RawOTLP()) == 0 {
		return
	}
	groupKey := serviceKey + "\x00" + string(rule) + "\x00" + offending
	if _, loaded := g.evidence.LoadOrStore(groupKey, struct{}{}); loaded {
		return
	}
	// If the write never lands, reopen the gate so a later violation can retry.
	g.keep(ctx, full, tx, repo.GuardrailEvidenceReason(rule, offending), repo.GuardrailEvidenceSamples,
		func() { g.evidence.Delete(groupKey) })
}
