package keycloak

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/odigos-io/central-backend/graph/model"
	"github.com/odigos-io/central-backend/internal/consts"
	"github.com/odigos-io/central-backend/internal/logger"
)

type KeycloakAdminService struct {
	host  string
	realm string
}

// OidcIdentityProviderConfig holds the configuration needed to create/update an OIDC IdP in Keycloak.
type OidcIdentityProviderConfig struct {
	ProviderAlias string
	ClientID      string
	ClientSecret  string
	DiscoveryURL  string
	TenantID      string
}

type SamlProviderConfig struct {
	EntityID string
	SSOURL   string
	Cert     string
}

// SamlIdentityProviderConfig holds the configuration needed to create/update a SAML IdP in Keycloak.
// SigningCertificateBase64 should be the base64 body of the X509 certificate (no PEM headers).
type SamlIdentityProviderConfig struct {
	ProviderAlias            string
	EntityID                 string
	IdpEntityID              string
	SingleSignOnServiceURL   string
	SigningCertificateBase64 string
	ValidateSignature        bool
	PostBindingAuthnRequest  bool
	PostBindingResponse      bool
}

type AuthProtocol string

const (
	AuthProtocolSAML AuthProtocol = "SAML"
	AuthProtocolOIDC AuthProtocol = "OIDC"
)

type IdentityProviderRepresentation struct {
	Alias                       string            `json:"alias"`
	DisplayName                 string            `json:"displayName"`
	InternalID                  string            `json:"internalId"`
	ProviderID                  string            `json:"providerId"`
	Enabled                     bool              `json:"enabled"`
	UpdateProfileFirstLoginMode string            `json:"updateProfileFirstLoginMode"`
	TrustEmail                  bool              `json:"trustEmail"`
	StoreToken                  bool              `json:"storeToken"`
	AddReadTokenRoleOnCreate    bool              `json:"addReadTokenRoleOnCreate"`
	AuthenticateByDefault       bool              `json:"authenticateByDefault"`
	LinkOnly                    bool              `json:"linkOnly"`
	HideOnLogin                 bool              `json:"hideOnLogin"`
	FirstBrokerLoginFlowAlias   string            `json:"firstBrokerLoginFlowAlias"`
	Config                      map[string]string `json:"config"`
}

// IdentityProviderUpsertPayload is the JSON payload Keycloak expects for creating/updating an IdP.
// It works for both OIDC ("oidc") and SAML ("saml") providers by changing ProviderID and Config.
type IdentityProviderUpsertPayload struct {
	ProviderID                  string            `json:"providerId"`
	Alias                       string            `json:"alias"`
	DisplayName                 string            `json:"displayName"`
	Enabled                     bool              `json:"enabled"`
	TrustEmail                  bool              `json:"trustEmail"`
	UpdateProfileFirstLoginMode string            `json:"updateProfileFirstLoginMode"`
	FirstBrokerLoginFlowAlias   string            `json:"firstBrokerLoginFlowAlias"`
	Config                      map[string]string `json:"config"`
}

type oidcDiscoveryDocument struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
	JwksURI               string `json:"jwks_uri"`
	Issuer                string `json:"issuer"`
	EndSessionEndpoint    string `json:"end_session_endpoint"`
}

type oidcDiscoveryEndpoints struct {
	AuthorizationURL string
	TokenURL         string
	UserInfoURL      string
	JWKSURL          string
	Issuer           string
	LogoutURL        string
}

// oidcIdentityProviderConfig is the typed Keycloak OIDC identity-provider config.
// Keycloak's Admin API expects config as map[string]string; ToConfigMap converts this struct.
type oidcIdentityProviderConfig struct {
	ClientAuthMethod      string `json:"clientAuthMethod"`
	ClientID              string `json:"clientId"`
	ClientSecret          string `json:"clientSecret"`
	UseDiscovery          string `json:"useDiscovery"`
	MetadataDescriptorURL string `json:"metadataDescriptorUrl"`
	SyncMode              string `json:"syncMode"`
	ValidateSignature     string `json:"validateSignature"`
	UseJwksURL            string `json:"useJwksUrl"`
	DefaultScope          string `json:"defaultScope,omitempty"`

	AuthorizationURL string `json:"authorizationUrl,omitempty"`
	TokenURL         string `json:"tokenUrl,omitempty"`
	UserInfoURL      string `json:"userInfoUrl,omitempty"`
	JWKSURL          string `json:"jwksUrl,omitempty"`
	Issuer           string `json:"issuer,omitempty"`
	LogoutURL        string `json:"logoutUrl,omitempty"`
}

