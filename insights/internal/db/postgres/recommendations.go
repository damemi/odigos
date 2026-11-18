package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// UpsertRecommendations writes one mining run's rows, keyed by (scope_type,
// scope_key, recommendation_id); the newest write wins per recommendation.
func (r *Repo) UpsertRecommendations(ctx context.Context, recs []repo.Recommendation) error {
	if len(recs) == 0 {
		return nil
	}
	now := time.Now()
	type keyed struct {
		key  string
		args []any
	}
	rows := make([]keyed, 0, len(recs))
	for _, rec := range recs {
		spec, err := json.Marshal(rec.Spec)
		if err != nil {
			return fmt.Errorf("encode spec %s: %w", rec.ID, err)
		}
		examples, err := json.Marshal(nonNilExamples(rec.Examples))
		if err != nil {
			return fmt.Errorf("encode examples %s: %w", rec.ID, err)
		}
		rules, err := json.Marshal(nonNilRules(rec.Rules))
		if err != nil {
			return fmt.Errorf("encode rules %s: %w", rec.ID, err)
		}
		appliedRules, err := json.Marshal(nonNilRuleKeys(rec.AppliedRules))
		if err != nil {
			return fmt.Errorf("encode applied rules %s: %w", rec.ID, err)
		}
		createdAt := rec.CreatedAt
		if createdAt.IsZero() {
			createdAt = now
		}
		minedAt := rec.MinedAt
		if minedAt.IsZero() {
			minedAt = now
		}
		scope, scopeKey := repo.NormalizeRecommendationScope(rec.Scope, rec.ScopeKey, rec.TransactionID)
		// Only service rows own their namespace/service; transaction rows take
		// them from the transaction at read time.
		namespace, service := "", ""
		if scope == repo.ScopeService {
			namespace, service = rec.Namespace, rec.Service
		}
		rows = append(rows, keyed{
			key: string(scope) + "\x00" + scopeKey + "\x00" + rec.ID,
			args: []any{
				string(scope), scopeKey, rec.ID,
				rec.TransactionID, namespace, service, string(rec.Kind), string(repo.NormalizeRecommendationState(rec.State)),
				rec.Stale, int32(rec.Rank),
				string(spec), rec.Transform, rec.Transport, string(rules), string(appliedRules),
				int32(rec.Observed), int32(rec.Held), int32(rec.Distinct), int32(rec.SampleCount), string(examples),
				minedAt, createdAt, now,
			},
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].key < rows[j].key })
	batch := make([][]any, len(rows))
	for i := range rows {
		batch[i] = rows[i].args
	}
	err := execBatch(ctx, r.pool,
		`INSERT INTO guardrail_recommendations (
			scope_type, scope_key, recommendation_id, transaction_id, namespace, service, kind, state, stale, rank,
			spec, transform, transport, rules, applied_rules, observed, held, distinct_values, sample_count, examples,
			mined_at, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23)
		 ON CONFLICT (scope_type, scope_key, recommendation_id) DO UPDATE SET
			transaction_id = EXCLUDED.transaction_id, namespace = EXCLUDED.namespace, service = EXCLUDED.service,
			kind = EXCLUDED.kind, state = EXCLUDED.state, stale = EXCLUDED.stale, rank = EXCLUDED.rank,
			spec = EXCLUDED.spec, transform = EXCLUDED.transform, transport = EXCLUDED.transport,
			rules = EXCLUDED.rules, applied_rules = EXCLUDED.applied_rules, observed = EXCLUDED.observed,
			held = EXCLUDED.held, distinct_values = EXCLUDED.distinct_values, sample_count = EXCLUDED.sample_count,
			examples = EXCLUDED.examples, mined_at = EXCLUDED.mined_at, created_at = EXCLUDED.created_at,
			updated_at = EXCLUDED.updated_at`,
		batch)
	if err != nil {
		return fmt.Errorf("upsert recommendations: %w", err)
	}
	return nil
}

func nonNilExamples(in []repo.RecommendationExample) []repo.RecommendationExample {
	if in == nil {
		return []repo.RecommendationExample{}
	}
	return in
}

func nonNilRules(in []repo.RecommendationRule) []repo.RecommendationRule {
	if in == nil {
		return []repo.RecommendationRule{}
	}
	return in
}

func nonNilRuleKeys(in []repo.RuleKey) []repo.RuleKey {
	if in == nil {
		return []repo.RuleKey{}
	}
	return in
}

