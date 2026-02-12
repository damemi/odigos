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

type IdentityProviderConfig struct {
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

func (e oidcDiscoveryEndpoints) ToConfigMap() map[string]string {
	out := map[string]string{}
	if e.AuthorizationURL != "" {
		out["authorizationUrl"] = e.AuthorizationURL
	}
	if e.TokenURL != "" {
		out["tokenUrl"] = e.TokenURL
	}
	if e.UserInfoURL != "" {
		out["userInfoUrl"] = e.UserInfoURL
	}
	if e.JWKSURL != "" {
		out["jwksUrl"] = e.JWKSURL
	}
	if e.Issuer != "" {
		out["issuer"] = e.Issuer
	}
	if e.LogoutURL != "" {
		out["logoutUrl"] = e.LogoutURL
	}
	return out
}

type oidcIdentityProviderConfig struct {
	ClientAuthMethod      string
	ClientID              string
	ClientSecret          string
	UseDiscovery          string
	MetadataDescriptorURL string
	SyncMode              string
	ValidateSignature     string
	UseJwksURL            string

	AuthorizationURL string
	TokenURL         string
	UserInfoURL      string
	JWKSURL          string
	Issuer           string
	LogoutURL        string
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

func (c oidcIdentityProviderConfig) ToConfigMap() map[string]string {
	out := map[string]string{
		"clientAuthMethod":      c.ClientAuthMethod,
		"clientId":              c.ClientID,
		"clientSecret":          c.ClientSecret,
		"useDiscovery":          c.UseDiscovery,
		"metadataDescriptorUrl": c.MetadataDescriptorURL,
		"syncMode":              c.SyncMode,
		"validateSignature":     c.ValidateSignature,
		"useJwksUrl":            c.UseJwksURL,
	}

	// Keep exact same key names as before.
	if c.AuthorizationURL != "" {
		out["authorizationUrl"] = c.AuthorizationURL
	}
	if c.TokenURL != "" {
		out["tokenUrl"] = c.TokenURL
	}
	if c.UserInfoURL != "" {
		out["userInfoUrl"] = c.UserInfoURL
	}
	if c.JWKSURL != "" {
		out["jwksUrl"] = c.JWKSURL
	}
	if c.Issuer != "" {
		out["issuer"] = c.Issuer
	}
	if c.LogoutURL != "" {
		out["logoutUrl"] = c.LogoutURL
	}

	return out
}

type samlIdentityProviderConfig struct {
	// NOTE: keep key names identical to previous map usage.
	EntityID                string
	SingleSignOnServiceURL  string
	SigningCertificate      string
	ValidateSignature       string
	PostBindingAuthnRequest string
	PostBindingResponse     string
}

func (c samlIdentityProviderConfig) ToConfigMap() map[string]string {
	return map[string]string{
		"entityId":                c.EntityID,
		"singleSignOnServiceUrl":  c.SingleSignOnServiceURL,
		"signingCertificate":      c.SigningCertificate,
		"validateSignature":       c.ValidateSignature,
		"postBindingAuthnRequest": c.PostBindingAuthnRequest,
		"postBindingResponse":     c.PostBindingResponse,
	}
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
	url := fmt.Sprintf("%s/realms/master/protocol/openid-connect/token", kc.host)
	payload := []byte("grant_type=password&client_id=admin-cli&username=" + adminUsername + "&password=" + adminPassword)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("failed to get admin token: %s", string(body))
	}

	var token tokenResponse
	err = json.Unmarshal(body, &token)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal admin token: %w", err)
	}

	return token.AccessToken, nil
}

func (kc *KeycloakAdminService) GetIdentityProvider(adminUsername string, adminPassword string, providerAlias string) (*IdentityProviderRepresentation, error) {
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
		return nil, fmt.Errorf("failed to get identity provider: status %d, response: %s", resp.StatusCode, string(responseBody))
	}

	var idpList []IdentityProviderRepresentation
	err = json.Unmarshal(responseBody, &idpList)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal identity provider during get: %w", err)
	}

	for _, idp := range idpList {
		if idp.Alias == providerAlias {
			return &idp, nil
		}
	}

	return nil, nil
}

func (kc *KeycloakAdminService) ConfigureIdentityProvider(adminUsername string, adminPassword string, config *IdentityProviderConfig) error {
	url := kc.identityProviderInstancesURL()

	adminToken, err := kc.GetAdminToken(adminUsername, adminPassword)
	if err != nil {
		return err
	}

	body, err := json.Marshal(getIDPConfig(config))
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

	return nil
}

