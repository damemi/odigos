package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	argorolloutsv1alpha1 "github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"
	"github.com/gin-gonic/gin"
	"github.com/odigos-io/odigos/api/k8sconsts"
	"github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common/consts"
	"github.com/odigos-io/odigos/frontend/graph/model"
	"github.com/odigos-io/odigos/frontend/kube"
	"github.com/odigos-io/odigos/frontend/services/common"
	"github.com/odigos-io/odigos/k8sutils/pkg/client"
	"github.com/odigos-io/odigos/k8sutils/pkg/workload"
	openshiftappsv1 "github.com/openshift/api/apps/v1"
	"golang.org/x/sync/errgroup"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

const (
	WorkloadKindNamespace        model.K8sResourceKind = "Namespace"
	WorkloadKindDeployment       model.K8sResourceKind = "Deployment"
	WorkloadKindStatefulSet      model.K8sResourceKind = "StatefulSet"
	WorkloadKindDaemonSet        model.K8sResourceKind = "DaemonSet"
	WorkloadKindStaticPod        model.K8sResourceKind = "StaticPod"
	WorkloadKindCronJob          model.K8sResourceKind = "CronJob"
	WorkloadKindDeploymentConfig model.K8sResourceKind = "DeploymentConfig"
	// WorkloadKindArgoRollout represents Argo Rollouts workload.
	// Note: The actual Kubernetes resource has kind "Rollout" (not "ArgoRollout"):
	//   apiVersion: argoproj.io/v1alpha1
	//   kind: Rollout
	// We use "ArgoRollout" in the variable name to distinguish it from other rollout concepts.
	WorkloadKindArgoRollout model.K8sResourceKind = "Rollout"
)

type InstanceCounts struct {
	TotalInstances   int
	HealthyInstances int
}

