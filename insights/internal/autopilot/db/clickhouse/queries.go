package clickhouse

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// DatabaseName is the ClickHouse database for autopilot tables, on the
// same server as insights. Structure-only call-trie nodes are flushed from
// Redis by the leader; the LLM loop reads them here.
const DatabaseName = "autopilot"

const (
	insightsTransactionsTable = "transactions"
	callTrieStructureTable    = "tx_call_trie_structure"
	trieRootParent            = "root"
)

// ListSourcesWithTransactions returns the distinct workload containers that
// have call-trie data. The trie table is the source of truth, not the insights
// inventory: most inventory sources never produce a trie (only java-ebpf does),
// and DeleteSource clears the trie, so a removed workload stops being listed
// instead of being re-deleted every tick.
func (c *Client) ListSourcesWithTransactions(ctx context.Context) ([]SourceKey, error) {
	if c == nil || c.conn == nil {
		return nil, fmt.Errorf("clickhouse: nil connection")
	}
	sql := fmt.Sprintf(`
SELECT DISTINCT
	Namespace,
	WorkloadKind,
	WorkloadName,
	ContainerName
FROM %q.%q
ORDER BY Namespace, WorkloadKind, WorkloadName, ContainerName
`, DatabaseName, callTrieStructureTable)

	rows, err := c.conn.Query(ctx, sql)
	if err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	defer rows.Close()

	var out []SourceKey
	for rows.Next() {
		var s SourceKey
		if err := rows.Scan(&s.Namespace, &s.WorkloadKind, &s.WorkloadName, &s.ContainerName); err != nil {
			return nil, fmt.Errorf("scan source: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListTransactions returns the transactions that have a call trie under the
// source, with their identity from the insights inventory. Membership comes
// from the trie (keyed by workload + TransactionId), not from
// transactions.source_identity, so a source lists exactly the transactions it
// recorded tries for.
func (c *Client) ListTransactions(ctx context.Context, source SourceKey) ([]Transaction, error) {
	if c == nil || c.conn == nil {
		return nil, fmt.Errorf("clickhouse: nil connection")
	}
	if err := validateK8SSource(source); err != nil {
		return nil, err
	}
	sql := fmt.Sprintf(`
SELECT
	transaction_id,
	operation,
	kind
FROM %q.%q FINAL
WHERE transaction_id IN (
	SELECT DISTINCT toInt64OrNull(TransactionId)
	FROM %q.%q
	WHERE Namespace = ?
		AND WorkloadKind = ?
		AND WorkloadName = ?
		AND ContainerName = ?
)
ORDER BY transaction_id
`, c.insightsDatabase, insightsTransactionsTable, DatabaseName, callTrieStructureTable)

	rows, err := c.conn.Query(ctx, sql,
		source.Namespace, source.WorkloadKind, source.WorkloadName, source.ContainerName)
	if err != nil {
		return nil, fmt.Errorf("list transactions: %w", err)
	}
	defer rows.Close()

	var out []Transaction
	for rows.Next() {
		var id int64
		var t Transaction
		if err := rows.Scan(&id, &t.RootSpanName, &t.RootSpanKind); err != nil {
			return nil, fmt.Errorf("scan transaction: %w", err)
		}
		t.ID = strconv.FormatInt(id, 10)
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetCallTrie returns one row per call-path node. DISTINCT collapses repeat
// flushes that ReplacingMergeTree has not merged yet.
func (c *Client) GetCallTrie(ctx context.Context, source SourceKey, transactionID string) ([]CallTrieNode, error) {
	if c == nil || c.conn == nil {
		return nil, fmt.Errorf("clickhouse: nil connection")
	}
	if err := validateK8SSource(source); err != nil {
		return nil, err
	}
	transactionID = strings.TrimSpace(transactionID)
	if transactionID == "" {
		return nil, fmt.Errorf("transactionId is required")
	}
	sql := fmt.Sprintf(`
SELECT DISTINCT
	NodeId,
	Parent,
	FunctionName,
	FrameType,
	SampleType
FROM %q.%q
WHERE Namespace = ?
	AND WorkloadKind = ?
	AND WorkloadName = ?
	AND ContainerName = ?
	AND TransactionId = ?
ORDER BY FunctionName, NodeId
`, DatabaseName, callTrieStructureTable)

	rows, err := c.conn.Query(ctx, sql,
		source.Namespace, source.WorkloadKind, source.WorkloadName, source.ContainerName, transactionID)
	if err != nil {
		return nil, fmt.Errorf("query call trie: %w", err)
	}
	defer rows.Close()

	return scanCallTrieNodes(rows)
}

func scanCallTrieNodes(rows driver.Rows) ([]CallTrieNode, error) {
	var out []CallTrieNode
	for rows.Next() {
		var nodeID, parent, name, frameType, sampleType string
		if err := rows.Scan(&nodeID, &parent, &name, &frameType, &sampleType); err != nil {
			return nil, err
		}
		parentID := parent
		if parentID == trieRootParent {
			parentID = ""
		}
		out = append(out, CallTrieNode{
			ID:         nodeID,
			ParentID:   parentID,
			Name:       name,
			FrameType:  frameType,
			SampleType: sampleType,
		})
	}
	return out, rows.Err()
}

func validateK8SSource(source SourceKey) error {
	if strings.TrimSpace(source.Namespace) == "" ||
		strings.TrimSpace(source.WorkloadKind) == "" ||
		strings.TrimSpace(source.WorkloadName) == "" ||
		strings.TrimSpace(source.ContainerName) == "" {
		return fmt.Errorf("namespace, workloadKind, workloadName, and containerName are required")
	}
	return nil
}

func validateSource(source SourceKey) error {
	if len(source.lookupAttributes()) == 0 {
		return fmt.Errorf("source resource attributes are required")
	}
	return nil
}

// validateWorkload is validateK8SSource for workload-level keys, which have no
// container.
func validateWorkload(source SourceKey) error {
	if strings.TrimSpace(source.Namespace) == "" ||
		strings.TrimSpace(source.WorkloadKind) == "" ||
		strings.TrimSpace(source.WorkloadName) == "" {
		return fmt.Errorf("namespace, workloadKind, and workloadName are required")
	}
	return nil
}
