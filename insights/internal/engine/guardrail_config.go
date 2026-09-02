package engine

// Service-guardrail configuration: the scoped, operator-declared segmentation
// rules.
//
// Mirrors AlertConfig. The engine keeps the service_guardrails table in an
// in-memory snapshot and resolves the per-transaction guardrail with a lock-free
// scope lookup — no DB round-trip on the per-trace path. The snapshot is swapped
// atomically, so a background Reload (and a UI edit) applies at runtime without a
// restart. Most specific scope wins (transaction → service → global), though v1
// only authors service-scoped guardrails.

import (
	"context"
	"sync/atomic"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// GuardrailConfig is a lock-free, in-memory snapshot of the service guardrails
// indexed by scope. Reads (Resolve) are wait-free; Reload swaps the whole
// snapshot atomically.
type GuardrailConfig struct {
	index atomic.Pointer[repo.ScopeIndex[repo.ServiceGuardrail]]
}

// newGuardrailConfig wraps an already-built scope index.
func newGuardrailConfig(idx *repo.ScopeIndex[repo.ServiceGuardrail]) *GuardrailConfig {
	cfg := &GuardrailConfig{}
	cfg.index.Store(idx)
	return cfg
}

// Resolve returns the most specific guardrail governing a (transaction,
// service): a transaction override wins over a service guardrail, which wins over
// a global one. serviceKey is repo.ServiceScopeKey(namespace, service). ok is
// false when no scope matches (the common case — most services have no
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

// Reload re-reads the service_guardrails table and atomically swaps the
// snapshot. On a DB error the current snapshot is left untouched and the error
// is returned, so a transient outage never wipes a good config.
func (cfg *GuardrailConfig) Reload(ctx context.Context, r repo.Repository) error {
	idx, err := loadServiceGuardrails(ctx, r)
	if err != nil {
		return err
	}
	cfg.index.Store(idx)
	return nil
}

// LoadGuardrailConfig reads the service_guardrails table into a fresh snapshot.
// Call once, at startup. On a DB error it returns the error plus an empty
// snapshot so the caller can log-and-continue (nothing is policed until a later
// Reload recovers).
func LoadGuardrailConfig(ctx context.Context, r repo.Repository) (*GuardrailConfig, error) {
	idx, err := loadServiceGuardrails(ctx, r)
	if err != nil {
		return newGuardrailConfig(&repo.ScopeIndex[repo.ServiceGuardrail]{}), err
	}
	return newGuardrailConfig(idx), nil
}

// loadServiceGuardrails reads every guardrail row and indexes it by scope via
// the shared repo.ScopeIndex.
func loadServiceGuardrails(ctx context.Context, r repo.Repository) (*repo.ScopeIndex[repo.ServiceGuardrail], error) {
	guardrails, err := r.ListServiceGuardrails(ctx)
	if err != nil {
		return nil, err
	}
	idx := &repo.ScopeIndex[repo.ServiceGuardrail]{}
	for _, guardrail := range guardrails {
		idx.Put(guardrail.Scope, guardrail.ScopeKey, guardrail)
	}
	return idx, nil
}
