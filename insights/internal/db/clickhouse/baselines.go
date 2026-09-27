package clickhouse

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// baselineKey identifies one (transaction, class) while assembling reads.
type baselineKey struct {
	txID  int64
	class string
}

// GetBaselineForTransaction returns every class row for one transaction in a
// single query. The engine's hot scoring path calls this once per trace on a
// cache miss.
func (r *Repo) GetBaselineForTransaction(ctx context.Context, transactionID int64) ([]repo.BaselineClass, error) {
	return r.baselinesFor(ctx, []int64{transactionID})
}

// settledClassesSQL collapses baseline_meta to one row per (transaction, class)
// with the class's settled flag: promoted or saturated (both max()-aggregated
// across replicas). Every "finished learning" roll-up derives from it so a
// saturated class counts as done, never as still learning. The aliases must
// not shadow the source columns: ClickHouse resolves `max(saturated) AS
// saturated` to the alias and rejects the nested aggregate.
const settledClassesSQL = `SELECT transaction_id, class,
			greatest(max(promoted), max(saturated)) AS is_settled, max(saturated) AS is_saturated
		 FROM baseline_meta
		 GROUP BY transaction_id, class`

// ListBaselinePromotion tallies settled-vs-total classes per transaction in a
// single aggregate query (see settledClassesSQL). Cold path; backs overview
// metrics without the per-transaction reads baselinesFor would need.
func (r *Repo) ListBaselinePromotion(ctx context.Context) ([]repo.BaselinePromotion, error) {
	return queryRows(ctx, r.conn, "list baseline promotion",
		`SELECT transaction_id, countIf(is_settled = 1) AS promoted, count() AS total
		 FROM (`+settledClassesSQL+`)
		 GROUP BY transaction_id`,
		func(s rowScanner) (repo.BaselinePromotion, error) {
			var (
				p               repo.BaselinePromotion
				promoted, total uint64
			)
			if err := s.Scan(&p.TransactionID, &promoted, &total); err != nil {
				return repo.BaselinePromotion{}, err
			}
			p.Promoted, p.Total = int(promoted), int(total)
			return p, nil
		})
}

