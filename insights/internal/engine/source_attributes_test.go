package engine

import (
	"testing"

	"github.com/odigos-io/odigos/common/consts"
	"github.com/odigos-io/odigos/insights/internal/engine/semconv"
)

func TestSourceResourceAttributes_KubernetesNormalized(t *testing.T) {
	got := sourceResourceAttributes(map[string]string{
		semconv.K8SNamespaceName:    "shop",
		"k8s.deployment.name":       "checkout",
		semconv.K8SContainerName:    "app",
		semconv.K8SPodName:          "checkout-7f9c",
		semconv.ServiceName:         "checkout",
		semconv.TelemetryDistroName: javaEbpfDistroName,
	})
	want := map[string]string{
		semconv.K8SNamespaceName:           "shop",
		consts.OdigosWorkloadKindAttribute: "Deployment",
		consts.OdigosWorkloadNameAttribute: "checkout",
		semconv.K8SContainerName:           "app",
	}
	if !sourceAttributesEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestSourceResourceAttributes_PrefersOdigosWorkload(t *testing.T) {
	got := sourceResourceAttributes(map[string]string{
		semconv.K8SNamespaceName:           "shop",
		consts.OdigosWorkloadKindAttribute: "Rollout",
		consts.OdigosWorkloadNameAttribute: "checkout",
		"k8s.deployment.name":              "checkout-canary",
		semconv.K8SContainerName:           "app",
	})
	if got[consts.OdigosWorkloadKindAttribute] != "Rollout" || got[consts.OdigosWorkloadNameAttribute] != "checkout" {
		t.Fatalf("got %#v", got)
	}
}

func TestSourceResourceAttributes_HostFallback(t *testing.T) {
	got := sourceResourceAttributes(map[string]string{
		semconv.HostName:          "vm-1",
		semconv.ServiceInstanceID: "i-abc",
		semconv.K8SNamespaceName:  "shop", // incomplete k8s identity
	})
	want := map[string]string{
		semconv.HostName:          "vm-1",
		semconv.ServiceInstanceID: "i-abc",
	}
	if !sourceAttributesEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestSourceResourceAttributes_Empty(t *testing.T) {
	if got := sourceResourceAttributes(nil); got != nil {
		t.Fatalf("nil attrs: %#v", got)
	}
	if got := sourceResourceAttributes(map[string]string{semconv.ServiceName: "only-service"}); got != nil {
		t.Fatalf("no source keys: %#v", got)
	}
}