func GetWorkloadsInNamespace(ctx context.Context, nsName string) ([]model.K8sActualSource, error) {
	namespace, err := kube.DefaultClient.CoreV1().Namespaces().Get(ctx, nsName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	g, ctx := errgroup.WithContext(ctx)
	var (
		deps          []model.K8sActualSource
		statefuls     []model.K8sActualSource
		daemons       []model.K8sActualSource
		crons         []model.K8sActualSource
		deployConfigs []model.K8sActualSource
		rollouts      []model.K8sActualSource
	)

	g.Go(func() error {
		var err error
		deps, err = getDeployments(ctx, *namespace)
		return err
	})

	g.Go(func() error {
		var err error
		statefuls, err = getStatefulSets(ctx, *namespace)
		return err
	})

	g.Go(func() error {
		var err error
		daemons, err = getDaemonSets(ctx, *namespace)
		return err
	})

	g.Go(func() error {
		var err error
		crons, err = getCronJobs(ctx, *namespace)
		return err
	})

	g.Go(func() error {
		// Only try to get DeploymentConfigs if they're available
		if !kube.IsDeploymentConfigAvailable() {
			deployConfigs = []model.K8sActualSource{}
			return nil
		}
		var err error
		deployConfigs, err = getDeploymentConfigs(ctx, *namespace)
		return err
	})

	g.Go(func() error {
		if !kube.IsArgoRolloutAvailable {
			rollouts = []model.K8sActualSource{}
			return nil
		}
		var err error
		rollouts, err = getRollouts(ctx, *namespace)
		return err
	})

	if err := g.Wait(); err != nil {
		return nil, err
	}

	items := make([]model.K8sActualSource, len(deps)+len(statefuls)+len(daemons)+len(crons)+len(deployConfigs)+len(rollouts))
	copy(items, deps)
	copy(items[len(deps):], statefuls)
	copy(items[len(deps)+len(statefuls):], daemons)
	copy(items[len(deps)+len(statefuls)+len(daemons):], crons)
	copy(items[len(deps)+len(statefuls)+len(daemons)+len(crons):], deployConfigs)
	copy(items[len(deps)+len(statefuls)+len(daemons)+len(crons)+len(deployConfigs):], rollouts)

	return items, nil
}

func getDeployments(ctx context.Context, namespace corev1.Namespace) ([]model.K8sActualSource, error) {
	var response []model.K8sActualSource
	err := client.ListWithPages(client.DefaultPageSize, kube.DefaultClient.AppsV1().Deployments(namespace.Name).List, ctx, &metav1.ListOptions{}, func(deps *appsv1.DeploymentList) error {
		for _, dep := range deps.Items {
			numberOfInstances := int(dep.Status.ReadyReplicas)
			response = append(response, model.K8sActualSource{
				Namespace:         dep.Namespace,
				Name:              dep.Name,
				Kind:              WorkloadKindDeployment,
				NumberOfInstances: &numberOfInstances,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return response, nil
}

func getDaemonSets(ctx context.Context, namespace corev1.Namespace) ([]model.K8sActualSource, error) {
	var response []model.K8sActualSource
	err := client.ListWithPages(client.DefaultPageSize, kube.DefaultClient.AppsV1().DaemonSets(namespace.Name).List, ctx, &metav1.ListOptions{}, func(dss *appsv1.DaemonSetList) error {
		for _, ds := range dss.Items {
			numberOfInstances := int(ds.Status.NumberReady)
			response = append(response, model.K8sActualSource{
				Namespace:         ds.Namespace,
				Name:              ds.Name,
				Kind:              WorkloadKindDaemonSet,
				NumberOfInstances: &numberOfInstances,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return response, nil
}

func getStatefulSets(ctx context.Context, namespace corev1.Namespace) ([]model.K8sActualSource, error) {
	var response []model.K8sActualSource
	err := client.ListWithPages(client.DefaultPageSize, kube.DefaultClient.AppsV1().StatefulSets(namespace.Name).List, ctx, &metav1.ListOptions{}, func(sss *appsv1.StatefulSetList) error {
		for _, ss := range sss.Items {
			numberOfInstances := int(ss.Status.ReadyReplicas)
			response = append(response, model.K8sActualSource{
				Namespace:         ss.Namespace,
				Name:              ss.Name,
				Kind:              WorkloadKindStatefulSet,
				NumberOfInstances: &numberOfInstances,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return response, nil
}

func getCronJobs(ctx context.Context, namespace corev1.Namespace) ([]model.K8sActualSource, error) {
	var response []model.K8sActualSource

	err := client.ListWithPages(client.DefaultPageSize, kube.DefaultClient.BatchV1().CronJobs(namespace.Name).List, ctx, &metav1.ListOptions{}, func(cjs *batchv1.CronJobList) error {
		for _, cj := range cjs.Items {
			numberOfInstances := len(cj.Status.Active)
			response = append(response, model.K8sActualSource{
				Namespace:         cj.Namespace,
				Name:              cj.Name,
				Kind:              WorkloadKindCronJob,
				NumberOfInstances: &numberOfInstances,
			})
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	return response, nil
}

func getDeploymentConfigs(ctx context.Context, namespace corev1.Namespace) ([]model.K8sActualSource, error) {
	var response []model.K8sActualSource

	// Use dynamic client for DeploymentConfigs
	dcClient := kube.DefaultClient.DynamicClient.Resource(schema.GroupVersionResource{
		Group:    "apps.openshift.io",
		Version:  "v1",
		Resource: "deploymentconfigs",
	}).Namespace(namespace.Name)

	// List all DeploymentConfigs in the namespace
	dcList, err := dcClient.List(ctx, metav1.ListOptions{})
	if err != nil {
		// If DeploymentConfigs API is not available (not OpenShift) or we don't have permission, just return empty list
		if apierrors.IsNotFound(err) || apierrors.IsMethodNotSupported(err) || apierrors.IsForbidden(err) {
			return response, nil
		}
		return nil, err
	}

	// Convert unstructured to typed
	for _, dcUnstructured := range dcList.Items {
		var dc openshiftappsv1.DeploymentConfig
		err := kube.Scheme.Convert(&dcUnstructured, &dc, nil)
		if err != nil {
			// If conversion fails, skip this item
			continue
		}

		numberOfInstances := int(dc.Status.AvailableReplicas)
		response = append(response, model.K8sActualSource{
			Namespace:         dc.Namespace,
			Name:              dc.Name,
			Kind:              WorkloadKindDeploymentConfig,
			NumberOfInstances: &numberOfInstances,
		})
	}

	return response, nil
}

func getRollouts(ctx context.Context, namespace corev1.Namespace) ([]model.K8sActualSource, error) {
	var response []model.K8sActualSource

	rolloutClient := kube.DefaultClient.DynamicClient.Resource(schema.GroupVersionResource{
		Group:    "argoproj.io",
		Version:  "v1alpha1",
		Resource: "rollouts",
	}).Namespace(namespace.Name)

	// List all Rollouts in the namespace
	rolloutList, err := rolloutClient.List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsMethodNotSupported(err) || apierrors.IsForbidden(err) {
			return response, nil
		}
		return nil, err
	}

	// Convert unstructured to typed
	for _, rolloutUnstructured := range rolloutList.Items {
		var rollout argorolloutsv1alpha1.Rollout
		err := kube.Scheme.Convert(&rolloutUnstructured, &rollout, nil)
		if err != nil {
			// If conversion fails, skip this item
			continue
		}

		numberOfInstances := int(rollout.Status.AvailableReplicas)
		response = append(response, model.K8sActualSource{
			Namespace:         rollout.Namespace,
			Name:              rollout.Name,
			Kind:              WorkloadKindArgoRollout,
			NumberOfInstances: &numberOfInstances,
		})
	}

	return response, nil
}

func RolloutRestartWorkload(ctx context.Context, namespace string, name string, kind model.K8sResourceKind) error {
	now := time.Now().Format(time.RFC3339)

	switch kind {
	case WorkloadKindDeployment:
		patchData := []byte(fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"%s"}}}}}`, now))
		_, err := kube.DefaultClient.AppsV1().Deployments(namespace).Patch(ctx, name, types.StrategicMergePatchType, patchData, metav1.PatchOptions{})
		if err != nil {
			return fmt.Errorf("failed to restart deployment: %w", err)
		}

	case WorkloadKindStatefulSet:
		patchData := []byte(fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"%s"}}}}}`, now))
		_, err := kube.DefaultClient.AppsV1().StatefulSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, patchData, metav1.PatchOptions{})
		if err != nil {
			return fmt.Errorf("failed to restart statefulset: %w", err)
		}

	case WorkloadKindDaemonSet:
		patchData := []byte(fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"%s"}}}}}`, now))
		_, err := kube.DefaultClient.AppsV1().DaemonSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, patchData, metav1.PatchOptions{})
		if err != nil {
			return fmt.Errorf("failed to restart daemonset: %w", err)
		}

	case WorkloadKindCronJob:
		// CronJobs do not support rolling restarts.
		// We return nil here to prevent an error, as this is a no-op.
		return nil

	case WorkloadKindDeploymentConfig:
		// Check if DeploymentConfig is available first
		if !kube.IsDeploymentConfigAvailable() {
			return fmt.Errorf("deploymentconfig resources are not available in this cluster")
		}

		// Use dynamic client for DeploymentConfig
		dcClient := kube.DefaultClient.DynamicClient.Resource(schema.GroupVersionResource{
			Group:    "apps.openshift.io",
			Version:  "v1",
			Resource: "deploymentconfigs",
		}).Namespace(namespace)

		patchData := []byte(fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"%s"}}}}}`, now))
		_, err := dcClient.Patch(ctx, name, types.MergePatchType, patchData, metav1.PatchOptions{})
		if err != nil {
			return fmt.Errorf("failed to restart deploymentconfig: %w", err)
		}

	case WorkloadKindArgoRollout:
		if !kube.IsArgoRolloutAvailable {
			return fmt.Errorf("argo rollouts resources are not available in this cluster")
		}

		rolloutClient := kube.DefaultClient.DynamicClient.Resource(schema.GroupVersionResource{
			Group:    "argoproj.io",
			Version:  "v1alpha1",
			Resource: "rollouts",
		}).Namespace(namespace)

		// Argo Rollouts use spec.restartAt field for restarting, not pod template annotations
		// https://github.com/argoproj/argo-rollouts/blob/cb1c33df7a2c2b1c2ed31b1ee0aa22621ef5577c/utils/replicaset/replicaset.go#L223-L232
		rolloutPatch := []byte(fmt.Sprintf(`{"spec":{"restartAt":"%s"}}`, now))
		_, err := rolloutClient.Patch(ctx, name, types.MergePatchType, rolloutPatch, metav1.PatchOptions{})
		if err != nil {
			return fmt.Errorf("failed to patch rollout: %w", err)
		}

	case WorkloadKindStaticPod:
		return errors.New("static pods can't be restarted")

	default:
		return fmt.Errorf("unsupported kind: %s (must be Deployment, StatefulSet, DaemonSet, CronJob, DeploymentConfig or Rollout)", kind)
	}

	return nil
}

func GetSourceCRD(ctx context.Context, nsName string, workloadName string, workloadKind model.K8sResourceKind) (*v1alpha1.Source, error) {
	sourceList := &v1alpha1.SourceList{}
	sourceList, err := kube.DefaultClient.OdigosClient.Sources(nsName).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{
			k8sconsts.WorkloadNamespaceLabel: nsName,
			k8sconsts.WorkloadNameLabel:      workloadName,
			k8sconsts.WorkloadKindLabel:      string(workloadKind),
		}).String(),
	})
	if err != nil {
		return nil, err
	}
	if len(sourceList.Items) == 0 {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "source"}, workloadName)
	}
	if len(sourceList.Items) > 1 {
		return nil, fmt.Errorf(`expected to get 1 source "%s", got %d`, workloadName, len(sourceList.Items))
	}

	return &sourceList.Items[0], nil
}

