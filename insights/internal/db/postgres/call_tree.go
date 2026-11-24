package postgres

import (
	"context"
	"fmt"
	"strconv"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// callTreeInsertChunk bounds the nodes sent in one INSERT.
const callTreeInsertChunk = 5000

// InsertCallTreeNodes stores autopilot call tree nodes. Nodes already stored
// are skipped, so re-sending a whole call tree only adds its new nodes. Each
// chunk is one statement: the columns go in as arrays and unnest turns them
// back into rows.
func (r *Repo) InsertCallTreeNodes(ctx context.Context, nodes []repo.CallTreeNode) error {
	for start := 0; start < len(nodes); start += callTreeInsertChunk {
		chunk := nodes[start:min(start+callTreeInsertChunk, len(nodes))]
		var cols [7][]any
		for _, n := range chunk {
			for i, v := range []any{
				n.SourceID, n.TransactionID, n.SampleType, n.NodeID, n.Parent, n.FunctionName, n.FrameType,
			} {
				cols[i] = append(cols[i], v)
			}
		}
		if _, err := r.pool.Exec(ctx, `
INSERT INTO autopilot_call_tree_nodes (
	source_id, transaction_id, sample_type, node_id, parent, function_name, frame_type
)
SELECT * FROM unnest(
	$1::bigint[], $2::bigint[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[]
)
ON CONFLICT DO NOTHING`, cols[0], cols[1], cols[2], cols[3], cols[4], cols[5], cols[6]); err != nil {
			return fmt.Errorf("insert call tree nodes: %w", err)
		}
	}
	return nil
}

// ListCallTreeTransactions returns one workload container's call trees, a page
// of transactions at a time, with each transaction's name from transactions. A
// container maps to one source_id (k8s_source_identity is unique on the four
// columns); the page is chosen by CPU-sample call paths, most first.
func (r *Repo) ListCallTreeTransactions(ctx context.Context, params repo.CallTreeTransactionsParams) ([]repo.CallTreeTransaction, error) {
	if params.TransactionIDs != nil && len(params.TransactionIDs) == 0 {
		return []repo.CallTreeTransaction{}, nil
	}
	query, args := callTreeTransactionsQuery(params)
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list call tree transactions: %w", err)
	}
	defer rows.Close()

	out := []repo.CallTreeTransaction{}
	for rows.Next() {
		var (
			txID                 int64
			operation, entryKind string
			node                 repo.CallTreeNode
		)
		if err := rows.Scan(&txID, &operation, &entryKind, &node.SourceID, &node.SampleType, &node.NodeID, &node.Parent, &node.FunctionName, &node.FrameType); err != nil {
			return nil, fmt.Errorf("scan call tree transactions: %w", err)
		}
		node.TransactionID = txID
		// Rows come grouped by transaction, in page order.
		if len(out) == 0 || out[len(out)-1].TransactionID != txID {
			out = append(out, repo.CallTreeTransaction{TransactionID: txID, Operation: operation, EntryKind: entryKind})
		}
		last := &out[len(out)-1]
		last.Nodes = append(last.Nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list call tree transactions: %w", err)
	}
	return out, nil
}

// callTreeTransactionsQuery builds ListCallTreeTransactions' query: the page of
// transaction ids first, then their nodes and names.
func callTreeTransactionsQuery(params repo.CallTreeTransactionsParams) (string, []any) {
	args := []any{params.Namespace, params.WorkloadKind, params.WorkloadName, params.ContainerName}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	filters := ""
	if params.TransactionIDs != nil {
		filters += "\n\t\tAND transaction_id = ANY(" + arg(params.TransactionIDs) + ")"
	}
	if params.Search != "" {
		filters += "\n\t\tAND strpos(transaction_id::text, " + arg(params.Search) + ") > 0"
	}
	limit := "ALL"
	if params.Limit > 0 {
		limit = arg(params.Limit)
	}
	offset := arg(max(params.Offset, 0))

	return `
WITH src AS (
	SELECT source_id FROM k8s_source_identity
	WHERE namespace = $1 AND workload_kind = $2 AND workload_name = $3 AND container_name = $4
), page AS (
	SELECT transaction_id, count(*) FILTER (WHERE parent = 'root' AND sample_type = 'samples') AS sample_roots
	FROM autopilot_call_tree_nodes
	WHERE source_id = (SELECT source_id FROM src)` + filters + `
	GROUP BY transaction_id
	ORDER BY sample_roots DESC, transaction_id::text
	LIMIT ` + limit + ` OFFSET ` + offset + `
)
SELECT p.transaction_id, coalesce(t.operation, ''), coalesce(t.entry_kind, ''),
	n.source_id, n.sample_type, n.node_id, n.parent, n.function_name, n.frame_type
FROM page AS p
JOIN autopilot_call_tree_nodes AS n
	ON n.source_id = (SELECT source_id FROM src) AND n.transaction_id = p.transaction_id
LEFT JOIN transactions AS t ON t.transaction_id = p.transaction_id
ORDER BY p.sample_roots DESC, p.transaction_id::text, n.sample_type, n.node_id`, args
}
