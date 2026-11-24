package autopilot

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/odigos-io/odigos/frontend/graph/model"
	"github.com/odigos-io/odigos/frontend/services/insights"
)

func node(id, parent, name, frameType, sampleType string) insights.AutopilotCallTreeNode {
	return insights.AutopilotCallTreeNode{ID: id, ParentID: parent, FunctionName: name, FrameType: frameType, SampleType: sampleType}
}

func TestTransactionsToModel(t *testing.T) {
	txs := TransactionsToModel([]insights.AutopilotCallTreeTransaction{
		{
			TransactionID:      7636099928166363546,
			Operation:          "POST /claims [http.response.status_code=200]",
			OperationName:      "POST /claims",
			IdentityDimensions: []insights.TransactionIdentityValue{{Key: "http.response.status_code", Value: "200"}},
			EntryKind:          "HTTP",
			Nodes: []insights.AutopilotCallTreeNode{
				node("samples:a", "", "submit", "jvm", "samples"),
				node("samples:b", "samples:a", "validate", "jvm", "samples"),
				node("events:a", "", "submit", "jvm", "events"),
			},
		},
		{TransactionID: -42},
	})
	require.Len(t, txs, 2)

	tx := txs[0]
	assert.Equal(t, "7636099928166363546", tx.ID)
	require.NotNil(t, tx.OperationName)
	assert.Equal(t, "POST /claims", *tx.OperationName)
	require.Len(t, tx.IdentityDimensions, 1)
	assert.Equal(t, "http.response.status_code", tx.IdentityDimensions[0].Key)
	assert.Equal(t, "200", tx.IdentityDimensions[0].Value)
	assert.Equal(t, "HTTP", *tx.Kind)

	require.Len(t, tx.CallTree, 3)
	assert.Equal(t, "samples:b", tx.CallTree[1].ID)
	assert.Equal(t, "validate", tx.CallTree[1].Name)
	require.NotNil(t, tx.CallTree[1].ParentID)
	assert.Equal(t, "samples:a", *tx.CallTree[1].ParentID)
	assert.Nil(t, tx.CallTree[0].ParentID)
	assert.Equal(t, "events", tx.CallTree[2].SampleType)

	// Not stored by insights yet: no name, and empty (not null) lists.
	assert.Equal(t, "-42", txs[1].ID)
	assert.Nil(t, txs[1].OperationName)
	assert.Nil(t, txs[1].Kind)
	assert.NotNil(t, txs[1].IdentityDimensions)
	assert.Empty(t, txs[1].IdentityDimensions)
	assert.NotNil(t, txs[1].CallTree)
	assert.Empty(t, txs[1].CallTree)
}

func TestTransactionsToModelClassifiesFrames(t *testing.T) {
	txs := TransactionsToModel([]insights.AutopilotCallTreeTransaction{{
		TransactionID: 1,
		Nodes: []insights.AutopilotCallTreeNode{
			node("n1", "", "void com.rowan.notify.Controller.create(java.lang.String)", "jvm", "samples"),
			node("n2", "n1", "java.util.concurrent.CompletableFuture org.springframework.kafka.core.KafkaTemplate.send(java.lang.String, java.lang.Object)", "jvm", "samples"),
			node("n3", "n2", "java.lang.Object java.util.HashMap.get(java.lang.Object)", "hotspot", "samples"),
			node("n4", "", "net/http.(*conn).serve", "go", "samples"),
			node("n5", "n1", "void io.odigos.spring.servlet.SpringServletAdvice.detach(long)", "jvm", "samples"),
		},
	}})
	require.Len(t, txs, 1)
	kinds := map[string]model.AutopilotFrameKind{}
	for _, n := range txs[0].CallTree {
		kinds[n.ID] = n.FrameKind
	}
	assert.Equal(t, model.AutopilotFrameKindApp, kinds["n1"])
	// Libraries classify as application code.
	assert.Equal(t, model.AutopilotFrameKindApp, kinds["n2"])
	assert.Equal(t, model.AutopilotFrameKindRuntime, kinds["n3"])
	// Non-JVM frames are not classified.
	assert.Equal(t, model.AutopilotFrameKindApp, kinds["n4"])
	// Odigos agent frames are shown as runtime.
	assert.Equal(t, model.AutopilotFrameKindRuntime, kinds["n5"])
}

func TestTransactionIDs(t *testing.T) {
	ids, ok := TransactionIDs([]string{"1", "tx-b", " -2 "})
	assert.True(t, ok)
	assert.Equal(t, []int64{1, -2}, ids)

	_, ok = TransactionIDs([]string{"tx-a"})
	assert.False(t, ok)
	_, ok = TransactionIDs([]string{})
	assert.False(t, ok)
}