func DeleteSourceWithAPI(c *gin.Context) {
	toggleSourceWithAPI(c, false)
}

func CreateSourceWithAPI(c *gin.Context) {
	toggleSourceWithAPI(c, true)
}

func toggleSourceWithAPI(c *gin.Context, enabled bool) {
	ctx := c.Request.Context()
	ns := c.Param("namespace")
	name := c.Param("name")
	kind := c.Param("kind")
	wk, ok := stringToWorkloadKind(kind)
	if !ok {
		c.JSON(400, gin.H{
			"message": fmt.Sprintf("invalid kind: %s", kind),
		})
		return
	}

	// TODO: check if we need to handle a stream name for remote API requests
	err := ToggleSourceCRD(ctx, ns, name, wk, enabled, "default")
	if err != nil {
		c.JSON(500, gin.H{
			"message": err.Error(),
		})
		return
	}

	c.JSON(200, gin.H{
		"message": "toggle successful",
	})
}

func stringToWorkloadKind(workloadKind string) (model.K8sResourceKind, bool) {
	switch strings.ToLower(workloadKind) {
	case "namespace":
		return WorkloadKindNamespace, true
	case "deployment":
		return WorkloadKindDeployment, true
	case "statefulset":
		return WorkloadKindStatefulSet, true
	case "daemonset":
		return WorkloadKindDaemonSet, true
	case "cronjob":
		return WorkloadKindCronJob, true
	case "rollout":
		return WorkloadKindArgoRollout, true
	case "staticpod":
		return WorkloadKindStaticPod, true
	}

	return "", false
}

