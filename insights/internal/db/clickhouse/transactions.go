package clickhouse

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// UpsertTransaction returns the row for (service, namespace, operation, kind),
// creating it if missing. Cold path / tests; the engine uses UpsertTransactions
// on the write-behind flush instead.
func (r *Repo) UpsertTransaction(ctx context.Context, service, namespace, operation string, kind repo.TransactionKind) (repo.Transaction, error) {
	tx := repo.NewTransaction(service, namespace, operation, kind)
	if err := r.UpsertTransactions(ctx, []repo.Transaction{tx}); err != nil {
		return repo.Transaction{}, err
	}
	return tx, nil
}

// UpsertTransactions batch-inserts transaction identity rows. Idempotent -
// same id every time, so ReplacingMergeTree collapses repeats.
func (r *Repo) UpsertTransactions(ctx context.Context, txs []repo.Transaction) error {
	if len(txs) == 0 {
		return nil
	}
	err := r.insert(ctx,
		"INSERT INTO transactions (transaction_id, service, namespace, operation, kind)",
		func(b driver.Batch) error {
			for _, tx := range txs {
				if err := b.Append(tx.ID, tx.Service, tx.Namespace, tx.Operation, string(tx.Kind)); err != nil {
					return err
				}
			}
			return nil
		})
	if err != nil {
		return fmt.Errorf("upsert transactions: %w", err)
	}
	return nil
}

// ListTransactions returns transactions for internal id→identity lookups (UI
// listings use ListServiceStats/ListTransactionStats instead). Used by the
// debug UI overview.
func (r *Repo) ListTransactions(ctx context.Context, limit int) ([]repo.Transaction, error) {
	if limit <= 0 {
		limit = 100
	}
	return queryRows(ctx, r.conn, "list transactions",
		"SELECT transaction_id, service, namespace, operation, kind FROM transactions FINAL ORDER BY transaction_id ASC LIMIT ?",
		scanTransaction, uint64(limit))
}

// ListTransactionRefs returns every transaction's id and owning service, for
// the engine's per-service admission index.
func (r *Repo) ListTransactionRefs(ctx context.Context) ([]repo.TransactionRef, error) {
	return queryRows(ctx, r.conn, "list transaction refs",
		"SELECT transaction_id, namespace, service FROM transactions FINAL",
		func(s rowScanner) (repo.TransactionRef, error) {
			var ref repo.TransactionRef
			err := s.Scan(&ref.ID, &ref.Namespace, &ref.Service)
			return ref, err
		})
}

// GetTransactionByID returns one transaction's identity. ok=false when unknown.
func (r *Repo) GetTransactionByID(ctx context.Context, transactionID int64) (repo.Transaction, bool, error) {
	return queryRow(ctx, r.conn, "get transaction",
		`SELECT transaction_id, service, namespace, operation, kind
		 FROM transactions FINAL
		 WHERE transaction_id = ?
		 LIMIT 1`,
		scanTransaction, transactionID)
}

func scanTransaction(s rowScanner) (repo.Transaction, error) {
	var t repo.Transaction
	var kind string
	if err := s.Scan(&t.ID, &t.Service, &t.Namespace, &t.Operation, &kind); err != nil {
		return repo.Transaction{}, err
	}
	t.Kind = repo.TransactionKind(kind)
	return t, nil
}

// RecordActivity appends per-transaction activity deltas to transaction_activity;
// the AggregatingMergeTree sums volume and maxes last_seen across replicas.
func (r *Repo) RecordActivity(ctx context.Context, deltas []repo.ActivityDelta) error {
	if len(deltas) == 0 {
		return nil
	}
	err := r.insert(ctx,
		"INSERT INTO transaction_activity (transaction_id, volume, last_seen)",
		func(b driver.Batch) error {
			for _, d := range deltas {
				seen := d.LastSeen
				if seen.IsZero() {
					seen = time.Now()
				}
				if err := b.Append(d.TransactionID, d.Volume, seen); err != nil {
					return err
				}
			}
			return nil
		})
	if err != nil {
		return fmt.Errorf("record activity: %w", err)
	}
	return nil
}

