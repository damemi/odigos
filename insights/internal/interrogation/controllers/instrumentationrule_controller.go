package controllers

import (
	"context"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// InstrumentationRuleReconciler forwards InstrumentationRule changes to the
// interrogation loop, which owns all rule writes. It does not write anything
// itself, so rule writes stay single-writer.
type InstrumentationRuleReconciler struct {
	client.Client
	// OnChange is called for every create, update, or delete. It must not
	// block.
	OnChange func()
}

// Reconcile is called when an InstrumentationRule is created, updated, or deleted.
func (r *InstrumentationRuleReconciler) Reconcile(_ context.Context, _ ctrl.Request) (ctrl.Result, error) {
	if r.OnChange != nil {
		r.OnChange()
	}
	return ctrl.Result{}, nil
}

// SetupWithManager registers the reconciler to watch InstrumentationRules.
func (r *InstrumentationRuleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&odigosv1.InstrumentationRule{}).
		Complete(r)
}
