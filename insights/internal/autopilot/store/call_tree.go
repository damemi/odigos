package store

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// ListSourcesWithTransactions returns the distinct workload containers that
// have call-tree data. The call tree table is the source of truth, not the insights
// inventory: most inventory sources never produce a call tree (only java-ebpf does),
// and DeleteSource clears the call tree, so a removed workload stops being listed
// instead of being re-deleted every tick. Call trees are keyed by source_id, and
// the workload comes from k8s_source_identity, so a call tree synced before the
// baseline flush wrote its source is listed from the next tick on.
func (c *Client) ListSourcesWithTransactions(ctx context.Context) ([]SourceKey, error) {
	if c == nil || c.pool == nil {
		return nil, fmt.Errorf("autopilot store: nil pool")
	}
	rows, err := c.pool.Query(ctx, `
SELECT DISTINCT k.namespace, k.workload_kind, k.workload_name, k.container_name
FROM `+callTreeNodesTable+` AS n
JOIN k8s_source_identity AS k USING (source_id)
ORDER BY k.namespace, k.workload_kind, k.workload_name, k.container_name`)
	if err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (SourceKey, error) {
		var s SourceKey
		err := row.Scan(&s.Namespace, &s.WorkloadKind, &s.WorkloadName, &s.ContainerName)
		return s, err
	})
	if err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	return out, nil
}

// ListTransactions returns the transactions that have a call tree under the
// source, with their identity from the insights inventory. Membership comes
// from the call tree (keyed by source + transaction id), so a source lists exactly
// the transactions it recorded call trees for.
func (c *Client) ListTransactions(ctx context.Context, source SourceKey) ([]Transaction, error) {
	if c == nil || c.pool == nil {
		return nil, fmt.Errorf("autopilot store: nil pool")
	}
	if err := validateK8SSource(source); err != nil {
		return nil, err
	}
	rows, err := c.pool.Query(ctx, `
SELECT t.transaction_id, t.operation, t.entry_kind
FROM transactions AS t
WHERE t.transaction_id IN (
	SELECT n.transaction_id
	FROM `+callTreeNodesTable+` AS n
	JOIN k8s_source_identity AS k USING (source_id)
	WHERE k.namespace = $1
		AND k.workload_kind = $2
		AND k.workload_name = $3
		AND k.container_name = $4
)
ORDER BY t.transaction_id`,
		source.Namespace, source.WorkloadKind, source.WorkloadName, source.ContainerName)
	if err != nil {
		return nil, fmt.Errorf("list transactions: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Transaction, error) {
		var id int64
		var t Transaction
		if err := row.Scan(&id, &t.RootSpanName, &t.RootSpanKind); err != nil {
			return t, err
		}
		t.ID = strconv.FormatInt(id, 10)
		return t, nil
	})
	if err != nil {
		return nil, fmt.Errorf("list transactions: %w", err)
	}
	return out, nil
}

// GetCallTree returns one row per call-path node of the transaction.
func (c *Client) GetCallTree(ctx context.Context, source SourceKey, transactionID string) ([]CallTreeNode, error) {
	if c == nil || c.pool == nil {
		return nil, fmt.Errorf("autopilot store: nil pool")
	}
	if err := validateK8SSource(source); err != nil {
		return nil, err
	}
	txID, err := parseTransactionID(transactionID)
	if err != nil {
		return nil, err
	}
	rows, err := c.pool.Query(ctx, `
SELECT n.node_id, n.parent, n.function_name, n.frame_type, n.sample_type
FROM `+callTreeNodesTable+` AS n
JOIN k8s_source_identity AS k USING (source_id)
WHERE k.namespace = $1
	AND k.workload_kind = $2
	AND k.workload_name = $3
	AND k.container_name = $4
	AND n.transaction_id = $5
ORDER BY n.function_name, n.node_id`,
		source.Namespace, source.WorkloadKind, source.WorkloadName, source.ContainerName, txID)
	if err != nil {
		return nil, fmt.Errorf("query call tree: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (CallTreeNode, error) {
		var n CallTreeNode
		if err := row.Scan(&n.ID, &n.ParentID, &n.Name, &n.FrameType, &n.SampleType); err != nil {
			return n, err
		}
		if n.ParentID == callTreeRootParent {
			n.ParentID = ""
		}
		return n, nil
	})
	if err != nil {
		return nil, fmt.Errorf("query call tree: %w", err)
	}
	return out, nil
}

// DeleteSource removes every container of the workload from the autopilot
// tables: call trees, analysis runs, and recommendations. Insights
// transactions are the shared inventory and are left in place.
func (c *Client) DeleteSource(ctx context.Context, source SourceKey) error {
	if c == nil || c.pool == nil {
		return fmt.Errorf("autopilot store: nil pool")
	}
	if err := validateWorkload(source); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, c.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
DELETE FROM `+callTreeNodesTable+`
WHERE source_id IN (
	SELECT source_id FROM k8s_source_identity
	WHERE namespace = $1 AND workload_kind = $2 AND workload_name = $3
)`, source.Namespace, source.WorkloadKind, source.WorkloadName); err != nil {
			return fmt.Errorf("delete %s: %w", callTreeNodesTable, err)
		}
		for _, table := range []string{runsTable, recommendationsTable} {
			if _, err := tx.Exec(ctx,
				`DELETE FROM `+table+` WHERE namespace = $1 AND workload_kind = $2 AND workload_name = $3`,
				source.Namespace, source.WorkloadKind, source.WorkloadName); err != nil {
				return fmt.Errorf("delete %s: %w", table, err)
			}
		}
		return nil
	})
}
