package services

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/odigos-io/central-backend/internal/config"
	"github.com/odigos-io/central-backend/internal/consts"
	"github.com/odigos-io/central-backend/internal/k8s"
)

type CredentialsService struct {
	k8sClient  *k8s.Client
	namespace  string
	secretName string
}

type AdminCredentials struct {
	Username string
	Password string
}

func NewCredentialsService() (*CredentialsService, error) {
	useK8sSecrets := os.Getenv("USE_K8S_SECRETS") == "true"

	if !useK8sSecrets {
		return &CredentialsService{
			k8sClient:  nil,
			namespace:  "",
			secretName: "",
		}, nil
	}

	k8sClient, err := k8s.CreateClient()
	if err != nil {
		return nil, fmt.Errorf("failed to create k8s client: %w", err)
	}

	namespace := config.GetEnv("KEYCLOAK_SECRET_NAMESPACE", consts.DefaultKeycloakSecretNamespace)
	secretName := config.GetEnv("KEYCLOAK_SECRET_NAME", consts.DefaultKeycloakSecretName)

	return &CredentialsService{
		k8sClient:  k8sClient,
		namespace:  namespace,
		secretName: secretName,
	}, nil
}

func (c *CredentialsService) GetAdminCredentials(ctx context.Context) (*AdminCredentials, error) {
	if c.k8sClient == nil {
		return c.getCredentialsFromEnv()
	}

	return c.getCredentialsFromSecret(ctx)
}

func (c *CredentialsService) getCredentialsFromEnv() (*AdminCredentials, error) {
	username := config.GetEnv("KEYCLOAK_ADMIN_USERNAME", consts.DefaultAdminUsername)
	password := config.GetEnv("KEYCLOAK_ADMIN_PASSWORD", consts.DefaultAdminPassword)

	if username == "" || password == "" {
		return nil, fmt.Errorf("KEYCLOAK_ADMIN_USERNAME and KEYCLOAK_ADMIN_PASSWORD environment variables must be set")
	}

	return &AdminCredentials{
		Username: username,
		Password: password,
	}, nil
}

func (c *CredentialsService) getCredentialsFromSecret(ctx context.Context) (*AdminCredentials, error) {
	secretData, err := c.k8sClient.GetSecretData(ctx, c.namespace, c.secretName)
	if err != nil {
		return nil, fmt.Errorf("failed to get secret data: %w", err)
	}

	usernameBytes, exists := secretData[consts.AdminUsernameKey]
	if !exists {
		return nil, fmt.Errorf("secret does not contain '%s' key", consts.AdminUsernameKey)
	}

	passwordBytes, exists := secretData[consts.AdminPasswordKey]
	if !exists {
		return nil, fmt.Errorf("secret does not contain '%s' key", consts.AdminPasswordKey)
	}

	username := strings.TrimSpace(string(usernameBytes))
	password := strings.TrimSpace(string(passwordBytes))

	if username == "" || password == "" {
		return nil, fmt.Errorf("admin credentials in secret cannot be empty")
	}

	return &AdminCredentials{
		Username: username,
		Password: password,
	}, nil
}
