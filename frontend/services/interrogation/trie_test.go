package interrogation

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildCallTrieSharedPrefix(t *testing.T) {
	const (
		nodeB  = "node-b"
		nodeBA = "node-ba"
		nodeBC = "node-bc"
		frameA = "a|hotspot|samples"
		frameB = "b|hotspot|samples"
		frameC = "c|hotspot|samples"
	)
	meta := map[string]string{
		nodeB:  "root\x1f" + frameB,
		nodeBA: nodeB + "\x1f" + frameA,
		nodeBC: nodeB + "\x1f" + frameC,
	}
	counts := map[string]string{
		nodeB:  "2",
		nodeBA: "1",
		nodeBC: "1",
	}

	nodes := buildCallTrie(meta, counts)
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

func TestGetTransactionCallTrie(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	c := NewClientWithRedis(rdb)

	const (
		nodeB  = "node-b"
		nodeBA = "node-ba"
		frameA = "a|hotspot|samples"
		frameB = "b|hotspot|samples"
	)
	require.NoError(t, rdb.HSet(context.Background(),
		"interrogation:tx:trie:meta:default/Deployment/checkout/app:lib@1 Server GET /pay",
		nodeB, "root\x1f"+frameB,
		nodeBA, nodeB+"\x1f"+frameA,
	).Err())
	require.NoError(t, rdb.HSet(context.Background(),
		"interrogation:tx:trie:counts:default/Deployment/checkout/app:lib@1 Server GET /pay",
		nodeB, "3",
		nodeBA, "2",
	).Err())

	nodes, ok, err := c.GetTransactionCallTrie(context.Background(), "default", "Deployment", "checkout", "app", "lib@1 Server GET /pay")
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, nodes, 2)
	assert.Equal(t, nodeB, nodes[0].ID)
	assert.Equal(t, "", nodes[0].ParentID)
	assert.Equal(t, "b", nodes[0].Name)
	assert.Equal(t, int64(3), nodes[0].SeenCount)
	assert.Equal(t, nodeBA, nodes[1].ID)
	assert.Equal(t, nodeB, nodes[1].ParentID)
	assert.Equal(t, "a", nodes[1].Name)
	assert.Equal(t, int64(2), nodes[1].SeenCount)

	_, ok, err = c.GetTransactionCallTrie(context.Background(), "default", "Deployment", "checkout", "app", "missing")
	require.NoError(t, err)
	assert.False(t, ok)
}