// recommendationCols is the joined row shape shared by list and get: the
// recommendation, its summed live counters (whole and per rule), and the
// scope's identity - stored on service rows, joined from the transaction
// otherwise.
const recommendationCols = `
	r.scope_type, r.scope_key, r.transaction_id, r.recommendation_id, r.kind, r.state, r.stale, r.rank,
	r.spec, r.transform, r.transport, r.rules, r.applied_rules, r.observed, r.held, r.distinct_values, r.sample_count, r.examples,
	r.mined_at, r.created_at, r.updated_at,
	COALESCE(lc.live_held, 0), COALESCE(lc.live_broken, 0), COALESCE(lc.live_first_at, 'epoch'), COALESCE(lc.live_last_at, 'epoch'), lc.live_by_rule,
	` + recommendationServiceExpr + `, ` + recommendationNamespaceExpr + `, COALESCE(t.operation, ''), COALESCE(t.kind, '')`

const (
	recommendationServiceExpr   = `CASE WHEN r.scope_type = 'service' THEN r.service ELSE COALESCE(t.service, '') END`
	recommendationNamespaceExpr = `CASE WHEN r.scope_type = 'service' THEN r.namespace ELSE COALESCE(t.namespace, '') END`
	// liveByRuleSep joins one rule's counters into a single aggregated string.
	liveByRuleSep = "|"
)

const recommendationFrom = `
	FROM guardrail_recommendations AS r
	LEFT JOIN (
		SELECT recommendation_id, sum(held)::bigint AS live_held, sum(broken)::bigint AS live_broken,
		       min(first_at) AS live_first_at, max(last_at) AS live_last_at,
		       array_agg(concat_ws('|', rule_key, held, broken,
		                           (extract(epoch FROM first_at) * 1000)::bigint, (extract(epoch FROM last_at) * 1000)::bigint))
		           FILTER (WHERE rule_key <> '') AS live_by_rule
		FROM (
			SELECT recommendation_id, rule_key, sum(held)::bigint AS held, sum(broken)::bigint AS broken,
			       min(first_at) AS first_at, max(last_at) AS last_at
			FROM recommendation_live_counts GROUP BY recommendation_id, rule_key
		) AS per_rule
		GROUP BY recommendation_id
	) AS lc ON lc.recommendation_id = r.recommendation_id
	LEFT JOIN transactions AS t ON t.transaction_id = r.transaction_id`

func scanRecommendation(s rowScanner) (repo.Recommendation, error) {
	var (
		rec                                 repo.Recommendation
		scope, kind, state, transform, spec string
		rules, appliedRules                 string
		rank, observed, held, distinct, n   int32
		examples                            string
		liveHeld, liveBroken                int64
		liveFirst, liveLast                 time.Time
		liveByRule                          []string
		service, namespace, operation, tkin string
	)
	if err := s.Scan(
		&scope, &rec.ScopeKey, &rec.TransactionID, &rec.ID, &kind, &state, &rec.Stale, &rank,
		&spec, &transform, &rec.Transport, &rules, &appliedRules, &observed, &held, &distinct, &n, &examples,
		&rec.MinedAt, &rec.CreatedAt, &rec.UpdatedAt,
		&liveHeld, &liveBroken, &liveFirst, &liveLast, &liveByRule,
		&service, &namespace, &operation, &tkin,
	); err != nil {
		return repo.Recommendation{}, err
	}
	rec.Scope = repo.PolicyScope(scope)
	rec.Kind = repo.RecommendationKind(kind)
	rec.State = repo.NormalizeRecommendationState(repo.RecommendationState(state))
	rec.Rank = int(rank)
	rec.Transform = transform
	rec.Observed, rec.Held, rec.Distinct, rec.SampleCount = int(observed), int(held), int(distinct), int(n)
	if spec != "" {
		if err := json.Unmarshal([]byte(spec), &rec.Spec); err != nil {
			return repo.Recommendation{}, fmt.Errorf("decode recommendation spec %s: %w", rec.ID, err)
		}
	}
	if examples != "" {
		if err := json.Unmarshal([]byte(examples), &rec.Examples); err != nil {
			return repo.Recommendation{}, fmt.Errorf("decode recommendation examples %s: %w", rec.ID, err)
		}
	}
	if rules != "" {
		if err := json.Unmarshal([]byte(rules), &rec.Rules); err != nil {
			return repo.Recommendation{}, fmt.Errorf("decode recommendation rules %s: %w", rec.ID, err)
		}
	}
	if appliedRules != "" {
		if err := json.Unmarshal([]byte(appliedRules), &rec.AppliedRules); err != nil {
			return repo.Recommendation{}, fmt.Errorf("decode recommendation applied rules %s: %w", rec.ID, err)
		}
	}
	rec.LiveHeld, rec.LiveBroken = liveHeld, liveBroken
	if liveHeld+liveBroken > 0 {
		rec.LiveSince, rec.LiveLast = liveFirst, liveLast
	}
	attachRuleLiveCounts(rec.Rules, liveByRule)
	rec.Service, rec.Namespace, rec.Operation, rec.TxKind = service, namespace, operation, repo.TransactionKind(tkin)
	return rec, nil
}

