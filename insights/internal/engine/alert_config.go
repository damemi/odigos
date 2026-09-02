package engine

// Alert-policy configuration: the scoped anomaly-firing policies.
//
// Mirrors LearningConfig. The engine keeps the alert_policies table in an
// in-memory snapshot and resolves the per-transaction policy with a lock-free
// scope lookup, so the per-trace path never hits the DB. The snapshot swaps
// atomically, so a background Reload (or UI edit) applies without a restart.
// The most specific scope wins (transaction → service → global) and fully
// replaces the broader one.

import (
	"context"
	"sync/atomic"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// AlertConfig is a lock-free, in-memory snapshot of the alert policies indexed
// by scope. Reads (Resolve) are wait-free; Reload swaps the whole snapshot
// atomically.
type AlertConfig struct {
	index atomic.Pointer[repo.ScopeIndex[repo.Policy]]
}

// newAlertConfig wraps an already-built scope index.
func newAlertConfig(idx *repo.ScopeIndex[repo.Policy]) *AlertConfig {
	cfg := &AlertConfig{}
	cfg.index.Store(idx)
	return cfg
}

// Resolve returns the most specific policy governing a (transaction, service),
// where serviceKey is repo.ServiceScopeKey(namespace, service). Disabled
// overrides are indexed too, so a disabled scope suppresses alerting rather
// than falling back to global — callers must check Policy.Enabled. ok is false
// when no scope matches at all.
func (cfg *AlertConfig) Resolve(transactionID int64, serviceKey string) (repo.Policy, bool) {
	if cfg == nil {
		return repo.Policy{}, false
	}
	if idx := cfg.index.Load(); idx != nil {
		return idx.Resolve(transactionID, serviceKey)
	}
	return repo.Policy{}, false
}

// Reload re-reads the alert_policies table and atomically swaps the snapshot. On
// a DB error the current snapshot is left untouched and the error is returned,
// so a transient outage never wipes a good config.
func (cfg *AlertConfig) Reload(ctx context.Context, r repo.Repository) error {
	idx, err := loadAlertPolicies(ctx, r)
	if err != nil {
		return err
	}
	cfg.index.Store(idx)
	return nil
}

// LoadAlertConfig reads the alert_policies table into a fresh snapshot. Call
// once, at startup. On a DB error it returns the error plus an empty snapshot so
// the caller can log-and-continue (nothing fires until a later Reload recovers).
func LoadAlertConfig(ctx context.Context, r repo.Repository) (*AlertConfig, error) {
	idx, err := loadAlertPolicies(ctx, r)
	if err != nil {
		return newAlertConfig(&repo.ScopeIndex[repo.Policy]{}), err
	}
	return newAlertConfig(idx), nil
}

// loadAlertPolicies reads every policy row and indexes it by scope via the
// shared repo.ScopeIndex. Both enabled and disabled rows are indexed so a
// disabled override suppresses alerting for its scope (see Resolve).
func loadAlertPolicies(ctx context.Context, r repo.Repository) (*repo.ScopeIndex[repo.Policy], error) {
	policies, err := r.ListAlertPolicies(ctx)
	if err != nil {
		return nil, err
	}
	idx := &repo.ScopeIndex[repo.Policy]{}
	for _, policy := range policies {
		idx.Put(policy.Scope, policy.ScopeKey, policy)
	}
	return idx, nil
}
