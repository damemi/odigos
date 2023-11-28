package kube

import (
	"context"

	"github.com/keyval-dev/odigos-enterprise/odiglet/pkg/ebpf"
	odigosv1 "github.com/keyval-dev/odigos/api/odigos/v1alpha1"
	"github.com/keyval-dev/odigos/common"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type InstrumentationConfigReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	Directors map[common.ProgrammingLanguage]ebpf.DirectorEnterprise
}

func (i *InstrumentationConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the InstrumentationConfig instrumentationConfig
	instrumentationConfig := &odigosv1.InstrumentationConfig{}
	err := i.Get(ctx, req.NamespacedName, instrumentationConfig)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	for _, director := range i.Directors {
		err = director.ApplyInstrumentationConfig(instrumentationConfig)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	logger.Info("Reconciling InstrumentationConfig", "instance", instrumentationConfig)

	return ctrl.Result{}, nil
}

func (i *InstrumentationConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&odigosv1.InstrumentationConfig{}).
		Complete(i)
}
