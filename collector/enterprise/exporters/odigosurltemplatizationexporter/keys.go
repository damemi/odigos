package odigosurltemplatizationexporter

import (
	"fmt"

	"go.opentelemetry.io/collector/pdata/ptrace"
)

const (
	redisKeyPrefixServer = "odigos:urltempl:unmatched:server"
	redisKeyPrefixClient = "odigos:urltempl:unmatched:client"
)

// unmatchedRedisKey is the Redis HASH for unmatched HTTP paths of one workload
// and span kind. Field = path, value = count. Empty if kind is not server/client.
func unmatchedRedisKey(workloadKey string, kind ptrace.SpanKind) string {
	switch kind {
	case ptrace.SpanKindServer:
		return fmt.Sprintf("%s:%s", redisKeyPrefixServer, workloadKey)
	case ptrace.SpanKindClient:
		return fmt.Sprintf("%s:%s", redisKeyPrefixClient, workloadKey)
	default:
		return ""
	}
}
