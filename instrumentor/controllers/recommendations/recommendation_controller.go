package recommendations

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	k8slogger "github.com/odigos-io/odigos/k8sutils/pkg/logger"
	k8sutils "github.com/odigos-io/odigos/k8sutils/pkg/utils"
	recommendationcatalog "github.com/odigos-io/odigos/recommendations"
)

type RecommendationReconciler struct {
	client.Client
}

// used to delete recommendations that are no longer in the catalog.
// also, in case a recommendation is somehow modified in etcd, this controller will sync it back to the desired state.
func (r *RecommendationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	catalogRec, ok := recommendationcatalog.GetByK8sObjectName(req.Name)
	if !ok {
		existing := &odigosv1.Recommendation{}
		if err := r.Get(ctx, req.NamespacedName, existing); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		k8slogger.FromContext(ctx).WithName("recommendations").
			Info("deleting undesired recommendation", "name", existing.Name, "type", existing.Spec.Type, "reason", "not found in recommendations catalog")
		return ctrl.Result{}, client.IgnoreNotFound(r.Delete(ctx, existing))
	}

	err := syncRecommendation(ctx, r.Client, catalogRec, req.Namespace)
	return k8sutils.K8SNoEffectiveConfigErrorHandler(err)
}