func validateSourceKind(ctx context.Context, workloadKind model.K8sResourceKind) error {
	switch workloadKind {
	// Namespace is not a workload, but we need it to "select future apps" by creating a Source CRD for it
	case WorkloadKindNamespace, WorkloadKindDeployment, WorkloadKindStatefulSet, WorkloadKindDaemonSet, WorkloadKindCronJob, WorkloadKindDeploymentConfig, WorkloadKindArgoRollout, WorkloadKindStaticPod:
		break
	default:
		return errors.New("unsupported workload kind: " + string(workloadKind))
	}

	if workloadKind == WorkloadKindStaticPod {
		config := GetConfig(ctx)
		if config.Tier != model.TierOnprem {
			return fmt.Errorf("StaticPod instrumentation is an enterprise (on-prem) feature")
		}
	}

	return nil
}

func dataStreamLabelKey(currentStreamName string) string {
	if currentStreamName == "" {
		return ""
	}
	return k8sconsts.SourceDataStreamLabelPrefix + currentStreamName
}

func EnsureSourceCRD(ctx context.Context, nsName string, workloadName string, workloadKind model.K8sResourceKind, currentStreamName string) (*v1alpha1.Source, error) {
	streamLabel := dataStreamLabelKey(currentStreamName)

	if err := validateSourceKind(ctx, workloadKind); err != nil {
		return nil, err
	}

	source, err := GetSourceCRD(ctx, nsName, workloadName, workloadKind)
	if err != nil && !apierrors.IsNotFound(err) {
		// unexpected error occurred while trying to get the source
		return nil, err
	}

	if source != nil && source.Spec.DisableInstrumentation && streamLabel != "" {
		// a disabled source rejoins only the current stream, not the streams it was in before it was disabled
		labels := map[string]any{streamLabel: "true"}
		for labelKey, labelValue := range source.Labels {
			if labelKey != streamLabel && strings.HasPrefix(labelKey, k8sconsts.SourceDataStreamLabelPrefix) && labelValue == "true" {
				labels[labelKey] = nil
			}
		}
		return mergePatchSourceCRD(ctx, nsName, source.Name, map[string]any{
			"metadata": map[string]any{"labels": labels},
			"spec":     map[string]any{common.DisableInstrumentationJsonKey: false},
		})
	}

	if source != nil {
		// source already exists, do not create a new one, instead update so it's not disabled anymore
		source, err = UpdateSourceCRDSpec(ctx, nsName, source.Name, common.DisableInstrumentationJsonKey, false)
		if err != nil {
			return nil, err
		}
		if streamLabel != "" {
			source, err = UpdateSourceCRDLabel(ctx, nsName, source.Name, streamLabel, "true")
			if err != nil {
				return nil, err
			}
		}
		return source, nil
	}

	return createSourceCRD(ctx, nsName, workloadName, workloadKind, streamLabels(streamLabel, "true"), false)
}

func streamLabels(streamLabel string, value string) map[string]string {
	if streamLabel == "" {
		return nil
	}
	return map[string]string{streamLabel: value}
}

func createSourceCRD(ctx context.Context, nsName string, workloadName string, workloadKind model.K8sResourceKind, labels map[string]string, disableInstrumentation bool) (*v1alpha1.Source, error) {
	newSource := &v1alpha1.Source{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "source-",
			Labels:       labels,
		},
		Spec: v1alpha1.SourceSpec{
			Workload: k8sconsts.PodWorkload{
				Namespace: nsName,
				Name:      workloadName,
				Kind:      k8sconsts.WorkloadKind(workloadKind),
			},
			DisableInstrumentation: disableInstrumentation,
		},
	}

	return CreateResourceWithGenerateName(ctx, func() (*v1alpha1.Source, error) {
		return kube.DefaultClient.OdigosClient.Sources(nsName).Create(ctx, newSource, metav1.CreateOptions{})
	})
}

// createDisabledSourceCRD keeps the current stream label "true": the instrumentor webhook requires one "true"
// data-stream label and would otherwise add the default stream. The workload is excluded by disableInstrumentation.
func createDisabledSourceCRD(ctx context.Context, nsName string, workloadName string, workloadKind model.K8sResourceKind, currentStreamName string) error {
	if err := validateSourceKind(ctx, workloadKind); err != nil {
		return err
	}
	_, err := createSourceCRD(ctx, nsName, workloadName, workloadKind, streamLabels(dataStreamLabelKey(currentStreamName), "true"), true)
	return err
}

