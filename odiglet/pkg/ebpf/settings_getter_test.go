package ebpf

import (
	"testing"

	"github.com/odigos-io/odigos/common/consts"
	"go.opentelemetry.io/otel/attribute"
	corev1 "k8s.io/api/core/v1"
)

func TestAppendContainerImageTag(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{
		{Name: "sidecar", Image: "envoyproxy/envoy:v1.30.0"},
		{Name: "app", Image: "registry.example.com:5000/shop/checkout:2.4.1"},
		{Name: "pinned", Image: "shop/pinned@sha256:959d1739054721c8d389a47ef6a07bb27f5d9d8871f8c3a9c516b1ebf28d1d99"},
	}}}

	got := appendContainerImageTag(nil, pod, "app")
	if len(got) != 1 || got[0] != attribute.String(consts.ContainerImageTagAttribute, "2.4.1") {
		t.Fatalf("app: got %v, want container.image.tag=2.4.1", got)
	}
	if got := appendContainerImageTag(nil, pod, "pinned"); len(got) != 0 {
		t.Fatalf("digest-only image should add no tag, got %v", got)
	}
	if got := appendContainerImageTag(nil, pod, "missing"); len(got) != 0 {
		t.Fatalf("unknown container should add no tag, got %v", got)
	}
}
