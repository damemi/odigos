package config

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/odigos-io/central-proxy/internal/logger"
	"github.com/odigos-io/central-proxy/kube"
	"github.com/odigos-io/odigos/api/k8sconsts"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/common/consts"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

func runningInCluster() bool {
	return os.Getenv("KUBERNETES_SERVICE_HOST") != ""
}

func GetCentralBackendURLAndClusterName(ctx context.Context, kubeClient *kube.Client) (string, string, error) {
	if !runningInCluster() {
		return DevCentralBackendURL(), DevClusterName(), nil
	}
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
		return "", "", fmt.Errorf("key \"%s\" not found in ConfigMap", "centralBackendURL")
	}

	if odigosConfig.ClusterName == "" {
		return "", "", fmt.Errorf("key \"%s\" not found in ConfigMap", "clusterName")
	}

	return odigosConfig.CentralBackendURL, odigosConfig.ClusterName, nil
}

func GetOdigosDeploymentId(ctx context.Context, kubeClient *kube.Client) (string, error) {
	if !runningInCluster() {
		return uuid.New().String(), nil
	}
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
	if !runningInCluster() {
		return DevVersion(), nil
	}
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

const tlsBaseDir = "/etc/tls"

// validateTLSFilePath ensures the file path is within the allowed TLS directory
func validateTLSFilePath(filePath string) error {
	if filePath == "" {
		return fmt.Errorf("file path is empty")
	}

	// Get absolute path
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for %s: %w", filePath, err)
	}

	// Clean the path to resolve any .. or . components
	cleanPath := filepath.Clean(absPath)

	// Check if the path is within the allowed directory
	if !strings.HasPrefix(cleanPath, tlsBaseDir+string(filepath.Separator)) && cleanPath != tlsBaseDir {
		return fmt.Errorf("TLS file path %s is not within allowed directory %s", filePath, tlsBaseDir)
	}

	return nil
}

// GetTLSConfig creates a TLS configuration based on environment variables
// CENTRAL_SKIP_TLS_VERIFY - if set to "true" (case-insensitive), skips TLS certificate verification
// TLS_CA_FILE - path to a CA certificate file (must be in /etc/tls/)
// TLS_CERT_FILE - path to client certificate file for mTLS (must be in /etc/tls/)
// TLS_KEY_FILE - path to client private key file for mTLS (must be in /etc/tls/)
func GetTLSConfig() (*tls.Config, error) {
	tlsConfig := &tls.Config{}

	// Check if TLS verification should be skipped
	skipVerify := strings.ToLower(strings.TrimSpace(os.Getenv("CENTRAL_SKIP_TLS_VERIFY")))
	if skipVerify == "true" {
		tlsConfig.InsecureSkipVerify = true
		logger.Log.Info("TLS certificate verification disabled", "CENTRAL_SKIP_TLS_VERIFY", "true")
	}

	// Check for custom CA certificate
	caFile := os.Getenv("TLS_CA_FILE")
	if caFile != "" {
		if err := validateTLSFilePath(caFile); err != nil {
			return nil, fmt.Errorf("invalid CA file path: %w", err)
		}

		caCert, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA certificate file %s: %w", caFile, err)
		}

		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA certificate from file %s", caFile)
		}

		tlsConfig.RootCAs = caCertPool
		logger.Log.Info("Custom CA certificate loaded", "caFile", caFile)
	}

	// Check for client certificate and key (mTLS)
	certFile := os.Getenv("TLS_CERT_FILE")
	keyFile := os.Getenv("TLS_KEY_FILE")
	if certFile != "" && keyFile != "" {
		if err := validateTLSFilePath(certFile); err != nil {
			return nil, fmt.Errorf("invalid cert file path: %w", err)
		}
		if err := validateTLSFilePath(keyFile); err != nil {
			return nil, fmt.Errorf("invalid key file path: %w", err)
		}

		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load client certificate and key: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
		logger.Log.Info("Client certificate loaded for mTLS authentication", "certFile", certFile, "keyFile", keyFile)
	} else if certFile != "" || keyFile != "" {
		return nil, fmt.Errorf("both TLS_CERT_FILE and TLS_KEY_FILE must be set together for mTLS")
	}

	return tlsConfig, nil
}
