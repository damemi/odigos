package engine

// Learning-phase configuration: the per-class promotion policies.
//
// Defaults are seeded by the schema migrations (see the clickhouse/migrate
// package). The engine keeps the learning_policies table in an in-memory
// snapshot and consults it on the per-trace path with a plain map lookup - no
// DB round-trip per request. The snapshot is swapped atomically, so a
// background Reload can pick up edits at runtime without ever moving the read
// onto the hot path and without a restart.

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// policySnapshot indexes every class's learning policies by scope, using the
// shared repo.ScopeIndex resolver (the same mechanism alert policies will use).
// One index per class holds that class's global default plus any narrower
// service/transaction overrides.
type policySnapshot map[repo.DeviationClass]*repo.ScopeIndex[repo.LearningPolicy]

// LearningConfig is a lock-free, in-memory snapshot of the promotion policies,
// indexed by class and scope. Reads (For / Resolve) are wait-free; Reload swaps
// the whole snapshot atomically.
type LearningConfig struct {
	policies atomic.Pointer[policySnapshot]
}

// newLearningConfig builds a snapshot from a flat class→policy map, treating
// every entry as that class's global default. Kept for tests and simple callers
// that don't need scoped overrides.
func newLearningConfig(policies map[repo.DeviationClass]repo.LearningPolicy) *LearningConfig {
	snap := make(policySnapshot, len(policies))
	for class, p := range policies {
		idx := &repo.ScopeIndex[repo.LearningPolicy]{}
		idx.Put(repo.ScopeGlobal, "", p)
		snap[class] = idx
	}
	cfg := &LearningConfig{}
	cfg.policies.Store(&snap)
	return cfg
}

// newSnapshotConfig wraps an already-built snapshot.
func newSnapshotConfig(snap policySnapshot) *LearningConfig {
	cfg := &LearningConfig{}
	cfg.policies.Store(&snap)
	return cfg
}

// For returns a class's global default policy, ignoring any scoped overrides.
// A class with no global row returns the zero policy, which shouldPromote
// degrades to the count-only DefaultMinObservations guard.
func (cfg *LearningConfig) For(class repo.DeviationClass) repo.LearningPolicy {
	if cfg == nil {
		return repo.LearningPolicy{}
	}
	if m := cfg.policies.Load(); m != nil {
		if idx := (*m)[class]; idx != nil {
			if p, ok := idx.Global(); ok {
				return p
			}
		}
	}
	return repo.LearningPolicy{}
}

// Resolve returns the most specific policy that governs a (transaction, class):
// a transaction override wins over a service override, which wins over the
// global default. serviceKey is repo.ServiceScopeKey(namespace, service). Pure
// in-memory lookup (two map probes at most) - safe on the per-trace path. With
// no match at any level it returns the zero policy (count-only default).
func (cfg *LearningConfig) Resolve(class repo.DeviationClass, transactionID int64, serviceKey string) repo.LearningPolicy {
	if cfg == nil {
		return repo.LearningPolicy{}
	}
	m := cfg.policies.Load()
	if m == nil {
		return repo.LearningPolicy{}
	}
	if idx := (*m)[class]; idx != nil {
		if p, ok := idx.Resolve(transactionID, serviceKey); ok {
			return p
		}
	}
	return repo.LearningPolicy{}
}

// Reload re-reads the learning_policies table and atomically swaps the
// snapshot. On a DB error the current snapshot is left untouched and the error
// is returned, so a transient outage never wipes a good config.
func (cfg *LearningConfig) Reload(ctx context.Context, r repo.Repository) error {
	snap, err := loadPolicies(ctx, r)
	if err != nil {
		return err
	}
	cfg.policies.Store(&snap)
	return nil
}

// LoadLearningConfig reads the learning_policies table into a fresh snapshot.
// Call once, at startup. On a DB error it returns the error plus an empty
// snapshot so the caller can log-and-continue (every class then falls back to
// the count-only guard) and a later Reload can recover.
func LoadLearningConfig(ctx context.Context, r repo.Repository) (*LearningConfig, error) {
	snap, err := loadPolicies(ctx, r)
	if err != nil {
		return newSnapshotConfig(policySnapshot{}), err
	}
	return newSnapshotConfig(snap), nil
}

// loadPolicies reads every policy row and indexes it by class and scope via the
// shared repo.ScopeIndex. An empty/unknown scope is treated as global; a
// transaction override whose ScopeKey isn't a valid transaction_id is skipped by
// ScopeIndex.Put (it could never match).
func loadPolicies(ctx context.Context, r repo.Repository) (policySnapshot, error) {
	rows, err := r.ListLearningPolicies(ctx)
	if err != nil {
		return nil, err
	}
	return indexLearningPolicies(rows), nil
}

// LearningConfigFromRows indexes an already-loaded policy list. Used by the
// baseline GET path so it can resolve the same transaction→service→global
// policy the promotion gate uses, without requiring the engine snapshot.
func LearningConfigFromRows(rows []repo.LearningPolicy) *LearningConfig {
	return newSnapshotConfig(indexLearningPolicies(rows))
}

func indexLearningPolicies(rows []repo.LearningPolicy) policySnapshot {
	snap := make(policySnapshot)
	for _, row := range rows {
		idx := snap[row.Class]
		if idx == nil {
			idx = &repo.ScopeIndex[repo.LearningPolicy]{}
			snap[row.Class] = idx
		}
		idx.Put(row.Scope, row.ScopeKey, row)
	}
	return snap
}

// timeSweepCutoffs returns the most permissive cutoffs across all loaded
// policies for time-based promotion conditions, plus whether any policy uses
// such a condition. The sweeper skips the DB phase when ok is false.
func (cfg *LearningConfig) timeSweepCutoffs(now time.Time) (repo.UnpromotedBaselineFilter, bool) {
	if cfg == nil {
		return repo.UnpromotedBaselineFilter{}, false
	}
	m := cfg.policies.Load()
	if m == nil {
		return repo.UnpromotedBaselineFilter{}, false
	}

	var minDurMins, minStableMins int64 = -1, -1
	for _, idx := range *m {
		idx.Each(func(p repo.LearningPolicy) {
			for _, cond := range p.Conditions {
				switch cond.Type {
				case repo.ConditionMinDuration:
					if minDurMins < 0 || cond.MinDurationMinutes < minDurMins {
						minDurMins = cond.MinDurationMinutes
					}
				case repo.ConditionStableDuration:
					if minStableMins < 0 || cond.StableMinutes < minStableMins {
						minStableMins = cond.StableMinutes
					}
				}
			}
		})
	}
	if minDurMins < 0 && minStableMins < 0 {
		return repo.UnpromotedBaselineFilter{}, false
	}

	filter := repo.UnpromotedBaselineFilter{}
	if minDurMins >= 0 {
		filter.MinDurationEnabled = true
		filter.MinDurationCutoff = now.Add(-time.Duration(minDurMins) * time.Minute)
	}
	if minStableMins >= 0 {
		filter.StableDurationEnabled = true
		filter.StableDurationCutoff = now.Add(-time.Duration(minStableMins) * time.Minute)
	}
	return filter, true
}
