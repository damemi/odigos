package instrumentation_ebpf

import (
	"context"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/distros"
	"github.com/odigos-io/odigos/k8sutils/pkg/containers"
	k8spod "github.com/odigos-io/odigos/k8sutils/pkg/pod"
	odigospredicate "github.com/odigos-io/odigos/k8sutils/pkg/predicate"
	"github.com/odigos-io/odigos/k8sutils/pkg/workload"
	"github.com/odigos-io/odigos/odiglet/pkg/ebpf"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

// PodsReconciler instruments the restarted containers of pods instrumented without a restart. Such pods lack the
// Odigos environment variables, so the process detector does not report the restarted process.
type PodsReconciler struct {
	client.Client
	InstrumentationRequests ebpf.K8sInstrumentationRequests
	DistributionGetter      *distros.Getter
}

func (r *PodsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	pod := &corev1.Pod{}
	if err := r.Get(ctx, req.NamespacedName, pod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	podWorkload, err := workload.PodWorkloadObject(pod)
	if err != nil || podWorkload == nil {
		return ctrl.Result{}, err
	}

	instrumentationConfig := &odigosv1.InstrumentationConfig{}
	instrumentationConfigKey := client.ObjectKey{
		Namespace: podWorkload.Namespace,
		Name:      workload.CalculateWorkloadRuntimeObjectName(podWorkload.Name, podWorkload.Kind),
	}
	if err := r.Get(ctx, instrumentationConfigKey, instrumentationConfig); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	err = sendInstrumentationRequest(ctx, r.Client, r.InstrumentationRequests, r.DistributionGetter, *podWorkload, instrumentationConfig)
	return resolveErrReconcileResult(err)
}

// containerRestartedPredicate passes the pod updates in which a pod is ready after one of its containers restarted.
type containerRestartedPredicate struct {
	odigospredicate.OnlyUpdatesPredicate
}

func (containerRestartedPredicate) Update(e event.UpdateEvent) bool {
	oldPod, oldOk := e.ObjectOld.(*corev1.Pod)
	newPod, newOk := e.ObjectNew.(*corev1.Pod)
	if !oldOk || !newOk {
		return false
	}

	// The instrumentation request skips pods whose containers are not all ready.
	if !k8spod.AllContainersReady(newPod) {
		return false
	}

	// A container restarted in this update, and the pod is already ready.
	if restartCountIncreased(oldPod, newPod) {
		return true
	}

	// The pod is ready again after a container restarted, e.g. once the container's readiness probe passed.
	if !k8spod.AllContainersReady(oldPod) {
		return hasRestartedContainer(newPod)
	}

	return false
}

func restartCountIncreased(oldPod, newPod *corev1.Pod) bool {
	for _, newStatus := range newPod.Status.ContainerStatuses {
		oldStatus := containers.GetContainerStatusByName(oldPod.Status.ContainerStatuses, newStatus.Name)
		if oldStatus == nil {
			continue
		}
		if newStatus.RestartCount > oldStatus.RestartCount {
			return true
		}
	}
	return false
}

func hasRestartedContainer(pod *corev1.Pod) bool {
	for _, status := range pod.Status.ContainerStatuses {
		if status.RestartCount > 0 {
			return true
		}
	}
	return false
}
