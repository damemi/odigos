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
	return fmt.Sprintf("ui.sse.broadcast:%s", config.Config.InstanceID)
}

func GetProxyMetadataKey(proxyID string) string {
	return fmt.Sprintf("proxy-meta:%s", proxyID)
}

func GetClusterSnapshotKey(clusterID string) string {
	return fmt.Sprintf("cluster-snapshot:%s", clusterID)
}

func GetClusterSnapshotsPattern() string {
	return "cluster-snapshot:*"
}

func ExtractClusterIDFromSnapshotKey(key string) string {
	const prefix = "cluster-snapshot:"
	if len(key) > len(prefix) {
		return key[len(prefix):]
	}
	return ""
}

const (
	SnapshotFieldData        = "data"
	SnapshotFieldTimestamp   = "timestamp"
	SnapshotFieldClusterName = "cluster_name"
	// The field name carries the summary format version: bumping it makes every
	// stored summary read as missing, so the startup backfill rebuilds them all.
	SnapshotFieldSummary = "summary_v1"
)
