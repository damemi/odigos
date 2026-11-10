package engine

import (
	"github.com/odigos-io/odigos/common/consts"
	"github.com/odigos-io/odigos/insights/internal/engine/semconv"
)

// vmSourceAttributeKeys are host/instance identity keys used when the span is
// not a complete Kubernetes workload. High-cardinality instance keys such as
// process.pid and k8s.pod.uid are excluded so DISTINCT source listings stay
// stable across process restarts and pod replacements.
var vmSourceAttributeKeys = []string{
	semconv.HostName,
	semconv.HostID,
	semconv.ServiceInstanceID,
}

// sourceResourceAttributes copies the resource attributes that identify a
// source (workload or host) for keyed lookups. Kubernetes identity is
// normalized to the Odigos workload keys plus namespace and container so
// every replica of the same workload writes the same map. When those four
// keys are incomplete, host/instance keys are copied instead.
func sourceResourceAttributes(attrs map[string]string) map[string]string {
	if len(attrs) == 0 {
		return nil
	}
	ns := attrs[semconv.K8SNamespaceName]
	kind, name := workloadKindAndName(attrs)
	container := attrs[semconv.K8SContainerName]
	if ns != "" && kind != "" && name != "" && container != "" {
		return map[string]string{
			semconv.K8SNamespaceName:           ns,
			consts.OdigosWorkloadKindAttribute: kind,
			consts.OdigosWorkloadNameAttribute: name,
			semconv.K8SContainerName:           container,
		}
	}
	out := make(map[string]string, len(vmSourceAttributeKeys))
	for _, k := range vmSourceAttributeKeys {
		if v := attrs[k]; v != "" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sourceAttributesEqual(a, b map[string]string) bool {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