// excludeFromStreamLabels excludes the workload from the current stream while other streams still select it.
// The instrumentor webhook requires one "true" data-stream label and would otherwise add the default stream,
// so one of the other streams is set when the source has none of its own.
func excludeFromStreamLabels(source *v1alpha1.Source, streamLabel string, otherStreams []string) map[string]string {
	labels := map[string]string{streamLabel: "false"}
	if source != nil {
		for labelKey, labelValue := range source.Labels {
			if labelKey != streamLabel && strings.HasPrefix(labelKey, k8sconsts.SourceDataStreamLabelPrefix) && labelValue == "true" {
				return labels
			}
		}
	}
	labels[dataStreamLabelKey(slices.Min(otherStreams))] = "true"
	return labels
}

// deleteSourceCRD removes the current data-stream from a source. A workload whose namespace is
// selected keeps a source that excludes it, anything else is deleted unless other streams still use it.
// namespaceSelected overrides the namespace source lookup for a namespace changed in the same request.
func deleteSourceCRD(ctx context.Context, nsName string, workloadName string, workloadKind model.K8sResourceKind, currentStreamName string, namespaceSelected *bool) error {
	source, err := GetSourceCRD(ctx, nsName, workloadName, workloadKind)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	// deselecting a disabled workload keeps it disabled, only disableInstrumentation: false removes its source
	if source != nil && source.Spec.DisableInstrumentation && workloadKind != WorkloadKindNamespace {
		return nil
	}

	// check for namespace source first
	var nsSource *v1alpha1.Source
	hasNamespace := false
	if workloadKind != WorkloadKindNamespace {
		var nsErr error
		nsSource, nsErr = GetSourceCRD(ctx, nsName, nsName, WorkloadKindNamespace)
		if nsErr != nil && !apierrors.IsNotFound(nsErr) {
			return nsErr
		}
		hasNamespace = nsSource != nil
		if namespaceSelected != nil {
			hasNamespace = *namespaceSelected
			if !hasNamespace {
				// the namespace is leaving the current stream, so its streams no longer apply to this workload
				nsSource = nil
			}
		}
	}

	streamLabel := dataStreamLabelKey(currentStreamName)

	// the data-streams that still select the workload once the current one is removed
	otherStreams := []string{}
	if streamLabel != "" {
		withCurrentStream := &v1alpha1.Source{}
		if source != nil {
			withCurrentStream = source.DeepCopy()
		}
		if withCurrentStream.Labels == nil {
			withCurrentStream.Labels = map[string]string{}
		}
		withCurrentStream.Labels[streamLabel] = "true"
		for _, name := range ExtractDataStreamsFromSource(withCurrentStream, nsSource) {
			if *name != currentStreamName {
				otherStreams = append(otherStreams, *name)
			}
		}
	}
	inOtherStreams := len(otherStreams) > 0

	if source == nil {
		if !hasNamespace {
			return nil
		}
		// create the excluding source directly, so the workload is never selected in between
		if !inOtherStreams {
			return createDisabledSourceCRD(ctx, nsName, workloadName, workloadKind, currentStreamName)
		}
		if err := validateSourceKind(ctx, workloadKind); err != nil {
			return err
		}
		_, err := createSourceCRD(ctx, nsName, workloadName, workloadKind, excludeFromStreamLabels(nil, streamLabel, otherStreams), false)
		return err
	}

	if hasNamespace {
		if !inOtherStreams {
			// we add "DisableInstrumentation" to the source
			_, err := UpdateSourceCRDSpec(ctx, nsName, source.Name, common.DisableInstrumentationJsonKey, true)
			return err
		}
		labels := map[string]any{}
		for labelKey, labelValue := range excludeFromStreamLabels(source, streamLabel, otherStreams) {
			labels[labelKey] = labelValue
		}
		_, err := mergePatchSourceCRD(ctx, nsName, source.Name, map[string]any{"metadata": map[string]any{"labels": labels}})
		return err
	}

	// namespace source does not exist.
	// we need to delete the workload source,
	// or remove the relevant data-stream label (if source is in multiple streams)
	if inOtherStreams {
		if _, ok := source.Labels[streamLabel]; !ok {
			return nil
		}
		_, err := UpdateSourceCRDLabel(ctx, nsName, source.Name, streamLabel, "")
		return err
	}

	return kube.DefaultClient.OdigosClient.Sources(nsName).Delete(ctx, source.Name, metav1.DeleteOptions{})
}

// disableSourceCRD creates the workload source already disabled, or disables the existing one.
func disableSourceCRD(ctx context.Context, nsName string, workloadName string, workloadKind model.K8sResourceKind, currentStreamName string) error {
	source, err := GetSourceCRD(ctx, nsName, workloadName, workloadKind)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	if source == nil {
		return createDisabledSourceCRD(ctx, nsName, workloadName, workloadKind, currentStreamName)
	}
	if source.Spec.DisableInstrumentation {
		return nil
	}
	_, err = UpdateSourceCRDSpec(ctx, nsName, source.Name, common.DisableInstrumentationJsonKey, true)
	return err
}

