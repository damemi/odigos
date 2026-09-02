package clickhouse

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
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
	query := `SELECT scope_type, scope_key, policy_id, name, fire_at_score, signal_weights, enricher_lists, enabled
	      FROM alert_policies FINAL`
	if enabledOnly {
		query += " WHERE enabled = 1"
	}
	query += " ORDER BY scope_type ASC, scope_key ASC"
	return queryRows(ctx, r.conn, "list policies", query, scanPolicy)
}

func scanPolicy(s rowScanner) (repo.Policy, error) {
	var (
		policy        repo.Policy
		scopeType     string
		fireAtScore   int32
		signalWeights map[string]int32 // native Map(String, Int32) column
		enricherLists map[string][]string
		enabled       uint8
	)
	if err := s.Scan(&scopeType, &policy.ScopeKey, &policy.ID, &policy.Name, &fireAtScore, &signalWeights, &enricherLists, &enabled); err != nil {
		return repo.Policy{}, err
	}
	policy.Scope = repo.NormalizePolicyScope(repo.PolicyScope(scopeType))
	policy.FireAtScore = int(fireAtScore)
	policy.Enabled = enabled == 1
	// Retype the column's keys/elements to the domain types; leave the maps nil
	// (not empty) when unset so a policy without overrides stays sparse.
	if len(signalWeights) > 0 {
		policy.SignalWeights = make(map[repo.SignalSource]int, len(signalWeights))
		for source, weight := range signalWeights {
			policy.SignalWeights[repo.SignalSource(source)] = int(weight)
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

// UpsertAlertPolicy writes one policy, keyed by (scope, scopeKey). The table is
// a ReplacingMergeTree ordered by scope, so re-inserting the same scope replaces
// it. A stable policy_id is derived from the scope when the caller leaves it 0,
// so the fired anomaly's PolicyID stays consistent across edits.
func (r *Repo) UpsertAlertPolicy(ctx context.Context, policy repo.Policy) error {
	// The columns are native Maps: narrow the domain's int weights to the
	// column's Int32 element type, and always pass a concrete (possibly empty)
	// map — the driver rejects a nil Map value.
	signalWeights := make(map[string]int32, len(policy.SignalWeights))
	for source, weight := range policy.SignalWeights {
		signalWeights[string(source)] = int32(weight)
	}
	enricherLists := make(map[string][]string, len(policy.EnricherLists))
	for key, list := range policy.EnricherLists {
		enricherLists[string(key)] = list
	}
	scope := repo.NormalizePolicyScope(policy.Scope)
	policyID := policy.ID
	if policyID == 0 {
		policyID = scopedPolicyID(scope, policy.ScopeKey)
	}
	enabled := uint8(0)
	if policy.Enabled {
		enabled = 1
	}
	err := r.insert(ctx,
		"INSERT INTO alert_policies (scope_type, scope_key, policy_id, name, fire_at_score, signal_weights, enricher_lists, enabled, updated_at)",
		func(batch driver.Batch) error {
			return batch.Append(
				string(scope),
				policy.ScopeKey,
				policyID,
				policy.Name,
				int32(policy.FireAtScore),
				signalWeights,
				enricherLists,
				enabled,
				time.Now(),
			)
		})
	if err != nil {
		return fmt.Errorf("upsert alert policy: %w", err)
	}
	return nil
}

// DeleteAlertPolicy drops one scoped override via an ALTER ... DELETE mutation
// (ReplacingMergeTree has no real delete); admin action, off the hot path.
func (r *Repo) DeleteAlertPolicy(ctx context.Context, scope repo.PolicyScope, scopeKey string) error {
	if err := r.conn.Exec(ctx,
		"ALTER TABLE alert_policies DELETE WHERE scope_type = ? AND scope_key = ?",
		string(repo.NormalizePolicyScope(scope)), scopeKey); err != nil {
		return fmt.Errorf("delete alert policy %s/%s: %w", scope, scopeKey, err)
	}
	return nil
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
