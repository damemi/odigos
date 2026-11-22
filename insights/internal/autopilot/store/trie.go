package store

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// trieInsertChunk bounds the nodes sent in one INSERT.
const trieInsertChunk = 5000

// InsertCallTrieNodes stores flushed trie nodes. Nodes already stored are
// skipped, so re-flushing a whole trie only adds its new nodes. Nodes whose
// transaction id is not an insights transaction id are dropped and counted in
// skipped.
func (c *Client) InsertCallTrieNodes(ctx context.Context, nodes []FlushedTrieNode) (skipped int, err error) {
	if c == nil || c.pool == nil {
		return 0, fmt.Errorf("autopilot store: nil pool")
	}
	for start := 0; start < len(nodes); start += trieInsertChunk {
		chunk := nodes[start:min(start+trieInsertChunk, len(nodes))]
		var cols [10][]any
		for _, n := range chunk {
			txID, err := strconv.ParseInt(n.TransactionID, 10, 64)
			if err != nil {
				skipped++
				continue
			}
			for i, v := range []any{
				n.Namespace, n.WorkloadKind, n.WorkloadName, n.ContainerName, txID,
				n.SampleType, n.NodeID, n.Parent, n.FunctionName, n.FrameType,
			} {
				cols[i] = append(cols[i], v)
			}
		}
		if len(cols[0]) == 0 {
			continue
		}
		if _, err := c.pool.Exec(ctx, `
INSERT INTO `+callTrieNodesTable+` (
	namespace, workload_kind, workload_name, container_name, transaction_id,
	sample_type, node_id, parent, function_name, frame_type
)
SELECT * FROM unnest(
	$1::text[], $2::text[], $3::text[], $4::text[], $5::bigint[],
	$6::text[], $7::text[], $8::text[], $9::text[], $10::text[]
)
ON CONFLICT DO NOTHING`, cols[0], cols[1], cols[2], cols[3], cols[4], cols[5], cols[6], cols[7], cols[8], cols[9]); err != nil {
			return skipped, fmt.Errorf("insert call trie nodes: %w", err)
		}
	}
	return skipped, nil
}

// ListSourcesWithTransactions returns the distinct workload containers that
// have call-trie data. The trie table is the source of truth, not the insights
// inventory: most inventory sources never produce a trie (only java-ebpf does),
// and DeleteSource clears the trie, so a removed workload stops being listed
// instead of being re-deleted every tick.
func (c *Client) ListSourcesWithTransactions(ctx context.Context) ([]SourceKey, error) {
	if c == nil || c.pool == nil {
		return nil, fmt.Errorf("autopilot store: nil pool")
	}
	rows, err := c.pool.Query(ctx, `
SELECT DISTINCT namespace, workload_kind, workload_name, container_name
FROM `+callTrieNodesTable+`
ORDER BY namespace, workload_kind, workload_name, container_name`)
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

// ListTransactions returns the transactions that have a call trie under the
// source, with their identity from the insights inventory. Membership comes
// from the trie (keyed by workload + transaction id), so a source lists exactly
// the transactions it recorded tries for.
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
	FROM `+callTrieNodesTable+` AS n
	WHERE n.namespace = $1
		AND n.workload_kind = $2
		AND n.workload_name = $3
		AND n.container_name = $4
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

// GetCallTrie returns one row per call-path node of the transaction.
func (c *Client) GetCallTrie(ctx context.Context, source SourceKey, transactionID string) ([]CallTrieNode, error) {
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
SELECT node_id, parent, function_name, frame_type, sample_type
FROM `+callTrieNodesTable+`
WHERE namespace = $1
	AND workload_kind = $2
	AND workload_name = $3
	AND container_name = $4
	AND transaction_id = $5
ORDER BY function_name, node_id`,
		source.Namespace, source.WorkloadKind, source.WorkloadName, source.ContainerName, txID)
	if err != nil {
		return nil, fmt.Errorf("query call trie: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (CallTrieNode, error) {
		var n CallTrieNode
		if err := row.Scan(&n.ID, &n.ParentID, &n.Name, &n.FrameType, &n.SampleType); err != nil {
			return n, err
		}
		if n.ParentID == trieRootParent {
			n.ParentID = ""
		}
		return n, nil
	})
	if err != nil {
		return nil, fmt.Errorf("query call trie: %w", err)
	}
	return out, nil
}

// DeleteSource removes every container of the workload from the autopilot
// tables: call tries, analysis runs, and recommendations. Insights
// transactions are the shared inventory and are left in place.
func (c *Client) DeleteSource(ctx context.Context, source SourceKey) error {
	if c == nil || c.pool == nil {
		return fmt.Errorf("autopilot store: nil pool")
	}
	if err := validateWorkload(source); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, c.pool, func(tx pgx.Tx) error {
		for _, table := range []string{callTrieNodesTable, runsTable, recommendationsTable} {
			if _, err := tx.Exec(ctx,
				`DELETE FROM `+table+` WHERE namespace = $1 AND workload_kind = $2 AND workload_name = $3`,
				source.Namespace, source.WorkloadKind, source.WorkloadName); err != nil {
				return fmt.Errorf("delete %s: %w", table, err)
			}
		}
		return nil
	})
}
