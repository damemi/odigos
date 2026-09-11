package interrogation

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseFunctionMember(t *testing.T) {
	fn, ok := parseFunctionMember("com.app.Handler.handle|jvm|events")
	require.True(t, ok)
	assert.Equal(t, Function{Name: "com.app.Handler.handle", FrameType: "jvm", SampleType: "events"}, fn)

	_, ok = parseFunctionMember("only-name")
	assert.False(t, ok)
	_, ok = parseFunctionMember("|jvm|events")
	assert.False(t, ok)
}

func TestListContainerTransactions(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	c := NewClientWithRedis(rdb)

	key1 := "interrogation:tx:funcs:default/Deployment/checkout/app:lib@1 Server GET /pay"
	key2 := "interrogation:tx:funcs:default/Deployment/checkout/app:lib@1 Server POST /cart"
	other := "interrogation:tx:funcs:default/Deployment/checkout/sidecar:lib@1 Server GET /pay"
	require.NoError(t, rdb.SAdd(context.Background(), key1,
		"com.app.Pay|jvm|events",
		"com.app.Encrypt|hotspot|samples",
	).Err())
	require.NoError(t, rdb.SAdd(context.Background(), key2, "com.app.Cart|jvm|events").Err())
	require.NoError(t, rdb.SAdd(context.Background(), other, "com.app.Other|jvm|events").Err())

	got, err := c.ListContainerTransactions(context.Background(), "default", "Deployment", "checkout", "app")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "default", got.Namespace)
	assert.Equal(t, "Deployment", got.Kind)
	assert.Equal(t, "checkout", got.Name)
	assert.Equal(t, "app", got.ContainerName)
	require.Len(t, got.Transactions, 2)

	byID := map[string]Transaction{}
	for _, tx := range got.Transactions {
		byID[tx.ID] = tx
	}
	require.Contains(t, byID, "lib@1 Server GET /pay")
	require.Contains(t, byID, "lib@1 Server POST /cart")
	assert.ElementsMatch(t, []Function{
		{Name: "com.app.Pay", FrameType: "jvm", SampleType: "events"},
		{Name: "com.app.Encrypt", FrameType: "hotspot", SampleType: "samples"},
	}, byID["lib@1 Server GET /pay"].Functions)
	assert.ElementsMatch(t, []Function{
		{Name: "com.app.Cart", FrameType: "jvm", SampleType: "events"},
	}, byID["lib@1 Server POST /cart"].Functions)
}

func TestListContainerTransactionsEmpty(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	c := NewClientWithRedis(rdb)

	got, err := c.ListContainerTransactions(context.Background(), "default", "Deployment", "missing", "app")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Empty(t, got.Transactions)
}

func TestListContainerTransactionsUnavailable(t *testing.T) {
	c := NewClient("")
	_, err := c.ListContainerTransactions(context.Background(), "default", "Deployment", "checkout", "app")
	assert.ErrorIs(t, err, ErrUnavailable)
}
