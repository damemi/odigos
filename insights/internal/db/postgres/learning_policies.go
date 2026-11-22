package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// ListLearningPolicies returns every learning policy (global class defaults
// plus scope overrides). Read at startup/reload to build the promotion gate's
// in-memory snapshot, not on the hot path.
func (r *Repo) ListLearningPolicies(ctx context.Context) ([]repo.LearningPolicy, error) {
	return queryRows(ctx, r.pool, "list learning policies",
		`SELECT class, scope_type, scope_key, match_mode, conditions, min_matches
		 FROM learning_policies ORDER BY class ASC, scope_type ASC, scope_key ASC`,
		scanLearningPolicy)
}

func scanLearningPolicy(s rowScanner) (repo.LearningPolicy, error) {
	var (
		class, scopeType, scopeKey, mode, conditions string
		minMatches                                   int64
	)
	if err := s.Scan(&class, &scopeType, &scopeKey, &mode, &conditions, &minMatches); err != nil {
		return repo.LearningPolicy{}, err
	}
	p := repo.LearningPolicy{
		Class:      repo.DeviationClass(class),
		Mode:       repo.LearningMode(mode),
		MinMatches: int(minMatches),
		Scope:      repo.NormalizePolicyScope(repo.PolicyScope(scopeType)),
		ScopeKey:   scopeKey,
	}
	if conditions != "" {
		if err := json.Unmarshal([]byte(conditions), &p.Conditions); err != nil {
			return repo.LearningPolicy{}, fmt.Errorf("decode conditions for %s: %w", class, err)
		}
	}
	return p, nil
}

// UpsertLearningPolicy inserts or updates one policy, keyed by (class, scope,
// scopeKey).
func (r *Repo) UpsertLearningPolicy(ctx context.Context, p repo.LearningPolicy) error {
	conditions, err := json.Marshal(p.Conditions)
	if err != nil {
		return fmt.Errorf("encode conditions: %w", err)
	}
	if _, err := r.pool.Exec(ctx,
		`INSERT INTO learning_policies (class, scope_type, scope_key, match_mode, conditions, min_matches, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (class, scope_type, scope_key) DO UPDATE SET
			match_mode = EXCLUDED.match_mode, conditions = EXCLUDED.conditions,
			min_matches = EXCLUDED.min_matches, updated_at = EXCLUDED.updated_at`,
		string(p.Class), string(repo.NormalizePolicyScope(p.Scope)), p.ScopeKey, string(p.Mode),
		string(conditions), int64(p.MinMatches), time.Now()); err != nil {
		return fmt.Errorf("upsert learning policy: %w", err)
	}
	return nil
}

// DeleteLearningPolicy removes one scoped row.
func (r *Repo) DeleteLearningPolicy(ctx context.Context, class repo.DeviationClass, scope repo.PolicyScope, scopeKey string) error {
	if _, err := r.pool.Exec(ctx,
		"DELETE FROM learning_policies WHERE class = $1 AND scope_type = $2 AND scope_key = $3",
		string(class), string(repo.NormalizePolicyScope(scope)), scopeKey); err != nil {
		return fmt.Errorf("delete learning policy %s/%s/%s: %w", class, scope, scopeKey, err)
	}
	return nil
}