// ListServiceStats rolls transactions up per (namespace, service), most active
// first. The activity subquery pre-aggregates per transaction so the LEFT JOIN
// counts each once and keeps services with no activity yet (volume 0).
func (r *Repo) ListServiceStats(ctx context.Context, limit int) ([]repo.ServiceStat, error) {
	if limit <= 0 {
		limit = 200
	}
	return queryRows(ctx, r.conn, "list service stats",
		`SELECT t.namespace, t.service,
			count() AS txn_count,
			sum(a.volume) AS volume,
			max(a.last_seen) AS last_seen
		 FROM transactions AS t FINAL
		 LEFT JOIN (
			SELECT transaction_id, sum(volume) AS volume, max(last_seen) AS last_seen
			FROM transaction_activity GROUP BY transaction_id
		 ) AS a ON a.transaction_id = t.transaction_id
		 GROUP BY t.namespace, t.service
		 ORDER BY volume DESC, txn_count DESC, t.service ASC
		 LIMIT ?`,
		scanServiceStat, uint64(limit))
}

// ListServiceNames returns every distinct service name seen in transactions.
func (r *Repo) ListServiceNames(ctx context.Context) ([]string, error) {
	return queryRows(ctx, r.conn, "list service names",
		`SELECT DISTINCT service
		 FROM transactions FINAL
		 WHERE service != ''
		 ORDER BY service ASC`,
		scanString)
}

func scanServiceStat(s rowScanner) (repo.ServiceStat, error) {
	var (
		stat     repo.ServiceStat
		txnCount uint64
	)
	if err := s.Scan(&stat.Namespace, &stat.Service, &txnCount, &stat.Volume, &stat.LastSeen); err != nil {
		return repo.ServiceStat{}, err
	}
	stat.TransactionCount = int64(txnCount)
	return stat, nil
}

// ListTransactionStats returns transactions with volume/last_seen and baseline
// promotion state. Sort column/order come from params (default volume desc).
// Promotion is aggregated in the same query, avoiding one baseline read per txn.
func (r *Repo) ListTransactionStats(ctx context.Context, params repo.TransactionListParams) ([]repo.TransactionStat, error) {
	params = params.WithDefaults()
	where := ""
	args := []any{}
	if params.Namespace != "" && params.Service != "" {
		where = "WHERE t.namespace = ? AND t.service = ?"
		args = append(args, params.Namespace, params.Service)
	}
	if params.Kind != "" {
		if where == "" {
			where = "WHERE t.kind = ?"
		} else {
			where += " AND t.kind = ?"
		}
		args = append(args, params.Kind)
	}
	if !params.ActiveSince.IsZero() {
		if where == "" {
			where = "WHERE a.last_seen >= ?"
		} else {
			where += " AND a.last_seen >= ?"
		}
		args = append(args, params.ActiveSince)
	}
	if !params.ActiveUntil.IsZero() {
		if where == "" {
			where = "WHERE a.last_seen <= ?"
		} else {
			where += " AND a.last_seen <= ?"
		}
		args = append(args, params.ActiveUntil)
	}
	orderBy, err := transactionStatsOrderBy(params.SortBy, params.SortOrder)
	if err != nil {
		return nil, err
	}
	args = append(args, uint64(params.Limit))
	return queryRows(ctx, r.conn, "list transaction stats", fmt.Sprintf(
		`SELECT t.transaction_id, t.service, t.namespace, t.operation, t.kind,
			a.volume, a.last_seen,
			p.total > 0 AS has_baseline,
			p.total > 0 AND p.settled = p.total AS promoted,
			p.saturated_classes
		 FROM transactions AS t FINAL
		 LEFT JOIN (
			SELECT transaction_id, sum(volume) AS volume, max(last_seen) AS last_seen
			FROM transaction_activity GROUP BY transaction_id
		 ) AS a ON a.transaction_id = t.transaction_id
		 LEFT JOIN (
			SELECT transaction_id, count() AS total, countIf(is_settled = 1) AS settled,
				arraySort(groupArrayIf(class, is_saturated = 1)) AS saturated_classes
			FROM (`+settledClassesSQL+`)
			GROUP BY transaction_id
		 ) AS p ON p.transaction_id = t.transaction_id
		 %s
		 ORDER BY %s, t.transaction_id ASC
		 LIMIT ?`, where, orderBy), scanTransactionStat, args...)
}