func (kc *KeycloakAdminService) ConfigureSamlIdentityProvider(adminUsername string, adminPassword string, config *SamlIdentityProviderConfig) error {
	url := kc.identityProviderInstancesURL()

	adminToken, err := kc.GetAdminToken(adminUsername, adminPassword)
	if err != nil {
		return err
	}

	body, err := json.Marshal(getSamlIDPConfig(config))
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

// UpdateIdentityProvider updates an existing IdP by alias using PUT.
func (kc *KeycloakAdminService) UpdateIdentityProvider(adminUsername string, adminPassword string, config *IdentityProviderConfig) error {
	return kc.updateIdentityProvider(adminUsername, adminPassword, config.ProviderAlias, getIDPConfig(config), AuthProtocolOIDC)
}

// UpdateSamlIdentityProvider updates an existing SAML IdP by alias using PUT.
func (kc *KeycloakAdminService) UpdateSamlIdentityProvider(adminUsername string, adminPassword string, config *SamlIdentityProviderConfig) error {
	if err := kc.updateIdentityProvider(adminUsername, adminPassword, config.ProviderAlias, getSamlIDPConfig(config), AuthProtocolSAML); err != nil {
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

// getIDPConfig builds the IDP config map for the given configuration.
func getIDPConfig(config *IdentityProviderConfig) IdentityProviderUpsertPayload {
	// Build the configuration map for the identity provider
	// https://www.keycloak.org/docs-api/latest/rest-api/index.html#IdentityProviderRepresentation

	oidcConfig := oidcIdentityProviderConfig{
		ClientAuthMethod:      "client_secret_post",
		ClientID:              config.ClientID,
		ClientSecret:          config.ClientSecret,
		UseDiscovery:          "true",
		MetadataDescriptorURL: config.DiscoveryURL,
		SyncMode:              "FORCE",
		ValidateSignature:     "true",
		UseJwksURL:            "true",
	}

	// Proactively populate endpoints from discovery so they are present even if KC doesn't import automatically
	if config.DiscoveryURL != "" {
		oidcConfig.ApplyDiscoveryEndpoints(getOIDCDiscoveryEndpoints(config))
	}

	return IdentityProviderUpsertPayload{
		ProviderID:                  "oidc",
		Alias:                       config.ProviderAlias,
		DisplayName:                 config.ProviderAlias,
		Enabled:                     true,
		TrustEmail:                  true,  // trusts the email from the IDP without verification, avoiding additional prompts
		UpdateProfileFirstLoginMode: "off", // prevents the account update prompt after first login
		FirstBrokerLoginFlowAlias:   "first broker login",
		Config:                      oidcConfig.ToConfigMap(),
	}
}

func getSamlIDPConfig(config *SamlIdentityProviderConfig) IdentityProviderUpsertPayload {
	samlConfig := samlIdentityProviderConfig{
		EntityID:                config.IdpEntityID,
		SingleSignOnServiceURL:  config.SingleSignOnServiceURL,
		SigningCertificate:      config.SigningCertificateBase64,
		ValidateSignature:       fmt.Sprintf("%t", config.ValidateSignature),
		PostBindingAuthnRequest: fmt.Sprintf("%t", config.PostBindingAuthnRequest),
		PostBindingResponse:     fmt.Sprintf("%t", config.PostBindingResponse),
	}

	return IdentityProviderUpsertPayload{
		ProviderID:                  "saml",
		Alias:                       config.ProviderAlias,
		DisplayName:                 config.ProviderAlias,
		Enabled:                     true,
		TrustEmail:                  true,
		UpdateProfileFirstLoginMode: "off",
		FirstBrokerLoginFlowAlias:   "first broker login",
		Config:                      samlConfig.ToConfigMap(),
	}
}

// getOIDCDiscoveryEndpoints fetches the OIDC discovery document and maps fields
// we care about into Keycloak IdP config keys.
func getOIDCDiscoveryEndpoints(config *IdentityProviderConfig) oidcDiscoveryEndpoints {
	if config.DiscoveryURL == "" {
		return oidcDiscoveryEndpoints{}
	}

	resp, err := http.Get(config.DiscoveryURL)
	if err != nil {
		return oidcDiscoveryEndpoints{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return oidcDiscoveryEndpoints{}
	}

	var data map[string]interface{}
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&data); err != nil {
		return oidcDiscoveryEndpoints{}
	}

	endpoints := oidcDiscoveryEndpoints{}

	copyIfString := func(getKey string, set func(string)) {
		if val, ok := data[getKey]; ok {
			if s, ok := val.(string); ok && s != "" {
				set(s)
			}
		}
	}

	copyIfString("authorization_endpoint", func(s string) { endpoints.AuthorizationURL = s })
	copyIfString("token_endpoint", func(s string) { endpoints.TokenURL = s })
	copyIfString("userinfo_endpoint", func(s string) { endpoints.UserInfoURL = s })
	copyIfString("jwks_uri", func(s string) { endpoints.JWKSURL = s })
	copyIfString("issuer", func(s string) { endpoints.Issuer = s })
	// Not all providers expose end_session_endpoint; copy if present
	copyIfString("end_session_endpoint", func(s string) { endpoints.LogoutURL = s })

	if endpoints.Issuer != "" {
		// replace "{tenantid}" with the tenant id
		endpoints.Issuer = strings.Replace(endpoints.Issuer, "{tenantid}", config.TenantID, 1)
	}

	return endpoints
}
