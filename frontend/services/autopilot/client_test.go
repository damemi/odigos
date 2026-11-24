package autopilot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAssembleTransactions(t *testing.T) {
	edges := []callTreeEdgeRow{
		{TransactionID: "tx-a", NodeID: "n1", Parent: "root", FunctionName: "Pay", FrameType: "jvm", SampleType: "events", Count: 7},
		{TransactionID: "tx-a", NodeID: "n2", Parent: "root", FunctionName: "Encrypt", FrameType: "hotspot", SampleType: "samples", Count: 3},
		{TransactionID: "tx-a", NodeID: "n3", Parent: "n2", FunctionName: "Encrypt", FrameType: "hotspot", SampleType: "samples", Count: 2},
		{TransactionID: "tx-b", NodeID: "n4", Parent: "root", FunctionName: "Cart", FrameType: "jvm", SampleType: "events", Count: 4},
		{TransactionID: "tx-b", NodeID: "n5", Parent: "root", FunctionName: "Cart", FrameType: "hotspot", SampleType: "samples", Count: 4},
	}

	txs := assembleTransactions(edges)
	require.Len(t, txs, 2)

	byID := map[string]Transaction{}
	for _, tx := range txs {
		byID[tx.ID] = tx
	}
	require.Contains(t, byID, "tx-a")
	require.Contains(t, byID, "tx-b")

	assert.Equal(t, int64(3), byID["tx-a"].SeenCount)
	assert.ElementsMatch(t, []Function{
		{Name: "Pay", FrameType: "jvm", SampleType: "events", SeenCount: 7},
		{Name: "Encrypt", FrameType: "hotspot", SampleType: "samples", SeenCount: 5},
	}, byID["tx-a"].Functions)

	assert.Equal(t, int64(4), byID["tx-b"].SeenCount)
	assert.ElementsMatch(t, []Function{
		{Name: "Cart", FrameType: "jvm", SampleType: "events", SeenCount: 4},
		{Name: "Cart", FrameType: "hotspot", SampleType: "samples", SeenCount: 4},
	}, byID["tx-b"].Functions)

	// Sorted by seenCount desc: tx-b(4), tx-a(3)
	assert.Equal(t, "tx-b", txs[0].ID)
	assert.Equal(t, "tx-a", txs[1].ID)
}