// transactionStatsOrderBy maps whitelist sort fields to SQL expressions.
func transactionStatsOrderBy(by repo.TransactionSortBy, order repo.SortOrder) (string, error) {
	col := ""
	switch by {
	case repo.SortByVolume, "":
		col = "a.volume"
	case repo.SortByLastSeen:
		col = "a.last_seen"
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

// DeleteTransaction permanently removes a transaction and all related state.
// Synchronous mutations (cold path). Order: findings → samples → baselines →
// activity → txn-scoped policies → identity.
func (r *Repo) DeleteTransaction(ctx context.Context, transactionID int64) error {
	sctx := syncMutationCtx(ctx)
	scopeKey := repo.TransactionScopeKey(transactionID)

	if err := r.DeleteAnomalyIssues(ctx, transactionID); err != nil {
		return err
	}
	if err := r.conn.Exec(sctx,
		`ALTER TABLE trace_observations DELETE WHERE transaction_id = ?`,
		transactionID); err != nil {
		return fmt.Errorf("delete observations %d: %w", transactionID, err)
	}
	if err := r.ResetTransactionBaselines(ctx, transactionID); err != nil {
		return err
	}
	if err := r.DeleteRecommendations(ctx, transactionID); err != nil {
		return err
	}
	if err := r.conn.Exec(sctx,
		`ALTER TABLE transaction_activity DELETE WHERE transaction_id = ?`,
		transactionID); err != nil {
		return fmt.Errorf("delete activity %d: %w", transactionID, err)
	}
	if err := r.conn.Exec(sctx,
		`ALTER TABLE learning_policies DELETE WHERE scope_type = ? AND scope_key = ?`,
		string(repo.ScopeTransaction), scopeKey); err != nil {
		return fmt.Errorf("delete learning policies %d: %w", transactionID, err)
	}
	if err := r.conn.Exec(sctx,
		`ALTER TABLE alert_policies DELETE WHERE scope_type = ? AND scope_key = ?`,
		string(repo.ScopeTransaction), scopeKey); err != nil {
		return fmt.Errorf("delete alert policies %d: %w", transactionID, err)
	}
	if err := r.conn.Exec(sctx,
		`ALTER TABLE transactions DELETE WHERE transaction_id = ?`,
		transactionID); err != nil {
		return fmt.Errorf("delete transaction %d: %w", transactionID, err)
	}
	return nil
}

// DeleteAllTransactions wipes every transaction and related per-txn state.
// Synchronous mutations (cold path). Used when identity dimensions change and
// the operator chooses to reset inventory.
func (r *Repo) DeleteAllTransactions(ctx context.Context) error {
	sctx := syncMutationCtx(ctx)
	type tableDelete struct {
		table string
		sql   string
		args  []any
	}
	tableDeletes := []tableDelete{
		{"anomaly_issues", `ALTER TABLE anomaly_issues DELETE WHERE 1`, nil},
		{"trace_observations", `ALTER TABLE trace_observations DELETE WHERE 1`, nil},
	}
	for _, tbl := range baselineTables {
		tableDeletes = append(tableDeletes, tableDelete{tbl, fmt.Sprintf("ALTER TABLE %s DELETE WHERE 1", tbl), nil})
	}
	tableDeletes = append(tableDeletes,
		tableDelete{"guardrail_recommendations", `ALTER TABLE guardrail_recommendations DELETE WHERE 1`, nil},
		tableDelete{"recommendation_live_counts", `ALTER TABLE recommendation_live_counts DELETE WHERE 1`, nil},
		tableDelete{"recommendation_mining_runs", `ALTER TABLE recommendation_mining_runs DELETE WHERE 1`, nil},
		tableDelete{"transaction_activity", `ALTER TABLE transaction_activity DELETE WHERE 1`, nil},
		tableDelete{"learning_policies", `ALTER TABLE learning_policies DELETE WHERE scope_type = ?`, []any{string(repo.ScopeTransaction)}},
		tableDelete{"alert_policies", `ALTER TABLE alert_policies DELETE WHERE scope_type = ?`, []any{string(repo.ScopeTransaction)}},
		tableDelete{"transactions", `ALTER TABLE transactions DELETE WHERE 1`, nil},
	)
	for _, d := range tableDeletes {
		if err := r.conn.Exec(sctx, d.sql, d.args...); err != nil {
			return fmt.Errorf("delete all %s: %w", d.table, err)
		}
	}
	return nil
}

func scanTransactionStat(s rowScanner) (repo.TransactionStat, error) {
	var (
		stat                  repo.TransactionStat
		kind                  string
		hasBaseline, promoted bool
		saturated             []string
	)
	if err := s.Scan(
		&stat.ID, &stat.Service, &stat.Namespace, &stat.Operation, &kind,
		&stat.Volume, &stat.LastSeen, &hasBaseline, &promoted, &saturated,
	); err != nil {
		return repo.TransactionStat{}, err
	}
	stat.Kind = repo.TransactionKind(kind)
	stat.HasBaseline = hasBaseline
	stat.Promoted = promoted
	for _, c := range saturated {
		stat.SaturatedClasses = append(stat.SaturatedClasses, repo.DeviationClass(c))
	}
	return stat, nil
}
