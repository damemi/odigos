package autopilot

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	commonlogger "github.com/odigos-io/odigos/common/logger"
)

// Fixed storage layout for the bundled insights ClickHouse (autopilot DB).
const (
	clickhouseUser         = "odigos_insights"
	clickhouseDatabase     = "autopilot"
	callTrieStructureTable = "tx_call_trie_structure"
)

const insertCallTrieStructureNodesSQL = `
INSERT INTO %q.%q (
	Namespace,
	WorkloadKind,
	WorkloadName,
	ContainerName,
	TransactionId,
	SampleType,
	NodeId,
	Parent,
	FunctionName,
	FrameType
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`

// callTrieStructureNode is one occurrence-only trie node flushed from Redis.
type callTrieStructureNode struct {
	Namespace     string
	WorkloadKind  string
	WorkloadName  string
	ContainerName string
	TransactionID string
	NodeID        string
	Parent        string
	FunctionName  string
	FrameType     string
	SampleType    string
}

type tracesClickHouse struct {
	db                         driver.Conn
	insertCallTrieStructureSQL string
	logger                     *commonlogger.OdigosLogger
}

// openTracesClickHouse dials the autopilot database using the insights
// ClickHouse DSN (any database in the DSN is rewritten to "autopilot").
// Tables are created by the Helm migrate Job (migrate.Apply), not here.
// It does not dial: the driver connects on first use and redials per query,
// so only a malformed DSN is an error.
func openTracesClickHouse(insightsDSN string, logger *commonlogger.OdigosLogger) (*tracesClickHouse, error) {
	opts, err := buildClickHouseOptions(insightsDSN)
	if err != nil {
		return nil, err
	}

	db, err := clickhouse.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("open clickhouse: %w", err)
	}

	return &tracesClickHouse{
		db: db,
		insertCallTrieStructureSQL: fmt.Sprintf(
			insertCallTrieStructureNodesSQL,
			clickhouseDatabase, callTrieStructureTable,
		),
		logger: logger,
	}, nil
}

func buildClickHouseOptions(insightsDSN string) (*clickhouse.Options, error) {
	if insightsDSN == "" {
		return nil, fmt.Errorf("clickhouse dsn is required")
	}
	dsn := insightsDSN
	if !strings.Contains(dsn, "://") {
		dsn = "clickhouse://" + dsn
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse clickhouse dsn: %w", err)
	}
	q := u.Query()
	// Short-lived correlation buffer: favor insert throughput.
	q.Set("async_insert", "true")
	q.Set("wait_for_async_insert", "0")
	q.Set("async_insert_busy_timeout_ms", "2000")
	q.Set("compress", "false")
	u.RawQuery = q.Encode()
	if u.User == nil || u.User.Username() == "" {
		u.User = url.UserPassword(clickhouseUser, "")
	}

	opts, err := clickhouse.ParseDSN(u.String())
	if err != nil {
		return nil, fmt.Errorf("parse clickhouse dsn: %w", err)
	}
	opts.Auth.Database = clickhouseDatabase
	opts.MaxOpenConns = 4
	opts.MaxIdleConns = 4
	return opts, nil
}

func (c *tracesClickHouse) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

func (c *tracesClickHouse) insertCallTrieStructure(ctx context.Context, nodes []callTrieStructureNode) error {
	if c == nil || c.db == nil || len(nodes) == 0 {
		return nil
	}
	batch, err := c.db.PrepareBatch(ctx, c.insertCallTrieStructureSQL)
	if err != nil {
		return fmt.Errorf("prepare call trie structure batch: %w", err)
	}
	for i := range nodes {
		n := &nodes[i]
		if err := batch.Append(
			n.Namespace,
			n.WorkloadKind,
			n.WorkloadName,
			n.ContainerName,
			n.TransactionID,
			n.SampleType,
			n.NodeID,
			n.Parent,
			n.FunctionName,
			n.FrameType,
		); err != nil {
			return fmt.Errorf("append call trie structure node: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("call trie structure insert failed: %w", err)
	}
	c.logger.Debug("inserted call trie structure nodes from redis", "nodes", len(nodes))
	return nil
}
