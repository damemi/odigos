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
	if ns, ok := attrs.Get(string(semconv.K8SNamespaceNameKey)); ok && ns.Str() != "" {
		identity.PutStr(string(semconv.K8SNamespaceNameKey), ns.Str())
	}
	if containerName, ok := attrs.Get(string(semconv.K8SContainerNameKey)); ok && containerName.Str() != "" {
		identity.PutStr(string(semconv.K8SContainerNameKey), containerName.Str())
	}

	kind, kindOk := attrs.Get(consts.OdigosWorkloadKindAttribute)
	name, nameOk := attrs.Get(consts.OdigosWorkloadNameAttribute)
	if kindOk && nameOk {
		kind.CopyTo(identity.PutEmpty(consts.OdigosWorkloadKindAttribute))
		name.CopyTo(identity.PutEmpty(consts.OdigosWorkloadNameAttribute))
		return identity
	}

	for _, attr := range K8sWorkloadNameAttributes {
		if val, ok := attrs.Get(attr.Key); ok && val.Type() == pcommon.ValueTypeStr {
			val.CopyTo(identity.PutEmpty(attr.Key))
			return identity
		}
	}
	return identity
}
