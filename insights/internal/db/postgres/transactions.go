package postgres

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// UpsertTransaction returns the row for (service, namespace, operation, kind,
// source identity), creating it if missing. Cold path / tests; the engine uses
// UpsertTransactions on the write-behind flush instead.
func (r *Repo) UpsertTransaction(ctx context.Context, service, namespace, operation string, kind repo.TransactionKind, sourceIdentity map[string]string) (repo.Transaction, error) {
	tx := repo.NewTransaction(service, namespace, operation, kind, sourceIdentity)
	if err := r.UpsertTransactions(ctx, []repo.Transaction{tx}); err != nil {
		return repo.Transaction{}, err
	}
	return tx, nil
}

// UpsertTransactions batch-writes transaction identity rows. Idempotent - the
// id is a hash of the identity, so a repeat overwrites the same row.
func (r *Repo) UpsertTransactions(ctx context.Context, txs []repo.Transaction) error {
	if len(txs) == 0 {
		return nil
	}
	sorted := append([]repo.Transaction(nil), txs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	rows := make([][]any, 0, len(sorted))
	for _, tx := range sorted {
		identity := tx.SourceIdentity
		if identity == nil {
			identity = map[string]string{}
		}
		rows = append(rows, []any{tx.ID, tx.Service, tx.Namespace, tx.Operation, string(tx.Kind), identity})
	}
	err := execBatch(ctx, r.pool,
		`INSERT INTO transactions (transaction_id, service, namespace, operation, kind, source_identity)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (transaction_id) DO UPDATE SET
			service = EXCLUDED.service, namespace = EXCLUDED.namespace, operation = EXCLUDED.operation,
			kind = EXCLUDED.kind, source_identity = EXCLUDED.source_identity`,
		rows)
	if err != nil {
		return fmt.Errorf("upsert transactions: %w", err)
	}
	return nil
}

const transactionCols = `transaction_id, service, namespace, operation, kind, source_identity`

// ListTransactions returns transactions for internal id→identity lookups (UI
// listings use ListServiceStats/ListTransactionStats instead).
func (r *Repo) ListTransactions(ctx context.Context, limit int) ([]repo.Transaction, error) {
	if limit <= 0 {
		limit = 100
	}
	return queryRows(ctx, r.pool, "list transactions",
		`SELECT `+transactionCols+` FROM transactions ORDER BY transaction_id ASC LIMIT $1`,
		scanTransaction, limit)
}

// ListTransactionRefs returns every transaction's id and owning service, for
// the engine's per-service admission index.
func (r *Repo) ListTransactionRefs(ctx context.Context) ([]repo.TransactionRef, error) {
	return queryRows(ctx, r.pool, "list transaction refs",
		"SELECT transaction_id, namespace, service FROM transactions",
		func(s rowScanner) (repo.TransactionRef, error) {
			var ref repo.TransactionRef
			err := s.Scan(&ref.ID, &ref.Namespace, &ref.Service)
			return ref, err
		})
}

// GetTransactionByID returns one transaction's identity. ok=false when unknown.
func (r *Repo) GetTransactionByID(ctx context.Context, transactionID int64) (repo.Transaction, bool, error) {
	return queryRow(ctx, r.pool, "get transaction",
		`SELECT `+transactionCols+` FROM transactions WHERE transaction_id = $1`,
		scanTransaction, transactionID)
}

func scanTransaction(s rowScanner) (repo.Transaction, error) {
	var t repo.Transaction
	var kind string
	if err := s.Scan(&t.ID, &t.Service, &t.Namespace, &t.Operation, &kind, &t.SourceIdentity); err != nil {
		return repo.Transaction{}, err
	}
	t.Kind = repo.TransactionKind(kind)
	return t, nil
}

// RecordActivity adds per-transaction activity deltas: volume is summed and
// last_seen keeps the newest value across replicas.
func (r *Repo) RecordActivity(ctx context.Context, deltas []repo.ActivityDelta) error {
	if len(deltas) == 0 {
		return nil
	}
	sorted := append([]repo.ActivityDelta(nil), deltas...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].TransactionID < sorted[j].TransactionID })
	rows := make([][]any, 0, len(sorted))
	for _, d := range sorted {
		seen := d.LastSeen
		if seen.IsZero() {
			seen = time.Now()
		}
		rows = append(rows, []any{d.TransactionID, d.Volume, seen})
	}
	err := execBatch(ctx, r.pool,
		`INSERT INTO transaction_activity AS a (transaction_id, volume, last_seen)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (transaction_id) DO UPDATE SET
			volume = a.volume + EXCLUDED.volume,
			last_seen = GREATEST(a.last_seen, EXCLUDED.last_seen)`,
		rows)
	if err != nil {
		return fmt.Errorf("record activity: %w", err)
	}
	return nil
}

