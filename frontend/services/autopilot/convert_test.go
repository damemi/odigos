package autopilot

import (
	"testing"

	"github.com/odigos-io/odigos/frontend/graph/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCallTreeToModelClassifiesFrames(t *testing.T) {
	nodes := CallTreeToModel([]CallTreeNode{
		{ID: "n1", Name: "void com.rowan.notify.Controller.create(java.lang.String)", FrameType: "jvm"},
		{ID: "n2", ParentID: "n1", Name: "java.util.concurrent.CompletableFuture org.springframework.kafka.core.KafkaTemplate.send(java.lang.String, java.lang.Object)", FrameType: "jvm"},
		{ID: "n3", ParentID: "n2", Name: "java.lang.Object java.util.HashMap.get(java.lang.Object)", FrameType: "hotspot"},
		{ID: "n4", ParentID: "n3", Name: "void com.rowan.notify.RenderMessageProducer.publish(java.lang.String)", FrameType: "jvm"},
		{ID: "n5", Name: "net/http.(*conn).serve", FrameType: "go"},
		{ID: "n6", ParentID: "n4", Name: "void io.odigos.spring.servlet.SpringServletAdvice.detach(long)", FrameType: "jvm"},
	})
	require.Len(t, nodes, 6)

	assert.Equal(t, model.AutopilotFrameKindApp, nodes[0].FrameKind)

	// Libraries classify as application code.
	assert.Equal(t, model.AutopilotFrameKindApp, nodes[1].FrameKind)

	assert.Equal(t, model.AutopilotFrameKindRuntime, nodes[2].FrameKind)

	assert.Equal(t, model.AutopilotFrameKindApp, nodes[3].FrameKind)

	// Non-JVM frames are not classified.
	assert.Equal(t, model.AutopilotFrameKindApp, nodes[4].FrameKind)

	// Odigos agent frames are shown as runtime.
	assert.Equal(t, model.AutopilotFrameKindRuntime, nodes[5].FrameKind)
}

func TestTransactionsToModelClassifiesFunctions(t *testing.T) {
	txs := TransactionsToModel([]Transaction{{
		ID: "tx-a",
		Functions: []Function{
			{Name: "java.lang.Object java.util.HashMap.get(java.lang.Object)", FrameType: "jvm"},
			{Name: "void com.rowan.account.AccountLookup.loadById(java.lang.String)", FrameType: "jvm"},
		},
	}})
	require.Len(t, txs, 1)
	fns := txs[0].Functions
	require.Len(t, fns, 2)
	assert.Equal(t, model.AutopilotFrameKindRuntime, fns[0].FrameKind)
	assert.Equal(t, model.AutopilotFrameKindApp, fns[1].FrameKind)
}
