package odigosconfigk8sextension

import (
	"errors"

	"go.opentelemetry.io/collector/pdata/pcommon"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"github.com/odigos-io/odigos/common/collector/sourceidentity"
)

func workloadIdentityFromResourceAttributes(attrs pcommon.Map) (string, pcommon.Map, error) {
	cacheKey, err := sourceidentity.K8sSourceKeyFromResourceAttributes(attrs)
	if err != nil {
		return "", pcommon.NewMap(), err
	}
	return cacheKey, sourceidentity.FromK8sResourceAttributes(attrs), nil
}

func getNamespace(attrs pcommon.Map) string {
	ns, ok := attrs.Get(string(semconv.K8SNamespaceNameKey))
	if !ok {
		return ""
	}
	return ns.Str()
}

// workloadContainerKeyFromResourceAttributes builds a workload-level cache key prefix
// from resource attributes. Unlike sourceidentity.K8sSourceKeyFromResourceAttributes, it does not
// require k8s.container.name. Returns the key in "ns/Kind/name/" format.
func workloadContainerKeyFromResourceAttributes(attrs pcommon.Map) (string, error) {
	ns := getNamespace(attrs)
	kind, name := sourceidentity.K8sWorkloadKindAndNameFromResourceAttributes(attrs)
	if ns == "" || kind == "" || name == "" {
		return "", errors.New("workload info cannot be calculated from the resource attributes")
	}
	return WorkloadKeyString(ns, kind, name) + "/", nil
}