func (c *oidcIdentityProviderConfig) ApplyDiscoveryEndpoints(e oidcDiscoveryEndpoints) {
	// Match the existing behavior: only set when non-empty, and allow discovery to override.
	if e.AuthorizationURL != "" {
		c.AuthorizationURL = e.AuthorizationURL
	}
	if e.TokenURL != "" {
		c.TokenURL = e.TokenURL
	}
	if e.UserInfoURL != "" {
		c.UserInfoURL = e.UserInfoURL
	}
	if e.JWKSURL != "" {
		c.JWKSURL = e.JWKSURL
	}
	if e.Issuer != "" {
		c.Issuer = e.Issuer
	}
	if e.LogoutURL != "" {
		c.LogoutURL = e.LogoutURL
	}
}

func (c oidcIdentityProviderConfig) ToConfigMap() (map[string]string, error) {
	body, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("marshal OIDC identity provider config: %w", err)
	}
	out := map[string]string{}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("unmarshal OIDC identity provider config: %w", err)
	}
	return out, nil
}

type samlIdentityProviderConfig struct {
	EntityID                string `json:"entityId"`
	SingleSignOnServiceURL  string `json:"singleSignOnServiceUrl"`
	SigningCertificate      string `json:"signingCertificate"`
	ValidateSignature       string `json:"validateSignature"`
	PostBindingAuthnRequest string `json:"postBindingAuthnRequest"`
	PostBindingResponse     string `json:"postBindingResponse"`
}

func (c samlIdentityProviderConfig) ToConfigMap() (map[string]string, error) {
	body, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("marshal SAML identity provider config: %w", err)
	}
	out := map[string]string{}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("unmarshal SAML identity provider config: %w", err)
	}
	return out, nil
}

func NewKeycloakAdminService() *KeycloakAdminService {
	return &KeycloakAdminService{
		host:  consts.GetKeycloakHost(),
		realm: consts.KeycloakRealm,
	}
}

func (kc *KeycloakAdminService) identityProviderInstancesURL() string {
	return fmt.Sprintf("%s/admin/realms/%s/identity-provider/instances", kc.host, kc.realm)
}

func (kc *KeycloakAdminService) identityProviderInstanceURL(providerAlias string) string {
	return fmt.Sprintf("%s/admin/realms/%s/identity-provider/instances/%s", kc.host, kc.realm, providerAlias)
}

func (kc *KeycloakAdminService) GetAdminToken(adminUsername string, adminPassword string) (string, error) {
	token, _, err := kc.GetAdminTokenWithExpiry(adminUsername, adminPassword)
	return token, err
}

// GetAdminTokenWithExpiry performs the master-realm password grant and returns how long
// the token stays usable for admin API calls (zero when Keycloak omits a lifetime).
// The window is the shorter of expires_in (JWT) and refresh_expires_in (SSO session).
// The admin API returns 401 after the session idles out, even while the JWT exp is still in the future.
func (kc *KeycloakAdminService) GetAdminTokenWithExpiry(adminUsername string, adminPassword string) (string, time.Duration, error) {
	url := fmt.Sprintf("%s/realms/master/protocol/openid-connect/token", kc.host)
	payload := []byte("grant_type=password&client_id=admin-cli&username=" + adminUsername + "&password=" + adminPassword)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(payload))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", 0, fmt.Errorf("failed to get admin token: %s", string(body))
	}

	var token tokenResponse
	err = json.Unmarshal(body, &token)
	if err != nil {
		return "", 0, fmt.Errorf("failed to unmarshal admin token: %w", err)
	}

	return token.AccessToken, usableAdminTokenLifetime(token.ExpiresIn, token.RefreshExpiresIn), nil
}

