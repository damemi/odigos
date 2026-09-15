package clickhouse

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// UpsertRecommendations writes one mining run's rows. ReplacingMergeTree keyed
// by (scope_type, scope_key, recommendation_id) with updated_at as version
// means the newest write wins per recommendation.
func (r *Repo) UpsertRecommendations(ctx context.Context, recs []repo.Recommendation) error {
	if len(recs) == 0 {
		return nil
	}
	now := time.Now()
	err := r.insert(ctx,
		`INSERT INTO guardrail_recommendations (
			scope_type, scope_key, transaction_id, recommendation_id, kind, state, stale, rank,
			spec, transform, transport, observed, held, distinct_values, sample_count, examples,
			mined_at, created_at, updated_at)`,
		func(b driver.Batch) error {
			for _, rec := range recs {
				spec, err := json.Marshal(rec.Spec)
				if err != nil {
					return fmt.Errorf("encode spec %s: %w", rec.ID, err)
				}
				examples, err := json.Marshal(nonNilExamples(rec.Examples))
				if err != nil {
					return fmt.Errorf("encode examples %s: %w", rec.ID, err)
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
				if err := b.Append(
					string(scope), scopeKey,
					rec.TransactionID, rec.ID, string(rec.Kind), string(repo.NormalizeRecommendationState(rec.State)),
					boolToUint8(rec.Stale), int32(rec.Rank),
					string(spec), rec.Transform, boolToUint8(rec.Transport), int32(rec.Observed), int32(rec.Held), int32(rec.Distinct), int32(rec.SampleCount), string(examples),
					minedAt, createdAt, now,
				); err != nil {
					return err
				}
			}
			return nil
		})
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

func boolToUint8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

// recommendationCols is the joined row shape shared by list and get: the
// recommendation, its summed live counters, and the transaction identity.
const recommendationCols = `
	r.scope_type, r.scope_key, r.transaction_id, r.recommendation_id, r.kind, r.state, r.stale, r.rank,
	r.spec, r.transform, r.transport, r.observed, r.held, r.distinct_values, r.sample_count, r.examples,
	r.mined_at, r.created_at, r.updated_at,
	lc.live_held, lc.live_broken, lc.live_first_at, lc.live_last_at,
	t.service, t.namespace, t.operation, t.tx_kind`

const recommendationFrom = `
	FROM guardrail_recommendations AS r FINAL
	LEFT JOIN (
		SELECT recommendation_id, sum(held) AS live_held, sum(broken) AS live_broken,
		       min(first_at) AS live_first_at, max(last_at) AS live_last_at
		FROM recommendation_live_counts GROUP BY recommendation_id
	) AS lc ON lc.recommendation_id = r.recommendation_id
	LEFT JOIN (
		SELECT transaction_id, service, namespace, operation, kind AS tx_kind
		FROM transactions FINAL
		WHERE transaction_id IN (SELECT transaction_id FROM guardrail_recommendations)
	) AS t ON t.transaction_id = r.transaction_id`

func scanRecommendation(s rowScanner) (repo.Recommendation, error) {
	var (
		rec                                 repo.Recommendation
		scope, kind, state, transform, spec string
		stale, transport                    uint8
		rank, observed, held, distinct, n   int32
		examples                            string
		liveHeld, liveBroken                uint64
		liveFirst, liveLast                 time.Time
		service, namespace, operation, tkin string
	)
	if err := s.Scan(
		&scope, &rec.ScopeKey, &rec.TransactionID, &rec.ID, &kind, &state, &stale, &rank,
		&spec, &transform, &transport, &observed, &held, &distinct, &n, &examples,
		&rec.MinedAt, &rec.CreatedAt, &rec.UpdatedAt,
		&liveHeld, &liveBroken, &liveFirst, &liveLast,
		&service, &namespace, &operation, &tkin,
	); err != nil {
		return repo.Recommendation{}, err
	}
	rec.Scope = repo.PolicyScope(scope)
	rec.Kind = repo.RecommendationKind(kind)
	rec.State = repo.NormalizeRecommendationState(repo.RecommendationState(state))
	rec.Stale = stale != 0
	rec.Rank = int(rank)
	rec.Transform = transform
	rec.Transport = transport != 0
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
	rec.LiveHeld, rec.LiveBroken = int64(liveHeld), int64(liveBroken)
	// A LEFT JOIN miss yields the zero DateTime, which is the epoch, not "never".
	if liveHeld+liveBroken > 0 {
		rec.LiveSince, rec.LiveLast = liveFirst, liveLast
	}
	rec.Service, rec.Namespace, rec.Operation, rec.TxKind = service, namespace, operation, repo.TransactionKind(tkin)
	return rec, nil
}

// ListRecommendations returns recommendations matching filter, best rank first
// within a transaction, most recently mined transactions first.
func (r *Repo) ListRecommendations(ctx context.Context, filter repo.RecommendationFilter) ([]repo.Recommendation, error) {
	var (
		where []string
		args  []any
	)
	if !filter.IncludeStale {
		where = append(where, "r.stale = 0")
	}
	if filter.Kind != "" {
		where = append(where, "r.kind = ?")
		args = append(args, string(filter.Kind))
	}
	if filter.State != "" {
		where = append(where, "r.state = ?")
		args = append(args, string(filter.State))
	}
	if filter.TransactionID != 0 {
		where = append(where, "r.transaction_id = ?")
		args = append(args, filter.TransactionID)
	}
	if filter.Namespace != "" {
		where = append(where, "t.namespace = ?")
		args = append(args, filter.Namespace)
	}
	if filter.Service != "" {
		where = append(where, "t.service = ?")
		args = append(args, filter.Service)
	}
	q := `SELECT ` + recommendationCols + recommendationFrom
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY r.mined_at DESC, r.transaction_id, r.rank ASC"
	return queryRows(ctx, r.conn, "list recommendations", q, scanRecommendation, args...)
}

// GetRecommendation returns one recommendation by id.
func (r *Repo) GetRecommendation(ctx context.Context, id string) (repo.Recommendation, bool, error) {
	return queryRow(ctx, r.conn, "get recommendation",
		`SELECT `+recommendationCols+recommendationFrom+` WHERE r.recommendation_id = ? LIMIT 1`,
		scanRecommendation, id)
}

// SetRecommendationState rewrites the row with the new state. Read-modify-write
// is fine here: decisions are rare operator clicks, and the miner carries the
// state over on its own rewrites.
func (r *Repo) SetRecommendationState(ctx context.Context, id string, state repo.RecommendationState) error {
	rec, ok, err := r.GetRecommendation(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("recommendation %s not found", id)
	}
	rec.State = repo.NormalizeRecommendationState(state)
	return r.UpsertRecommendations(ctx, []repo.Recommendation{rec})
}

// AddRecommendationLiveCounts appends one increment row per delta; the
// AggregatingMergeTree sums them in the background.
func (r *Repo) AddRecommendationLiveCounts(ctx context.Context, deltas []repo.RecommendationLiveDelta) error {
	if len(deltas) == 0 {
		return nil
	}
	err := r.insert(ctx,
		`INSERT INTO recommendation_live_counts (scope_type, scope_key, transaction_id, recommendation_id, held, broken, first_at, last_at)`,
		func(b driver.Batch) error {
			for _, d := range deltas {
				scope, scopeKey := repo.NormalizeRecommendationScope(d.Scope, d.ScopeKey, d.TransactionID)
				if err := b.Append(string(scope), scopeKey, d.TransactionID, d.ID, uint64(d.Held), uint64(d.Broken), d.FirstAt, d.LastAt); err != nil {
					return err
				}
			}
			return nil
		})
	if err != nil {
		return fmt.Errorf("add recommendation live counts: %w", err)
	}
	return nil
}

// DeleteRecommendations removes one transaction's (or, for 0, every)
// recommendation rows, live counters, and mining run.
func (r *Repo) DeleteRecommendations(ctx context.Context, transactionID int64) error {
	sctx := syncMutationCtx(ctx)
	where, args := "1", []any{}
	if transactionID != 0 {
		where, args = "transaction_id = ?", []any{transactionID}
	}
	for _, tbl := range []string{"guardrail_recommendations", "recommendation_live_counts", "recommendation_mining_runs"} {
		if err := r.conn.Exec(sctx, fmt.Sprintf("ALTER TABLE %s DELETE WHERE %s", tbl, where), args...); err != nil {
			return fmt.Errorf("delete %s for %d: %w", tbl, transactionID, err)
		}
	}
	return nil
}

// ListRecommendationMiningCandidates finds promoted transactions with relation
// samples that still need mining. A transaction qualifies when it has no run,
// its run saw fewer samples than are stored now, or its in-progress claim is
// older than staleClaim (the claiming replica died).
func (r *Repo) ListRecommendationMiningCandidates(ctx context.Context, scoredClasses int, staleClaim time.Duration, limit int) ([]repo.RecommendationMiningCandidate, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.conn.Query(ctx, `
		WITH promoted AS (
			SELECT transaction_id
			FROM (SELECT transaction_id, class, max(promoted) AS p FROM baseline_meta GROUP BY transaction_id, class)
			GROUP BY transaction_id
			HAVING min(p) = 1 AND count() >= ?
		),
		samples AS (
			SELECT transaction_id, toInt32(uniqExact(trace_id)) AS n
			FROM trace_observations
			WHERE sample_reason = ? AND transaction_id IN (SELECT transaction_id FROM promoted)
			GROUP BY transaction_id
		),
		runs AS (
			SELECT transaction_id, sample_count, mined_at, claimed_at FROM recommendation_mining_runs FINAL
		)
		SELECT s.transaction_id, s.n
		FROM samples AS s
		LEFT JOIN runs AS m ON m.transaction_id = s.transaction_id
		WHERE m.transaction_id = 0
		   OR (toUnixTimestamp64Milli(m.mined_at) != 0 AND m.sample_count < s.n)
		   OR (toUnixTimestamp64Milli(m.mined_at) = 0 AND m.claimed_at < now64(3) - INTERVAL ? SECOND)
		ORDER BY s.transaction_id
		LIMIT ?`,
		uint64(scoredClasses), repo.RelationSampleReason, uint64(staleClaim/time.Second), uint64(limit))
	if err != nil {
		return nil, fmt.Errorf("list recommendation mining candidates: %w", err)
	}
	defer rows.Close()
	var out []repo.RecommendationMiningCandidate
	for rows.Next() {
		var (
			id int64
			n  int32
		)
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("list recommendation mining candidates: %w", err)
		}
		out = append(out, repo.RecommendationMiningCandidate{TransactionID: id, SampleCount: int(n)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list recommendation mining candidates: %w", err)
	}
	return out, nil
}

// RecordRecommendationMiningRun writes the claim or outcome for one transaction.
func (r *Repo) RecordRecommendationMiningRun(ctx context.Context, run repo.RecommendationMiningRun) error {
	minedAt := run.MinedAt
	if minedAt.IsZero() {
		minedAt = time.Unix(0, 0)
	}
	err := r.insert(ctx,
		`INSERT INTO recommendation_mining_runs (transaction_id, claimed_at, mined_at, sample_count, candidates, updated_at)`,
		func(b driver.Batch) error {
			return b.Append(run.TransactionID, run.ClaimedAt, minedAt, int32(run.SampleCount), int32(run.Candidates), time.Now())
		})
	if err != nil {
		return fmt.Errorf("record recommendation mining run: %w", err)
	}
	return nil
}
