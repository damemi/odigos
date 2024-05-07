package kube

import (
	"context"

	"github.com/odigos-io/odigos-enterprise/odiglet/pkg/ebpf"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/common/utils"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type InstrumentationConfigReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	Directors ebpf.DirectorsEnterpriseMap
}

func (i *InstrumentationConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	workloadName, workloadKind, err := utils.GetTargetFromRuntimeName(req.Name)
	if err != nil {
		return ctrl.Result{}, err
	}

	podWorkload := &common.PodWorkload{
		Namespace: req.Namespace,
		Kind:      workloadKind,
		Name:      workloadName,
	}

	// Fetch the InstrumentationConfig instrumentationConfig
	instrumentationConfig := &odigosv1.InstrumentationConfig{}
	err = i.Get(ctx, req.NamespacedName, instrumentationConfig)
	if err != nil {
		if apierrors.IsNotFound(err) {
			instrumentationConfig = nil
		} else {
			return ctrl.Result{}, err
		}
	}

	for _, director := range i.Directors {
		err = director.ApplyInstrumentationConfig(podWorkload, instrumentationConfig)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

func (i *InstrumentationConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&odigosv1.InstrumentationConfig{}).
		Complete(i)
}
