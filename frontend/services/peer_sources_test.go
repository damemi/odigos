package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	collectormetrics "github.com/odigos-io/odigos/frontend/services/collector_metrics"
)

func TestPeerSourcesSeparatesNamespaces(t *testing.T) {
	edge := func(clientNs, serverNs string) collectormetrics.ServiceGraphEdge {
		return collectormetrics.ServiceGraphEdge{
			RequestCount:    1,
			ToNodeIsVirtual: true,
			Attributes:      map[string]string{"client_k8s_namespace_name": clientNs, "server_k8s_namespace_name": serverNs},
		}
	}
	edges := ServiceGraphEdges{
		"order-service|odimall": {"mysql|odimall": edge("odimall", "odimall")},
		"stock-service|catalog": {"mysql|catalog": edge("catalog", "catalog")},
		"api-gateway|odimall":   {"mysql|catalog": edge("odimall", "catalog"), "mysql|odimall": edge("odimall", "odimall")},
		"load-generator":        {"mysql": {RequestCount: 1, Attributes: map[string]string{}}},
	}

	inbound := PeerSources(edges, "mysql", "odimall").Inbound
	require.Len(t, inbound, 3, "odimall callers plus the one without a namespace dimension")
	for _, peer := range inbound {
		assert.NotEqual(t, "stock-service|catalog", peer.NodeID)
		assert.False(t, peer.IsVirtual, "a caller is never virtual")
	}

	outbound := PeerSources(edges, "api-gateway", "odimall").Outbound
	assert.Len(t, outbound, 2, "mysql in two namespaces stays two peers")
}