// attachRuleLiveCounts spreads the aggregated per-rule counters onto the
// recommendation's rules. Counters for a rule no longer on the card are dropped.
func attachRuleLiveCounts(rules []repo.RecommendationRule, encoded []string) {
	if len(rules) == 0 || len(encoded) == 0 {
		return
	}
	for _, line := range encoded {
		parts := strings.Split(line, liveByRuleSep)
		if len(parts) != 5 {
			continue
		}
		for i := range rules {
			if string(rules[i].Rule) != parts[0] {
				continue
			}
			held, _ := strconv.ParseInt(parts[1], 10, 64)
			broken, _ := strconv.ParseInt(parts[2], 10, 64)
			first, _ := strconv.ParseInt(parts[3], 10, 64)
			last, _ := strconv.ParseInt(parts[4], 10, 64)
			rules[i].LiveChecked = held + broken
			rules[i].LiveViolated = broken
			if held+broken > 0 {
				rules[i].LiveSince = time.UnixMilli(first).UTC()
				rules[i].LiveLast = time.UnixMilli(last).UTC()
			}
		}
	}
}

// ListRecommendations returns recommendations matching filter, best rank first
// within a transaction, most recently mined transactions first.
func (r *Repo) ListRecommendations(ctx context.Context, filter repo.RecommendationFilter) ([]repo.Recommendation, error) {
	var (
		where []string
		a     args
	)
	if !filter.IncludeStale {
		where = append(where, "NOT r.stale")
	}
	if filter.Kind != "" {
		where = append(where, "r.kind = "+a.add(string(filter.Kind)))
	}
	if filter.Scope != "" {
		where = append(where, "r.scope_type = "+a.add(string(filter.Scope)))
	}
	if filter.State != "" {
		where = append(where, "r.state = "+a.add(string(filter.State)))
	}
	if filter.TransactionID != 0 {
		where = append(where, "r.transaction_id = "+a.add(filter.TransactionID))
	}
	if filter.Namespace != "" {
		where = append(where, recommendationNamespaceExpr+" = "+a.add(filter.Namespace))
	}
	if filter.Service != "" {
		where = append(where, recommendationServiceExpr+" = "+a.add(filter.Service))
	}
	q := `SELECT ` + recommendationCols + recommendationFrom
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY r.mined_at DESC, r.transaction_id, r.rank ASC"
	return queryRows(ctx, r.pool, "list recommendations", q, scanRecommendation, a...)
}

// GetRecommendation returns one recommendation by id.
func (r *Repo) GetRecommendation(ctx context.Context, id string) (repo.Recommendation, bool, error) {
	return queryRow(ctx, r.pool, "get recommendation",
		`SELECT `+recommendationCols+recommendationFrom+` WHERE r.recommendation_id = $1 LIMIT 1`,
		scanRecommendation, id)
}