func TestCallTreeFromEdgesSharedPrefix(t *testing.T) {
	const (
		nodeB  = "node-b"
		nodeBA = "node-ba"
		nodeBC = "node-bc"
	)
	edges := []callTreeEdgeRow{
		{TransactionID: "tx", NodeID: nodeB, Parent: "root", FunctionName: "b", FrameType: "hotspot", SampleType: "samples", Count: 2},
		{TransactionID: "tx", NodeID: nodeBA, Parent: nodeB, FunctionName: "a", FrameType: "hotspot", SampleType: "samples", Count: 1},
		{TransactionID: "tx", NodeID: nodeBC, Parent: nodeB, FunctionName: "c", FrameType: "hotspot", SampleType: "samples", Count: 1},
	}

	nodes := callTreeFromEdges(edges)
	require.Len(t, nodes, 3)

	byID := map[string]CallTreeNode{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	require.Contains(t, byID, nodeB)
	assert.Equal(t, "", byID[nodeB].ParentID)
	assert.Equal(t, "b", byID[nodeB].Name)
	assert.Equal(t, int64(2), byID[nodeB].SeenCount)

	require.Contains(t, byID, nodeBA)
	assert.Equal(t, nodeB, byID[nodeBA].ParentID)
	assert.Equal(t, "a", byID[nodeBA].Name)
	assert.Equal(t, int64(1), byID[nodeBA].SeenCount)

	require.Contains(t, byID, nodeBC)
	assert.Equal(t, nodeB, byID[nodeBC].ParentID)
	assert.Equal(t, "c", byID[nodeBC].Name)

	// Sorted by seenCount desc, then name: b(2), a(1), c(1)
	assert.Equal(t, nodeB, nodes[0].ID)
	assert.Equal(t, nodeBA, nodes[1].ID)
	assert.Equal(t, nodeBC, nodes[2].ID)
}

func TestGetTransactionSampleTraceUnavailable(t *testing.T) {
	c := NewClient("")
	got, err := c.GetTransactionSampleTrace(context.Background(), "default", "Deployment", "checkout", "app", "tx-1")
	assert.ErrorIs(t, err, ErrUnavailable)
	assert.Empty(t, got)
}

func TestListContainerTransactionsUnavailable(t *testing.T) {
	c := NewClient("")
	_, err := c.ListContainerTransactions(context.Background(), "default", "Deployment", "checkout", "app", TransactionsFilter{})
	assert.ErrorIs(t, err, ErrUnavailable)
}

func TestGetTransactionCallTreeUnavailable(t *testing.T) {
	c := NewClient("")
	_, ok, err := c.GetTransactionCallTree(context.Background(), "default", "Deployment", "checkout", "app", "tx-1")
	assert.ErrorIs(t, err, ErrUnavailable)
	assert.False(t, ok)
}

func TestNewClientEmptyConfig(t *testing.T) {
	c := NewClient("")
	assert.Nil(t, c.db)
	c = NewClient("postgres://%zz")
	assert.Nil(t, c.db)
}

func TestNewClientDoesNotConnect(t *testing.T) {
	c := NewClient("postgres://user:pass@localhost:1/db")
	require.NotNil(t, c.db)
	assert.NoError(t, c.Close())
}

type fakeConn struct {
	query string
	args  []any
	rows  []callTreeEdgeRow
	// The insights inventory lookup, kept apart from the call-tree query above.
	namesQuery string
	namesArgs  []any
	names      []fakeName
	namesErr   error
}

type fakeName struct {
	id              int64
	operation, kind string
}

func (c *fakeConn) Query(_ context.Context, query string, args ...any) (pgx.Rows, error) {
	if strings.Contains(query, "FROM "+insightsTransactionsTable) {
		c.namesQuery = query
		c.namesArgs = args
		if c.namesErr != nil {
			return nil, c.namesErr
		}
		return &fakeNameRows{rows: c.names, next: -1}, nil
	}
	c.query = query
	c.args = args
	return &fakeRows{rows: c.rows, next: -1}, nil
}

type fakeNameRows struct {
	pgx.Rows
	rows []fakeName
	next int
}

func (r *fakeNameRows) Next() bool {
	r.next++
	return r.next < len(r.rows)
}

func (r *fakeNameRows) Scan(dest ...any) error {
	row := r.rows[r.next]
	*dest[0].(*int64) = row.id
	*dest[1].(*string) = row.operation
	*dest[2].(*string) = row.kind
	return nil
}

func (r *fakeNameRows) Err() error { return nil }
func (r *fakeNameRows) Close()     {}

type fakeRows struct {
	pgx.Rows
	rows []callTreeEdgeRow
	next int
}

func (r *fakeRows) Next() bool {
	r.next++
	return r.next < len(r.rows)
}

func (r *fakeRows) Scan(dest ...any) error {
	row := r.rows[r.next]
	*dest[0].(*string) = row.TransactionID
	*dest[1].(*string) = row.NodeID
	*dest[2].(*string) = row.Parent
	*dest[3].(*string) = row.FunctionName
	*dest[4].(*string) = row.FrameType
	*dest[5].(*string) = row.SampleType
	*dest[6].(*int64) = int64(row.Count)
	return nil
}

func (r *fakeRows) Err() error { return nil }
func (r *fakeRows) Close()     {}

func TestListContainerTransactionsNoFilter(t *testing.T) {
	conn := &fakeConn{rows: []callTreeEdgeRow{
		{TransactionID: "tx-a", NodeID: "n1", Parent: "root", FunctionName: "Pay", FrameType: "jvm", SampleType: "samples", Count: 2},
	}}
	c := newClientWithQuerier(conn)

	got, err := c.ListContainerTransactions(context.Background(), "default", "Deployment", "checkout", "app", TransactionsFilter{})
	require.NoError(t, err)
	require.Len(t, got.Transactions, 1)
	assert.Equal(t, "tx-a", got.Transactions[0].ID)

	assert.Equal(t, []any{"default", "Deployment", "checkout", "app"}, conn.args)
	assert.NotContains(t, conn.query, "ANY(")
	assert.NotContains(t, conn.query, "strpos")
	assert.NotContains(t, conn.query, "LIMIT")
}

func TestListContainerTransactionsNames(t *testing.T) {
	conn := &fakeConn{
		rows: []callTreeEdgeRow{
			{TransactionID: "-42", NodeID: "n1", Parent: "root", FunctionName: "Pay", FrameType: "jvm", SampleType: "samples", Count: 2},
			{TransactionID: "7636099928166363546", NodeID: "n2", Parent: "root", FunctionName: "Buy", FrameType: "jvm", SampleType: "samples", Count: 1},
		},
		names: []fakeName{
			{id: -42, operation: "POST /pay", kind: "HTTP"},
			{id: 7636099928166363546, operation: "POST /buy [http.response.status_code=500]", kind: "HTTP"},
		},
	}
	c := newClientWithQuerier(conn)

	got, err := c.ListContainerTransactions(context.Background(), "default", "Deployment", "checkout", "app", TransactionsFilter{})
	require.NoError(t, err)

	byID := map[string]Transaction{}
	for _, tx := range got.Transactions {
		byID[tx.ID] = tx
	}
	assert.Equal(t, "POST /pay", byID["-42"].Operation)
	assert.Equal(t, "HTTP", byID["-42"].Kind)
	assert.Equal(t, "POST /buy [http.response.status_code=500]", byID["7636099928166363546"].Operation)

	assert.Contains(t, conn.namesQuery, "WHERE transaction_id = ANY($1)")
	require.Len(t, conn.namesArgs, 1)
	assert.ElementsMatch(t, []int64{-42, 7636099928166363546}, conn.namesArgs[0])
}

func TestListContainerTransactionsNamesFailureKeepsTransactions(t *testing.T) {
	conn := &fakeConn{
		rows:     []callTreeEdgeRow{{TransactionID: "42", NodeID: "n1", Parent: "root", FunctionName: "Pay", FrameType: "jvm", SampleType: "samples", Count: 2}},
		namesErr: errors.New("no such table"),
	}
	c := newClientWithQuerier(conn)

	got, err := c.ListContainerTransactions(context.Background(), "default", "Deployment", "checkout", "app", TransactionsFilter{})
	require.NoError(t, err)
	require.Len(t, got.Transactions, 1)
	assert.Empty(t, got.Transactions[0].Operation)
}

func TestListContainerTransactionsFilters(t *testing.T) {
	conn := &fakeConn{}
	c := newClientWithQuerier(conn)

	_, err := c.ListContainerTransactions(context.Background(), "default", "Deployment", "checkout", "app", TransactionsFilter{
		IDs:    []string{"1", "tx-b", "-2"},
		Search: " Pay ",
	})
	require.NoError(t, err)

	assert.Contains(t, conn.query, "AND transaction_id = ANY($5)")
	assert.Contains(t, conn.query, "AND strpos(lower(transaction_id::text), lower($6)) > 0")
	assert.NotContains(t, conn.query, "LIMIT")
	// Ids that are not insights int64 hashes match nothing and are dropped.
	assert.Equal(t, []any{
		"default", "Deployment", "checkout", "app",
		[]int64{1, -2},
		"Pay",
	}, conn.args)
}

func TestListContainerTransactionsPage(t *testing.T) {
	conn := &fakeConn{}
	c := newClientWithQuerier(conn)

	_, err := c.ListContainerTransactions(context.Background(), "default", "Deployment", "checkout", "app", TransactionsFilter{
		Search: "pay",
		Limit:  25,
		Offset: 50,
	})
	require.NoError(t, err)

	assert.Contains(t, conn.query, "ORDER BY count(*) FILTER (WHERE parent = 'root' AND sample_type = 'samples') DESC, transaction_id::text ASC")
	assert.Contains(t, conn.query, "LIMIT $6 OFFSET $7")
	assert.Equal(t, []any{"default", "Deployment", "checkout", "app", "pay", 25, 50}, conn.args)
}

func TestListContainerTransactionsOffsetWithoutLimit(t *testing.T) {
	conn := &fakeConn{}
	c := newClientWithQuerier(conn)

	_, err := c.ListContainerTransactions(context.Background(), "default", "Deployment", "checkout", "app", TransactionsFilter{Offset: 10})
	require.NoError(t, err)

	assert.Contains(t, conn.query, "LIMIT ALL OFFSET $5")
	assert.Equal(t, []any{"default", "Deployment", "checkout", "app", 10}, conn.args)
}

func TestListContainerTransactionsEmptyIDs(t *testing.T) {
	conn := &fakeConn{}
	c := newClientWithQuerier(conn)

	got, err := c.ListContainerTransactions(context.Background(), "default", "Deployment", "checkout", "app", TransactionsFilter{IDs: []string{}})
	require.NoError(t, err)
	assert.Empty(t, got.Transactions)
	assert.Empty(t, conn.query)

	got, err = c.ListContainerTransactions(context.Background(), "default", "Deployment", "checkout", "app", TransactionsFilter{IDs: []string{"tx-a"}})
	require.NoError(t, err)
	assert.Empty(t, got.Transactions)
	assert.Empty(t, conn.query)
}

func TestListContainerTransactionsNegativePage(t *testing.T) {
	c := newClientWithQuerier(&fakeConn{})

	_, err := c.ListContainerTransactions(context.Background(), "default", "Deployment", "checkout", "app", TransactionsFilter{Limit: -1})
	assert.Error(t, err)
	_, err = c.ListContainerTransactions(context.Background(), "default", "Deployment", "checkout", "app", TransactionsFilter{Offset: -1})
	assert.Error(t, err)
}

func TestListSourcesUnavailable(t *testing.T) {
	c := NewClient("")
	_, err := c.ListSources(context.Background())
	assert.ErrorIs(t, err, ErrUnavailable)
}

type fakeSourcesConn struct {
	query string
	rows  []SourceTransactions
}

func (c *fakeSourcesConn) Query(_ context.Context, query string, _ ...any) (pgx.Rows, error) {
	c.query = query
	return &fakeSourceRows{rows: c.rows, next: -1}, nil
}

type fakeSourceRows struct {
	pgx.Rows
	rows []SourceTransactions
	next int
}

func (r *fakeSourceRows) Next() bool {
	r.next++
	return r.next < len(r.rows)
}

func (r *fakeSourceRows) Scan(dest ...any) error {
	row := r.rows[r.next]
	*dest[0].(*string) = row.Namespace
	*dest[1].(*string) = row.Kind
	*dest[2].(*string) = row.Name
	*dest[3].(*string) = row.ContainerName
	*dest[4].(*int64) = int64(row.TransactionCount)
	return nil
}

func (r *fakeSourceRows) Err() error { return nil }
func (r *fakeSourceRows) Close()     {}

func TestListSources(t *testing.T) {
	conn := &fakeSourcesConn{rows: []SourceTransactions{
		{Namespace: "shop", Kind: "Deployment", Name: "checkout", ContainerName: "app", TransactionCount: 3},
	}}
	c := newClientWithQuerier(conn)

	got, err := c.ListSources(context.Background())
	require.NoError(t, err)
	assert.Equal(t, conn.rows, got)
	assert.Contains(t, conn.query, "count(DISTINCT transaction_id)")
	assert.Contains(t, conn.query, "GROUP BY namespace, workload_kind, workload_name, container_name")
}

func TestSourcesToModelDropsUnknownKinds(t *testing.T) {
	got := SourcesToModel([]SourceTransactions{
		{Namespace: "shop", Kind: "Deployment", Name: "checkout", ContainerName: "app", TransactionCount: 3},
		{Namespace: "shop", Kind: "NotAKind", Name: "odd", ContainerName: "app", TransactionCount: 1},
	})
	require.Len(t, got, 1)
	assert.Equal(t, "checkout", got[0].ID.Name)
	assert.Equal(t, 3, got[0].TransactionCount)
}
