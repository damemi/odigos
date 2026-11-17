package node

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	"github.com/odigos-io/odigos/api/k8sconsts"
	"github.com/odigos-io/odigos/common/consts"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
)

// IsGKEManagedNode reports whether to skip node label setup on GKE Autopilot.
// Autopilot is auto-detected by the gk3- node name prefix, or can be forced via GKE_AUTOPILOT=true.
func IsGKEManagedNode(nodeName string) bool {
	if forced, err := strconv.ParseBool(env.GetEnvVarOrDefault(k8sconsts.GKEAutopilotEnvVar, "false")); err == nil && forced {
		return true
	}
	return strings.HasPrefix(nodeName, k8sconsts.GKEManagedNodeNamePrefix)
}

func PrepareNodeForOdigosInstallation(clientset *kubernetes.Clientset, nodeName string) error {
	ctx := context.Background()

	return retry.OnError(retry.DefaultBackoff, apierrors.IsConflict, func() error {
		node, err := clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get node %s: %w", nodeName, err)
		}

		// Remove startup taint if exists
		newTaints := make([]v1.Taint, 0, len(node.Spec.Taints))
		for _, taint := range node.Spec.Taints {
			if taint.Key == consts.KarpenterStartupTaintKey && taint.Effect == v1.TaintEffectNoSchedule {
				continue
			}
			newTaints = append(newTaints, taint)
		}
		node.Spec.Taints = newTaints

		// Add Odigos Installed label
		if node.Labels == nil {
			node.Labels = make(map[string]string)
		}

		// Installs that ran the OSS odiglet labeled their nodes with the OSS label.
		delete(node.Labels, k8sconsts.OdigletOSSInstalledLabel)
		node.Labels[k8sconsts.OdigletEnterpriseInstalledLabel] = k8sconsts.OdigletInstalledLabelValue

		_, err = clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
		return err
	})
}
