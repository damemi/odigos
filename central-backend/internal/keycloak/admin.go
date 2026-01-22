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

func NewKeycloakAdminService() *KeycloakAdminService {
	return &KeycloakAdminService{
		host:  consts.GetKeycloakHost(),
		realm: consts.KeycloakRealm,
	}
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
	url := fmt.Sprintf("%s/admin/realms/%s/identity-provider/instances", kc.host, kc.realm)

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
	url := fmt.Sprintf("%s/admin/realms/%s/identity-provider/instances", kc.host, kc.realm)

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

// UpdateIdentityProvider updates an existing IdP by alias using PUT.
func (kc *KeycloakAdminService) UpdateIdentityProvider(adminUsername string, adminPassword string, config *IdentityProviderConfig) error {
	url := fmt.Sprintf("%s/admin/realms/%s/identity-provider/instances/%s", kc.host, kc.realm, config.ProviderAlias)

	adminToken, err := kc.GetAdminToken(adminUsername, adminPassword)
	if err != nil {
		return err
	}

	body, err := json.Marshal(getIDPConfig(config))
	if err != nil {
		return fmt.Errorf("failed to marshal identity provider update: %w", err)
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
		return fmt.Errorf("failed to update identity provider: status %d, response: %+v", resp.StatusCode, responseBody)
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
func getIDPConfig(config *IdentityProviderConfig) map[string]interface{} {
	// Build the configuration map for the identity provider
	// https://www.keycloak.org/docs-api/latest/rest-api/index.html#IdentityProviderRepresentation

	oidcConfig := map[string]string{
		"clientAuthMethod":      "client_secret_post",
		"clientId":              config.ClientID,
		"clientSecret":          config.ClientSecret,
		"useDiscovery":          "true",
		"metadataDescriptorUrl": config.DiscoveryURL,
		"syncMode":              "FORCE",
		"validateSignature":     "true",
		"useJwksUrl":            "true",
	}

	// Proactively populate endpoints from discovery so they are present even if KC doesn't import automatically
	if config.DiscoveryURL != "" {
		for k, v := range getOIDCDiscoveryEndpoints(config) {
			oidcConfig[k] = v
		}
	}

	idpConfig := map[string]interface{}{
		"providerId":                  "oidc",
		"alias":                       config.ProviderAlias,
		"displayName":                 config.ProviderAlias,
		"enabled":                     true,
		"trustEmail":                  true,  // trusts the email from the IDP without verification, avoiding additional prompts
		"updateProfileFirstLoginMode": "off", // prevents the account update prompt after first login
		"firstBrokerLoginFlowAlias":   "first broker login",
		"config":                      oidcConfig,
	}

	return idpConfig
}

// getOIDCDiscoveryEndpoints fetches the OIDC discovery document and maps fields
// we care about into Keycloak IdP config keys.
func getOIDCDiscoveryEndpoints(config *IdentityProviderConfig) map[string]string {
	endpoints := map[string]string{}

	if config.DiscoveryURL == "" {
		return endpoints
	}

	resp, err := http.Get(config.DiscoveryURL)
	if err != nil {
		return endpoints
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return endpoints
	}

	var data map[string]interface{}
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&data); err != nil {
		return endpoints
	}

	// Helper to safely copy string fields
	copyIfString := func(targetKey, sourceKey string) {
		if val, ok := data[sourceKey]; ok {
			if s, ok := val.(string); ok && s != "" {
				endpoints[targetKey] = s
			}
		}
	}

	copyIfString("authorizationUrl", "authorization_endpoint")
	copyIfString("tokenUrl", "token_endpoint")
	copyIfString("userInfoUrl", "userinfo_endpoint")
	copyIfString("jwksUrl", "jwks_uri")
	copyIfString("issuer", "issuer")
	// Not all providers expose end_session_endpoint; copy if present
	copyIfString("logoutUrl", "end_session_endpoint")

	if endpoints["issuer"] != "" {
		// replace "{tenantid}" with the tenant id
		endpoints["issuer"] = strings.Replace(endpoints["issuer"], "{tenantid}", config.TenantID, 1)
	}

	return endpoints
}
