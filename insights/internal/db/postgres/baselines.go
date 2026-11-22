package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// baselineKey identifies one (transaction, class) while assembling reads.
type baselineKey struct {
	txID  int64
	class string
}

// GetBaselineForTransaction returns every class row for one transaction. The
// engine calls this on a baseline cache miss.
func (r *Repo) GetBaselineForTransaction(ctx context.Context, transactionID int64) ([]repo.BaselineClass, error) {
	return r.baselinesFor(ctx, r.pool, []int64{transactionID})
}

// ListUnpromotedBaselines returns unpromoted classes that may be eligible for
// time-based promotion, prefiltered by filter. The engine re-evaluates each row
// with shouldPromote before promoting.
func (r *Repo) ListUnpromotedBaselines(ctx context.Context, filter repo.UnpromotedBaselineFilter) ([]repo.UnpromotedBaselineCandidate, error) {
	if !filter.MinDurationEnabled && !filter.StableDurationEnabled {
		return nil, nil
	}
	var (
		a         args
		timeConds []string
	)
	if filter.MinDurationEnabled {
		timeConds = append(timeConds, "learning_started_at <= "+a.add(filter.MinDurationCutoff))
	}
	if filter.StableDurationEnabled {
		timeConds = append(timeConds, "(observation_count_at_last_change > 0 AND last_changed_at <= "+a.add(filter.StableDurationCutoff)+")")
	}
	query := fmt.Sprintf(
		`SELECT transaction_id, class, observation_count, learning_started_at,
			last_changed_at, observation_count_at_last_change
		 FROM baseline_meta
		 WHERE NOT promoted AND NOT saturated AND (%s)
		 ORDER BY transaction_id, class`, joinOr(timeConds))
	if filter.Limit > 0 {
		query += " LIMIT " + a.add(filter.Limit)
	}
	return queryRows(ctx, r.pool, "list unpromoted baselines", query, func(s rowScanner) (repo.UnpromotedBaselineCandidate, error) {
		var (
			row   repo.UnpromotedBaselineCandidate
			class string
			last  time.Time
		)
		if err := s.Scan(&row.TransactionID, &class, &row.ObservationCount, &row.LearningStartedAt, &last, &row.ObservationCountAtLastChange); err != nil {
			return repo.UnpromotedBaselineCandidate{}, err
		}
		row.Class = repo.DeviationClass(class)
		row.LastChangedAt = normalizeEpoch(last)
		return row, nil
	}, a...)
}

func joinOr(conds []string) string {
	if len(conds) == 1 {
		return conds[0]
	}
	out := "(" + conds[0] + ")"
	for _, c := range conds[1:] {
		out += " OR (" + c + ")"
	}
	return out
}

// baselinesFor rebuilds baseline rows for one or more transactions from the
// three storage tables. Cold path - never per trace.
func (r *Repo) baselinesFor(ctx context.Context, q querier, transactionIDs []int64) ([]repo.BaselineClass, error) {
	if len(transactionIDs) == 0 {
		return nil, nil
	}

	metas, err := queryRows(ctx, q, "get baseline meta",
		`SELECT transaction_id, class, observation_count, promoted, saturated, data_schema_version,
			learning_started_at, last_changed_at, observation_count_at_last_change
		 FROM baseline_meta WHERE transaction_id = ANY($1)
		 ORDER BY transaction_id, class`,
		func(s rowScanner) (repo.BaselineClass, error) {
			var (
				bc          repo.BaselineClass
				class       string
				version     int32
				lastChanged time.Time
			)
			if err := s.Scan(&bc.TransactionID, &class, &bc.ObservationCount, &bc.Promoted, &bc.Saturated, &version,
				&bc.LearningStartedAt, &lastChanged, &bc.ObservationCountAtLastChange); err != nil {
				return repo.BaselineClass{}, err
			}
			bc.Class = repo.DeviationClass(class)
			bc.DataSchemaVersion = int(version)
			bc.LastChangedAt = normalizeEpoch(lastChanged)
			return bc, nil
		}, transactionIDs)
	if err != nil {
		return nil, err
	}
	if len(metas) == 0 {
		return nil, nil
	}

	members := map[baselineKey][]string{}
	memberRows, err := q.Query(ctx,
		`SELECT transaction_id, class, member FROM baseline_set_members WHERE transaction_id = ANY($1)`,
		transactionIDs)
	if err != nil {
		return nil, fmt.Errorf("get baseline members: %w", err)
	}
	for memberRows.Next() {
		var txID int64
		var class, member string
		if err := memberRows.Scan(&txID, &class, &member); err != nil {
			memberRows.Close()
			return nil, fmt.Errorf("scan baseline member: %w", err)
		}
		k := baselineKey{txID: txID, class: class}
		members[k] = append(members[k], member)
	}
	memberRows.Close()
	if err := memberRows.Err(); err != nil {
		return nil, fmt.Errorf("get baseline members: %w", err)
	}

	counters := map[baselineKey][]repo.Counter{}
	counterRows, err := q.Query(ctx,
		`SELECT transaction_id, class, bucket, count FROM baseline_counters WHERE transaction_id = ANY($1)`,
		transactionIDs)
	if err != nil {
		return nil, fmt.Errorf("get baseline counters: %w", err)
	}
	for counterRows.Next() {
		var txID, count int64
		var class, bucket string
		if err := counterRows.Scan(&txID, &class, &bucket, &count); err != nil {
			counterRows.Close()
			return nil, fmt.Errorf("scan baseline counter: %w", err)
		}
		k := baselineKey{txID: txID, class: class}
		counters[k] = append(counters[k], repo.Counter{Bucket: bucket, Count: count})
	}
	counterRows.Close()
	if err := counterRows.Err(); err != nil {
		return nil, fmt.Errorf("get baseline counters: %w", err)
	}

	for i := range metas {
		k := baselineKey{txID: metas[i].TransactionID, class: string(metas[i].Class)}
		data, err := r.codec.Decode(metas[i].Class, members[k], counters[k])
		if err != nil {
			return nil, fmt.Errorf("decode baseline %d/%s: %w", k.txID, k.class, err)
		}
		metas[i].Data = data
	}
	return metas, nil
}

