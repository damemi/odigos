package snapshot

import (
	"context"
	"testing"

	"github.com/odigos-io/central-proxy/internal/logger"
	"github.com/odigos-io/central-proxy/kube"
	odigosfake "github.com/odigos-io/odigos/api/generated/odigos/clientset/versioned/fake"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

func TestGenerateSnapshotIncludesEmptyNamespaces(t *testing.T) {
	logger.InitLogger()

	clientset := kubernetesfake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "empty"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ignored"}},
	)
	cache := kube.NewCache(clientset, odigosfake.NewSimpleClientset())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	if err := cache.Start(ctx); err != nil {
		t.Fatalf("failed to start cache: %v", err)
	}

	got, err := NewGenerator("cluster-id", "cluster-name", cache, []string{"ignored"}).GenerateSnapshot()
	if err != nil {
		t.Fatalf("GenerateSnapshot() error = %v", err)
	}

	if len(got.Namespaces) != 1 {
		t.Fatalf("GenerateSnapshot() returned %d namespaces, want 1", len(got.Namespaces))
	}

	namespace := got.Namespaces[0]
	if namespace.Name != "empty" {
		t.Errorf("namespace name = %q, want %q", namespace.Name, "empty")
	}
	if namespace.Selected {
		t.Error("empty namespace unexpectedly selected")
	}
	if namespace.Workloads == nil {
		t.Error("empty namespace workloads is nil, want an empty list")
	}
	if len(namespace.Workloads) != 0 {
		t.Errorf("empty namespace has %d workloads, want 0", len(namespace.Workloads))
	}
}
