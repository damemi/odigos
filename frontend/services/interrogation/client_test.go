package interrogation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAssembleTransactions(t *testing.T) {
	edges := []callTrieEdgeRow{
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

func TestCallTrieFromEdgesSharedPrefix(t *testing.T) {
	const (
		nodeB  = "node-b"
		nodeBA = "node-ba"
		nodeBC = "node-bc"
	)
	edges := []callTrieEdgeRow{
		{TransactionID: "tx", NodeID: nodeB, Parent: "root", FunctionName: "b", FrameType: "hotspot", SampleType: "samples", Count: 2},
		{TransactionID: "tx", NodeID: nodeBA, Parent: nodeB, FunctionName: "a", FrameType: "hotspot", SampleType: "samples", Count: 1},
		{TransactionID: "tx", NodeID: nodeBC, Parent: nodeB, FunctionName: "c", FrameType: "hotspot", SampleType: "samples", Count: 1},
	}

	nodes := callTrieFromEdges(edges)
	require.Len(t, nodes, 3)

	byID := map[string]CallTrieNode{}
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
	c := NewClient("", "")
	got, err := c.GetTransactionSampleTrace(context.Background(), "default", "Deployment", "checkout", "app", "tx-1")
	assert.ErrorIs(t, err, ErrUnavailable)
	assert.Empty(t, got)
}

func TestListContainerTransactionsUnavailable(t *testing.T) {
	c := NewClient("", "")
	_, err := c.ListContainerTransactions(context.Background(), "default", "Deployment", "checkout", "app")
	assert.ErrorIs(t, err, ErrUnavailable)
}

func TestGetTransactionCallTrieUnavailable(t *testing.T) {
	c := NewClient("tcp://localhost:9000", "")
	_, ok, err := c.GetTransactionCallTrie(context.Background(), "default", "Deployment", "checkout", "app", "tx-1")
	assert.ErrorIs(t, err, ErrUnavailable)
	assert.False(t, ok)
}

func TestNewClientEmptyConfig(t *testing.T) {
	c := NewClient("", "secret")
	assert.Nil(t, c.db)
	c = NewClient("tcp://localhost:9000", "")
	assert.Nil(t, c.db)
}