// usableAdminTokenLifetime is how long the admin API will accept this access token.
// A missing refresh lifetime means Keycloak omitted it, so the JWT expiry is used.
// A missing JWT expiry with a refresh lifetime still has a live session.
func usableAdminTokenLifetime(expiresIn, refreshExpiresIn int) time.Duration {
	seconds := expiresIn
	if refreshExpiresIn > 0 && (seconds <= 0 || refreshExpiresIn < seconds) {
		seconds = refreshExpiresIn
	}
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func (kc *KeycloakAdminService) ListIdentityProviders(adminUsername string, adminPassword string) ([]IdentityProviderRepresentation, error) {
	url := kc.identityProviderInstancesURL()

	adminToken, err := kc.GetAdminToken(adminUsername, adminPassword)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	responseBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to list identity providers: status %d, response: %s", resp.StatusCode, string(responseBody))
	}

	var idpList []IdentityProviderRepresentation
	err = json.Unmarshal(responseBody, &idpList)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal identity providers during list: %w", err)
	}

	return idpList, nil
}

func (kc *KeycloakAdminService) GetIdentityProvider(adminUsername string, adminPassword string, providerAlias string) (*IdentityProviderRepresentation, error) {
	idpList, err := kc.ListIdentityProviders(adminUsername, adminPassword)
	if err != nil {
		return nil, err
	}

	for _, idp := range idpList {
		if idp.Alias == providerAlias {
			return &idp, nil
		}
	}

	return nil, nil
}

func (kc *KeycloakAdminService) ConfigureOidcIdentityProvider(adminUsername string, adminPassword string, config *OidcIdentityProviderConfig) error {
	url := kc.identityProviderInstancesURL()

	adminToken, err := kc.GetAdminToken(adminUsername, adminPassword)
	if err != nil {
		return err
	}

	payload, err := getIDPConfig(config)
	if err != nil {
		return err
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal identity provider config: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	responseBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("failed to configure identity provider: status %d, response: %+v", resp.StatusCode, responseBody)
	}

	if err := kc.ensureOidcAttributeMappers(adminToken, config.ProviderAlias); err != nil {
		logger.Log.Error("Failed to create OIDC attribute mappers", "provider", config.ProviderAlias, "error", err)
	}

	return nil
}

func (kc *KeycloakAdminService) ConfigureSamlIdentityProvider(adminUsername string, adminPassword string, config *SamlIdentityProviderConfig) error {
	url := kc.identityProviderInstancesURL()

	adminToken, err := kc.GetAdminToken(adminUsername, adminPassword)
	if err != nil {
		return err
	}

	payload, err := getSamlIDPConfig(config)
	if err != nil {
		return err
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal SAML identity provider config: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	responseBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("failed to configure SAML identity provider: status %d, response: %+v", resp.StatusCode, responseBody)
	}

	if err := kc.ensureSamlAttributeMappers(adminToken, config.ProviderAlias); err != nil {
		logger.Log.Error("Failed to create SAML attribute mappers", "provider", config.ProviderAlias, "error", err)
	}

	return nil
}

func (kc *KeycloakAdminService) updateIdentityProvider(adminUsername string, adminPassword string, providerAlias string, payload IdentityProviderUpsertPayload, providerTypeForError AuthProtocol) error {
	url := kc.identityProviderInstanceURL(providerAlias)

	adminToken, err := kc.GetAdminToken(adminUsername, adminPassword)
	if err != nil {
		return err
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal %s identity provider update: %w", providerTypeForError, err)
	}

	req, err := http.NewRequest("PUT", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create update request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send update request: %w", err)
	}
	defer resp.Body.Close()

	responseBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("failed to update %s identity provider: status %d, response: %+v", providerTypeForError, resp.StatusCode, responseBody)
	}

	return nil
}

