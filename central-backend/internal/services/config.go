package services

import (
	"context"

	"github.com/odigos-io/odigos/central-backend/internal/config"
	"github.com/odigos-io/odigos/central-backend/internal/k8s"
	"github.com/odigos-io/odigos/central-backend/internal/logger"
	"github.com/odigos-io/odigos/api/k8sconsts"
	"github.com/odigos-io/odigos/common/consts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// /this is the type of the service that gets the odigos central version currently empty
// ConfigService provides access to system-level configuration.
// It is intentionally modeled as a service to allow future extensions
// such as caching, watchers, or additional cluster-level config lookups.
type ConfigService struct {
	k8sClient *k8s.Client
}

func NewConfigService(k8sClient *k8s.Client) *ConfigService {
	return &ConfigService{k8sClient: k8sClient}
}

func GetCurrentNamespace() string {

	return config.GetEnv(consts.CurrentNamespaceEnvVar, k8sconsts.DefaultOdigosCentralNamespace)
}

func (svc *ConfigService) GetOdigosCentralVersion(ctx context.Context) string {
	if svc.k8sClient == nil {
		logger.Log.Error("k8s client is not initialized for system version lookup")
		return "unknown"
	}

	currentNamespace := GetCurrentNamespace()
	odigosDeployment, err := svc.k8sClient.CoreV1().ConfigMaps(currentNamespace).Get(ctx, k8sconsts.OdigosCentralDeploymentConfigMapName, metav1.GetOptions{})
	if err != nil {
		logger.Log.Error("Failed to get system version ConfigMap", "error", err, "namespace", currentNamespace, "configMap", k8sconsts.OdigosCentralDeploymentConfigMapName)
		return "unknown"
	}

	version := odigosDeployment.Data[k8sconsts.OdigosCentralDeploymentConfigMapVersionKey]
	if version == "" {
		return "unknown"
	}
	return version
}
