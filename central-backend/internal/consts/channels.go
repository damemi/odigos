package consts

import (
	"fmt"

	"github.com/odigos-io/central-backend/internal/config"
)

func GetRequestsChannel(proxyID string) string {
	return fmt.Sprintf("graphql.requests.proxy:%s", proxyID)
}

func GetResponsesChannel(proxyID, requestId string) string {
	return fmt.Sprintf("graphql.responses.proxy:%s.request:%s", proxyID, requestId)
}

func GetProxyMetaConnectionsPattern() string {
	return "proxy-meta:*"
}

func GetSSEBroadcastChannel() string {
	centralizedBackendID := config.Config.CentralizedBackendID
	return fmt.Sprintf("ui.sse.broadcast:%s", centralizedBackendID)
}

// Proxy metadata (durable info such as name, type, version, timestamps)
func GetProxyMetadataKey(proxyID string) string {
	centralizedBackendID := config.Config.CentralizedBackendID
	return fmt.Sprintf("proxy-meta:%s:%s", centralizedBackendID, proxyID)
}
