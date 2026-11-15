package sourceidentity

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"github.com/odigos-io/odigos/common/consts"
)

func TestFromK8sResourceAttributes(t *testing.T) {
	attrs := pcommon.NewMap()
	attrs.PutStr(string(semconv.K8SNamespaceNameKey), "default")
	attrs.PutStr(string(semconv.K8SDeploymentNameKey), "checkout")
	attrs.PutStr(string(semconv.K8SContainerNameKey), "app")
	attrs.PutStr(string(semconv.K8SPodNameKey), "checkout-abc")

	identity := FromK8sResourceAttributes(attrs)
	require.Equal(t, map[string]any{
		string(semconv.K8SNamespaceNameKey):  "default",
		string(semconv.K8SDeploymentNameKey): "checkout",
		string(semconv.K8SContainerNameKey):  "app",
	}, identity.AsRaw())
}

func TestFromK8sResourceAttributes_PrefersOdigosWorkloadAttributes(t *testing.T) {
	attrs := pcommon.NewMap()
	attrs.PutStr(string(semconv.K8SNamespaceNameKey), "default")
	attrs.PutStr(string(semconv.K8SDeploymentNameKey), "checkout")
	attrs.PutStr(consts.OdigosWorkloadKindAttribute, "DeploymentConfig")
	attrs.PutStr(consts.OdigosWorkloadNameAttribute, "checkout")

	identity := FromK8sResourceAttributes(attrs)
	require.Equal(t, map[string]any{
		string(semconv.K8SNamespaceNameKey): "default",
		consts.OdigosWorkloadKindAttribute:  "DeploymentConfig",
		consts.OdigosWorkloadNameAttribute:  "checkout",
	}, identity.AsRaw())
}

func TestFromK8sResourceAttributesMap(t *testing.T) {
	identity := FromK8sResourceAttributesMap(map[string]string{
		string(semconv.K8SNamespaceNameKey):  "default",
		string(semconv.K8SDeploymentNameKey): "checkout",
		string(semconv.K8SContainerNameKey):  "app",
		string(semconv.K8SPodNameKey):        "checkout-abc",
	})
	require.Equal(t, map[string]string{
		string(semconv.K8SNamespaceNameKey):  "default",
		string(semconv.K8SDeploymentNameKey): "checkout",
		string(semconv.K8SContainerNameKey):  "app",
	}, identity)
}

func TestFromK8sResourceAttributesMap_PrefersOdigosWorkloadAttributes(t *testing.T) {
	identity := FromK8sResourceAttributesMap(map[string]string{
		string(semconv.K8SNamespaceNameKey):  "default",
		string(semconv.K8SDeploymentNameKey): "checkout",
		consts.OdigosWorkloadKindAttribute:   "DeploymentConfig",
		consts.OdigosWorkloadNameAttribute:   "checkout",
	})
	require.Equal(t, map[string]string{
		string(semconv.K8SNamespaceNameKey): "default",
		consts.OdigosWorkloadKindAttribute:  "DeploymentConfig",
		consts.OdigosWorkloadNameAttribute:  "checkout",
	}, identity)
}