// ListUnpromotedBaselines returns unpromoted classes that may be eligible for
// time-based promotion, prefiltered by filter. The engine re-evaluates each row
// with shouldPromote before promoting.
func (r *Repo) ListUnpromotedBaselines(ctx context.Context, filter repo.UnpromotedBaselineFilter) ([]repo.UnpromotedBaselineCandidate, error) {
	if !filter.MinDurationEnabled && !filter.StableDurationEnabled {
		return nil, nil
	}
	var timeConds []string
	var args []any
	if filter.MinDurationEnabled {
		timeConds = append(timeConds, "min(learning_started_at) <= ?")
		args = append(args, filter.MinDurationCutoff)
	}
	if filter.StableDurationEnabled {
		timeConds = append(timeConds, "(max(observation_count_at_last_change) > 0 AND max(last_changed_at) <= ?)")
		args = append(args, filter.StableDurationCutoff)
	}
	query := fmt.Sprintf(
		`SELECT transaction_id, class,
			sum(observation_count), min(learning_started_at),
			max(last_changed_at), max(observation_count_at_last_change)
		 FROM baseline_meta
		 GROUP BY transaction_id, class
		 HAVING max(promoted) = 0 AND max(saturated) = 0 AND (%s)
		 ORDER BY transaction_id, class`, joinOr(timeConds))
	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}
	return queryRows(ctx, r.conn, "list unpromoted baselines", query, func(s rowScanner) (repo.UnpromotedBaselineCandidate, error) {
		var (
			row   repo.UnpromotedBaselineCandidate
			class string
			count int64
			start time.Time
			last  time.Time
			at    int64
		)
		if err := s.Scan(&row.TransactionID, &class, &count, &start, &last, &at); err != nil {
			return repo.UnpromotedBaselineCandidate{}, err
		}
		row.Class = repo.DeviationClass(class)
		row.ObservationCount = count
		row.LearningStartedAt = start
		row.LastChangedAt = normalizeEpoch(last)
		row.ObservationCountAtLastChange = at
		return row, nil
	}, args...)
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
// three storage tables, re-aggregating at read time (sum/max/min, group members)
// so the result is correct even before ClickHouse collapses background parts.
// Cold path - never per trace.
func (r *Repo) baselinesFor(ctx context.Context, transactionIDs []int64) ([]repo.BaselineClass, error) {
	if len(transactionIDs) == 0 {
		return nil, nil
	}
	in := intInClause(transactionIDs)

	// Per-class scalars drive the result: a class exists iff it has a meta row.
	type metaRow struct {
		bc      repo.BaselineClass
		hasData bool
	}
	byKey := map[baselineKey]*metaRow{}
	order := []baselineKey{}

	metaRows, err := r.conn.Query(ctx, fmt.Sprintf(
		`SELECT transaction_id, class,
			sum(observation_count), max(promoted), max(saturated), max(data_schema_version), min(learning_started_at),
			max(last_changed_at), max(observation_count_at_last_change)
		 FROM baseline_meta WHERE transaction_id IN %s
		 GROUP BY transaction_id, class
		 ORDER BY transaction_id, class`, in))
	if err != nil {
		return nil, fmt.Errorf("get baseline meta: %w", err)
	}
	for metaRows.Next() {
		var (
			txID          int64
			class         string
			count         int64
			promo         uint8
			saturated     uint8
			version       int32
			started       time.Time
			lastChanged   time.Time
			countAtChange int64
		)
		if err := metaRows.Scan(&txID, &class, &count, &promo, &saturated, &version, &started, &lastChanged, &countAtChange); err != nil {
			metaRows.Close()
			return nil, fmt.Errorf("scan baseline meta: %w", err)
		}
		k := baselineKey{txID: txID, class: class}
		byKey[k] = &metaRow{bc: repo.BaselineClass{
			TransactionID:                txID,
			Class:                        repo.DeviationClass(class),
			ObservationCount:             count,
			Promoted:                     promo == 1,
			Saturated:                    saturated == 1,
			DataSchemaVersion:            int(version),
			LearningStartedAt:            started,
			LastChangedAt:                normalizeEpoch(lastChanged),
			ObservationCountAtLastChange: countAtChange,
		}}
		order = append(order, k)
	}
	if err := metaRows.Err(); err != nil {
		metaRows.Close()
		return nil, err
	}
	metaRows.Close()

	// Collect set members and additive counters per (transaction, class).
	members := map[baselineKey][]string{}
	memberRows, err := r.conn.Query(ctx, fmt.Sprintf(
		`SELECT transaction_id, class, member FROM baseline_set_members
		 WHERE transaction_id IN %s GROUP BY transaction_id, class, member`, in))
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
	if err := memberRows.Err(); err != nil {
		memberRows.Close()
		return nil, err
	}
	memberRows.Close()

	counters := map[baselineKey][]repo.Counter{}
	counterRows, err := r.conn.Query(ctx, fmt.Sprintf(
		`SELECT transaction_id, class, bucket, sum(count) FROM baseline_counters
		 WHERE transaction_id IN %s GROUP BY transaction_id, class, bucket`, in))
	if err != nil {
		return nil, fmt.Errorf("get baseline counters: %w", err)
	}
	for counterRows.Next() {
		var txID int64
		var class, bucket string
		var count int64
		if err := counterRows.Scan(&txID, &class, &bucket, &count); err != nil {
			counterRows.Close()
			return nil, fmt.Errorf("scan baseline counter: %w", err)
		}
		k := baselineKey{txID: txID, class: class}
		counters[k] = append(counters[k], repo.Counter{Bucket: bucket, Count: count})
	}
	if err := counterRows.Err(); err != nil {
		counterRows.Close()
		return nil, err
	}
	counterRows.Close()

	out := make([]repo.BaselineClass, 0, len(order))
	for _, k := range order {
		m := byKey[k]
		data, err := r.codec.Decode(m.bc.Class, members[k], counters[k])
		if err != nil {
			return nil, fmt.Errorf("decode baseline %d/%s: %w", k.txID, k.class, err)
		}
		m.bc.Data = data
		out = append(out, m.bc)
	}
	return out, nil
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

// normalizeEpoch maps the "never grew" sentinel (the Unix epoch we store when
// LastChangedAt is unset, since Go's zero time is out of DateTime64 range) back
// to Go's zero time, so callers can test it with IsZero uniformly.
func normalizeEpoch(t time.Time) time.Time {
	if t.Unix() <= 0 {
		return time.Time{}
	}
	return t
}

// ApplyBaselineDeltas appends each delta's contribution to the aggregate tables
// (no read, no lock) and returns the post-merge authoritative rows so the
// write-behind flusher can adopt them back into memory. The merge argument is
// unused: ClickHouse does the merge in its background merges and at read time,
// which is the whole point of this backend.
//
// Idempotency: set members are idempotent by construction; the additive
// counters/observation_count can double-count if the same flush is retried,
// which at worst promotes a class marginally early - accepted in this phase
// (see "Additive counters can be over-counted if a flush is retried" in
// TECH_DEBT.md). Runs on the flush timer, never per trace.
func (r *Repo) ApplyBaselineDeltas(ctx context.Context, deltas []repo.BaselineDelta, _ repo.BaselineMerger) ([]repo.BaselineClass, error) {
	if len(deltas) == 0 {
		return nil, nil
	}

	memberBatch, err := r.conn.PrepareBatch(ctx, "INSERT INTO baseline_set_members (transaction_id, class, member)")
	if err != nil {
		return nil, fmt.Errorf("prepare members: %w", err)
	}
	counterBatch, err := r.conn.PrepareBatch(ctx, "INSERT INTO baseline_counters (transaction_id, class, bucket, count)")
	if err != nil {
		_ = memberBatch.Abort()
		return nil, fmt.Errorf("prepare counters: %w", err)
	}
	metaBatch, err := r.conn.PrepareBatch(ctx,
		"INSERT INTO baseline_meta (transaction_id, class, observation_count, promoted, saturated, data_schema_version, learning_started_at, last_changed_at, observation_count_at_last_change)")
	if err != nil {
		_ = memberBatch.Abort()
		_ = counterBatch.Abort()
		return nil, fmt.Errorf("prepare meta: %w", err)
	}

	memberN, counterN := 0, 0
	txnSeen := map[int64]struct{}{}
	for _, d := range deltas {
		classMembers, classCounters, err := r.codec.Encode(d.Class, d.Contribution)
		if err != nil {
			abortAll(memberBatch, counterBatch, metaBatch)
			return nil, fmt.Errorf("encode baseline %d/%s: %w", d.TransactionID, d.Class, err)
		}
		for _, m := range classMembers {
			if err := memberBatch.Append(d.TransactionID, string(d.Class), m); err != nil {
				abortAll(memberBatch, counterBatch, metaBatch)
				return nil, fmt.Errorf("append member: %w", err)
			}
			memberN++
		}
		for _, c := range classCounters {
			if err := counterBatch.Append(d.TransactionID, string(d.Class), c.Bucket, c.Count); err != nil {
				abortAll(memberBatch, counterBatch, metaBatch)
				return nil, fmt.Errorf("append counter: %w", err)
			}
			counterN++
		}

		schema := int32(d.DataSchemaVersion)
		if schema == 0 {
			schema = 1
		}
		started := d.LearningStartedAt
		if started.IsZero() {
			started = time.Now()
		}
		var promoted, saturated uint8
		if d.Promote {
			promoted = 1
		}
		if d.Saturate {
			saturated = 1
		}
		// last_changed_at is max()-aggregated: send the epoch (not Go's zero
		// year-1 time, which is out of DateTime64 range) when the baseline has
		// never grown, so a real change always wins the max.
		lastChanged := d.LastChangedAt
		if lastChanged.IsZero() {
			lastChanged = time.Unix(0, 0)
		}
		if err := metaBatch.Append(d.TransactionID, string(d.Class), d.CountDelta, promoted, saturated, schema, started, lastChanged, d.ObservationCountAtLastChange); err != nil {
			abortAll(memberBatch, counterBatch, metaBatch)
			return nil, fmt.Errorf("append meta: %w", err)
		}
		txnSeen[d.TransactionID] = struct{}{}
	}

	// Send meta always (one row per delta); send member/counter only if non-empty.
	if memberN > 0 {
		if err := memberBatch.Send(); err != nil {
			_ = counterBatch.Abort()
			_ = metaBatch.Abort()
			return nil, fmt.Errorf("send members: %w", err)
		}
	} else {
		_ = memberBatch.Abort()
	}
	if counterN > 0 {
		if err := counterBatch.Send(); err != nil {
			_ = metaBatch.Abort()
			return nil, fmt.Errorf("send counters: %w", err)
		}
	} else {
		_ = counterBatch.Abort()
	}
	if err := metaBatch.Send(); err != nil {
		return nil, fmt.Errorf("send meta: %w", err)
	}

	ids := make([]int64, 0, len(txnSeen))
	for id := range txnSeen {
		ids = append(ids, id)
	}
	return r.baselinesFor(ctx, ids)
}

// PromoteBaselineClass flips a class's promoted flag on. Called by the
// promotion gate (and the debug UI) once a class clears the observation/age
// thresholds.
func (r *Repo) PromoteBaselineClass(ctx context.Context, transactionID int64, class repo.DeviationClass) error {
	// promoted is max()-aggregated, so appending a promoted=1 meta row flips it
	// on without touching the summed count or the min learning_started_at.
	err := r.insert(ctx,
		"INSERT INTO baseline_meta (transaction_id, class, observation_count, promoted, data_schema_version, learning_started_at)",
		func(b driver.Batch) error {
			return b.Append(transactionID, string(class), int64(0), uint8(1), int32(1), time.Now())
		})
	if err != nil {
		return fmt.Errorf("promote baseline: %w", err)
	}
	return nil
}

// baselineTables are the three storage tables a baseline is spread across; a
// reset must clear the class from all of them.
var baselineTables = []string{"baseline_meta", "baseline_set_members", "baseline_counters"}

// syncMutationCtx forces ALTER ... DELETE to run synchronously: Exec returns
// only once the mutation has actually removed the rows. Reset MUST be
// synchronous - the default async mutation returns immediately, and under live
// traffic the engine reloads the not-yet-deleted baseline and the write-behind
// flusher re-inserts it, silently resurrecting a "reset" transaction (and,
// worse, resurrecting a promoted class the engine then refuses to re-learn).
func syncMutationCtx(ctx context.Context) context.Context {
	return clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"mutations_sync": 2, // wait for the mutation to finish on all replicas
	}))
}

