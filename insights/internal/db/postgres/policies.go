package postgres

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"time"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// ListEnabledPolicies returns the enabled alert policies across every scope.
func (r *Repo) ListEnabledPolicies(ctx context.Context) ([]repo.Policy, error) {
	return r.queryPolicies(ctx, true)
}

// ListAlertPolicies returns every alert policy (all scopes, enabled or not).
func (r *Repo) ListAlertPolicies(ctx context.Context) ([]repo.Policy, error) {
	return r.queryPolicies(ctx, false)
}

func (r *Repo) queryPolicies(ctx context.Context, enabledOnly bool) ([]repo.Policy, error) {
	query := `SELECT scope_type, scope_key, policy_id, name, fire_at_score, signal_weights, enricher_lists, enabled, notify_muted, excluded_integration_ids
	      FROM alert_policies`
	if enabledOnly {
		query += " WHERE enabled"
	}
	query += " ORDER BY scope_type ASC, scope_key ASC"
	return queryRows(ctx, r.pool, "list policies", query, scanPolicy)
}

func scanPolicy(s rowScanner) (repo.Policy, error) {
	var (
		policy        repo.Policy
		scopeType     string
		fireAtScore   int32
		signalWeights map[string]int
		enricherLists map[string][]string
		muted         bool
		excluded      []string
	)
	if err := s.Scan(&scopeType, &policy.ScopeKey, &policy.ID, &policy.Name, &fireAtScore, &signalWeights, &enricherLists, &policy.Enabled, &muted, &excluded); err != nil {
		return repo.Policy{}, err
	}
	policy.Scope = repo.NormalizePolicyScope(repo.PolicyScope(scopeType))
	policy.FireAtScore = int(fireAtScore)
	policy.Delivery = scanDelivery(muted, excluded)
	// Retype the column's keys to the domain types; leave the maps nil (not
	// empty) when unset so a policy without overrides stays sparse.
	if len(signalWeights) > 0 {
		policy.SignalWeights = make(map[repo.SignalSource]int, len(signalWeights))
		for source, weight := range signalWeights {
			policy.SignalWeights[repo.SignalSource(source)] = weight
		}
	}
	if len(enricherLists) > 0 {
		policy.EnricherLists = make(map[repo.EnricherListKey][]string, len(enricherLists))
		for key, list := range enricherLists {
			policy.EnricherLists[repo.EnricherListKey(key)] = list
		}
	}
	return policy, nil
}

// UpsertAlertPolicy writes one policy, keyed by (scope, scopeKey), replacing
// any previous version. A stable policy_id is derived from the scope when the
// caller leaves it 0, so the fired anomaly's PolicyID stays consistent across
// edits.
func (r *Repo) UpsertAlertPolicy(ctx context.Context, policy repo.Policy) error {
	signalWeights := make(map[string]int, len(policy.SignalWeights))
	for source, weight := range policy.SignalWeights {
		signalWeights[string(source)] = weight
	}
	enricherLists := make(map[string][]string, len(policy.EnricherLists))
	for key, list := range policy.EnricherLists {
		enricherLists[string(key)] = nonNilStrings(list)
	}
	scope := repo.NormalizePolicyScope(policy.Scope)
	policyID := policy.ID
	if policyID == 0 {
		policyID = scopedPolicyID(scope, policy.ScopeKey)
	}
	if _, err := r.pool.Exec(ctx,
		`INSERT INTO alert_policies (scope_type, scope_key, policy_id, name, fire_at_score, signal_weights, enricher_lists, enabled, notify_muted, excluded_integration_ids, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 ON CONFLICT (scope_type, scope_key) DO UPDATE SET
			policy_id = EXCLUDED.policy_id, name = EXCLUDED.name, fire_at_score = EXCLUDED.fire_at_score,
			signal_weights = EXCLUDED.signal_weights, enricher_lists = EXCLUDED.enricher_lists, enabled = EXCLUDED.enabled,
			notify_muted = EXCLUDED.notify_muted, excluded_integration_ids = EXCLUDED.excluded_integration_ids,
			updated_at = EXCLUDED.updated_at`,
		string(scope), policy.ScopeKey, policyID, policy.Name, int32(policy.FireAtScore),
		signalWeights, enricherLists, policy.Enabled,
		policy.Delivery.Muted, nonNilStrings(policy.Delivery.ExcludedIntegrationIDs), time.Now()); err != nil {
		return fmt.Errorf("upsert alert policy: %w", err)
	}
	return nil
}

// DeleteAlertPolicy drops one scoped override.
func (r *Repo) DeleteAlertPolicy(ctx context.Context, scope repo.PolicyScope, scopeKey string) error {
	if _, err := r.pool.Exec(ctx,
		"DELETE FROM alert_policies WHERE scope_type = $1 AND scope_key = $2",
		string(repo.NormalizePolicyScope(scope)), scopeKey); err != nil {
		return fmt.Errorf("delete alert policy %s/%s: %w", scope, scopeKey, err)
	}
	return nil
}

// nonNilStrings returns s, or an empty slice for nil, so array and JSON columns
// store [] rather than NULL.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// scanDelivery maps the notify_muted and excluded_integration_ids columns to a
// source's Delivery.
func scanDelivery(muted bool, excluded []string) repo.Delivery {
	d := repo.Delivery{Muted: muted}
	if len(excluded) > 0 {
		d.ExcludedIntegrationIDs = excluded
	}
	return d
}

// scopedPolicyID gives each scope a stable policy_id: 1 for the global default,
// and a deterministic positive hash of "scope/key" for an override. Determinism
// keeps a fired anomaly's PolicyID stable across edits to the same scope.
func scopedPolicyID(scope repo.PolicyScope, scopeKey string) int64 {
	if repo.NormalizePolicyScope(scope) == repo.ScopeGlobal {
		return 1
	}
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(string(repo.NormalizePolicyScope(scope)) + "/" + scopeKey))
	id := int64(hasher.Sum64() & math.MaxInt64)
	if id <= 1 {
		id += 2 // keep 1 reserved for global and avoid 0
	}
	return id
}
