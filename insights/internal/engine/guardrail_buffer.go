package engine

import (
	"strconv"
	"sync"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/metrics"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

// guardrailBuffer accumulates guardrail-violation occurrences in memory, keyed
// by (scope, scopeKey, rule, offending), so a steadily-broken edge - which
// re-violates on every request - is flushed as one summed increment per flush
// interval instead of one row per trace. drain hands the accumulated groups to
// RecordGuardrailViolations and resets the map. Every occurrence is still
// counted (no cooldown/suppression): the count is the security signal.
type guardrailBuffer struct {
	mu  sync.Mutex
	agg map[string]*guardrailAgg
}

// guardrailAgg is one group's running tally since the last flush.
type guardrailAgg struct {
	scope     repo.PolicyScope
	scopeKey  string
	rule      repo.RuleKey
	offending string
	// inventoryEpoch is the transaction-inventory generation this replica was on
	// while counting. It is part of the group key, so counts never merge across
	// a "delete all transactions" reset and a superseded group can be dropped
	// whole at flush time (see drain).
	inventoryEpoch int
	count          int64
	maxScore       int
	firstSeen      time.Time
	lastSeen       time.Time
	lastTrace      string
}

func newGuardrailBuffer() *guardrailBuffer {
	return &guardrailBuffer{agg: map[string]*guardrailAgg{}}
}

// add records one occurrence of a (scope, scopeKey, rule, offending) violation
// counted while this replica was on inventoryEpoch. Safe for concurrent callers
// (the per-trace path).
func (b *guardrailBuffer) add(scope repo.PolicyScope, scopeKey string, rule repo.RuleKey, offending string, score int, traceID string, at time.Time, inventoryEpoch int) {
	key := string(scope) + "\x00" + scopeKey + "\x00" + string(rule) + "\x00" + offending + "\x00" + strconv.Itoa(inventoryEpoch)
	b.mu.Lock()
	defer b.mu.Unlock()
	group := b.agg[key]
	if group == nil {
		group = &guardrailAgg{
			scope:          scope,
			scopeKey:       scopeKey,
			rule:           rule,
			offending:      offending,
			inventoryEpoch: inventoryEpoch,
		}
		b.agg[key] = group
	}
	group.count++
	if score > group.maxScore {
		group.maxScore = score
	}
	if group.firstSeen.IsZero() || at.Before(group.firstSeen) {
		group.firstSeen = at
	}
	if at.After(group.lastSeen) {
		group.lastSeen = at
		group.lastTrace = traceID
	}
}

// drain returns the accumulated groups as flush increments and resets the
// buffer. Returns nil when nothing has accumulated (the common case).
//
// Groups left behind by an inventory reset are discarded instead of returned:
// after the operator deletes all transactions, the identities they name are gone
// and writing them would put findings back in the UI that the reset just
// cleared. currentInventoryEpoch is the generation live in storage, not the one
// this replica believes in - see liveInventoryEpoch.
func (b *guardrailBuffer) drain(currentInventoryEpoch int) []repo.GuardrailViolationHit {
	b.mu.Lock()
	agg := b.agg
	b.agg = map[string]*guardrailAgg{}
	b.mu.Unlock()
	if len(agg) == 0 {
		return nil
	}
	hits := make([]repo.GuardrailViolationHit, 0, len(agg))
	for _, group := range agg {
		if group.supersededByInventoryReset(currentInventoryEpoch) {
			continue
		}
		hits = append(hits, repo.GuardrailViolationHit{
			Scope:       group.scope,
			ScopeKey:    group.scopeKey,
			RuleKey:     group.rule,
			Offending:   group.offending,
			Occurrences: group.count,
			MaxScore:    group.maxScore,
			FirstSeen:   group.firstSeen,
			LastSeen:    group.lastSeen,
			LastTraceID: group.lastTrace,
		})
	}
	return hits
}

// empty reports whether nothing has accumulated since the last drain, so the
// flush can be skipped entirely (the common case - most services have no
// guardrail).
func (b *guardrailBuffer) empty() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.agg) == 0
}

// shadowBuffer accumulates shadow-evaluation outcomes per open recommendation
// (see guardrailRuntime.shadowEvaluate) so a transaction with thousands of
// traces per flush interval costs one summed row, exactly like violations.
type shadowBuffer struct {
	mu  sync.Mutex
	agg map[string]*repo.RecommendationLiveDelta
}

func newShadowBuffer() *shadowBuffer {
	return &shadowBuffer{agg: map[string]*repo.RecommendationLiveDelta{}}
}

// add records one live trace on which a transaction recommendation held or broke.
func (b *shadowBuffer) add(id string, transactionID int64, held bool, at time.Time) {
	b.record(id, repo.RecommendationLiveDelta{
		ID: id, Scope: repo.ScopeTransaction, ScopeKey: repo.TransactionScopeKey(transactionID),
		TransactionID: transactionID,
	}, held, at)
}

// addRule records one live trace on which a proposed rule of a service
// guardrail card held or broke; each rule keeps its own counter.
func (b *shadowBuffer) addRule(id, serviceKey string, rule repo.RuleKey, held bool, at time.Time) {
	b.record(id+"\x00"+string(rule), repo.RecommendationLiveDelta{
		ID: id, Scope: repo.ScopeService, ScopeKey: serviceKey, Rule: rule,
	}, held, at)
}

func (b *shadowBuffer) record(key string, init repo.RecommendationLiveDelta, held bool, at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	d := b.agg[key]
	if d == nil {
		init.FirstAt = at
		d = &init
		b.agg[key] = d
	}
	if held {
		d.Held++
	} else {
		d.Broken++
	}
	if at.Before(d.FirstAt) {
		d.FirstAt = at
	}
	if at.After(d.LastAt) {
		d.LastAt = at
	}
}

// drain returns the accumulated deltas and resets the buffer.
func (b *shadowBuffer) drain() []repo.RecommendationLiveDelta {
	b.mu.Lock()
	agg := b.agg
	b.agg = map[string]*repo.RecommendationLiveDelta{}
	b.mu.Unlock()
	if len(agg) == 0 {
		return nil
	}
	out := make([]repo.RecommendationLiveDelta, 0, len(agg))
	var held, broken int
	for _, d := range agg {
		out = append(out, *d)
		held += int(d.Held)
		broken += int(d.Broken)
	}
	metrics.AddRecommendationShadow(held, broken)
	return out
}

// supersededByInventoryReset reports whether this group's counts describe
// transaction identities that no longer exist. Only allowed_transactions names a
// transaction identity; the other rules police service edges, which deleting
// transactions does not affect.
func (agg *guardrailAgg) supersededByInventoryReset(currentInventoryEpoch int) bool {
	return agg.rule == risk.RuleAllowedTransactions && supersededByReset(agg.inventoryEpoch, currentInventoryEpoch)
}