// removeWorkloadSourceCRD removes the workload's own source, so it follows its namespace source again.
// An enabled source that other data-streams still select only loses the current stream.
func removeWorkloadSourceCRD(ctx context.Context, nsName string, workloadName string, workloadKind model.K8sResourceKind, currentStreamName string) error {
	source, err := GetSourceCRD(ctx, nsName, workloadName, workloadKind)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}

	streamLabel := dataStreamLabelKey(currentStreamName)
	if !source.Spec.DisableInstrumentation && streamLabel != "" {
		for labelKey, labelValue := range source.Labels {
			if labelKey != streamLabel && strings.HasPrefix(labelKey, k8sconsts.SourceDataStreamLabelPrefix) && labelValue == "true" {
				if _, ok := source.Labels[streamLabel]; ok {
					_, err = UpdateSourceCRDLabel(ctx, nsName, source.Name, streamLabel, "")
				}
				return err
			}
		}
	}

	return kube.DefaultClient.OdigosClient.Sources(nsName).Delete(ctx, source.Name, metav1.DeleteOptions{})
}

func UpdateSourceCRDSpec(ctx context.Context, nsName string, crdName string, specField string, newValue any) (*v1alpha1.Source, error) {
	// the patch document must be marshalled rather than formatted, otherwise a value
	// containing a quote closes the JSON string and the rest is parsed as patch syntax.
	patch, err := json.Marshal([]map[string]any{
		{
			"op":    "replace",
			"path":  "/spec/" + specField,
			"value": newValue,
		},
	})
	if err != nil {
		return nil, err
	}

	source, err := kube.DefaultClient.OdigosClient.Sources(nsName).Patch(
		ctx, crdName, types.JSONPatchType, patch, metav1.PatchOptions{},
	)

	return source, err
}

// mergePatchSourceCRD applies labels and spec fields in one update, so the webhooks only see the final state.
func mergePatchSourceCRD(ctx context.Context, nsName string, crdName string, patch map[string]any) (*v1alpha1.Source, error) {
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return nil, err
	}
	return kube.DefaultClient.OdigosClient.Sources(nsName).Patch(ctx, crdName, types.MergePatchType, patchBytes, metav1.PatchOptions{})
}

func UpdateSourceCRDLabel(ctx context.Context, nsName string, crdName string, labelKey string, newValue string) (*v1alpha1.Source, error) {
	escapedLabel := strings.ReplaceAll(labelKey, "/", "~1")   // replace "/" with "~1" to escape it for JSON patch
	escapedLabel = strings.ReplaceAll(escapedLabel, "\"", "") // remove quotes to avoid JSON parsing issues

	patchOps := []map[string]interface{}{}

	if newValue == "" {
		// if no value, remove the label
		patchOps = append(patchOps, map[string]interface{}{
			"op":   "remove",
			"path": fmt.Sprintf("/metadata/labels/%s", escapedLabel),
		})
	} else {
		// if value is provided, replace the label
		patchOps = append(patchOps, map[string]interface{}{
			"op":    "replace",
			"path":  fmt.Sprintf("/metadata/labels/%s", escapedLabel),
			"value": newValue,
		})
	}

	patchBytes, err := json.Marshal(patchOps)
	if err != nil {
		return nil, err
	}

	source, err := kube.DefaultClient.OdigosClient.Sources(nsName).Patch(
		ctx, crdName, types.JSONPatchType, patchBytes, metav1.PatchOptions{},
	)

	return source, err
}

func ToggleSourceCRD(ctx context.Context, nsName string, workloadName string, workloadKind model.K8sResourceKind, enabled bool, currentStreamName string) error {
	if enabled {
		_, err := EnsureSourceCRD(ctx, nsName, workloadName, workloadKind, currentStreamName)
		return err
	} else {
		return deleteSourceCRD(ctx, nsName, workloadName, workloadKind, currentStreamName, nil)
	}
}

func persistWorkloadSourceCRD(ctx context.Context, workload *model.PersistNamespaceSourceInput, namespaceSelected *bool) error {
	switch {
	case workload.Selected:
		_, err := EnsureSourceCRD(ctx, workload.Namespace, workload.Name, workload.Kind, workload.CurrentStreamName)
		return err
	case workload.DisableInstrumentation == nil:
		return deleteSourceCRD(ctx, workload.Namespace, workload.Name, workload.Kind, workload.CurrentStreamName, namespaceSelected)
	case *workload.DisableInstrumentation:
		return disableSourceCRD(ctx, workload.Namespace, workload.Name, workload.Kind, workload.CurrentStreamName)
	default:
		return removeWorkloadSourceCRD(ctx, workload.Namespace, workload.Name, workload.Kind, workload.CurrentStreamName)
	}
}

