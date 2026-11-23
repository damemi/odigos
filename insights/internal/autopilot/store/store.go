// Package store keeps the autopilot call trees, analysis runs, and
// recommendations in the insights Postgres. The tables are created by the
// insights migrations (see db/postgres/migrate), not here.
package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	callTreeNodesTable   = "autopilot_call_tree_nodes"
	runsTable            = "autopilot_runs"
	recommendationsTable = "autopilot_recommendations"
	// requestedProbesTable holds the probes on-demand interrogations asked for.
	requestedProbesTable = "interrogation_requested_probes"
	// adkSessionsTable is ADK's session table, where an interrogation run lives.
	adkSessionsTable = "adk.sessions"

	// callTreeRootParent is the parent of a root call-tree node.
	callTreeRootParent = "root"
)

// Client is the autopilot store. Construct with Open.
type Client struct {
	pool *pgxpool.Pool
}

// Open builds a pool for dsn (postgres://user:pass@host:5432/db). Connections
// are opened lazily, so Open does not require the server to be up yet.
func Open(dsn string) (*Client, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres dsn: %w", err)
	}
	// Only the leader's loop and call tree flusher use the pool.
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	return &Client{pool: pool}, nil
}

// Ping verifies the DB is reachable.
func (c *Client) Ping(ctx context.Context) error {
	if c == nil || c.pool == nil {
		return fmt.Errorf("autopilot store: nil pool")
	}
	return c.pool.Ping(ctx)
}

// SchemaReady reports whether the migrate Job has created the autopilot and
// interrogation tables. The loop waits on this the same way insights waits on
// learning_policies.
func (c *Client) SchemaReady(ctx context.Context) error {
	if c == nil || c.pool == nil {
		return fmt.Errorf("autopilot store: nil pool")
	}
	var missing []string
	if err := c.pool.QueryRow(ctx, `
SELECT coalesce(array_agg(t), '{}')
FROM unnest($1::text[]) AS t
WHERE to_regclass(t) IS NULL
`, []string{callTreeNodesTable, runsTable, recommendationsTable, requestedProbesTable, adkSessionsTable}).Scan(&missing); err != nil {
		return fmt.Errorf("schema not ready: %w", err)
	}
	if len(missing) > 0 {
		return fmt.Errorf("schema not ready: tables missing: %v", missing)
	}
	return nil
}

// Close releases the pool.
func (c *Client) Close() error {
	if c == nil || c.pool == nil {
		return nil
	}
	c.pool.Close()
	return nil
}

// parseTransactionID converts a transaction id to the insights int64 hash it
// encodes.
func parseTransactionID(id string) (int64, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return 0, fmt.Errorf("transactionId is required")
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid transactionId %q: %w", id, err)
	}
	return n, nil
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
