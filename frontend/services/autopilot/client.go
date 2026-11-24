// Package autopilot reads transaction call trees from the autopilot
// tables in the insights Postgres, written by the insights service.
package autopilot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Must match the insights autopilot storage layout (insights/internal/autopilot/store).
const (
	callTreeTable      = "autopilot_call_tree_nodes"
	callTreeRootParent = "root"

	// The insights transaction inventory: each transaction's operation and kind, keyed by the
	// same hashed id the call tree stores as transaction_id (see insights/AGENTS.md).
	insightsTransactionsTable = "transactions"

	postgresConnectTimeout = 5 * time.Second
	postgresQueryTimeout   = 5 * time.Second
)

var (
	// ErrNotEnabled is returned when autopilot is off in effective config.
	ErrNotEnabled = errors.New("autopilot is not enabled")
	// ErrUnavailable is returned when Postgres cannot be reached or is not configured.
	ErrUnavailable = errors.New("autopilot postgres is unavailable")
)

// Function is one aggregated profile function for a transaction.
type Function struct {
	Name       string
	FrameType  string
	SampleType string
	// SeenCount is how many stored call-tree nodes call this function.
	SeenCount int64
}

// Transaction is one transaction id with its aggregated function set.
type Transaction struct {
	ID string
	// Operation and Kind name the transaction (e.g. "POST /buy [http.response.status_code=500]", "HTTP"),
	// from the insights inventory. Empty when the inventory has no row for the id.
	Operation string
	Kind      string
	// SeenCount is how many sample root nodes the transaction's call tree has.
	SeenCount int64
	Functions []Function
}

// CallTreeNode is one flat node in a transaction call tree.
// ParentID is empty for roots (stored parent "root").
type CallTreeNode struct {
	ID         string
	ParentID   string
	Name       string
	FrameType  string
	SampleType string
	SeenCount  int64
}

// SourceTransactions counts the transactions stored for one workload container.
type SourceTransactions struct {
	Namespace        string
	Kind             string
	Name             string
	ContainerName    string
	TransactionCount uint64
}

// ContainerTransactions is the result for one workload container.
type ContainerTransactions struct {
	Namespace     string
	Kind          string
	Name          string
	ContainerName string
	Transactions  []Transaction
}

type callTreeEdgeRow struct {
	TransactionID string
	NodeID        string
	Parent        string
	FunctionName  string
	FrameType     string
	SampleType    string
	Count         uint64
}

// TransactionsFilter narrows ListContainerTransactions. The zero value matches
// every transaction of the container.
type TransactionsFilter struct {
	// IDs restricts results to these transaction ids when non-nil. An empty
	// non-nil slice matches nothing.
	IDs []string
	// Search is a substring match on the transaction id.
	Search string
	// Limit caps the number of transactions returned; 0 means no limit.
	Limit int
	// Offset skips this many matching transactions in result order.
	Offset int
}

// querier is the read surface of a pgx pool, so tests can substitute it.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Client reads the autopilot tables (read-only).
type Client struct {
	db    querier
	close func()
}

// NewClient builds a client for the insights Postgres at dsn
// (postgres://user:pass@host:5432/db). An empty dsn leaves the client
// unconfigured; reads then return ErrUnavailable. Does not connect at
// construction so UI bootstrap succeeds when Postgres is not deployed.
func NewClient(dsn string) *Client {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return &Client{}
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return &Client{}
	}
	cfg.ConnConfig.ConnectTimeout = postgresConnectTimeout
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return &Client{}
	}
	return &Client{db: pool, close: pool.Close}
}

// newClientWithQuerier is for tests.
func newClientWithQuerier(db querier) *Client {
	return &Client{db: db}
}

// Close closes the underlying pool.
func (c *Client) Close() error {
	if c == nil || c.close == nil {
		return nil
	}
	c.close()
	return nil
}

// query runs sql with the client's query timeout. The returned cancel must
// be called once the rows are consumed.
func (c *Client) query(ctx context.Context, sql string, args ...any) (pgx.Rows, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(ctx, postgresQueryTimeout)
	rows, err := c.db.Query(ctx, sql, args...)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return rows, cancel, nil
}