func GetInstrumentationInstances(ctx context.Context, namespace string, name string, kind string) ([]*v1alpha1.InstrumentationInstance, error) {
	result := make([]*v1alpha1.InstrumentationInstance, 0)

	listOptions := metav1.ListOptions{}
	if namespace != "" && name != "" && kind != "" {
		objectName := workload.CalculateWorkloadRuntimeObjectName(name, kind)
		if len(objectName) > 63 {
			// prevents k8s error: must be no more than 63 characters
			// see https://kubernetes.io/docs/concepts/overview/working-with-objects/names/#dns-label-names
			return result, nil
		}
		listOptions.LabelSelector = fmt.Sprintf("%s=%s", consts.InstrumentedAppNameLabel, objectName)
	}

	list, err := kube.DefaultClient.OdigosClient.InstrumentationInstances("").List(ctx, listOptions)
	if err != nil {
		return nil, err
	}

	for _, instance := range list.Items {
		result = append(result, &instance)
	}

	return result, nil
}

func getInstrumentationInstancesConditions(ctx context.Context, namespace string, name string, kind string) ([]*model.SourceConditions, error) {
	result := make([]*model.SourceConditions, 0)
	conditionsMap := make(map[string]*model.SourceConditions)
	instanceCountsMap := make(map[string]*InstanceCounts)

	items, err := GetInstrumentationInstances(ctx, namespace, name, kind)
	if err != nil {
		return nil, err
	}

	// Count instances and group by workload
	for _, instance := range items {
		objectName, exists := instance.Labels[consts.InstrumentedAppNameLabel]
		if !exists {
			continue
		}
		pw, err := workload.ExtractWorkloadInfoFromRuntimeObjectName(objectName, instance.Namespace)
		if err != nil {
			continue
		}
		key := fmt.Sprintf("%s/%s/%s", pw.Namespace, pw.Name, pw.Kind)

		if _, exists := conditionsMap[key]; !exists {
			conditionsMap[key] = &model.SourceConditions{
				Namespace:  pw.Namespace,
				Name:       pw.Name,
				Kind:       model.K8sResourceKind(pw.Kind),
				Conditions: []*model.Condition{},
			}
			instanceCountsMap[key] = &InstanceCounts{
				TotalInstances:   0,
				HealthyInstances: 0,
			}
		}

		instanceCountsMap[key].TotalInstances++
		if instance.Status.Healthy != nil && *instance.Status.Healthy {
			instanceCountsMap[key].HealthyInstances++
		}
	}

	// Create conditions for each workload
	for _, item := range conditionsMap {
		key := fmt.Sprintf("%s/%s/%s", item.Namespace, item.Name, item.Kind)
		instanceCounts := instanceCountsMap[key]

		status := model.ConditionStatusSuccess
		if instanceCounts.HealthyInstances < instanceCounts.TotalInstances {
			status = model.ConditionStatusError
		}

		reason := v1alpha1.InstrumentationInstancesHealth
		message := fmt.Sprintf("%d/%d instances are healthy", instanceCounts.HealthyInstances, instanceCounts.TotalInstances)

		item.Conditions = append(item.Conditions, &model.Condition{
			Type:    reason,
			Status:  status,
			Reason:  &reason,
			Message: &message,
		})

		result = append(result, item)
	}

	return result, nil
}

