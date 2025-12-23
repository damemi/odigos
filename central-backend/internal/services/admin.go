package services

import (
	"context"
	"fmt"

	"github.com/odigos-io/central-backend/graph/model"
	"github.com/odigos-io/central-backend/internal/keycloak"
	"github.com/odigos-io/central-backend/internal/logger"
	"github.com/odigos-io/central-backend/internal/utils"
)

type AdminService struct {
	adminCredentials *AdminCredentials
	kcAdmin          *keycloak.KeycloakAdminService
}

func NewAdminService(adminCredentials *AdminCredentials) *AdminService {
	return &AdminService{
		adminCredentials: adminCredentials,
		kcAdmin:          keycloak.NewKeycloakAdminService(),
	}
}

func (svc *AdminService) GetAdminToken() (string, error) {
	return svc.kcAdmin.GetAdminToken(svc.adminCredentials.Username, svc.adminCredentials.Password)
}

// DetermineSystemInitialized checks if the system has been initialized by querying Keycloak
// for non-admin, non-service-account users.
func (svc *AdminService) DetermineSystemInitialized() bool {
	adminToken, err := svc.GetAdminToken()
	if err != nil {
		logger.Log.Error("Failed to get admin token to determine initialization state", "error", err)
		return false
	}

	userService := keycloak.NewKeycloakUserService()
	users, err := userService.ListUsers(adminToken)
	if err != nil {
		logger.Log.Error("Failed to list users from Keycloak to determine initialization state", "error", err)
		return false
	}

	for _, u := range users {
		if _, isServiceAccount := u["serviceAccountClientId"]; isServiceAccount {
			continue
		}
		username, _ := u["username"].(string)
		if username == svc.adminCredentials.Username {
			continue
		}
		return true
	}
	return false
}

func (svc *AdminService) GetToken(ctx context.Context) (string, error) {
	isInitialized := svc.DetermineSystemInitialized()

	if !isInitialized {
		return svc.GetAdminToken()
	}
	if token := utils.GetTokenFromContext(ctx); token != "" {
		if expired, _ := utils.IsTokenExpired(token); expired {
			return "", fmt.Errorf("authentication expired")
		}
		_, err := svc.ValidateAdminPermissions(token)
		if err == nil {
			return token, nil
		}
	}
	return "", fmt.Errorf("admin permissions required")
}

func (svc *AdminService) ValidateAdminPermissions(userToken string) (string, error) {
	roles, err := utils.ExtractRolesFromToken(userToken)
	if err != nil {
		return "", fmt.Errorf("failed to extract roles from token: %w", err)
	}

	hasAdminRole := false
	for _, role := range roles {
		if role == "admin" {
			hasAdminRole = true
			break
		}
	}

	if !hasAdminRole {
		return "", fmt.Errorf("user does not have admin permissions")
	}

	email, err := utils.ExtractEmailFromToken(userToken)
	if err != nil {
		return "", fmt.Errorf("failed to extract email from token: %w", err)
	}

	return email, nil
}

func (svc *AdminService) GetIdentityProvider(providerID string) (*keycloak.IdentityProviderRepresentation, error) {
	return svc.kcAdmin.GetIdentityProvider(svc.adminCredentials.Username, svc.adminCredentials.Password, providerID)
}

func (svc *AdminService) EnsureIdentityProvider(config *model.IdentityProviderInput) error {
	if config.ProviderID == "" || config.ClientID == "" || config.ClientSecret == "" || config.DiscoveryURL == "" || config.TenantID == "" {
		return fmt.Errorf("invalid identity provider configuration, missing required fields")
	}

	identityProvider, err := svc.kcAdmin.GetIdentityProvider(svc.adminCredentials.Username, svc.adminCredentials.Password, string(config.ProviderID))
	if err != nil {
		return fmt.Errorf("failed to get identity provider: %w", err)
	}

	// If provider does not exist, create it
	if identityProvider == nil {
		return svc.kcAdmin.ConfigureIdentityProvider(svc.adminCredentials.Username, svc.adminCredentials.Password, convertIdentityProviderInputToKeycloakConfig(config))
	}

	// If provider exists, update it
	return svc.kcAdmin.UpdateIdentityProvider(svc.adminCredentials.Username, svc.adminCredentials.Password, convertIdentityProviderInputToKeycloakConfig(config))

}

func convertIdentityProviderInputToKeycloakConfig(input *model.IdentityProviderInput) *keycloak.IdentityProviderConfig {
	return &keycloak.IdentityProviderConfig{
		ProviderAlias: string(input.ProviderID),
		ClientID:      input.ClientID,
		ClientSecret:  input.ClientSecret,
		DiscoveryURL:  input.DiscoveryURL,
		TenantID:      input.TenantID,
	}
}