// ListSources returns every workload container that has stored transactions, in
// a single query, so callers can find sources with autopilot transactions without probing each one.
func (c *Client) ListSources(ctx context.Context) ([]SourceTransactions, error) {
	if c == nil || c.db == nil {
		return nil, ErrUnavailable
	}

	rows, cancel, err := c.query(ctx, `
SELECT
	namespace,
	workload_kind,
	workload_name,
	container_name,
	count(DISTINCT transaction_id) AS transactions
FROM `+callTreeTable+`
GROUP BY namespace, workload_kind, workload_name, container_name
ORDER BY namespace, workload_name, workload_kind, container_name`)
	if err != nil {
		return nil, fmt.Errorf("%w: query sources: %v", ErrUnavailable, err)
	}
	defer cancel()
	defer rows.Close()

	out := []SourceTransactions{}
	for rows.Next() {
		var s SourceTransactions
		var count int64
		if err := rows.Scan(&s.Namespace, &s.Kind, &s.Name, &s.ContainerName, &count); err != nil {
			return nil, fmt.Errorf("%w: scan sources: %v", ErrUnavailable, err)
		}
		s.TransactionCount = uint64(count)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: scan sources: %v", ErrUnavailable, err)
	}
	return out, nil
}

// ListContainerTransactions returns the transactions of the workload container
// that match filter, with aggregated functions from the call tree, ordered by
// seenCount desc, then id.
func (c *Client) ListContainerTransactions(ctx context.Context, namespace, kind, name, containerName string, filter TransactionsFilter) (*ContainerTransactions, error) {
	if c == nil || c.db == nil {
		return nil, ErrUnavailable
	}
	namespace = strings.TrimSpace(namespace)
	kind = strings.TrimSpace(kind)
	name = strings.TrimSpace(name)
	containerName = strings.TrimSpace(containerName)
	if namespace == "" || kind == "" || name == "" || containerName == "" {
		return nil, fmt.Errorf("namespace, kind, name, and containerName are required")
	}
	if filter.Limit < 0 || filter.Offset < 0 {
		return nil, fmt.Errorf("limit and offset must not be negative")
	}

	result := &ContainerTransactions{
		Namespace:     namespace,
		Kind:          kind,
		Name:          name,
		ContainerName: containerName,
		Transactions:  []Transaction{},
	}
	if filter.IDs != nil {
		// Stored ids are the insights int64 hashes, so other ids match nothing.
		filter.IDs = slices.DeleteFunc(slices.Clone(filter.IDs), func(id string) bool {
			_, err := strconv.ParseInt(id, 10, 64)
			return err != nil
		})
		if len(filter.IDs) == 0 {
			return result, nil
		}
	}

	query, args := buildListEdgesQuery(namespace, kind, name, containerName, filter)
	rows, cancel, err := c.query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: query call tree: %v", ErrUnavailable, err)
	}
	defer cancel()
	defer rows.Close()

	edges, err := scanCallTreeEdges(rows)
	if err != nil {
		return nil, fmt.Errorf("%w: scan call tree: %v", ErrUnavailable, err)
	}

	result.Transactions = assembleTransactions(edges)
	// Names are display only: without them the UI falls back to the id, so a failed lookup must not fail the page.
	if err := c.attachNames(ctx, result.Transactions); err != nil {
		log.Printf("autopilot: naming transactions of %s/%s/%s: %v", namespace, kind, name, err)
	}
	return result, nil
}

// attachNames fills each transaction's Operation and Kind from the insights inventory, in one query.
func (c *Client) attachNames(ctx context.Context, txs []Transaction) error {
	byID := make(map[int64][]int, len(txs))
	ids := make([]int64, 0, len(txs))
	for i, tx := range txs {
		id, err := strconv.ParseInt(tx.ID, 10, 64)
		if err != nil {
			continue
		}
		if _, seen := byID[id]; !seen {
			ids = append(ids, id)
		}
		byID[id] = append(byID[id], i)
	}
	if len(ids) == 0 {
		return nil
	}

	rows, cancel, err := c.query(ctx, `
SELECT transaction_id, operation, entry_kind
FROM `+insightsTransactionsTable+`
WHERE transaction_id = ANY($1)`, ids)
	if err != nil {
		return fmt.Errorf("query transaction names: %w", err)
	}
	defer cancel()
	defer rows.Close()

	for rows.Next() {
		var id int64
		var operation, txKind string
		if err := rows.Scan(&id, &operation, &txKind); err != nil {
			return fmt.Errorf("scan transaction names: %w", err)
		}
		for _, i := range byID[id] {
			txs[i].Operation = operation
			txs[i].Kind = txKind
		}
	}
	return rows.Err()
}

