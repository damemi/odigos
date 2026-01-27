package services

import (
	"fmt"
	"strings"

	"github.com/odigos-io/central-backend/graph/model"
	"github.com/odigos-io/central-backend/internal/keycloak"
	"github.com/odigos-io/central-backend/internal/logger"
	"github.com/odigos-io/central-backend/internal/utils"
)

type AdminService struct {
	adminCredentials *AdminCredentials
	kcAdmin          *keycloak.KeycloakAdminService
}

// ShouldIgnoreKeycloakUser returns true for users that should be filtered out
// from user listings (service accounts and the built-in admin user).
func ShouldIgnoreKeycloakUser(u map[string]any, adminUsername string) bool {
	// Ignore Keycloak service accounts and the built-in admin user.
	if _, isServiceAccount := u["serviceAccountClientId"]; isServiceAccount {
		return true
	}
	username, _ := u["username"].(string)
	if username == adminUsername {
		return true
	}
	return false
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

// GetAdminUsername returns the configured Keycloak admin username.
func (svc *AdminService) GetAdminUsername() string {
	return svc.adminCredentials.Username
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
		if ShouldIgnoreKeycloakUser(u, svc.adminCredentials.Username) {
			continue
		}
		return true
	}
	return false
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

func (svc *AdminService) EnsureSamlIdentityProvider(config *model.SamlIdentityProviderInput) error {
	if config.ProviderID == "" || config.IdpEntityID == "" || config.SingleSignOnServiceURL == "" || config.SigningCertificate == "" {
		return fmt.Errorf("invalid SAML identity provider configuration, missing required fields")
	}
	identityProvider, err := svc.kcAdmin.GetIdentityProvider(svc.adminCredentials.Username, svc.adminCredentials.Password, strings.ToLower(string(config.ProviderID)))
	if err != nil {
		return fmt.Errorf("failed to get identity provider: %w", err)
	}

	samlConfig := convertSamlIdentityProviderInputToKeycloakConfig(config)

	// If provider does not exist, create it
	if identityProvider == nil {
		return svc.kcAdmin.ConfigureSamlIdentityProvider(svc.adminCredentials.Username, svc.adminCredentials.Password, samlConfig)
	}

	// If provider exists, update it
	return svc.kcAdmin.UpdateSamlIdentityProvider(svc.adminCredentials.Username, svc.adminCredentials.Password, samlConfig)
}

func convertSamlIdentityProviderInputToKeycloakConfig(input *model.SamlIdentityProviderInput) *keycloak.SamlIdentityProviderConfig {
	return &keycloak.SamlIdentityProviderConfig{
		ProviderAlias:            strings.ToLower(string(input.ProviderID)),
		IdpEntityID:              input.IdpEntityID,
		SingleSignOnServiceURL:   input.SingleSignOnServiceURL,
		SigningCertificateBase64: input.SigningCertificate,
		ValidateSignature:        true,
		PostBindingAuthnRequest:  false,
		PostBindingResponse:      false,
	}
}

func (svc *AdminService) NeedToSetAdminUser() bool {

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
		if ShouldIgnoreKeycloakUser(u, svc.adminCredentials.Username) {
			continue
		}

		userID, ok := u["id"].(string)
		if !ok || userID == "" {
			continue
		}

		role, err := userService.GetUserRole(adminToken, userID)
		if err != nil {
			logger.Log.Error("Failed to get user role while determining if admin user is needed", "error", err, "userID", userID)
			continue
		}

		// If there is already a real user with admin role, we do NOT need to set an admin user.
		if role == keycloak.RoleAdmin {
			return false
		}
	}

	// No real user with admin role found -> we need to set one.
	return true
}
