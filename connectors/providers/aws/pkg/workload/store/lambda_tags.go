package store

import (
	"encoding/json"
	"strings"
)

const (
	TagLambdaManaged   = "odigos:managed"
	TagLambdaPending   = "odigos:pending"
	TagLambdaIaC       = "odigos:iac"
	TagLambdaSDK       = "odigos:sdk-layer"
	TagLambdaCollector = "odigos:collector-layer"
)

var LambdaTagKeys = []string{TagLambdaManaged, TagLambdaPending, TagLambdaIaC, TagLambdaSDK, TagLambdaCollector}

// Pending snapshots need recovery or cleanup even when configuration was never applied.
// Keep that decision separate from the workload's reported instrumentation state.
func LambdaHasPendingInstrumentation(fn LambdaFunction) bool {
	switch tags := fn.Metadata["odigos_tags"].(type) {
	case map[string]string:
		return tags[TagLambdaPending] != ""
	case map[string]any:
		value, _ := tags[TagLambdaPending].(string)
		return value != ""
	default:
		return false
	}
}

// LambdaAttributionEnvironment is an in-memory compatibility view, never written to AWS.
// Legacy environment markers remain readable during migration; new markers are resource tags.
func LambdaAttributionEnvironment(env, tags map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range env {
		out[k] = v
	}
	if ref := tags[TagLambdaManaged]; ref != "" {
		marker := map[string]any{"v": 2, "snapshot": strings.TrimPrefix(ref, "v2:"), "layer": tags[TagLambdaSDK], "collectorLayer": tags[TagLambdaCollector]}
		raw, _ := json.Marshal(marker)
		out[EnvOdigosManaged] = string(raw)
	} else if tags[TagLambdaIaC] != "" {
		marker := map[string]any{"v": 1, "layer": tags[TagLambdaSDK], "collectorLayer": tags[TagLambdaCollector]}
		raw, _ := json.Marshal(marker)
		out[EnvOdigosIaC] = string(raw)
	}
	return out
}
func LambdaOwnershipTags(tags map[string]string) map[string]string {
	out := map[string]string{}
	for _, key := range LambdaTagKeys {
		if value, ok := tags[key]; ok {
			out[key] = value
		}
	}
	return out
}
