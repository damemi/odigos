package controllers

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(odigosv1.AddToScheme(scheme))
}

// Options configures the controller-runtime manager.
type Options struct {
	Logger logr.Logger
	// OnRuleChange is called on every InstrumentationRule event. It must not
	// block.
	OnRuleChange func()
}

// CreateManager builds a controller-runtime manager that caches InstrumentationRules
// and ConfigMaps in the Odigos namespace, and InstrumentationConfigs in every
// namespace (they live next to the workload).
//
// The manager runs inside the insights process, which owns the HTTP, metrics,
// and health endpoints, so it binds no servers of its own. It does not elect a
// leader: the caller starts it only on the replica holding the insights lease,
// and uses the cache to list InstrumentationRules.
func CreateManager(opts Options) (ctrl.Manager, error) {
	ctrl.SetLogger(opts.Logger)

	odigosNs := env.GetCurrentNamespace()
	nsSelector := client.InNamespace(odigosNs).AsSelector()

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
		HealthProbeBindAddress: "0",
		Cache: cache.Options{
			DefaultTransform: cache.TransformStripManagedFields(),
			ByObject: map[client.Object]cache.ByObject{
				&odigosv1.InstrumentationRule{}: {
					Field: nsSelector,
				},
				&corev1.ConfigMap{}: {
					Field: nsSelector,
				},
				&odigosv1.InstrumentationConfig{}: {},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create manager: %w", err)
	}

	if err := (&InstrumentationRuleReconciler{
		Client:   mgr.GetClient(),
		OnChange: opts.OnRuleChange,
	}).SetupWithManager(mgr); err != nil {
		return nil, fmt.Errorf("setup InstrumentationRule reconciler: %w", err)
	}

	// ByObject only selects how a type is watched. GetInformer is what lists
	// InstrumentationConfigs into the cache when the manager starts. They have
	// no reconciler, and they live in workload namespaces.
	if _, err := mgr.GetCache().GetInformer(context.Background(), &odigosv1.InstrumentationConfig{}); err != nil {
		return nil, fmt.Errorf("instrumentation config informer: %w", err)
	}

	return mgr, nil
}
