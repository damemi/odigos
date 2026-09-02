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

	config   atomic.Pointer[GuardrailConfig]
	buf      *guardrailBuffer
	known    atomic.Pointer[map[string]map[string]struct{}]
	evidence sync.Map // groupKey string -> struct{}, write-once evidence gate
}

func newGuardrailRuntime(r repo.Repository, clock func() time.Time, warn func(string, error)) *guardrailRuntime {
	return &guardrailRuntime{repo: r, clock: clock, warn: warn, buf: newGuardrailBuffer()}
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
	guardrail, ok := g.config.Load().Resolve(tx.ID, serviceKey)
	if !ok {
		return
	}
	now := g.clock()
	inventoryEpoch := system.settings.TransactionInventoryEpoch
	for _, violation := range guardrailViolations(t, tx, guardrail, g.inMeshServices(tx.Namespace), system.identityFingerprint) {
		g.buf.add(repo.ScopeService, serviceKey, violation.rule, violation.item, violation.score, t.TraceID, now, inventoryEpoch)
		g.captureEvidence(ctx, full, tx, serviceKey, violation.rule, violation.item, now)
	}
}

// flushViolations drains the buffered occurrences and records them as summed
// increments. Called on the flush cadence; a failure is logged, not fatal.
// Counts an inventory reset invalidated are dropped — see liveInventoryEpoch.
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
// publishes a copy via compare-and-swap, retrying if a racing writer won — so no
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
// added — the copy-on-write input for the CAS in remember. Only the outer map
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
// so a forbidden-caller violation — whose offending span sits above this
// sub-trace's root — is visible.
func (g *guardrailRuntime) captureEvidence(ctx context.Context, full *Trace, tx repo.Transaction, serviceKey string, rule repo.RuleKey, offending string, now time.Time) {
	if full == nil || len(full.RawOTLP()) == 0 {
		return
	}
	groupKey := serviceKey + "\x00" + string(rule) + "\x00" + offending
	if _, loaded := g.evidence.LoadOrStore(groupKey, struct{}{}); loaded {
		return
	}
	ctx, cancel := detachedEvidenceContext(ctx)
	defer cancel()

	reason := repo.GuardrailEvidenceReason(rule, offending)
	n, err := g.repo.CountObservations(ctx, tx.ID, reason)
	if err != nil {
		g.warn("count guardrail evidence", err)
		g.evidence.Delete(groupKey)
		return
	}
	if n >= repo.GuardrailEvidenceSamples {
		return
	}
	if err := g.repo.InsertObservation(ctx, repo.Observation{
		TransactionID: tx.ID,
		TraceID:       full.TraceID,
		ObservedAt:    now,
		DurationMs:    full.DurationMs,
		SampleReason:  reason,
		RawOTLP:       full.RawOTLP(),
	}); err != nil {
		g.warn("store guardrail evidence", err)
		g.evidence.Delete(groupKey)
		return
	}
}