// UpdateOidcIdentityProvider updates an existing OIDC IdP by alias using PUT.
func (kc *KeycloakAdminService) UpdateOidcIdentityProvider(adminUsername string, adminPassword string, config *OidcIdentityProviderConfig) error {
	payload, err := getIDPConfig(config)
	if err != nil {
		return err
	}
	if err := kc.updateIdentityProvider(adminUsername, adminPassword, config.ProviderAlias, payload, AuthProtocolOIDC); err != nil {
		return err
	}

	adminToken, err := kc.GetAdminToken(adminUsername, adminPassword)
	if err != nil {
		return err
	}

	if err := kc.ensureOidcAttributeMappers(adminToken, config.ProviderAlias); err != nil {
		logger.Log.Error("Failed to ensure OIDC attribute mappers on update", "provider", config.ProviderAlias, "error", err)
	}

	return nil
}

// UpdateSamlIdentityProvider updates an existing SAML IdP by alias using PUT.
func (kc *KeycloakAdminService) UpdateSamlIdentityProvider(adminUsername string, adminPassword string, config *SamlIdentityProviderConfig) error {
	payload, err := getSamlIDPConfig(config)
	if err != nil {
		return err
	}
	if err := kc.updateIdentityProvider(adminUsername, adminPassword, config.ProviderAlias, payload, AuthProtocolSAML); err != nil {
		return err
	}

	adminToken, err := kc.GetAdminToken(adminUsername, adminPassword)
	if err != nil {
		return err
	}

	if err := kc.ensureSamlAttributeMappers(adminToken, config.ProviderAlias); err != nil {
		logger.Log.Error("Failed to ensure SAML attribute mappers on update", "provider", config.ProviderAlias, "error", err)
	}

	return nil
}

type samlAttributeMapper struct {
	Name          string
	AttributeName string
	UserAttribute string
}

// ensureSamlAttributeMappers creates standard SAML attribute mappers (email, firstName, lastName)
// for the given identity provider. It skips mappers that already exist.
func (kc *KeycloakAdminService) ensureSamlAttributeMappers(adminToken string, providerAlias string) error {
	mappers := []samlAttributeMapper{
		{
			Name:          "email",
			AttributeName: "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress",
			UserAttribute: "email",
		},
		{
			Name:          "firstName",
			AttributeName: "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/givenname",
			UserAttribute: "firstName",
		},
		{
			Name:          "lastName",
			AttributeName: "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/surname",
			UserAttribute: "lastName",
		},
	}

	mappersURL := fmt.Sprintf("%s/admin/realms/%s/identity-provider/instances/%s/mappers", kc.host, kc.realm, providerAlias)

	// Get existing mappers to avoid duplicates
	req, err := http.NewRequest("GET", mappersURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create GET mappers request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to get existing mappers: %w", err)
	}
	defer resp.Body.Close()

	var existingMappers []map[string]any
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&existingMappers); err != nil {
			return fmt.Errorf("failed to decode existing mappers: %w", err)
		}
	}

	existingNames := make(map[string]bool)
	for _, m := range existingMappers {
		if name, ok := m["name"].(string); ok {
			existingNames[name] = true
		}
	}

	for _, mapper := range mappers {
		if existingNames[mapper.Name] {
			continue
		}

		payload := map[string]any{
			"name":                     mapper.Name,
			"identityProviderAlias":    providerAlias,
			"identityProviderMapper":   "saml-user-attribute-idp-mapper",
			"config": map[string]string{
				"syncMode":       "FORCE",
				"user.attribute": mapper.UserAttribute,
				"attribute.name": mapper.AttributeName,
			},
		}

		body, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("failed to marshal mapper %s: %w", mapper.Name, err)
		}

		req, err := http.NewRequest("POST", mappersURL, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("failed to create mapper request for %s: %w", mapper.Name, err)
		}
		req.Header.Set("Authorization", "Bearer "+adminToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("failed to create mapper %s: %w", mapper.Name, err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusCreated {
			return fmt.Errorf("failed to create mapper %s: status %d", mapper.Name, resp.StatusCode)
		}

		logger.Log.Info("Created SAML attribute mapper", "provider", providerAlias, "mapper", mapper.Name)
	}

	return nil
}

type oidcAttributeMapper struct {
	Name      string
	ClaimName string
	UserAttr  string
}