// ResetBaselineClass deletes one (transaction, class) from every baseline table
// via ClickHouse mutations. Runs synchronously (see syncMutationCtx); heavy but
// rare (operator "re-learn"), never on the hot path. The class re-seeds itself
// in its learning phase on the next trace, once the caller has evicted the
// in-memory entry.
func (r *Repo) ResetBaselineClass(ctx context.Context, transactionID int64, class repo.DeviationClass) error {
	sctx := syncMutationCtx(ctx)
	for _, tbl := range baselineTables {
		if err := r.conn.Exec(sctx,
			fmt.Sprintf("ALTER TABLE %s DELETE WHERE transaction_id = ? AND class = ?", tbl),
			transactionID, string(class)); err != nil {
			return fmt.Errorf("reset %s for %d/%s: %w", tbl, transactionID, class, err)
		}
	}
	return nil
}

// ResetTransactionBaselines deletes every class of a transaction from all
// baseline tables (the whole-transaction "re-learn"). Synchronous, cold path only.
func (r *Repo) ResetTransactionBaselines(ctx context.Context, transactionID int64) error {
	sctx := syncMutationCtx(ctx)
	for _, tbl := range baselineTables {
		if err := r.conn.Exec(sctx,
			fmt.Sprintf("ALTER TABLE %s DELETE WHERE transaction_id = ?", tbl),
			transactionID); err != nil {
			return fmt.Errorf("reset %s for %d: %w", tbl, transactionID, err)
		}
	}
	return nil
}