// ListServiceStats rolls transactions up per (namespace, service), most active
// first. Services with no activity yet are kept with volume 0.
func (r *Repo) ListServiceStats(ctx context.Context, limit int) ([]repo.ServiceStat, error) {
	if limit <= 0 {
		limit = 200
	}
	return queryRows(ctx, r.pool, "list service stats",
		`SELECT t.namespace, t.service,
			count(*) AS txn_count,
			COALESCE(sum(a.volume), 0) AS volume,
			COALESCE(max(a.last_seen), 'epoch') AS last_seen
		 FROM transactions AS t
		 LEFT JOIN transaction_activity AS a ON a.transaction_id = t.transaction_id
		 GROUP BY t.namespace, t.service
		 ORDER BY volume DESC, txn_count DESC, t.service ASC
		 LIMIT $1`,
		scanServiceStat, limit)
}

// ListServiceNames returns every distinct service name seen in transactions.
func (r *Repo) ListServiceNames(ctx context.Context) ([]string, error) {
	return queryRows(ctx, r.pool, "list service names",
		`SELECT DISTINCT service FROM transactions WHERE service <> '' ORDER BY service ASC`,
		scanString)
}

func scanServiceStat(s rowScanner) (repo.ServiceStat, error) {
	var stat repo.ServiceStat
	if err := s.Scan(&stat.Namespace, &stat.Service, &stat.TransactionCount, &stat.Volume, &stat.LastSeen); err != nil {
		return repo.ServiceStat{}, err
	}
	return stat, nil
}

// classStateSQL is one row per (transaction, class) with the class's settled
// flag: promoted or saturated. Every "finished learning" roll-up derives from
// it so a saturated class counts as done, never as still learning.
const classStateSQL = `SELECT transaction_id, class,
		(promoted OR saturated) AS is_settled, saturated AS is_saturated
	 FROM baseline_meta`

// ListTransactionStats returns transactions with volume/last_seen and baseline
// promotion state. Sort column/order come from params (default volume desc).
// Promotion is aggregated in the same query, avoiding one baseline read per txn.
func (r *Repo) ListTransactionStats(ctx context.Context, params repo.TransactionListParams) ([]repo.TransactionStat, error) {
	params = params.WithDefaults()
	orderBy, err := transactionStatsOrderBy(params.SortBy, params.SortOrder)
	if err != nil {
		return nil, err
	}

	var a args
	where := "WHERE true"
	// Scope the per-class aggregate to the requested service so a
	// single-service lookup does not aggregate every baseline in the database.
	classScope := ""
	if params.Namespace != "" && params.Service != "" {
		ns, svc := a.add(params.Namespace), a.add(params.Service)
		where += " AND t.namespace = " + ns + " AND t.service = " + svc
		classScope = "WHERE transaction_id IN (SELECT transaction_id FROM transactions WHERE namespace = " + ns + " AND service = " + svc + ")"
	}
	if params.Kind != "" {
		where += " AND t.kind = " + a.add(string(params.Kind))
	}
	if !params.ActiveSince.IsZero() {
		where += " AND COALESCE(a.last_seen, 'epoch') >= " + a.add(params.ActiveSince)
	}
	if !params.ActiveUntil.IsZero() {
		where += " AND COALESCE(a.last_seen, 'epoch') <= " + a.add(params.ActiveUntil)
	}
	limit := a.add(params.Limit)

	return queryRows(ctx, r.pool, "list transaction stats", fmt.Sprintf(
		`SELECT t.transaction_id, t.service, t.namespace, t.operation, t.kind,
			COALESCE(a.volume, 0) AS volume, COALESCE(a.last_seen, 'epoch') AS last_seen,
			COALESCE(p.total, 0) > 0 AS has_baseline,
			COALESCE(p.total, 0) > 0 AND p.settled = p.total AS promoted,
			p.saturated_classes
		 FROM transactions AS t
		 LEFT JOIN transaction_activity AS a ON a.transaction_id = t.transaction_id
		 LEFT JOIN (
			SELECT transaction_id, count(*) AS total, count(*) FILTER (WHERE is_settled) AS settled,
				array_agg(class ORDER BY class) FILTER (WHERE is_saturated) AS saturated_classes
			FROM (`+classStateSQL+`) AS c
			%[1]s
			GROUP BY transaction_id
		 ) AS p ON p.transaction_id = t.transaction_id
		 %[2]s
		 ORDER BY %[3]s, t.transaction_id ASC
		 LIMIT %[4]s`, classScope, where, orderBy, limit), scanTransactionStat, a...)
}