// ensureOidcAttributeMappers creates standard OIDC attribute mappers (email, firstName, lastName)
// for the given identity provider. It skips mappers that already exist.
func (kc *KeycloakAdminService) ensureOidcAttributeMappers(adminToken string, providerAlias string) error {
	mappers := []oidcAttributeMapper{
		// Azure AD often does not return the "email" claim even with the email scope.
		// Map "preferred_username" (UPN) to email first as a fallback, then let the
		// real "email" claim overwrite it if present.
		{Name: "email-from-upn", ClaimName: "preferred_username", UserAttr: "email"},
		{Name: "email", ClaimName: "email", UserAttr: "email"},
		{Name: "firstName", ClaimName: "given_name", UserAttr: "firstName"},
		{Name: "lastName", ClaimName: "family_name", UserAttr: "lastName"},
	}

	mappersURL := fmt.Sprintf("%s/admin/realms/%s/identity-provider/instances/%s/mappers", kc.host, kc.realm, providerAlias)

	// Get existing mappers to avoid duplicates
	req, err := http.NewRequest("GET", mappersURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create GET mappers request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to get existing mappers: %w", err)
	}
	defer resp.Body.Close()

	var existingMappers []map[string]any
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&existingMappers); err != nil {
			return fmt.Errorf("failed to decode existing mappers: %w", err)
		}
	}

	existingNames := make(map[string]bool)
	for _, m := range existingMappers {
		if name, ok := m["name"].(string); ok {
			existingNames[name] = true
		}
	}

	for _, mapper := range mappers {
		if existingNames[mapper.Name] {
			continue
		}

		payload := map[string]any{
			"name":                   mapper.Name,
			"identityProviderAlias":  providerAlias,
			"identityProviderMapper": "oidc-user-attribute-idp-mapper",
			"config": map[string]string{
				"syncMode":       "FORCE",
				"claim":          mapper.ClaimName,
				"user.attribute": mapper.UserAttr,
			},
		}

		body, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("failed to marshal mapper %s: %w", mapper.Name, err)
		}

		req, err := http.NewRequest("POST", mappersURL, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("failed to create mapper request for %s: %w", mapper.Name, err)
		}
		req.Header.Set("Authorization", "Bearer "+adminToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("failed to create mapper %s: %w", mapper.Name, err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusCreated {
			return fmt.Errorf("failed to create mapper %s: status %d", mapper.Name, resp.StatusCode)
		}

		logger.Log.Info("Created OIDC attribute mapper", "provider", providerAlias, "mapper", mapper.Name)
	}

	return nil
}

