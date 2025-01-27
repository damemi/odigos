/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/odigos-io/odigos/cli/cmd"
	"github.com/odigos-io/odigos/cli/cmd/resources"
	"github.com/odigos-io/odigos/cli/pkg/autodetect"
	cmdcontext "github.com/odigos-io/odigos/cli/pkg/cmd_context"
	"github.com/odigos-io/odigos/cli/pkg/kube"

	"github.com/odigos-io/odigos/api/k8sconsts"
	"github.com/odigos-io/odigos/common"
	operatorv1alpha1 "github.com/odigos-io/odigos/operator/api/v1alpha1"
)

// OdigosReconciler reconciles a Odigos object
type OdigosReconciler struct {
	client.Client
	KubeClient *kube.Client
	Scheme     *runtime.Scheme
}

// +kubebuilder:rbac:groups=operator.odigos.io,resources=odigos,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=operator.odigos.io,resources=odigos/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=operator.odigos.io,resources=odigos/finalizers,verbs=update
// +kubebuilder:rbac:groups=odigos.io,resources=collectorsgroups,verbs=get;list;watch;create;patch;update;delete
// +kubebuilder:rbac:groups=odigos.io,resources=collectorsgroups/status,verbs=get;list;watch
// +kubebuilder:rbac:groups=odigos.io,resources=destinations,verbs=get;list;watch
// +kubebuilder:rbac:groups=odigos.io,resources=instrumentationrules,verbs=get;list;watch;patch;delete;create
// +kubebuilder:rbac:groups=odigos.io,resources=instrumentationinstances,verbs=get;list;watch;patch;delete;create
// +kubebuilder:rbac:groups=odigos.io,resources=instrumentationinstances/status,verbs=get;patch;update
// +kubebuilder:rbac:groups=odigos.io,resources=instrumentationconfigs,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=odigos.io,resources=instrumentationconfigs/status,verbs=get;watch;update;patch
// +kubebuilder:rbac:groups=odigos.io,resources=instrumentedapplications,verbs=delete;get;list;watch
// +kubebuilder:rbac:groups=odigos.io,resources=sources,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=odigos.io,resources=sources/finalizers,verbs=update
// +kubebuilder:rbac:groups=odigos.io,resources=processors,verbs=get;list;watch;patch;delete;create
// +kubebuilder:rbac:groups="",resources=secrets,verbs=list;watch;create;patch;get
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;delete;patch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=create;get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=nodes/proxy,verbs=get;list
// +kubebuilder:rbac:groups="",resources=nodes/stats,verbs=get;list
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/status,verbs=get
// +kubebuilder:rbac:groups="",resources=endpoints,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;get
// +kubebuilder:rbac:groups=apps,resources=deployments;replicasets;daemonsets;statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments/status;daemonsets/status;statefulsets/status,verbs=get
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings;roles;rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=security.openshift.io,resources=securitycontextconstraints,verbs=use
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=mutatingwebhookconfigurations;validatingwebhookconfigurations,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Odigos object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.0/pkg/reconcile
func (r *OdigosReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	odigos := &operatorv1alpha1.Odigos{}
	err := r.Client.Get(ctx, req.NamespacedName, odigos)
	if err != nil {
		return ctrl.Result{}, err
	}

	ns := odigos.GetNamespace()
	// Check if Odigos already installed
	odigosCm := &corev1.ConfigMap{}
	err = r.Client.Get(ctx, types.NamespacedName{Name: k8sconsts.OdigosDeploymentConfigMapName, Namespace: ns}, odigosCm)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	if err == nil {
		return ctrl.Result{Requeue: false}, errors.New("odigos is already installed in namespace")
	}

	// Check if the cluster meets the minimum requirements
	clusterKind := cmdcontext.ClusterKindFromContext(ctx)
	if clusterKind == autodetect.KindUnknown {
		logger.Info("Unknown Kubernetes cluster detected, proceeding with installation")
	} else {
		logger.Info(fmt.Sprintf("Detected cluster: Kubernetes kind: %s\n", clusterKind))
	}

	k8sVersion := cmdcontext.K8SVersionFromContext(ctx)
	if k8sVersion != nil {
		if k8sVersion.LessThan(k8sconsts.MinK8SVersionForInstallation) {
			return ctrl.Result{Requeue: false}, errors.New("odigos requires Kubernetes version " + k8sconsts.MinK8SVersionForInstallation.String() + " but found " + k8sVersion.String())
		}
		fmt.Printf("Detected cluster: Kubernetes version: %s\n", k8sVersion.String())
	}

	var odigosProToken string
	odigosTier := common.CommunityOdigosTier
	if odigos.Spec.APIKey != "" {
		odigosTier = common.CloudOdigosTier
		odigosProToken = odigos.Spec.APIKey
		err = cmd.VerifyOdigosCloudApiKey(odigos.Spec.APIKey)
		if err != nil {
			return ctrl.Result{Requeue: false}, errors.New("invalid Odigos API Key format")
		}
	} else if odigos.Spec.OnPremToken != "" {
		odigosTier = common.OnPremOdigosTier
		odigosProToken = odigos.Spec.OnPremToken
	}

	// validate user input profiles against available profiles
	cmd.ValidateUserInputProfiles(odigosTier)

	selectedProfiles := []common.ProfileName{}
	for _, profile := range odigos.Spec.Profiles {
		selectedProfiles = append(selectedProfiles, common.ProfileName(profile))
	}
	odigosConfig := common.OdigosConfiguration{
		ConfigVersion:             1,
		TelemetryEnabled:          odigos.Spec.TelemetryEnabled,
		OpenshiftEnabled:          odigos.Spec.OpenShiftEnabled,
		IgnoredNamespaces:         odigos.Spec.IgnoredNamespaces,
		IgnoredContainers:         odigos.Spec.IgnoredContainers,
		SkipWebhookIssuerCreation: odigos.Spec.SkipWebhookIssuerCreation,
		Psp:                       odigos.Spec.PodSecurityPolicy,
		ImagePrefix:               odigos.Spec.ImagePrefix,
		Profiles:                  odigos.Spec.Profiles,
		UiMode:                    common.UiMode(odigos.Spec.UIMode),
	}

	logger.Info("Installing Odigos version " + odigos.Spec.Version + " in namespace " + ns)

	resourceManagers := resources.CreateResourceManagers(r.KubeClient, ns, odigosTier, &odigosProToken, &odigosConfig, odigos.Spec.Version)
	err = resources.ApplyResourceManagers(ctx, r.KubeClient, resourceManagers, "Creating")
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *OdigosReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&operatorv1alpha1.Odigos{}).
		Complete(r)
}