// UpsertBaselineClass merges one class's baseline via a single delta apply.
// Cold path only (seeding and admin/UI writes); the hot path uses
// ApplyBaselineDeltas.
func (r *Repo) UpsertBaselineClass(ctx context.Context, bc repo.BaselineClass) error {
	_, err := r.ApplyBaselineDeltas(ctx, []repo.BaselineDelta{{
		TransactionID:                bc.TransactionID,
		Class:                        bc.Class,
		Contribution:                 bc.Data,
		CountDelta:                   bc.ObservationCount,
		Promote:                      bc.Promoted,
		Saturate:                     bc.Saturated,
		DataSchemaVersion:            bc.DataSchemaVersion,
		LearningStartedAt:            bc.LearningStartedAt,
		LastChangedAt:                bc.LastChangedAt,
		ObservationCountAtLastChange: bc.ObservationCountAtLastChange,
	}}, nil)
	return err
}

// ApplyBaselineDeltas merges each delta into the baseline tables in one
// transaction and returns the post-merge rows so the write-behind flusher can
// adopt them back into memory. The merge argument is unused: the upserts merge
// in SQL (set union, summed counters, min/max scalars), so replicas flushing
// the same class never overwrite each other.
//
// Idempotency: set members are idempotent by construction; the additive
// counters/observation_count can double-count if a committed flush is retried
// because its reply was lost (see TECH_DEBT.md). Runs on the flush timer,
// never per trace.
func (r *Repo) ApplyBaselineDeltas(ctx context.Context, deltas []repo.BaselineDelta, _ repo.BaselineMerger) ([]repo.BaselineClass, error) {
	if len(deltas) == 0 {
		return nil, nil
	}

	var (
		memTx                []int64
		memClass, memMember  []string
		cntTx, cntCount      []int64
		cntClass, cntBucket  []string
		metaTx, metaCount    []int64
		metaClass            []string
		metaPromo, metaSat   []bool
		metaSchema           []int32
		metaStarted, metaLCA []time.Time
		metaCountAtChange    []int64
	)
	txnSeen := map[int64]struct{}{}
	for _, d := range deltas {
		classMembers, classCounters, err := r.codec.Encode(d.Class, d.Contribution)
		if err != nil {
			return nil, fmt.Errorf("encode baseline %d/%s: %w", d.TransactionID, d.Class, err)
		}
		for _, m := range classMembers {
			memTx = append(memTx, d.TransactionID)
			memClass = append(memClass, string(d.Class))
			memMember = append(memMember, m)
		}
		for _, c := range classCounters {
			cntTx = append(cntTx, d.TransactionID)
			cntClass = append(cntClass, string(d.Class))
			cntBucket = append(cntBucket, c.Bucket)
			cntCount = append(cntCount, c.Count)
		}

		schema := int32(d.DataSchemaVersion)
		if schema == 0 {
			schema = 1
		}
		started := d.LearningStartedAt
		if started.IsZero() {
			started = time.Now()
		}
		metaTx = append(metaTx, d.TransactionID)
		metaClass = append(metaClass, string(d.Class))
		metaCount = append(metaCount, d.CountDelta)
		metaPromo = append(metaPromo, d.Promote)
		metaSat = append(metaSat, d.Saturate)
		metaSchema = append(metaSchema, schema)
		metaStarted = append(metaStarted, started)
		metaLCA = append(metaLCA, orEpoch(d.LastChangedAt))
		metaCountAtChange = append(metaCountAtChange, d.ObservationCountAtLastChange)
		txnSeen[d.TransactionID] = struct{}{}
	}

	ids := make([]int64, 0, len(txnSeen))
	for id := range txnSeen {
		ids = append(ids, id)
	}

	var out []repo.BaselineClass
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		// Rows are inserted in key order so replicas flushing overlapping
		// classes lock them in the same order instead of deadlocking.
		if len(memTx) > 0 {
			if _, err := tx.Exec(ctx,
				`INSERT INTO baseline_set_members (transaction_id, class, member)
				 SELECT tx, cl, m FROM unnest($1::bigint[], $2::text[], $3::text[]) AS u(tx, cl, m)
				 ORDER BY tx, cl, m
				 ON CONFLICT DO NOTHING`,
				memTx, memClass, memMember); err != nil {
				return fmt.Errorf("insert members: %w", err)
			}
		}
		if len(cntTx) > 0 {
			if _, err := tx.Exec(ctx,
				`INSERT INTO baseline_counters AS c (transaction_id, class, bucket, count)
				 SELECT tx, cl, b, sum(n) FROM unnest($1::bigint[], $2::text[], $3::text[], $4::bigint[]) AS u(tx, cl, b, n)
				 GROUP BY tx, cl, b
				 ORDER BY tx, cl, b
				 ON CONFLICT (transaction_id, class, bucket) DO UPDATE SET count = c.count + EXCLUDED.count`,
				cntTx, cntClass, cntBucket, cntCount); err != nil {
				return fmt.Errorf("upsert counters: %w", err)
			}
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO baseline_meta AS m (transaction_id, class, observation_count, promoted, saturated,
				data_schema_version, learning_started_at, last_changed_at, observation_count_at_last_change)
			 SELECT tx, cl, sum(n), bool_or(p), bool_or(s), max(v), min(st), max(lc), max(ac)
			 FROM unnest($1::bigint[], $2::text[], $3::bigint[], $4::bool[], $5::bool[], $6::int[],
				$7::timestamptz[], $8::timestamptz[], $9::bigint[]) AS u(tx, cl, n, p, s, v, st, lc, ac)
			 GROUP BY tx, cl
			 ORDER BY tx, cl
			 ON CONFLICT (transaction_id, class) DO UPDATE SET
				observation_count = m.observation_count + EXCLUDED.observation_count,
				promoted = m.promoted OR EXCLUDED.promoted,
				saturated = m.saturated OR EXCLUDED.saturated,
				data_schema_version = GREATEST(m.data_schema_version, EXCLUDED.data_schema_version),
				learning_started_at = LEAST(m.learning_started_at, EXCLUDED.learning_started_at),
				last_changed_at = GREATEST(m.last_changed_at, EXCLUDED.last_changed_at),
				observation_count_at_last_change = GREATEST(m.observation_count_at_last_change, EXCLUDED.observation_count_at_last_change)`,
			metaTx, metaClass, metaCount, metaPromo, metaSat, metaSchema, metaStarted, metaLCA, metaCountAtChange); err != nil {
			return fmt.Errorf("upsert meta: %w", err)
		}
		var err error
		out, err = r.baselinesFor(ctx, tx, ids)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("apply baseline deltas: %w", err)
	}
	return out, nil
}

// PromoteBaselineClass flips a class's promoted flag on. Called by the
// promotion gate once a class clears the observation/age thresholds.
func (r *Repo) PromoteBaselineClass(ctx context.Context, transactionID int64, class repo.DeviationClass) error {
	if _, err := r.pool.Exec(ctx,
		`INSERT INTO baseline_meta (transaction_id, class, promoted)
		 VALUES ($1, $2, true)
		 ON CONFLICT (transaction_id, class) DO UPDATE SET promoted = true`,
		transactionID, string(class)); err != nil {
		return fmt.Errorf("promote baseline: %w", err)
	}
	return nil
}

// ResetBaselineClass deletes one (transaction, class) from every baseline
// table. The class re-seeds itself in its learning phase on the next trace,
// once the caller has evicted the in-memory entry.
func (r *Repo) ResetBaselineClass(ctx context.Context, transactionID int64, class repo.DeviationClass) error {
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		for _, tbl := range baselineTables {
			if _, err := tx.Exec(ctx,
				"DELETE FROM "+tbl+" WHERE transaction_id = $1 AND class = $2",
				transactionID, string(class)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reset baseline %d/%s: %w", transactionID, class, err)
	}
	return nil
}

// ResetTransactionBaselines deletes every class of a transaction from all
// baseline tables (the whole-transaction "re-learn").
func (r *Repo) ResetTransactionBaselines(ctx context.Context, transactionID int64) error {
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		for _, tbl := range baselineTables {
			if _, err := tx.Exec(ctx, "DELETE FROM "+tbl+" WHERE transaction_id = $1", transactionID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reset baselines %d: %w", transactionID, err)
	}
	return nil
}

// baselineTables are the three tables a baseline is spread across; a reset
// must clear the class from all of them.
var baselineTables = []string{"baseline_meta", "baseline_set_members", "baseline_counters"}
