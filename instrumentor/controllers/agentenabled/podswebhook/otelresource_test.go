package podswebhook

import (
	"strings"
	"testing"

	"github.com/odigos-io/odigos/api/k8sconsts"
	corev1 "k8s.io/api/core/v1"
)

func TestInjectOtelResourceAttributesIncludesImageTag(t *testing.T) {
	pw := k8sconsts.PodWorkload{Namespace: "shop", Kind: k8sconsts.WorkloadKindDeployment, Name: "checkout"}

	container := &corev1.Container{Name: "app", Image: "registry.example.com:5000/shop/checkout:2.4.1"}
	InjectOtelResourceAndServiceNameEnvVars(EnvVarNamesMap{}, container, "", pw, "checkout", nil)
	if value := envValue(container, otelResourceAttributesEnvVarName); !strings.Contains(value, "container.image.tag=2.4.1") {
		t.Fatalf("OTEL_RESOURCE_ATTRIBUTES = %q, want container.image.tag=2.4.1", value)
	}

	pinned := &corev1.Container{Name: "app", Image: "shop/checkout@sha256:959d1739054721c8d389a47ef6a07bb27f5d9d8871f8c3a9c516b1ebf28d1d99"}
	InjectOtelResourceAndServiceNameEnvVars(EnvVarNamesMap{}, pinned, "", pw, "checkout", nil)
	if value := envValue(pinned, otelResourceAttributesEnvVarName); strings.Contains(value, "container.image.tag") {
		t.Fatalf("digest-only image should add no tag, got %q", value)
	}
}

func envValue(container *corev1.Container, name string) string {
	for _, env := range container.Env {
		if env.Name == name {
			return env.Value
		}
	}
	return ""
}
