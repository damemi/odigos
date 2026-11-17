package clickhouse

import (
	"context"
	"fmt"

	ch "github.com/ClickHouse/clickhouse-go/v2"
)

// DeleteSource removes every container of the workload from the autopilot
// tables that store its rows: call-trie structure, analysis runs, and
// recommendations. Insights transactions are the shared inventory and are left
// in place.
func (c *Client) DeleteSource(ctx context.Context, source SourceKey) error {
	if c == nil || c.conn == nil {
		return fmt.Errorf("clickhouse: nil connection")
	}
	if err := validateSource(source); err != nil {
		return err
	}
	if source.Namespace == "" || source.WorkloadKind == "" || source.WorkloadName == "" {
		return fmt.Errorf("namespace, workloadKind, and workloadName are required")
	}
	ctx = ch.Context(ctx, ch.WithSettings(ch.Settings{
		"mutations_sync": 2,
	}))
	where := `Namespace = ? AND WorkloadKind = ? AND WorkloadName = ?`
	args := []any{source.Namespace, source.WorkloadKind, source.WorkloadName}
	for _, table := range []string{callTrieStructureTable, runsTable, recommendationsTable} {
		sql := fmt.Sprintf("ALTER TABLE %q.%q DELETE WHERE %s", DatabaseName, table, where)
		if err := c.conn.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("delete %s: %w", table, err)
		}
	}
	return nil
}
