package gqlapi

import (
	"strings"

	"github.com/vektah/gqlparser/v2/gqlerror"
)

// TypedManagedCollectorError preserves the provider's actionable message while
// giving clients a stable branch for common lifecycle and validation failures.
func TypedManagedCollectorError(err error) error {
	message := err.Error()
	extensions := map[string]any{}
	switch {
	case strings.Contains(message, "already has a Collector"):
		extensions["code"] = "COLLECTOR_EXISTS"
	case strings.Contains(message, "teardown is in progress") || strings.Contains(message, "tearing-down"):
		extensions["code"] = "TEARDOWN_IN_PROGRESS"
	case strings.Contains(message, "assume role") || strings.Contains(message, "AssumeRole"):
		extensions["code"] = "AWS_CREDENTIALS_UNAVAILABLE"
	case strings.Contains(message, "already has a credential"):
		extensions["code"] = "CREDENTIAL_EXISTS"
	case strings.Contains(message, "no active credential to rotate"):
		extensions["code"] = "CREDENTIAL_NOT_ACTIVE"
	case strings.Contains(message, "has no enabled managed Collector") || strings.Contains(message, "has no Collector configured"):
		extensions["code"] = "COLLECTOR_NOT_FOUND"
	default:
		for _, candidate := range []struct{ match, field string }{
			{"providerOverrides.aws.clusterRef", "providerOverrides.aws.clusterRef"},
			{"providerOverrides.aws.executionRoleArn", "providerOverrides.aws.executionRoleArn"},
			{"providerOverrides.aws.subnetIds", "providerOverrides.aws.subnetIds"},
			{"retention.idleScaleToZeroAfterHours", "retention.idleScaleToZeroAfterHours"},
			{"retention.idleDeleteAfterHours", "retention.idleDeleteAfterHours"},
			{"ingress minFallbackPrefixLength", "ingress.minFallbackPrefixLength"},
			{"ingress.additionalCidrs", "ingress.additionalCidrs"},
			{"scaling targetCpuPercent", "scaling.targetCpuPercent"},
			{"scaling targetMemoryPercent", "scaling.targetMemoryPercent"},
			{"scaling desiredCount", "scaling.desiredCount"},
			{"scaling minCount", "scaling.minCount"},
			{"scaling maxCount", "scaling.maxCount"},
			{"scaling mode", "scaling.mode"},
			{"transport mode", "transport.mode"},
			{"transport.mode", "transport.mode"},
			{"rolloutBatchSize", "rolloutBatchSize"},
			{"logRetentionDays", "logRetentionDays"},
			{"credentialId", "credentialId"},
			{"secretRef", "secretRef"},
			{"networkId", "networkId"},
			{"memory", "memory"},
			{"cpu", "cpu"},
			{"arch", "arch"},
		} {
			if strings.Contains(message, candidate.match) {
				extensions["code"] = "INVALID_FIELD"
				extensions["field"] = candidate.field
				break
			}
		}
	}
	if len(extensions) == 0 {
		return err
	}
	return &gqlerror.Error{Message: message, Extensions: extensions}
}
