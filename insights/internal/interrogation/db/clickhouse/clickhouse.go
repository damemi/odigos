// Package clickhouse opens a connection to the interrogation ClickHouse database
// (same bundled server as insights; database name "interrogation").
package clickhouse

import (
	"context"
	"fmt"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

const defaultInsightsDatabase = "odigos_insights"

// Client is a thin wrapper around a ClickHouse connection for the interrogation DB.
type Client struct {
	conn             driver.Conn
	insightsDatabase string
}

// Open dials ClickHouse at dsn (clickhouse://user:pass@host:9000/interrogation).
// insightsDatabase is the insights inventory database (transactions table).
// The connection is lazy — Open does not require the server to be up yet.
func Open(dsn, insightsDatabase string) (*Client, error) {
	opts, err := ch.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse clickhouse dsn: %w", err)
	}
	conn, err := ch.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("open clickhouse: %w", err)
	}
	if insightsDatabase == "" {
		insightsDatabase = defaultInsightsDatabase
	}
	return &Client{conn: conn, insightsDatabase: insightsDatabase}, nil
}

// InsightsDatabase returns the database the insights inventory is read from.
func (c *Client) InsightsDatabase() string {
	if c == nil {
		return ""
	}
	return c.insightsDatabase
}

// Conn returns the underlying driver connection for queries.
func (c *Client) Conn() driver.Conn {
	if c == nil {
		return nil
	}
	return c.conn
}

// Ping verifies the DB is reachable.
func (c *Client) Ping(ctx context.Context) error {
	if c == nil || c.conn == nil {
		return fmt.Errorf("clickhouse: nil connection")
	}
	return c.conn.Ping(ctx)
}

// SchemaReady reports whether the migrate Job has created the interrogation
// tables. The loop waits on this the same way insights waits on learning_policies.
func (c *Client) SchemaReady(ctx context.Context) error {
	if c == nil || c.conn == nil {
		return fmt.Errorf("clickhouse: nil connection")
	}
	var n uint64
	if err := c.conn.QueryRow(ctx, `
SELECT count()
FROM system.tables
WHERE database = ? AND name IN (?, ?, ?)
`, DatabaseName, callTrieStructureTable, runsTable, recommendationsTable).Scan(&n); err != nil {
		return fmt.Errorf("schema not ready: %w", err)
	}
	if n < 3 {
		return fmt.Errorf("schema not ready: interrogation tables missing")
	}
	// ListTransactions reads the insights inventory across databases. Checking
	// system.tables also checks access: it lists only tables this user can see.
	var inv uint64
	if err := c.conn.QueryRow(ctx, `
SELECT count()
FROM system.tables
WHERE database = ? AND name = ?
`, c.insightsDatabase, insightsTransactionsTable).Scan(&inv); err != nil {
		return fmt.Errorf("schema not ready: %w", err)
	}
	if inv == 0 {
		return fmt.Errorf("schema not ready: %s.%s not visible", c.insightsDatabase, insightsTransactionsTable)
	}
	if err := c.conn.QueryRow(ctx, `
SELECT count()
FROM system.columns
WHERE database = ? AND table = ? AND name IN (?, ?)
`, DatabaseName, runsTable, "AnalysisStatus", "AnalysisRevision").Scan(&n); err != nil {
		return fmt.Errorf("schema not ready: %w", err)
	}
	if n < 2 {
		return fmt.Errorf("schema not ready: tx_runs analysis columns missing")
	}
	return nil
}

// Close releases the connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}
