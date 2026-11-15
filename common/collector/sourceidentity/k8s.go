// Package sourceidentity extracts the subset of OpenTelemetry resource attributes
// that identify the Odigos source a resource belongs to, per platform (k8s, vm agent, cloud connectors, ...).
package sourceidentity

import (
	"go.opentelemetry.io/collector/pdata/pcommon"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"github.com/odigos-io/odigos/common/consts"
)

// K8sArgoRolloutNameAttribute is the attribute key for Argo Rollout name (no semconv key).
const K8sArgoRolloutNameAttribute = "k8s.argoproj.rollout.name"

// K8sWorkloadNameAttributes defines the order in which k8s workload name attributes are checked.
// The first matching attribute supplies the workload name, and Kind is the matching workload kind.
var K8sWorkloadNameAttributes = []struct {
	Key  string
	Kind string
}{
	{Key: string(semconv.K8SDeploymentNameKey), Kind: "Deployment"},
	{Key: string(semconv.K8SStatefulSetNameKey), Kind: "StatefulSet"},
	{Key: string(semconv.K8SDaemonSetNameKey), Kind: "DaemonSet"},
	{Key: string(semconv.K8SCronJobNameKey), Kind: "CronJob"},
	{Key: K8sArgoRolloutNameAttribute, Kind: "Rollout"},
}

// FromK8sResourceAttributes returns a new map holding only the k8s resource attributes that
// identify the Odigos source of a k8s workload container: namespace, container name, and the workload kind/name.
// Odigos-specific workload kind/name attributes are preferred when both are present,
// otherwise the first present attribute from K8sWorkloadNameAttributes is used.
func FromK8sResourceAttributes(attrs pcommon.Map) pcommon.Map {
	identity := pcommon.NewMap()
	for _, key := range k8sIdentityKeys(func(key string) (string, bool, bool) {
		val, ok := attrs.Get(key)
		return val.Str(), ok && val.Type() == pcommon.ValueTypeStr, ok
	}) {
		val, _ := attrs.Get(key)
		val.CopyTo(identity.PutEmpty(key))
	}
	return identity
}

// FromK8sResourceAttributesMap is FromK8sResourceAttributes for resource attributes held
// as a plain string map, for consumers that don't work with pdata.
func FromK8sResourceAttributesMap(attrs map[string]string) map[string]string {
	identity := map[string]string{}
	for _, key := range k8sIdentityKeys(func(key string) (string, bool, bool) {
		val, ok := attrs[key]
		return val, ok, ok
	}) {
		identity[key] = attrs[key]
	}
	return identity
}

// k8sIdentityKeys returns the keys that make up the k8s source identity, independent of the
// map type holding the attributes. get reports an attribute's string value, whether it is a
// string, and whether it is present at all.
func k8sIdentityKeys(get func(key string) (val string, isStr bool, ok bool)) []string {
	keys := make([]string, 0, 4)
	for _, key := range []string{string(semconv.K8SNamespaceNameKey), string(semconv.K8SContainerNameKey)} {
		if val, isStr, _ := get(key); isStr && val != "" {
			keys = append(keys, key)
		}
	}

	_, _, kindOk := get(consts.OdigosWorkloadKindAttribute)
	_, _, nameOk := get(consts.OdigosWorkloadNameAttribute)
	if kindOk && nameOk {
		return append(keys, consts.OdigosWorkloadKindAttribute, consts.OdigosWorkloadNameAttribute)
	}

	for _, attr := range K8sWorkloadNameAttributes {
		if _, isStr, _ := get(attr.Key); isStr {
			return append(keys, attr.Key)
		}
	}
	return keys
}
