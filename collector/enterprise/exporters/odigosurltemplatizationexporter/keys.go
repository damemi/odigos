package odigosurltemplatizationexporter

import (
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/odigos-io/odigos-enterprise/common-enterprise/urltemplatization"
)

// unmatchedRedisKey is the Redis HASH for unmatched HTTP paths of one workload
// and span kind. Field = path, value = count. Empty if kind is not server/client.
func unmatchedRedisKey(workloadKey string, kind ptrace.SpanKind) string {
	switch kind {
	case ptrace.SpanKindServer:
		return urltemplatization.UnmatchedPathsRedisKeyPrefixServer + workloadKey
	case ptrace.SpanKindClient:
		return urltemplatization.UnmatchedPathsRedisKeyPrefixClient + workloadKey
	default:
		return ""
	}
}