// buildListEdgesQuery selects the call-tree edges of every transaction
// matching filter. Paging runs in a subquery over transaction ids, ordered
// like assembleTransactions, so each page holds whole transactions. filter.IDs
// must hold only int64 ids.
func buildListEdgesQuery(namespace, kind, name, containerName string, filter TransactionsFilter) (string, []any) {
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	where := fmt.Sprintf(`namespace = %s
	AND workload_kind = %s
	AND workload_name = %s
	AND container_name = %s`, arg(namespace), arg(kind), arg(name), arg(containerName))
	if filter.IDs != nil {
		ids := make([]int64, 0, len(filter.IDs))
		for _, id := range filter.IDs {
			n, _ := strconv.ParseInt(id, 10, 64)
			ids = append(ids, n)
		}
		where += `
	AND transaction_id = ANY(` + arg(ids) + `)`
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		where += `
	AND strpos(lower(transaction_id::text), lower(` + arg(search) + `)) > 0`
	}

	if filter.Limit > 0 || filter.Offset > 0 {
		limit := "ALL"
		if filter.Limit > 0 {
			limit = arg(filter.Limit)
		}
		where += fmt.Sprintf(`
	AND transaction_id IN (
		SELECT transaction_id
		FROM %s
		WHERE %s
		GROUP BY transaction_id
		ORDER BY count(*) FILTER (WHERE parent = '%s' AND sample_type = 'samples') DESC, transaction_id::text ASC
		LIMIT %s OFFSET %s
	)`, callTreeTable, where, callTreeRootParent, limit, arg(filter.Offset))
	}

	return fmt.Sprintf(`
SELECT
	transaction_id::text,
	node_id,
	parent,
	function_name,
	frame_type,
	sample_type,
	count(*) AS count
FROM %s
WHERE %s
GROUP BY transaction_id, node_id, parent, function_name, frame_type, sample_type`, callTreeTable, where), args
}

// GetTransactionSampleTrace returns the stored OTLP JSON sample for a
// transaction. Autopilot does not store sample traces, so it is always empty.
func (c *Client) GetTransactionSampleTrace(ctx context.Context, namespace, kind, name, containerName, txID string) (string, error) {
	if c == nil || c.db == nil {
		return "", ErrUnavailable
	}
	namespace = strings.TrimSpace(namespace)
	kind = strings.TrimSpace(kind)
	name = strings.TrimSpace(name)
	containerName = strings.TrimSpace(containerName)
	txID = strings.TrimSpace(txID)
	if namespace == "" || kind == "" || name == "" || containerName == "" || txID == "" {
		return "", fmt.Errorf("namespace, kind, name, containerName, and transactionId are required")
	}
	return "", nil
}

// GetTransactionCallTree returns flat call tree nodes for a transaction.
// ok is false when no edges exist (caller should treat as null).
func (c *Client) GetTransactionCallTree(ctx context.Context, namespace, kind, name, containerName, txID string) (nodes []CallTreeNode, ok bool, err error) {
	if c == nil || c.db == nil {
		return nil, false, ErrUnavailable
	}
	namespace = strings.TrimSpace(namespace)
	kind = strings.TrimSpace(kind)
	name = strings.TrimSpace(name)
	containerName = strings.TrimSpace(containerName)
	txID = strings.TrimSpace(txID)
	if namespace == "" || kind == "" || name == "" || containerName == "" || txID == "" {
		return nil, false, fmt.Errorf("namespace, kind, name, containerName, and transactionId are required")
	}
	id, err := strconv.ParseInt(txID, 10, 64)
	if err != nil {
		// Stored ids are the insights int64 hashes, so no call tree has this id.
		return nil, false, nil
	}

	rows, cancel, err := c.query(ctx, `
SELECT
	transaction_id::text,
	node_id,
	parent,
	function_name,
	frame_type,
	sample_type,
	count(*) AS count
FROM `+callTreeTable+`
WHERE namespace = $1
	AND workload_kind = $2
	AND workload_name = $3
	AND container_name = $4
	AND transaction_id = $5
GROUP BY transaction_id, node_id, parent, function_name, frame_type, sample_type`,
		namespace, kind, name, containerName, id)
	if err != nil {
		return nil, false, fmt.Errorf("%w: query call tree: %v", ErrUnavailable, err)
	}
	defer cancel()
	defer rows.Close()

	edges, err := scanCallTreeEdges(rows)
	if err != nil {
		return nil, false, fmt.Errorf("%w: scan call tree: %v", ErrUnavailable, err)
	}
	if len(edges) == 0 {
		return nil, false, nil
	}
	return callTreeFromEdges(edges), true, nil
}

func scanCallTreeEdges(rows pgx.Rows) ([]callTreeEdgeRow, error) {
	var out []callTreeEdgeRow
	for rows.Next() {
		var r callTreeEdgeRow
		var count int64
		if err := rows.Scan(
			&r.TransactionID,
			&r.NodeID,
			&r.Parent,
			&r.FunctionName,
			&r.FrameType,
			&r.SampleType,
			&count,
		); err != nil {
			return nil, err
		}
		r.Count = uint64(count)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Compile-time check that the pool satisfies querier.
var _ querier = (*pgxpool.Pool)(nil)