// transactionStatsOrderBy maps whitelist sort fields to SQL expressions.
func transactionStatsOrderBy(by repo.TransactionSortBy, order repo.SortOrder) (string, error) {
	col := ""
	switch by {
	case repo.SortByVolume, "":
		col = "volume"
	case repo.SortByLastSeen:
		col = "last_seen"
	case repo.SortByService:
		col = "t.service"
	case repo.SortByNamespace:
		col = "t.namespace"
	default:
		return "", fmt.Errorf("%w: sort_by %q", repo.ErrInvalidSort, by)
	}
	dir := "ASC"
	if order == repo.SortDesc {
		dir = "DESC"
	}
	return col + " " + dir, nil
}

func scanTransactionStat(s rowScanner) (repo.TransactionStat, error) {
	var (
		stat      repo.TransactionStat
		kind      string
		saturated []string
	)
	if err := s.Scan(
		&stat.ID, &stat.Service, &stat.Namespace, &stat.Operation, &kind,
		&stat.Volume, &stat.LastSeen, &stat.HasBaseline, &stat.Promoted, &saturated,
	); err != nil {
		return repo.TransactionStat{}, err
	}
	stat.Kind = repo.TransactionKind(kind)
	for _, c := range saturated {
		stat.SaturatedClasses = append(stat.SaturatedClasses, repo.DeviationClass(c))
	}
	return stat, nil
}

// DeleteTransaction permanently removes a transaction and all related state in
// one transaction: findings, samples, baselines, recommendations, activity,
// transaction-scoped policies, then the identity.
func (r *Repo) DeleteTransaction(ctx context.Context, transactionID int64) error {
	scopeKey := repo.TransactionScopeKey(transactionID)
	scope := string(repo.ScopeTransaction)
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		deletes := []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM anomaly_issues WHERE transaction_id = $1`, []any{transactionID}},
			{`DELETE FROM trace_observations WHERE transaction_id = $1`, []any{transactionID}},
			{`DELETE FROM baseline_meta WHERE transaction_id = $1`, []any{transactionID}},
			{`DELETE FROM baseline_set_members WHERE transaction_id = $1`, []any{transactionID}},
			{`DELETE FROM baseline_counters WHERE transaction_id = $1`, []any{transactionID}},
			{`DELETE FROM guardrail_recommendations WHERE transaction_id = $1`, []any{transactionID}},
			{`DELETE FROM recommendation_live_counts WHERE transaction_id = $1`, []any{transactionID}},
			{`DELETE FROM recommendation_mining_runs WHERE transaction_id = $1`, []any{transactionID}},
			{`DELETE FROM transaction_activity WHERE transaction_id = $1`, []any{transactionID}},
			{`DELETE FROM learning_policies WHERE scope_type = $1 AND scope_key = $2`, []any{scope, scopeKey}},
			{`DELETE FROM alert_policies WHERE scope_type = $1 AND scope_key = $2`, []any{scope, scopeKey}},
			{`DELETE FROM transactions WHERE transaction_id = $1`, []any{transactionID}},
		}
		for _, d := range deletes {
			if _, err := tx.Exec(ctx, d.sql, d.args...); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("delete transaction %d: %w", transactionID, err)
	}
	return nil
}

// DeleteAllTransactions wipes every transaction and related per-txn state in
// one transaction. Used when identity dimensions change and the operator
// chooses to reset inventory.
func (r *Repo) DeleteAllTransactions(ctx context.Context) error {
	scope := string(repo.ScopeTransaction)
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `TRUNCATE anomaly_issues, trace_observations,
			baseline_meta, baseline_set_members, baseline_counters,
			guardrail_recommendations, recommendation_live_counts, recommendation_mining_runs,
			transaction_activity, transactions`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM learning_policies WHERE scope_type = $1`, scope); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM alert_policies WHERE scope_type = $1`, scope)
		return err
	})
	if err != nil {
		return fmt.Errorf("delete all transactions: %w", err)
	}
	return nil
}