// SetRecommendationState changes one recommendation's state in place.
func (r *Repo) SetRecommendationState(ctx context.Context, id string, state repo.RecommendationState) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE guardrail_recommendations SET state = $2, updated_at = $3 WHERE recommendation_id = $1`,
		id, string(repo.NormalizeRecommendationState(state)), time.Now())
	if err != nil {
		return fmt.Errorf("set recommendation state %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("recommendation %s not found", id)
	}
	return nil
}

// AddRecommendationLiveCounts adds one increment per delta to the
// recommendation's live counters.
func (r *Repo) AddRecommendationLiveCounts(ctx context.Context, deltas []repo.RecommendationLiveDelta) error {
	if len(deltas) == 0 {
		return nil
	}
	type keyed struct {
		key  string
		args []any
	}
	rows := make([]keyed, 0, len(deltas))
	for _, d := range deltas {
		scope, scopeKey := repo.NormalizeRecommendationScope(d.Scope, d.ScopeKey, d.TransactionID)
		rows = append(rows, keyed{
			key:  string(scope) + "\x00" + scopeKey + "\x00" + d.ID + "\x00" + string(d.Rule),
			args: []any{string(scope), scopeKey, d.ID, string(d.Rule), d.TransactionID, int64(d.Held), int64(d.Broken), d.FirstAt, d.LastAt},
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].key < rows[j].key })
	batch := make([][]any, len(rows))
	for i := range rows {
		batch[i] = rows[i].args
	}
	err := execBatch(ctx, r.pool,
		`INSERT INTO recommendation_live_counts AS c
			(scope_type, scope_key, recommendation_id, rule_key, transaction_id, held, broken, first_at, last_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 ON CONFLICT (scope_type, scope_key, recommendation_id, rule_key) DO UPDATE SET
			held = c.held + EXCLUDED.held,
			broken = c.broken + EXCLUDED.broken,
			first_at = LEAST(c.first_at, EXCLUDED.first_at),
			last_at = GREATEST(c.last_at, EXCLUDED.last_at)`,
		batch)
	if err != nil {
		return fmt.Errorf("add recommendation live counts: %w", err)
	}
	return nil
}

// DeleteRecommendations removes one transaction's (or, for 0, every)
// recommendation rows, live counters, and mining run.
func (r *Repo) DeleteRecommendations(ctx context.Context, transactionID int64) error {
	tables := []string{"guardrail_recommendations", "recommendation_live_counts", "recommendation_mining_runs"}
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		for _, tbl := range tables {
			var err error
			if transactionID == 0 {
				_, err = tx.Exec(ctx, "DELETE FROM "+tbl)
			} else {
				_, err = tx.Exec(ctx, "DELETE FROM "+tbl+" WHERE transaction_id = $1", transactionID)
			}
			if err != nil {
				return fmt.Errorf("%s: %w", tbl, err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("delete recommendations for %d: %w", transactionID, err)
	}
	return nil
}

// ListRecommendationMiningCandidates finds finished-learning transactions
// (every class promoted or saturated) with relation samples that still need
// mining: no run yet, or a run that saw fewer samples than are stored now.
func (r *Repo) ListRecommendationMiningCandidates(ctx context.Context, scoredClasses int, limit int) ([]repo.RecommendationMiningCandidate, error) {
	if limit <= 0 {
		limit = 100
	}
	return queryRows(ctx, r.pool, "list recommendation mining candidates", `
		WITH promoted AS (
			SELECT transaction_id
			FROM baseline_meta
			GROUP BY transaction_id
			HAVING bool_and(promoted OR saturated) AND count(*) >= $1
		),
		samples AS (
			SELECT transaction_id, count(DISTINCT trace_id)::int AS n
			FROM trace_observations
			WHERE sample_reason = $2 AND transaction_id IN (SELECT transaction_id FROM promoted)
			GROUP BY transaction_id
		)
		SELECT s.transaction_id, s.n
		FROM samples AS s
		LEFT JOIN recommendation_mining_runs AS m ON m.transaction_id = s.transaction_id
		WHERE m.transaction_id IS NULL
		   OR m.sample_count < s.n
		ORDER BY s.transaction_id
		LIMIT $3`,
		func(s rowScanner) (repo.RecommendationMiningCandidate, error) {
			var (
				c repo.RecommendationMiningCandidate
				n int32
			)
			if err := s.Scan(&c.TransactionID, &n); err != nil {
				return repo.RecommendationMiningCandidate{}, err
			}
			c.SampleCount = int(n)
			return c, nil
		},
		scoredClasses, repo.RelationSampleReason, limit)
}

// RecordRecommendationMiningRun writes the claim or outcome for one transaction.
func (r *Repo) RecordRecommendationMiningRun(ctx context.Context, run repo.RecommendationMiningRun) error {
	if _, err := r.pool.Exec(ctx,
		`INSERT INTO recommendation_mining_runs (transaction_id, claimed_at, mined_at, sample_count, candidates, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (transaction_id) DO UPDATE SET
			claimed_at = EXCLUDED.claimed_at, mined_at = EXCLUDED.mined_at, sample_count = EXCLUDED.sample_count,
			candidates = EXCLUDED.candidates, updated_at = EXCLUDED.updated_at`,
		run.TransactionID, run.ClaimedAt, orEpoch(run.MinedAt), int32(run.SampleCount), int32(run.Candidates), time.Now()); err != nil {
		return fmt.Errorf("record recommendation mining run: %w", err)
	}
	return nil
}