func getWorkloadsConditions(ctx context.Context, namespace string, name string, kind string) ([]*model.SourceConditions, error) {
	result := make([]*model.SourceConditions, 0)
	conditionsMap := make(map[string]*model.SourceConditions)

	// Deployments
	if model.K8sResourceKind(kind) == model.K8sResourceKindDeployment || kind == "" {
		deployments := make([]appsv1.Deployment, 0)

		if namespace == "" && name == "" && kind == "" {
			list, err := kube.DefaultClient.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}

			deployments = append(deployments, list.Items...)
		} else {
			dep, err := kube.DefaultClient.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}

			deployments = append(deployments, *dep)
		}

		for _, dep := range deployments {
			key := fmt.Sprintf("%s/%s/%s", dep.Namespace, dep.Name, model.K8sResourceKindDeployment)

			if _, exists := conditionsMap[key]; !exists {
				conditionsMap[key] = &model.SourceConditions{
					Namespace:  dep.Namespace,
					Name:       dep.Name,
					Kind:       model.K8sResourceKindDeployment,
					Conditions: []*model.Condition{},
				}
			}

			for _, c := range dep.Status.Conditions {
				status := TransformConditionStatus(metav1.ConditionStatus(c.Status), string(c.Type), c.Reason)

				conditionsMap[key].Conditions = append(conditionsMap[key].Conditions, &model.Condition{
					Status:  status,
					Type:    string(c.Type),
					Reason:  &c.Reason,
					Message: &c.Message,
				})
			}
		}
	}

	// DaemonSets
	if model.K8sResourceKind(kind) == model.K8sResourceKindDaemonSet || kind == "" {
		daemonSets := make([]appsv1.DaemonSet, 0)

		if namespace == "" && name == "" && kind == "" {
			list, err := kube.DefaultClient.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}

			daemonSets = append(daemonSets, list.Items...)
		} else {
			ds, err := kube.DefaultClient.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}

			daemonSets = append(daemonSets, *ds)
		}

		for _, ds := range daemonSets {
			key := fmt.Sprintf("%s/%s/%s", ds.Namespace, ds.Name, model.K8sResourceKindDaemonSet)

			if _, exists := conditionsMap[key]; !exists {
				conditionsMap[key] = &model.SourceConditions{
					Namespace:  ds.Namespace,
					Name:       ds.Name,
					Kind:       model.K8sResourceKindDaemonSet,
					Conditions: []*model.Condition{},
				}
			}

			for _, c := range ds.Status.Conditions {
				status := TransformConditionStatus(metav1.ConditionStatus(c.Status), string(c.Type), c.Reason)

				conditionsMap[key].Conditions = append(conditionsMap[key].Conditions, &model.Condition{
					Status:  status,
					Type:    string(c.Type),
					Reason:  &c.Reason,
					Message: &c.Message,
				})
			}
		}
	}

	// StatefulSets
	if model.K8sResourceKind(kind) == model.K8sResourceKindStatefulSet || kind == "" {
		statefulSets := make([]appsv1.StatefulSet, 0)

		if namespace == "" && name == "" && kind == "" {
			list, err := kube.DefaultClient.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}

			statefulSets = append(statefulSets, list.Items...)
		} else {
			ss, err := kube.DefaultClient.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}

			statefulSets = append(statefulSets, *ss)
		}

		for _, ss := range statefulSets {
			key := fmt.Sprintf("%s/%s/%s", ss.Namespace, ss.Name, model.K8sResourceKindStatefulSet)

			if _, exists := conditionsMap[key]; !exists {
				conditionsMap[key] = &model.SourceConditions{
					Namespace:  ss.Namespace,
					Name:       ss.Name,
					Kind:       model.K8sResourceKindStatefulSet,
					Conditions: []*model.Condition{},
				}
			}

			for _, c := range ss.Status.Conditions {
				status := TransformConditionStatus(metav1.ConditionStatus(c.Status), string(c.Type), c.Reason)

				conditionsMap[key].Conditions = append(conditionsMap[key].Conditions, &model.Condition{
					Status:  status,
					Type:    string(c.Type),
					Reason:  &c.Reason,
					Message: &c.Message,
				})
			}
		}
	}

	// Final result
	for _, item := range conditionsMap {
		result = append(result, item)
	}

	return result, nil
}

func GetOtherConditionsForSources(ctx context.Context, namespace string, name string, kind string) ([]*model.SourceConditions, error) {
	result := make([]*model.SourceConditions, 0)
	conditionsMap := make(map[string]*model.SourceConditions)

	instancesConditions, err := getInstrumentationInstancesConditions(ctx, namespace, name, kind)
	if err != nil {
		return nil, err
	}
	for _, instanceItem := range instancesConditions {
		key := fmt.Sprintf("%s/%s/%s", instanceItem.Namespace, instanceItem.Name, instanceItem.Kind)
		if _, exists := conditionsMap[key]; !exists {
			conditionsMap[key] = instanceItem
		} else {
			conditionsMap[key].Conditions = append(conditionsMap[key].Conditions, instanceItem.Conditions...)
		}
	}

	// TODO: uncomment this when we have a better way to differentiate between errors
	// workloadsConditions, err := getWorkloadsConditions(ctx, namespace, name, kind)
	// if err != nil {
	// 	return nil, err
	// }
	// for _, workloadItem := range workloadsConditions {
	// 	key := fmt.Sprintf("%s/%s/%s", workloadItem.Namespace, workloadItem.Name, workloadItem.Kind)
	// 	if _, exists := conditionsMap[key]; !exists {
	// 		conditionsMap[key] = workloadItem
	// 	} else {
	// 		conditionsMap[key].Conditions = append(conditionsMap[key].Conditions, workloadItem.Conditions...)
	// 		SortConditions(conditionsMap[key].Conditions)
	// 	}
	// }

	for _, item := range conditionsMap {
		result = append(result, item)
	}

	return result, nil
}

func UninstrumentCluster(ctx context.Context) error {
	// use the non-cached client to make sure we don't miss any one
	list, err := kube.DefaultClient.OdigosClient.Sources("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	namespaceSources := make([]v1alpha1.Source, 0)
	workloadSources := make([]v1alpha1.Source, 0)

	for _, source := range list.Items {
		if source.Spec.Workload.Kind == k8sconsts.WorkloadKind(WorkloadKindNamespace) {
			namespaceSources = append(namespaceSources, source)
		} else {
			workloadSources = append(workloadSources, source)
		}
	}

	for _, source := range namespaceSources {
		err = kube.DefaultClient.OdigosClient.Sources(source.Namespace).Delete(ctx, source.Name, metav1.DeleteOptions{})
		if err != nil {
			return err
		}
	}

	for _, source := range workloadSources {
		err = kube.DefaultClient.OdigosClient.Sources(source.Namespace).Delete(ctx, source.Name, metav1.DeleteOptions{})
		if err != nil {
			return err
		}
	}

	return nil
}
