package services

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/odigos-io/central-backend/graph/model"
	"github.com/odigos-io/central-backend/internal/keycloak"
	"github.com/odigos-io/central-backend/internal/logger"
	"github.com/odigos-io/central-backend/internal/utils"
	"golang.org/x/sync/singleflight"
)

type AdminService struct {
	adminCredentials *AdminCredentials
	kcAdmin          *keycloak.KeycloakAdminService

	// The admin token comes from a password grant, which is CPU-heavy on Keycloak.
	// It is cached for most of the time Keycloak will still accept it, and
	// concurrent refreshes share one grant. The admin API rejects the JWT once
	// the user session idles out, which is sooner than the access-token expiry.
	tokenMu        sync.Mutex
	token          string
	tokenExpiresAt time.Time
	tokenFlight    singleflight.Group
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
	svc.tokenMu.Lock()
	if svc.token != "" && time.Now().Before(svc.tokenExpiresAt) {
		token := svc.token
		svc.tokenMu.Unlock()
		return token, nil
	}
	svc.tokenMu.Unlock()

	v, err, _ := svc.tokenFlight.Do("admin", func() (any, error) {
		token, expiresIn, err := svc.kcAdmin.GetAdminTokenWithExpiry(svc.adminCredentials.Username, svc.adminCredentials.Password)
		if err != nil {
			return "", err
		}
		// Refresh at 3/4 of the usable lifetime so callers never receive a token about to
		// expire. That lifetime is already capped by the Keycloak session, not just JWT exp.
		// Without a reported lifetime, don't cache.
		if expiresIn > 0 {
			svc.tokenMu.Lock()
			svc.token = token
			svc.tokenExpiresAt = time.Now().Add(expiresIn * 3 / 4)
			svc.tokenMu.Unlock()
		}
		return token, nil
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

// InvalidateAdminToken drops the cached admin token so the next lookup performs
// a new password grant. Used when Keycloak has rejected the token or restarted.
func (svc *AdminService) InvalidateAdminToken() {
	svc.tokenMu.Lock()
	svc.token = ""
	svc.tokenExpiresAt = time.Time{}
	svc.tokenMu.Unlock()
}

// WithAdminToken runs fn with the cached admin token. A Keycloak 401 drops the
// cache and retries fn once with a new grant. Other errors are returned as-is.
func (svc *AdminService) WithAdminToken(fn func(token string) error) error {
	token, err := svc.GetAdminToken()
	if err != nil {
		return err
	}
	err = fn(token)
	if !isKeycloakUnauthorized(err) {
		return err
	}
	svc.InvalidateAdminToken()
	token, err = svc.GetAdminToken()
	if err != nil {
		return err
	}
	return fn(token)
}

func isKeycloakUnauthorized(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "http 401") || strings.Contains(msg, "status 401")
}

// GetAdminUsername returns the configured Keycloak admin username.
func (svc *AdminService) GetAdminUsername() string {
	return svc.adminCredentials.Username
}

// DetermineSystemInitialized checks if the system has been initialized by querying Keycloak
// for non-admin, non-service-account users. It reports false when Keycloak cannot be
// queried, so use it only for display; authorization decisions must use IsSystemInitialized.
func (svc *AdminService) DetermineSystemInitialized() bool {
	initialized, err := svc.IsSystemInitialized()
	if err != nil {
		logger.Log.Error("Failed to determine initialization state", "error", err)
		return false
	}
	return initialized
}

// IsSystemInitialized reports whether any real (non-admin, non-service-account) user
// exists. Callers that unlock first-run setup must treat an error as initialized.
func (svc *AdminService) IsSystemInitialized() (bool, error) {
	userService := keycloak.NewKeycloakUserService()
	initialized := false
	err := svc.WithAdminToken(func(adminToken string) error {
		users, err := userService.ListUsers(adminToken)
		if err != nil {
			return err
		}
		for _, u := range users {
			if ShouldIgnoreKeycloakUser(u, svc.adminCredentials.Username) {
				continue
			}
			initialized = true
			return nil
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return initialized, nil
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

func (svc *AdminService) ListIdentityProviders() ([]keycloak.IdentityProviderRepresentation, error) {
	return svc.kcAdmin.ListIdentityProviders(svc.adminCredentials.Username, svc.adminCredentials.Password)
}

// oidcTenantIDRequired reports whether Tenant ID is needed for OIDC setup.
// Required when the discovery URL templates {tenantid} (e.g. Okta), or for Azure
// whose multi-tenant discovery document returns an issuer containing {tenantid}.
// Google and other static discovery URLs do not need a tenant value.
func oidcTenantIDRequired(providerID model.AuthProviderID, discoveryURL string) bool {
	if strings.Contains(discoveryURL, "{tenantid}") {
		return true
	}
	return providerID == model.AuthProviderIDAzure
}

// EnsureOidcIdentityProvider creates or updates an OIDC identity provider. On update
// an empty client secret keeps the stored one while the provider still points at the
// same identity provider application (see canKeepClientSecret).
func (svc *AdminService) EnsureOidcIdentityProvider(config *model.OidcIdentityProviderInput) error {
	if config.ProviderID == "" || config.ClientID == "" || config.DiscoveryURL == "" {
		return fmt.Errorf("invalid OIDC identity provider configuration, missing required fields")
	}
	if oidcTenantIDRequired(config.ProviderID, config.DiscoveryURL) && config.TenantID == "" {
		return fmt.Errorf("invalid OIDC identity provider configuration, tenant ID is required for this discovery URL")
	}

	identityProvider, err := svc.kcAdmin.GetIdentityProvider(svc.adminCredentials.Username, svc.adminCredentials.Password, strings.ToLower(string(config.ProviderID)))
	if err != nil {
		return fmt.Errorf("failed to get identity provider: %w", err)
	}
	if err := checkSameProtocol(identityProvider, keycloak.IdentityProviderTypeOIDC); err != nil {
		return err
	}

	kcConfig := convertOidcIdentityProviderInputToKeycloakConfig(config)

	// If provider does not exist, create it
	if identityProvider == nil {
		if config.ClientSecret == "" || config.ClientSecret == keycloak.SecretMask {
			return fmt.Errorf("invalid OIDC identity provider configuration, client secret is required")
		}
		return svc.kcAdmin.ConfigureOidcIdentityProvider(svc.adminCredentials.Username, svc.adminCredentials.Password, kcConfig)
	}

	// If provider exists, update it. Keycloak keeps the stored secret when it receives the mask.
	if kcConfig.ClientSecret == "" || kcConfig.ClientSecret == keycloak.SecretMask {
		if !canKeepClientSecret(identityProvider, config) {
			return fmt.Errorf("invalid OIDC identity provider configuration, client secret is required when the client ID, discovery URL or tenant ID changes")
		}
		kcConfig.ClientSecret = keycloak.SecretMask
	}
	kcConfig.Disabled = !identityProvider.Enabled
	return svc.kcAdmin.UpdateOidcIdentityProvider(svc.adminCredentials.Username, svc.adminCredentials.Password, kcConfig)
}

// canKeepClientSecret reports whether an update may keep the stored client secret.
// Keycloak sends the secret to the token endpoint found through the discovery URL, so
// keeping it while the client ID, discovery URL or tenant changes would hand the stored
// secret to whatever server the new values point at.
func canKeepClientSecret(existing *keycloak.IdentityProviderRepresentation, config *model.OidcIdentityProviderInput) bool {
	stored := existing.Config
	return stored[keycloak.ConfigKeyClientSecret] != "" &&
		stored[keycloak.ConfigKeyClientID] == config.ClientID &&
		stored[keycloak.ConfigKeyMetadataDescriptorURL] == keycloak.ResolveDiscoveryURL(config.DiscoveryURL, config.TenantID) &&
		stored[keycloak.ConfigKeyOdigosTenantID] == config.TenantID
}

// checkSameProtocol rejects changing an existing provider between OIDC and SAML:
// Keycloak keeps the original type on update, which would leave the provider broken,
// and recreating it would drop its group role mappings.
func checkSameProtocol(existing *keycloak.IdentityProviderRepresentation, providerType string) error {
	if existing == nil || existing.ProviderID == providerType {
		return nil
	}
	return fmt.Errorf("identity provider %q is set up with %s; changing its protocol is not supported", existing.Alias, strings.ToUpper(existing.ProviderID))
}

func convertOidcIdentityProviderInputToKeycloakConfig(input *model.OidcIdentityProviderInput) *keycloak.OidcIdentityProviderConfig {
	return &keycloak.OidcIdentityProviderConfig{
		ProviderAlias: strings.ToLower(string(input.ProviderID)),
		ClientID:      input.ClientID,
		ClientSecret:  input.ClientSecret,
		DiscoveryURL:  input.DiscoveryURL,
		TenantID:      input.TenantID,
	}
}

// EnsureSamlIdentityProvider creates or updates a SAML identity provider. On update
// an empty signing certificate keeps the stored one.
func (svc *AdminService) EnsureSamlIdentityProvider(config *model.SamlIdentityProviderInput) error {
	if config.ProviderID == "" || config.IdpEntityID == "" || config.SingleSignOnServiceURL == "" {
		return fmt.Errorf("invalid SAML identity provider configuration, missing required fields")
	}
	identityProvider, err := svc.kcAdmin.GetIdentityProvider(svc.adminCredentials.Username, svc.adminCredentials.Password, strings.ToLower(string(config.ProviderID)))
	if err != nil {
		return fmt.Errorf("failed to get identity provider: %w", err)
	}
	if err := checkSameProtocol(identityProvider, keycloak.IdentityProviderTypeSAML); err != nil {
		return err
	}

	samlConfig := convertSamlIdentityProviderInputToKeycloakConfig(config)
	if samlConfig.SigningCertificateBase64 == "" && identityProvider != nil {
		samlConfig.SigningCertificateBase64 = identityProvider.Config[keycloak.ConfigKeySigningCertificate]
	}
	if samlConfig.SigningCertificateBase64 == "" {
		return fmt.Errorf("invalid SAML identity provider configuration, signing certificate is required")
	}

	// If provider does not exist, create it
	if identityProvider == nil {
		return svc.kcAdmin.ConfigureSamlIdentityProvider(svc.adminCredentials.Username, svc.adminCredentials.Password, samlConfig)
	}

	// If provider exists, update it
	samlConfig.Disabled = !identityProvider.Enabled
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
	userService := keycloak.NewKeycloakUserService()
	need := true
	err := svc.WithAdminToken(func(adminToken string) error {
		users, err := userService.ListUsers(adminToken)
		if err != nil {
			return err
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
				// Fail closed (WithAdminToken retries a 401 once): an unknown role
				// may belong to an existing admin.
				return err
			}

			// If there is already a real user with admin or owner role, we do NOT need to set an admin user.
			if role == keycloak.RoleAdmin || role == keycloak.RoleOwner {
				need = false
				return nil
			}
		}
		need = true
		return nil
	})
	if err != nil {
		logger.Log.Error("Failed to determine if an admin user is needed", "error", err)
		return false
	}
	return need
}
