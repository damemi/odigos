package sourceidentity

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"github.com/odigos-io/odigos/common/consts"
)

func TestK8sSourceKeyFromResourceAttributes(t *testing.T) {
	attrs := pcommon.NewMap()
	attrs.PutStr(string(semconv.K8SNamespaceNameKey), "default")
	attrs.PutStr(string(semconv.K8SDeploymentNameKey), "checkout")
	attrs.PutStr(string(semconv.K8SContainerNameKey), "app")

	key, err := K8sSourceKeyFromResourceAttributes(attrs)
	require.NoError(t, err)
	require.Equal(t, "default/Deployment/checkout/app", key)

	kind, name := K8sWorkloadKindAndNameFromResourceAttributes(attrs)
	require.Equal(t, "Deployment", kind)
	require.Equal(t, "checkout", name)
}

func TestK8sSourceKeyFromResourceAttributes_PrefersOdigosWorkloadAttributes(t *testing.T) {
	key, err := K8sSourceKeyFromResourceAttributesMap(map[string]string{
		string(semconv.K8SNamespaceNameKey):  "default",
		string(semconv.K8SDeploymentNameKey): "checkout",
		string(semconv.K8SContainerNameKey):  "app",
		consts.OdigosWorkloadKindAttribute:   "DeploymentConfig",
		consts.OdigosWorkloadNameAttribute:   "checkout",
	})
	require.NoError(t, err)
	require.Equal(t, "default/DeploymentConfig/checkout/app", key)
}

func TestK8sSourceKeyFromResourceAttributes_MissingAttributes(t *testing.T) {
	attrs := pcommon.NewMap()
	attrs.PutStr(string(semconv.K8SNamespaceNameKey), "default")
	attrs.PutStr(string(semconv.K8SDeploymentNameKey), "checkout")

	_, err := K8sSourceKeyFromResourceAttributes(attrs)
	require.Error(t, err, "container name is required")

	_, err = K8sSourceKeyFromResourceAttributesMap(map[string]string{string(semconv.K8SNamespaceNameKey): "default"})
	require.Error(t, err)
}
