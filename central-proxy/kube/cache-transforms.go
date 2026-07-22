package kube

import (
	"fmt"
	"strings"

	"github.com/odigos-io/odigos/api/k8sconsts"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	cacheutils "github.com/odigos-io/odigos/k8sutils/pkg/cache"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Transforms keep only fields needed for cluster snapshots and dirty tracking.
// ResourceVersion is retained so UpdateFunc can ignore no-op resyncs.

func namespacesTransformFunc(obj interface{}) (interface{}, error) {
	ns, ok := obj.(*corev1.Namespace)
	if !ok {
		return nil, fmt.Errorf("expected a Namespace, got %T", obj)
	}

	if cacheutils.IsObjectTransformed(ns) {
		return ns, nil
	}

	minimal := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:            ns.Name,
			ResourceVersion: ns.ResourceVersion,
		},
	}
	cacheutils.MarkObjectAsTransformed(minimal)
	return minimal, nil
}

func deploymentsTransformFunc(obj interface{}) (interface{}, error) {
	deployment, ok := obj.(*appsv1.Deployment)
	if !ok {
		return nil, fmt.Errorf("expected a Deployment, got %T", obj)
	}

	if cacheutils.IsObjectTransformed(deployment) {
		return deployment, nil
	}

	minimal := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       deployment.Namespace,
			Name:            deployment.Name,
			ResourceVersion: deployment.ResourceVersion,
		},
	}
	cacheutils.MarkObjectAsTransformed(minimal)
	return minimal, nil
}

func statefulsetsTransformFunc(obj interface{}) (interface{}, error) {
	statefulset, ok := obj.(*appsv1.StatefulSet)
	if !ok {
		return nil, fmt.Errorf("expected a StatefulSet, got %T", obj)
	}

	if cacheutils.IsObjectTransformed(statefulset) {
		return statefulset, nil
	}

	minimal := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       statefulset.Namespace,
			Name:            statefulset.Name,
			ResourceVersion: statefulset.ResourceVersion,
		},
	}
	cacheutils.MarkObjectAsTransformed(minimal)
	return minimal, nil
}

func daemonsetsTransformFunc(obj interface{}) (interface{}, error) {
	daemonset, ok := obj.(*appsv1.DaemonSet)
	if !ok {
		return nil, fmt.Errorf("expected a DaemonSet, got %T", obj)
	}

	if cacheutils.IsObjectTransformed(daemonset) {
		return daemonset, nil
	}

	minimal := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       daemonset.Namespace,
			Name:            daemonset.Name,
			ResourceVersion: daemonset.ResourceVersion,
		},
	}
	cacheutils.MarkObjectAsTransformed(minimal)
	return minimal, nil
}

func cronjobsTransformFunc(obj interface{}) (interface{}, error) {
	cronjob, ok := obj.(*batchv1.CronJob)
	if !ok {
		return nil, fmt.Errorf("expected a CronJob, got %T", obj)
	}

	if cacheutils.IsObjectTransformed(cronjob) {
		return cronjob, nil
	}

	minimal := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       cronjob.Namespace,
			Name:            cronjob.Name,
			ResourceVersion: cronjob.ResourceVersion,
		},
	}
	cacheutils.MarkObjectAsTransformed(minimal)
	return minimal, nil
}

func deploymentConfigsTransformFunc(obj interface{}) (interface{}, error) {
	deploymentConfig, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("expected a DeploymentConfig, got %T", obj)
	}

	if cacheutils.IsObjectTransformed(deploymentConfig) {
		return deploymentConfig, nil
	}

	minimal := &unstructured.Unstructured{}
	minimal.SetAPIVersion(deploymentConfig.GetAPIVersion())
	minimal.SetKind(deploymentConfig.GetKind())
	minimal.SetNamespace(deploymentConfig.GetNamespace())
	minimal.SetName(deploymentConfig.GetName())
	minimal.SetResourceVersion(deploymentConfig.GetResourceVersion())
	cacheutils.MarkObjectAsTransformed(minimal)
	return minimal, nil
}

func instrumentationConfigsTransformFunc(obj interface{}) (interface{}, error) {
	ic, ok := obj.(*odigosv1.InstrumentationConfig)
	if !ok {
		return nil, fmt.Errorf("expected an InstrumentationConfig, got %T", obj)
	}

	if cacheutils.IsObjectTransformed(ic) {
		return ic, nil
	}

	// Keep only data-stream labels (needed for snapshot dataStreamNames).
	labels := map[string]string{}
	for k, v := range ic.Labels {
		if strings.HasPrefix(k, k8sconsts.SourceDataStreamLabelPrefix) {
			labels[k] = v
		}
	}

	// Keep only language + container name from runtime details.
	runtimeDetails := make([]odigosv1.RuntimeDetailsByContainer, 0, len(ic.Status.RuntimeDetailsByContainer))
	for _, d := range ic.Status.RuntimeDetailsByContainer {
		runtimeDetails = append(runtimeDetails, odigosv1.RuntimeDetailsByContainer{
			ContainerName: d.ContainerName,
			Language:      d.Language,
		})
	}

	minimal := &odigosv1.InstrumentationConfig{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       ic.Namespace,
			Name:            ic.Name,
			ResourceVersion: ic.ResourceVersion,
			Labels:          labels,
		},
		Spec: odigosv1.InstrumentationConfigSpec{
			ServiceName: ic.Spec.ServiceName,
		},
		Status: odigosv1.InstrumentationConfigStatus{
			Conditions:                ic.Status.Conditions,
			RuntimeDetailsByContainer: runtimeDetails,
		},
	}
	cacheutils.MarkObjectAsTransformed(minimal)
	return minimal, nil
}

func sourcesTransformFunc(obj interface{}) (interface{}, error) {
	source, ok := obj.(*odigosv1.Source)
	if !ok {
		return nil, fmt.Errorf("expected a Source, got %T", obj)
	}

	if cacheutils.IsObjectTransformed(source) {
		return source, nil
	}

	minimal := &odigosv1.Source{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       source.Namespace,
			Name:            source.Name,
			ResourceVersion: source.ResourceVersion,
		},
		Spec: odigosv1.SourceSpec{
			Workload:        source.Spec.Workload,
			OtelServiceName: source.Spec.OtelServiceName,
		},
	}
	cacheutils.MarkObjectAsTransformed(minimal)
	return minimal, nil
}
