package sourceidentity

import (
	"errors"
	"fmt"

	"go.opentelemetry.io/collector/pdata/pcommon"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"github.com/odigos-io/odigos/common/consts"
)

// K8sSourceKey returns the cache key of a k8s workload container: "namespace/kind/name/container".
func K8sSourceKey(namespace, kind, name, containerName string) string {
	return fmt.Sprintf("%s/%s/%s/%s", namespace, kind, name, containerName)
}

// K8sSourceKeyFromResourceAttributes returns the K8sSourceKey of the workload container the
// resource attributes belong to. It reads k8s.namespace.name, k8s.container.name, and the
// workload kind/name (see FromK8sResourceAttributes), and errors when any of them is missing.
func K8sSourceKeyFromResourceAttributes(attrs pcommon.Map) (string, error) {
	return k8sSourceKey(pcommonGetter(attrs))
}

// K8sSourceKeyFromResourceAttributesMap is K8sSourceKeyFromResourceAttributes for resource
// attributes held as a plain string map, for consumers that don't work with pdata.
func K8sSourceKeyFromResourceAttributesMap(attrs map[string]string) (string, error) {
	return k8sSourceKey(mapGetter(attrs))
}

// K8sWorkloadKindAndNameFromResourceAttributes returns the k8s workload kind and name the
// resource attributes belong to, or empty strings when they hold none (see k8sKindAndName).
func K8sWorkloadKindAndNameFromResourceAttributes(attrs pcommon.Map) (kind, name string) {
	return k8sKindAndName(pcommonGetter(attrs))
}

// K8sWorkloadKindAndNameFromResourceAttributesMap is K8sWorkloadKindAndNameFromResourceAttributes
// for resource attributes held as a plain string map, for consumers that don't work with pdata.
func K8sWorkloadKindAndNameFromResourceAttributesMap(attrs map[string]string) (kind, name string) {
	return k8sKindAndName(mapGetter(attrs))
}

func k8sSourceKey(get attrGetter) (string, error) {
	ns := attrString(get, string(semconv.K8SNamespaceNameKey))
	kind, name := k8sKindAndName(get)
	containerName := attrString(get, string(semconv.K8SContainerNameKey))
	if ns == "" || kind == "" || name == "" || containerName == "" {
		return "", errors.New("workload info cannot be calculated from the resource attributes")
	}
	return K8sSourceKey(ns, kind, name, containerName), nil
}

// k8sKindAndName returns the workload kind and name. Odigos-specific attributes are preferred
// when both are present (e.g. an OpenShift DeploymentConfig instead of its Deployment),
// otherwise the first string attribute from K8sWorkloadNameAttributes supplies the name.
func k8sKindAndName(get attrGetter) (string, string) {
	kind, _, kindOk := get(consts.OdigosWorkloadKindAttribute)
	name, _, nameOk := get(consts.OdigosWorkloadNameAttribute)
	if kindOk && nameOk {
		return kind, name
	}
	for _, attr := range K8sWorkloadNameAttributes {
		if val, isStr, _ := get(attr.Key); isStr {
			return attr.Kind, val
		}
	}
	return "", ""
}

func attrString(get attrGetter, key string) string {
	val, _, _ := get(key)
	return val
}
