package instrumentation_ebpf

import (
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/odigos-io/odigos/api/k8sconsts"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	commonlogger "github.com/odigos-io/odigos/common/logger"
	"github.com/odigos-io/odigos/distros"
	"github.com/odigos-io/odigos/instrumentation"
	"github.com/odigos-io/odigos/odiglet/pkg/ebpf"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
)

func SetupWithManager(
	mgr ctrl.Manager,
	configUpdates chan<- instrumentation.ConfigUpdate[ebpf.K8sConfigGroup],
	InstrumentationRequests ebpf.K8sInstrumentationRequests,
	distributionGetter *distros.Getter,
) error {
	commonlogger.LoggerCompat().With("subsystem", "instrumentationebpf").Info("Starting reconcilers for ebpf instrumentation")
	var err error

	err = builder.
		ControllerManagedBy(mgr).
		Named("InstrumentationConfigReconciler_ebpf").
		For(&odigosv1.InstrumentationConfig{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(&InstrumentationConfigReconciler{
			Client:                  mgr.GetClient(),
			Scheme:                  mgr.GetScheme(),
			ConfigUpdates:           configUpdates,
			InstrumentationRequests: InstrumentationRequests,
			DistributionGetter:      distributionGetter,
		})
	if err != nil {
		return err
	}

	// Pods that went through the webhook have the Odigos environment variables, so the process detector reports their
	// restarted processes. Known gap: the label is per pod, so a container the webhook skipped and that was
	// instrumented later is not instrumented again after it restarts.
	notInjectedByWebhook, err := predicate.LabelSelectorPredicate(metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key:      k8sconsts.OdigosAgentsMetaHashLabel,
			Operator: metav1.LabelSelectorOpDoesNotExist,
		}},
	})
	if err != nil {
		return err
	}

	err = builder.
		ControllerManagedBy(mgr).
		Named("PodsReconciler_ebpf").
		For(&corev1.Pod{}).
		WithEventFilter(predicate.And(notInjectedByWebhook, containerRestartedPredicate{})).
		Complete(&PodsReconciler{
			Client:                  mgr.GetClient(),
			InstrumentationRequests: InstrumentationRequests,
			DistributionGetter:      distributionGetter,
		})
	if err != nil {
		return err
	}

	return nil
}
