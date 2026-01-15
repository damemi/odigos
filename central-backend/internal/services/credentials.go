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
	k8sClient      *k8s.Client
	namespace      string
	secretName     string
	oidcSecretName string
}

type AdminCredentials struct {
	Username string
	Password string
}

type OidcCredentials struct {
	Provider     string
	ClientId     string
	ClientSecret string
	DiscoveryUrl string
	TenantId     string
}

func NewCredentialsService(k8sClient *k8s.Client) (*CredentialsService, error) {
	useK8sSecrets := os.Getenv("USE_K8S_SECRETS") == "true"

	if !useK8sSecrets {
		return &CredentialsService{
			k8sClient:      nil,
			namespace:      "",
			secretName:     "",
			oidcSecretName: "",
		}, nil
	}

	if k8sClient == nil {
		return nil, fmt.Errorf("USE_K8S_SECRETS is true but k8s client is not initialized")
	}

	namespace := config.GetEnv("KEYCLOAK_SECRET_NAMESPACE", consts.DefaultKeycloakSecretNamespace)
	secretName := config.GetEnv("KEYCLOAK_SECRET_NAME", consts.DefaultKeycloakSecretName)
	oidcSecretName := config.GetEnv("KEYCLOAK_OIDC_SECRET_NAME", consts.DefaultKeycloakOidcSecretName)

	return &CredentialsService{
		k8sClient:      k8sClient,
		namespace:      namespace,
		secretName:     secretName,
		oidcSecretName: oidcSecretName,
	}, nil
}

func (c *CredentialsService) GetAdminCredentials(ctx context.Context) (*AdminCredentials, error) {
	if c.k8sClient == nil {
		return c.getAdminCredentialsFromEnv()
	}

	return c.getAdminCredentialsFromSecret(ctx)
}

func (c *CredentialsService) getAdminCredentialsFromEnv() (*AdminCredentials, error) {
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

func (c *CredentialsService) getAdminCredentialsFromSecret(ctx context.Context) (*AdminCredentials, error) {
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

func (c *CredentialsService) GetOidcCredentials(ctx context.Context) (*OidcCredentials, error) {
	if c.k8sClient == nil {
		return c.getOidcCredentialsFromEnv()
	}

	return c.getOidcCredentialsFromSecret(ctx)
}

func (c *CredentialsService) getOidcCredentialsFromEnv() (*OidcCredentials, error) {
	provider := config.GetEnv("KEYCLOAK_OIDC_PROVIDER", "")
	clientId := config.GetEnv("KEYCLOAK_OIDC_CLIENT_ID", "")
	clientSecret := config.GetEnv("KEYCLOAK_OIDC_CLIENT_SECRET", "")
	discoveryUrl := config.GetEnv("KEYCLOAK_OIDC_DISCOVERY_URL", "")
	tenantId := config.GetEnv("KEYCLOAK_OIDC_TENANT_ID", "")

	return &OidcCredentials{
		Provider:     provider,
		ClientId:     clientId,
		ClientSecret: clientSecret,
		DiscoveryUrl: discoveryUrl,
		TenantId:     tenantId,
	}, nil
}

func (c *CredentialsService) getOidcCredentialsFromSecret(ctx context.Context) (*OidcCredentials, error) {
	secretData, err := c.k8sClient.GetSecretData(ctx, c.namespace, c.oidcSecretName)
	if err != nil {
		return nil, fmt.Errorf("failed to get secret data: %w", err)
	}

	providerBytes, exists := secretData[consts.OidcProviderKey]
	provider := ""
	if exists {
		provider = strings.TrimSpace(string(providerBytes))
	}

	clientIdBytes, exists := secretData[consts.OidcClientIdKey]
	clientId := ""
	if exists {
		clientId = strings.TrimSpace(string(clientIdBytes))
	}

	clientSecretBytes, exists := secretData[consts.OidcClientSecretKey]
	clientSecret := ""
	if exists {
		clientSecret = strings.TrimSpace(string(clientSecretBytes))
	}

	discoveryUrlBytes, exists := secretData[consts.OidcDiscoveryUrlKey]
	discoveryUrl := ""
	if exists {
		discoveryUrl = strings.TrimSpace(string(discoveryUrlBytes))
	}

	tenantIdBytes, exists := secretData[consts.OidcTenantIdKey]
	tenantId := ""
	if exists {
		tenantId = strings.TrimSpace(string(tenantIdBytes))
	}

	return &OidcCredentials{
		Provider:     provider,
		ClientId:     clientId,
		ClientSecret: clientSecret,
		DiscoveryUrl: discoveryUrl,
		TenantId:     tenantId,
	}, nil
}