func GetKeycloakAdminTokenWithRetry(ctx context.Context, shutdownCh <-chan struct{}, username, password string) (string, error) {
	logger.Log.Info("Waiting for Keycloak to become ready")
	var err error
	kc := NewKeycloakAdminService()
	start := time.Now()
	attempt := 0
	backoff := 1 * time.Second
	maxBackoff := 30 * time.Second

	for {
		select {
		case <-shutdownCh:
			err = fmt.Errorf("shutdown signal received")
			return "", err
		default:
		}

		attempt++
		// First, wait for Keycloak to be reachable/cancelable (HealthCheck uses context).
		attemptCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		go func() {
			select {
			case <-shutdownCh:
				cancel()
			case <-attemptCtx.Done():
			}
		}()
		h := kc.HealthCheck(attemptCtx)
		cancel()

		if h.Status == model.HealthStatusHealthy {
			// Keycloak is up; now fetch token (no context support here by request).
			adminToken, err := kc.GetAdminToken(username, password)
			if err == nil {
				logger.Log.Info("Successfully obtained Keycloak admin token", "attempt", attempt, "elapsed", time.Since(start))
				return adminToken, nil
			}
		} else {
			err = fmt.Errorf("keycloak healthcheck not healthy")
		}

		timer := time.NewTimer(backoff)
		select {
		case <-shutdownCh:
			timer.Stop()
			return "", fmt.Errorf("shutdown signal received")
		case <-timer.C:
		}

		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// resolveDiscoveryURL replaces {tenantid} placeholders in the discovery URL.
func resolveDiscoveryURL(discoveryURL, tenantID string) string {
	return strings.ReplaceAll(discoveryURL, "{tenantid}", tenantID)
}

// getIDPConfig builds the IDP config map for the given configuration.
func getIDPConfig(config *OidcIdentityProviderConfig) (IdentityProviderUpsertPayload, error) {
	// Build the configuration map for the identity provider
	// https://www.keycloak.org/docs-api/latest/rest-api/index.html#IdentityProviderRepresentation

	discoveryURL := resolveDiscoveryURL(config.DiscoveryURL, config.TenantID)

	oidcConfig := oidcIdentityProviderConfig{
		ClientAuthMethod:      "client_secret_post",
		ClientID:              config.ClientID,
		ClientSecret:          config.ClientSecret,
		UseDiscovery:          "true",
		MetadataDescriptorURL: discoveryURL,
		SyncMode:              "FORCE",
		ValidateSignature:     "true",
		UseJwksURL:            "true",
		DefaultScope:          "openid email profile",
	}

	// Proactively populate endpoints from discovery so they are present even if KC doesn't import automatically
	if discoveryURL != "" {
		oidcConfig.ApplyDiscoveryEndpoints(getOIDCDiscoveryEndpoints(config, discoveryURL))
	}

	configMap, err := oidcConfig.ToConfigMap()
	if err != nil {
		return IdentityProviderUpsertPayload{}, err
	}

	return IdentityProviderUpsertPayload{
		ProviderID:                  "oidc",
		Alias:                       config.ProviderAlias,
		DisplayName:                 config.ProviderAlias,
		Enabled:                     true,
		TrustEmail:                  true,  // trusts the email from the IDP without verification, avoiding additional prompts
		UpdateProfileFirstLoginMode: "off", // prevents the account update prompt after first login
		FirstBrokerLoginFlowAlias:   "first broker login",
		Config:                      configMap,
	}, nil
}

func getSamlIDPConfig(config *SamlIdentityProviderConfig) (IdentityProviderUpsertPayload, error) {
	samlConfig := samlIdentityProviderConfig{
		EntityID:                config.IdpEntityID,
		SingleSignOnServiceURL:  config.SingleSignOnServiceURL,
		SigningCertificate:      config.SigningCertificateBase64,
		ValidateSignature:       fmt.Sprintf("%t", config.ValidateSignature),
		PostBindingAuthnRequest: fmt.Sprintf("%t", config.PostBindingAuthnRequest),
		PostBindingResponse:     fmt.Sprintf("%t", config.PostBindingResponse),
	}

	configMap, err := samlConfig.ToConfigMap()
	if err != nil {
		return IdentityProviderUpsertPayload{}, err
	}

	return IdentityProviderUpsertPayload{
		ProviderID:                  "saml",
		Alias:                       config.ProviderAlias,
		DisplayName:                 config.ProviderAlias,
		Enabled:                     true,
		TrustEmail:                  true,
		UpdateProfileFirstLoginMode: "off",
		FirstBrokerLoginFlowAlias:   "first broker login",
		Config:                      configMap,
	}, nil
}

// getOIDCDiscoveryEndpoints fetches the OIDC discovery document and maps fields
// we care about into Keycloak IdP config keys.
func getOIDCDiscoveryEndpoints(config *OidcIdentityProviderConfig, discoveryURL string) oidcDiscoveryEndpoints {
	if discoveryURL == "" {
		return oidcDiscoveryEndpoints{}
	}

	resp, err := http.Get(discoveryURL)
	if err != nil {
		return oidcDiscoveryEndpoints{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return oidcDiscoveryEndpoints{}
	}

	var data oidcDiscoveryDocument
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return oidcDiscoveryEndpoints{}
	}

	endpoints := oidcDiscoveryEndpoints{
		AuthorizationURL: data.AuthorizationEndpoint,
		TokenURL:         data.TokenEndpoint,
		UserInfoURL:      data.UserinfoEndpoint,
		JWKSURL:          data.JwksURI,
		Issuer:           data.Issuer,
		LogoutURL:        data.EndSessionEndpoint,
	}

	if endpoints.Issuer != "" {
		// Azure multi-tenant discovery returns issuer with a {tenantid} placeholder.
		endpoints.Issuer = strings.ReplaceAll(endpoints.Issuer, "{tenantid}", config.TenantID)
	}

	return endpoints
}
