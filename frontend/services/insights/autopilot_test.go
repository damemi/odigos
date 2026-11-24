package insights

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListAutopilotCallTrees(t *testing.T) {
	client, captured := newCapturingClient(t, `{"count":1,"items":[{"transaction_id":7636099928166363546,"operation":"POST /claims [http.response.status_code=200]",
		"operation_name":"POST /claims","identity_dimensions":[{"key":"http.response.status_code","value":"200"}],"entry_kind":"HTTP",
		"nodes":[{"id":"samples:a","function_name":"submit","frame_type":"jvm","sample_type":"samples"},
		         {"id":"samples:b","parent_id":"samples:a","function_name":"validate","frame_type":"jvm","sample_type":"samples"}]}]}`)

	got, err := client.ListAutopilotCallTrees(context.Background(), ListAutopilotCallTreesParams{
		Namespace:      "claims-demo",
		WorkloadKind:   "Deployment",
		WorkloadName:   "claims-intake",
		ContainerName:  "app",
		TransactionIDs: []int64{1, -2},
		Search:         "76",
		Limit:          25,
		Offset:         50,
	})
	require.NoError(t, err)

	assert.Equal(t, http.MethodGet, captured.method)
	assert.Equal(t, "/api/v1/autopilot/call-trees", captured.path)
	assert.Equal(t, url.Values{
		"namespace":      {"claims-demo"},
		"workload_kind":  {"Deployment"},
		"workload_name":  {"claims-intake"},
		"container_name": {"app"},
		"transaction_id": {"1", "-2"},
		"search":         {"76"},
		"limit":          {"25"},
		"offset":         {"50"},
	}, captured.query)

	require.Len(t, got, 1)
	assert.Equal(t, int64(7636099928166363546), got[0].TransactionID)
	assert.Equal(t, "POST /claims", got[0].OperationName)
	assert.Equal(t, []TransactionIdentityValue{{Key: "http.response.status_code", Value: "200"}}, got[0].IdentityDimensions)
	require.Len(t, got[0].Nodes, 2)
	assert.Equal(t, "", got[0].Nodes[0].ParentID)
	assert.Equal(t, "samples:a", got[0].Nodes[1].ParentID)
}

func TestListAutopilotCallTreesOmitsUnsetFilters(t *testing.T) {
	client, captured := newCapturingClient(t, `{"count":0,"items":[]}`)
	got, err := client.ListAutopilotCallTrees(context.Background(), ListAutopilotCallTreesParams{
		Namespace: "ns", WorkloadKind: "Deployment", WorkloadName: "w", ContainerName: "c",
	})
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Equal(t, url.Values{"namespace": {"ns"}, "workload_kind": {"Deployment"}, "workload_name": {"w"}, "container_name": {"c"}}, captured.query)
}
