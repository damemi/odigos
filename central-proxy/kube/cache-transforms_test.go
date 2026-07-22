package kube

import (
	"testing"

	"github.com/odigos-io/odigos/api/k8sconsts"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	cacheutils "github.com/odigos-io/odigos/k8sutils/pkg/cache"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestDeploymentsTransformFunc_KeepsIdentityOnly(t *testing.T) {
	replicas := int32(3)
	input := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       "app-ns",
			Name:            "app",
			ResourceVersion: "42",
			Labels:          map[string]string{"app": "web"},
			Annotations:     map[string]string{"large": "payload"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "busybox"}},
				},
			},
		},
	}

	out, err := deploymentsTransformFunc(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	minimal, ok := out.(*appsv1.Deployment)
	if !ok {
		t.Fatalf("expected *appsv1.Deployment, got %T", out)
	}

	if minimal.Namespace != "app-ns" || minimal.Name != "app" || minimal.ResourceVersion != "42" {
		t.Fatalf("identity fields not preserved: %+v", minimal.ObjectMeta)
	}
	if minimal.Spec.Template.Spec.Containers != nil {
		t.Fatalf("expected pod template to be stripped, got %+v", minimal.Spec.Template)
	}
	if minimal.Labels != nil {
		t.Fatalf("expected labels stripped")
	}
	if !cacheutils.IsObjectTransformed(minimal) {
		t.Fatalf("expected object to be marked transformed")
	}

	// Second transform should be a no-op identity return.
	again, err := deploymentsTransformFunc(minimal)
	if err != nil {
		t.Fatalf("unexpected error on second transform: %v", err)
	}
	if again != minimal {
		t.Fatalf("expected idempotent transform to return same pointer")
	}
}

func TestDeploymentConfigsTransformFunc_KeepsIdentityOnly(t *testing.T) {
	input := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apps.openshift.io/v1",
		"kind":       "DeploymentConfig",
		"metadata": map[string]interface{}{
			"namespace":       "app-ns",
			"name":            "app",
			"resourceVersion": "42",
			"labels":          map[string]interface{}{"app": "web"},
		},
		"spec": map[string]interface{}{
			"replicas": int64(3),
		},
	}}

	out, err := deploymentConfigsTransformFunc(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	minimal, ok := out.(*unstructured.Unstructured)
	if !ok {
		t.Fatalf("expected *unstructured.Unstructured, got %T", out)
	}
	if minimal.GetAPIVersion() != "apps.openshift.io/v1" || minimal.GetKind() != "DeploymentConfig" {
		t.Fatalf("group/version/kind not preserved: %s %s", minimal.GetAPIVersion(), minimal.GetKind())
	}
	if minimal.GetNamespace() != "app-ns" || minimal.GetName() != "app" || minimal.GetResourceVersion() != "42" {
		t.Fatalf("identity fields not preserved: %+v", minimal.Object)
	}
	if _, ok := minimal.Object["spec"]; ok {
		t.Fatalf("expected spec stripped, got %+v", minimal.Object["spec"])
	}
	if minimal.GetLabels() != nil {
		t.Fatal("expected labels stripped")
	}
	if !cacheutils.IsObjectTransformed(minimal) {
		t.Fatal("expected object to be marked transformed")
	}

	again, err := deploymentConfigsTransformFunc(minimal)
	if err != nil {
		t.Fatalf("unexpected error on second transform: %v", err)
	}
	if again != minimal {
		t.Fatal("expected idempotent transform to return same pointer")
	}
}

func TestInstrumentationConfigsTransformFunc_KeepsSnapshotFields(t *testing.T) {
	input := &odigosv1.InstrumentationConfig{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       "app-ns",
			Name:            "deployment-app",
			ResourceVersion: "7",
		},
		Spec: odigosv1.InstrumentationConfigSpec{
			ServiceName: "checkout",
		},
		Status: odigosv1.InstrumentationConfigStatus{
			Conditions: []metav1.Condition{{
				Type:    odigosv1.RuntimeDetectionStatusConditionType,
				Status:  metav1.ConditionTrue,
				Reason:  string(odigosv1.RuntimeDetectionReasonDetectedSuccessfully),
				Message: "ok",
			}},
		},
	}

	out, err := instrumentationConfigsTransformFunc(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	minimal := out.(*odigosv1.InstrumentationConfig)
	if minimal.Spec.ServiceName != "checkout" {
		t.Fatalf("expected ServiceName preserved, got %q", minimal.Spec.ServiceName)
	}
	if len(minimal.Status.Conditions) != 1 {
		t.Fatalf("expected conditions preserved")
	}
}

func TestSourcesTransformFunc_KeepsWorkload(t *testing.T) {
	input := &odigosv1.Source{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       "app-ns",
			Name:            "ns-source",
			ResourceVersion: "9",
		},
		Spec: odigosv1.SourceSpec{
			Workload: k8sconsts.PodWorkload{
				Name:      "app-ns",
				Namespace: "app-ns",
				Kind:      k8sconsts.WorkloadKindNamespace,
			},
			OtelServiceName: "my-service",
		},
	}

	out, err := sourcesTransformFunc(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	minimal := out.(*odigosv1.Source)
	if minimal.Spec.Workload.Kind != k8sconsts.WorkloadKindNamespace || minimal.Spec.Workload.Name != "app-ns" {
		t.Fatalf("expected workload preserved, got %+v", minimal.Spec.Workload)
	}
	if minimal.Spec.OtelServiceName != "my-service" {
		t.Fatalf("expected otelServiceName preserved, got %q", minimal.Spec.OtelServiceName)
	}
}
