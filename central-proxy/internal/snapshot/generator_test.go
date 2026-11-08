package snapshot

import (
	"context"
	"testing"

	"github.com/odigos-io/central-proxy/internal/logger"
	"github.com/odigos-io/central-proxy/kube"
	odigosfake "github.com/odigos-io/odigos/api/generated/odigos/clientset/versioned/fake"
	"github.com/odigos-io/odigos/api/k8sconsts"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

func TestGenerateSnapshotIncludesEmptyNamespaces(t *testing.T) {
	logger.InitLogger()

	clientset := kubernetesfake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "empty"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ignored"}},
	)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cache := kube.NewCache(ctx, &kube.Client{
		Interface:    clientset,
		OdigosClient: odigosfake.NewSimpleClientset(),
	})

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

func TestGenerateSnapshotIncludesDeploymentConfigs(t *testing.T) {
	logger.InitLogger()

	clientset := kubernetesfake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ignored"}},
	)
	dynamicClient := dynamicfake.NewSimpleDynamicClient(
		runtime.NewScheme(),
		newDeploymentConfig("app", "checkout"),
		newDeploymentConfig("ignored", "system"),
	)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cache := kube.NewCache(ctx, &kube.Client{
		Interface:     clientset,
		OdigosClient:  odigosfake.NewSimpleClientset(),
		DynamicClient: dynamicClient,
	})

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
	workloads := got.Namespaces[0].Workloads
	if len(workloads) != 1 {
		t.Fatalf("DeploymentConfig namespace has %d workloads, want 1", len(workloads))
	}
	if workloads[0].Namespace != "app" || workloads[0].Name != "checkout" || workloads[0].Kind != "DeploymentConfig" {
		t.Errorf("unexpected DeploymentConfig snapshot: %+v", workloads[0])
	}
}

func TestGenerateSnapshotMarksDisabledWorkloads(t *testing.T) {
	logger.InitLogger()

	clientset := kubernetesfake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "checkout"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "cart"}},
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "checkout"}},
	)
	odigosClient := odigosfake.NewSimpleClientset(
		// The namespace is auto-instrumented, but checkout's own Source excludes it.
		newSource("app", "app", k8sconsts.WorkloadKindNamespace, false),
		newSource("app", "checkout", k8sconsts.WorkloadKindDeployment, true),
		newSource("app", "cart", k8sconsts.WorkloadKindDeployment, false),
	)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cache := kube.NewCache(ctx, &kube.Client{
		Interface:    clientset,
		OdigosClient: odigosClient,
	})

	if err := cache.Start(ctx); err != nil {
		t.Fatalf("failed to start cache: %v", err)
	}

	got, err := NewGenerator("cluster-id", "cluster-name", cache, nil).GenerateSnapshot()
	if err != nil {
		t.Fatalf("GenerateSnapshot() error = %v", err)
	}

	if len(got.Namespaces) != 1 {
		t.Fatalf("GenerateSnapshot() returned %d namespaces, want 1", len(got.Namespaces))
	}
	disabled := map[string]bool{}
	for _, w := range got.Namespaces[0].Workloads {
		disabled[w.Kind+"/"+w.Name] = w.Disabled
	}
	want := map[string]bool{"Deployment/checkout": true, "Deployment/cart": false, "StatefulSet/checkout": false}
	for key, wantDisabled := range want {
		gotDisabled, ok := disabled[key]
		if !ok {
			t.Errorf("workload %s missing from snapshot", key)
			continue
		}
		if gotDisabled != wantDisabled {
			t.Errorf("workload %s disabled = %v, want %v", key, gotDisabled, wantDisabled)
		}
	}
}

func newSource(namespace, name string, kind k8sconsts.WorkloadKind, disableInstrumentation bool) *odigosv1.Source {
	return &odigosv1.Source{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: string(kind) + "-" + name},
		Spec: odigosv1.SourceSpec{
			Workload:               k8sconsts.PodWorkload{Namespace: namespace, Name: name, Kind: kind},
			DisableInstrumentation: disableInstrumentation,
		},
	}
}

func newDeploymentConfig(namespace, name string) *unstructured.Unstructured {
	deploymentConfig := &unstructured.Unstructured{}
	deploymentConfig.SetAPIVersion("apps.openshift.io/v1")
	deploymentConfig.SetKind("DeploymentConfig")
	deploymentConfig.SetNamespace(namespace)
	deploymentConfig.SetName(name)
	return deploymentConfig
}
