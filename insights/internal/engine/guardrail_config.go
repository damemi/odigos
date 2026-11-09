package engine

// Service-guardrail configuration: the scoped, operator-declared segmentation
// rules.
//
// Mirrors AlertConfig. The engine keeps the service_guardrails table in an
// in-memory snapshot and resolves the per-transaction guardrail with a lock-free
// scope lookup - no DB round-trip on the per-trace path. The snapshot is swapped
// atomically, so a background Reload (and a UI edit) applies at runtime without a
// restart. Most specific scope wins (transaction → service → global), though v1
// only authors service-scoped guardrails.

import (
	"context"
	"strconv"
	"sync/atomic"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// GuardrailConfig is a lock-free, in-memory snapshot of the service guardrails
// indexed by scope. Reads (Resolve) are wait-free; Reload swaps the whole
// snapshot atomically.
//
// Transaction-scoped guardrails are kept OUT of the allowlist scope index and in
// a separate byTxn map: the shared scope stack is most-specific-replaces, so a
// transaction row routed through Resolve would shadow the service/global
// allowlist for that transaction. Transaction guardrails are instead evaluated
// additively (see guardrailRuntime.evaluate), so they layer on top of the
// service allowlist rather than replacing it.
type GuardrailConfig struct {
	index atomic.Pointer[repo.ScopeIndex[repo.ServiceGuardrail]]
	byTxn atomic.Pointer[map[int64]repo.ServiceGuardrail]
	// shadow holds the open recommendations per transaction, evaluated on live
	// traffic to build confidence without ever firing a finding.
	shadow atomic.Pointer[map[int64][]shadowSpec]
	// shadowServices holds the open service guardrail cards by service scope
	// key, each proposed allowlist checked silently against the service's
	// live traffic.
	shadowServices atomic.Pointer[map[string]shadowServiceCard]
}

// shadowSpec is one open recommendation as the per-trace path checks it.
type shadowSpec struct {
	id   string
	spec repo.CorrelationSpec
}

// shadowServiceCard is one open service guardrail recommendation shaped for
// the per-trace path: the proposed lists as a guardrail so the same allowlist
// extractors that police enforced rules can be reused unchanged.
type shadowServiceCard struct {
	id        string
	rules     []repo.RuleKey
	guardrail repo.ServiceGuardrail
}

// maxShadowSpecsPerTransaction bounds the per-trace cost of shadow evaluation:
// only the strongest open recommendations of a transaction are checked live.
const maxShadowSpecsPerTransaction = 10

// newGuardrailConfig wraps an already-built scope index, byTxn map, and shadow sets.
func newGuardrailConfig(idx *repo.ScopeIndex[repo.ServiceGuardrail], byTxn map[int64]repo.ServiceGuardrail, shadow map[int64][]shadowSpec, shadowServices map[string]shadowServiceCard) *GuardrailConfig {
	cfg := &GuardrailConfig{}
	cfg.index.Store(idx)
	cfg.byTxn.Store(&byTxn)
	cfg.shadow.Store(&shadow)
	cfg.shadowServices.Store(&shadowServices)
	return cfg
}

// ShadowSpecs returns the open recommendations to shadow-evaluate for a
// transaction. The common case (no open recommendations) is one map miss.
func (cfg *GuardrailConfig) shadowSpecs(transactionID int64) []shadowSpec {
	if cfg == nil {
		return nil
	}
	if m := cfg.shadow.Load(); m != nil {
		return (*m)[transactionID]
	}
	return nil
}

// shadowServiceCard returns the open service guardrail card for a service, if
// any. The common case (no open card) is one map miss.
func (cfg *GuardrailConfig) shadowService(serviceKey string) (shadowServiceCard, bool) {
	if cfg == nil {
		return shadowServiceCard{}, false
	}
	if m := cfg.shadowServices.Load(); m != nil {
		card, ok := (*m)[serviceKey]
		return card, ok
	}
	return shadowServiceCard{}, false
}

// Resolve returns the most specific guardrail governing a (transaction,
// service): a transaction override wins over a service guardrail, which wins over
// a global one. serviceKey is repo.ServiceScopeKey(namespace, service). ok is
// false when no scope matches (the common case - most services have no
// guardrail). Nil-safe so an engine without a loaded snapshot policing nothing.
func (cfg *GuardrailConfig) Resolve(transactionID int64, serviceKey string) (repo.ServiceGuardrail, bool) {
	if cfg == nil {
		return repo.ServiceGuardrail{}, false
	}
	if idx := cfg.index.Load(); idx != nil {
		return idx.Resolve(transactionID, serviceKey)
	}
	return repo.ServiceGuardrail{}, false
}

// ResolveTransactionGuardrail returns the transaction-scoped guardrail for a
// transaction, if one is configured. It is separate from Resolve so transaction
// guardrails layer additively on top of the service/global allowlist rather than
// replacing it. Nil-safe.
func (cfg *GuardrailConfig) ResolveTransactionGuardrail(transactionID int64) (repo.ServiceGuardrail, bool) {
	if cfg == nil {
		return repo.ServiceGuardrail{}, false
	}
	if m := cfg.byTxn.Load(); m != nil {
		g, ok := (*m)[transactionID]
		return g, ok
	}
	return repo.ServiceGuardrail{}, false
}

// Reload re-reads the service_guardrails table and atomically swaps the
// snapshot. On a DB error the current snapshot is left untouched and the error
// is returned, so a transient outage never wipes a good config.
func (cfg *GuardrailConfig) Reload(ctx context.Context, r repo.Repository) error {
	idx, byTxn, err := loadServiceGuardrails(ctx, r)
	if err != nil {
		return err
	}
	shadow, err := loadShadowSpecs(ctx, r)
	if err != nil {
		return err
	}
	shadowServices, err := loadShadowServiceCards(ctx, r)
	if err != nil {
		return err
	}
	cfg.index.Store(idx)
	cfg.byTxn.Store(&byTxn)
	cfg.shadow.Store(&shadow)
	cfg.shadowServices.Store(&shadowServices)
	return nil
}

// LoadGuardrailConfig reads the service_guardrails table into a fresh snapshot.
// Call once, at startup. On a DB error it returns the error plus an empty
// snapshot so the caller can log-and-continue (nothing is policed until a later
// Reload recovers).
func LoadGuardrailConfig(ctx context.Context, r repo.Repository) (*GuardrailConfig, error) {
	idx, byTxn, err := loadServiceGuardrails(ctx, r)
	if err != nil {
		return newGuardrailConfig(&repo.ScopeIndex[repo.ServiceGuardrail]{}, map[int64]repo.ServiceGuardrail{}, nil, nil), err
	}
	shadow, err := loadShadowSpecs(ctx, r)
	if err != nil {
		return newGuardrailConfig(idx, byTxn, nil, nil), err
	}
	shadowServices, err := loadShadowServiceCards(ctx, r)
	if err != nil {
		return newGuardrailConfig(idx, byTxn, shadow, nil), err
	}
	return newGuardrailConfig(idx, byTxn, shadow, shadowServices), nil
}

// loadShadowServiceCards reads the open service guardrail recommendations and
// shapes each card's proposed lists as an all-enforced guardrail for the
// per-trace check.
func loadShadowServiceCards(ctx context.Context, r repo.Repository) (map[string]shadowServiceCard, error) {
	recs, err := r.ListRecommendations(ctx, repo.RecommendationFilter{
		Kind:  repo.RecommendationKindServiceGuardrail,
		State: repo.RecommendationOpen,
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]shadowServiceCard, len(recs))
	for _, rec := range recs {
		if len(rec.Rules) == 0 {
			continue
		}
		card := shadowServiceCard{id: rec.ID, guardrail: repo.ServiceGuardrail{
			Scope: repo.ScopeService, ScopeKey: rec.ScopeKey,
			RuleLists: make(map[repo.RuleKey][]string, len(rec.Rules)),
			RuleModes: make(map[repo.RuleKey]repo.RuleMode, len(rec.Rules)),
		}}
		for _, rule := range rec.Rules {
			card.rules = append(card.rules, rule.Rule)
			card.guardrail.RuleLists[rule.Rule] = rule.Items
			card.guardrail.RuleModes[rule.Rule] = repo.RuleModeEnforce
			if rule.Fingerprint != "" {
				repo.SetRuleFingerprint(&card.guardrail, rule.Rule, rule.Fingerprint)
			}
		}
		out[rec.ScopeKey] = card
	}
	return out, nil
}

// loadShadowSpecs reads the open attribute_correlation recommendations, best
// rank first, keeping at most maxShadowSpecsPerTransaction per transaction.
func loadShadowSpecs(ctx context.Context, r repo.Repository) (map[int64][]shadowSpec, error) {
	recs, err := r.ListRecommendations(ctx, repo.RecommendationFilter{
		Kind:  repo.RecommendationKindAttributeCorrelation,
		State: repo.RecommendationOpen,
	})
	if err != nil {
		return nil, err
	}
	out := map[int64][]shadowSpec{}
	for _, rec := range recs {
		if len(out[rec.TransactionID]) >= maxShadowSpecsPerTransaction {
			continue
		}
		out[rec.TransactionID] = append(out[rec.TransactionID], shadowSpec{id: rec.ID, spec: rec.Spec})
	}
	return out, nil
}

// loadServiceGuardrails reads every guardrail row and indexes it: service/global
// rows go into the shared scope index (allowlist resolution), while
// transaction-scoped rows go into a separate byTxn map so they can be evaluated
// additively without shadowing the service allowlist.
func loadServiceGuardrails(ctx context.Context, r repo.Repository) (*repo.ScopeIndex[repo.ServiceGuardrail], map[int64]repo.ServiceGuardrail, error) {
	guardrails, err := r.ListServiceGuardrails(ctx)
	if err != nil {
		return nil, nil, err
	}
	idx := &repo.ScopeIndex[repo.ServiceGuardrail]{}
	byTxn := map[int64]repo.ServiceGuardrail{}
	for _, guardrail := range guardrails {
		if repo.NormalizePolicyScope(guardrail.Scope) == repo.ScopeTransaction {
			if id, perr := strconv.ParseInt(guardrail.ScopeKey, 10, 64); perr == nil {
				byTxn[id] = guardrail
			}
			continue
		}
		idx.Put(guardrail.Scope, guardrail.ScopeKey, guardrail)
	}
	return idx, byTxn, nil
}
