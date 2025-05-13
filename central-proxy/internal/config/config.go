package config

import (
	"context"
	"fmt"

	"github.com/odigos-io/central-proxy/kube"
	"github.com/odigos-io/odigos/api/k8sconsts"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/common/consts"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

func GetCentralBackendURLAndClusterName(ctx context.Context, kubeClient *kube.Client) (string, string, error) {
	ns := env.GetCurrentNamespace()
	configMap, err := kubeClient.CoreV1().ConfigMaps(ns).Get(ctx, consts.OdigosConfigurationName, metav1.GetOptions{})
	if err != nil {
		return "", "", err
	}
	var odigosConfig common.OdigosConfiguration
	if err := yaml.Unmarshal([]byte(configMap.Data[consts.OdigosConfigurationFileName]), &odigosConfig); err != nil {
		return "", "", err
	}

	if odigosConfig.CentralBackendURL == "" {
		return "", "", fmt.Errorf("key %s not found in ConfigMap", odigosConfig.CentralBackendURL)
	}

	if odigosConfig.ClusterName == "" {
		return "", "", fmt.Errorf("key %s not found in ConfigMap", odigosConfig.ClusterName)
	}

	return odigosConfig.CentralBackendURL, odigosConfig.ClusterName, nil
}

func GetOdigosDeploymentId(ctx context.Context, kubeClient *kube.Client) (string, error) {
	ns := env.GetCurrentNamespace()

	configMap, err := kubeClient.CoreV1().ConfigMaps(ns).Get(ctx, k8sconsts.OdigosDeploymentConfigMapName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	deploymentId, exists := configMap.Data[k8sconsts.OdigosDeploymentConfigMapOdigosDeploymentIDKey]
	if !exists || deploymentId == "" {
		return "", fmt.Errorf("key odigos-deployment-id not found in ConfigMap")
	}

	return deploymentId, nil
}

func GetOdigosVersion(ctx context.Context, kubeClient *kube.Client) (string, error) {
	ns := env.GetCurrentNamespace()

	configMap, err := kubeClient.CoreV1().ConfigMaps(ns).Get(ctx, k8sconsts.OdigosDeploymentConfigMapName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	version, exists := configMap.Data[consts.OdigosVersionEnvVarName]
	if !exists || version == "" {
		return "", fmt.Errorf("key %s not found in ConfigMap", consts.OdigosVersionEnvVarName)
	}

	return version, nil
}
