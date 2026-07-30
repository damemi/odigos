package keycloak

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/odigos-io/central-backend/graph/model"
	"github.com/odigos-io/central-backend/internal/consts"
)

const (
	RoleAdmin      = "admin"
	RoleOwner      = "owner"
	RoleMaintainer = "maintainer"
	RoleViewer     = "viewer"
	RoleNoAccess   = "no-access"
)

const (
	ClientTypePassword = "password"
	WildcardOrigin     = "*"
)

type tokenResponse struct {
	AccessToken string `json:"access_token"`
}

type ClientConfig struct {
	ClientID                  string   `json:"clientId"`
	Name                      string   `json:"name"`
	Enabled                   bool     `json:"enabled"`
	PublicClient              bool     `json:"publicClient"`
	DirectAccessGrantsEnabled bool     `json:"directAccessGrantsEnabled"`
	StandardFlowEnabled       bool     `json:"standardFlowEnabled"`
	ImplicitFlowEnabled       bool     `json:"implicitFlowEnabled"`
	ServiceAccountsEnabled    bool     `json:"serviceAccountsEnabled"`
	WebOrigins                []string `json:"webOrigins"`
	RedirectUris              []string `json:"redirectUris"`
}

type RoleData struct {
	Name string `json:"name"`
}

type clientListItem struct {
	ClientID string `json:"clientId"`
}

// OdigosUIClientExists reports whether the odigos-central public client is present.
// Used after Keycloak restarts with ephemeral H2, when the client can disappear
// even if the realm discovery endpoint already looks healthy.
func OdigosUIClientExists(token, keycloakHost, realm string) (bool, error) {
	clientsURL := fmt.Sprintf("%s/admin/realms/%s/clients?clientId=%s", keycloakHost, realm, consts.ClientID)
	req, err := http.NewRequest(http.MethodGet, clientsURL, nil)
	if err != nil {
		return false, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to get clients: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("failed to list clients: status %d, body: %s", resp.StatusCode, string(responseBody))
	}

	var clients []clientListItem
	if err := json.NewDecoder(resp.Body).Decode(&clients); err != nil {
		return false, fmt.Errorf("failed to decode clients: %w", err)
	}
	for _, client := range clients {
		if client.ClientID == consts.ClientID {
			return true, nil
		}
	}
	return false, nil
}

func CreateOdigosUIClient(token, keycloakHost, realm string) error {
	exists, err := OdigosUIClientExists(token, keycloakHost, realm)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	clientsURL := fmt.Sprintf("%s/admin/realms/%s/clients", keycloakHost, realm)

	clientConfig := ClientConfig{
		ClientID:                  consts.ClientID,
		Name:                      "Odigos Central",
		Enabled:                   true,
		PublicClient:              true,
		DirectAccessGrantsEnabled: true,
		StandardFlowEnabled:       true,
		ImplicitFlowEnabled:       false,
		ServiceAccountsEnabled:    false,
		WebOrigins:                []string{WildcardOrigin},
		RedirectUris:              []string{WildcardOrigin},
	}

	body, err := json.Marshal(clientConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal client config: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, clientsURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create POST request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		responseBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to create client: status %d, body: %s", resp.StatusCode, string(responseBody))
	}

	return nil
}

func EnsureRequiredRoles(adminToken, keycloakHost, realm string) error {
	requiredRoles := []string{RoleAdmin, RoleOwner, RoleMaintainer, RoleViewer, RoleNoAccess}

	for _, roleName := range requiredRoles {
		err := createRoleIfNotExists(adminToken, keycloakHost, realm, roleName)
		if err != nil {
			return fmt.Errorf("failed to ensure role %s: %w", roleName, err)
		}
	}

	return nil
}

// firstBrokerLoginFlowAlias is Keycloak's built-in first-broker-login flow.
const firstBrokerLoginFlowAlias = "first broker login"

// reviewProfileProviderID is the authenticator that shows "Update account information"
// during first IdP login. Disabling it lets Create User If Unique finish without a UI step.
const reviewProfileProviderID = "idp-review-profile"

// authenticationExecutionInfo is a subset of Keycloak's AuthenticationExecutionInfoRepresentation.
// Keycloak 24 rejects unknown fields such as "priority" on PUT, so we only send known fields.
type authenticationExecutionInfo struct {
	ID                   string `json:"id"`
	ProviderID           string `json:"providerId,omitempty"`
	DisplayName          string `json:"displayName,omitempty"`
	Requirement          string `json:"requirement"`
	AuthenticationConfig string `json:"authenticationConfig,omitempty"`
	Configurable         bool   `json:"configurable"`
	Level                int    `json:"level"`
	Index                int    `json:"index"`
	Alias                string `json:"alias,omitempty"`
	Description          string `json:"description,omitempty"`
	FlowID               string `json:"flowId,omitempty"`
	AuthenticationFlow   bool   `json:"authenticationFlow,omitempty"`
}

// EnsureFirstBrokerLoginSkipsReviewProfile disables the Review Profile step so first
// IdP logins create the local user and return an auth code without an HTML interstitial.
func EnsureFirstBrokerLoginSkipsReviewProfile(adminToken, keycloakHost, realm string) error {
	execURL := fmt.Sprintf("%s/admin/realms/%s/authentication/flows/%s/executions",
		keycloakHost, realm, url.PathEscape(firstBrokerLoginFlowAlias))

	req, err := http.NewRequest(http.MethodGet, execURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create first-broker-login executions request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to list first-broker-login executions: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to list first-broker-login executions: status %d, body: %s", resp.StatusCode, string(body))
	}

	var executions []authenticationExecutionInfo
	if err := json.NewDecoder(resp.Body).Decode(&executions); err != nil {
		return fmt.Errorf("failed to decode first-broker-login executions: %w", err)
	}

	var review *authenticationExecutionInfo
	for i := range executions {
		if executions[i].ProviderID == reviewProfileProviderID {
			review = &executions[i]
			break
		}
	}
	if review == nil {
		// Flow shape can vary by Keycloak version; nothing to change.
		return nil
	}
	if review.Requirement == "DISABLED" {
		return nil
	}

	review.Requirement = "DISABLED"
	payload, err := json.Marshal(review)
	if err != nil {
		return fmt.Errorf("failed to marshal review-profile execution: %w", err)
	}

	putReq, err := http.NewRequest(http.MethodPut, execURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create review-profile update request: %w", err)
	}
	putReq.Header.Set("Authorization", "Bearer "+adminToken)
	putReq.Header.Set("Content-Type", "application/json")

	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		return fmt.Errorf("failed to disable review-profile execution: %w", err)
	}
	defer putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent && putResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(putResp.Body)
		return fmt.Errorf("failed to disable review-profile execution: status %d, body: %s", putResp.StatusCode, string(body))
	}
	return nil
}

type requiredActionProvider struct {
	Alias         string `json:"alias"`
	Name          string `json:"name,omitempty"`
	ProviderID    string `json:"providerId,omitempty"`
	Enabled       bool   `json:"enabled"`
	DefaultAction bool   `json:"defaultAction"`
	Priority      int    `json:"priority"`
}

// EnsureVerifyProfileDisabled turns off the VERIFY_PROFILE required action.
// With Keycloak's declarative user profile enabled by default, first IdP logins
// otherwise stall on an "Update account information" required-action page even
// when first-broker-login Review Profile is disabled.
func EnsureVerifyProfileDisabled(adminToken, keycloakHost, realm string) error {
	listURL := fmt.Sprintf("%s/admin/realms/%s/authentication/required-actions", keycloakHost, realm)
	req, err := http.NewRequest(http.MethodGet, listURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create required-actions request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to list required-actions: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to list required-actions: status %d, body: %s", resp.StatusCode, string(body))
	}

	var actions []requiredActionProvider
	if err := json.NewDecoder(resp.Body).Decode(&actions); err != nil {
		return fmt.Errorf("failed to decode required-actions: %w", err)
	}

	var verify *requiredActionProvider
	for i := range actions {
		if actions[i].Alias == "VERIFY_PROFILE" || actions[i].ProviderID == "VERIFY_PROFILE" {
			verify = &actions[i]
			break
		}
	}
	if verify == nil {
		return nil
	}
	if !verify.Enabled && !verify.DefaultAction {
		return nil
	}

	verify.Enabled = false
	verify.DefaultAction = false
	payload, err := json.Marshal(verify)
	if err != nil {
		return fmt.Errorf("failed to marshal VERIFY_PROFILE action: %w", err)
	}

	putURL := fmt.Sprintf("%s/admin/realms/%s/authentication/required-actions/%s",
		keycloakHost, realm, url.PathEscape(verify.Alias))
	putReq, err := http.NewRequest(http.MethodPut, putURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create VERIFY_PROFILE update request: %w", err)
	}
	putReq.Header.Set("Authorization", "Bearer "+adminToken)
	putReq.Header.Set("Content-Type", "application/json")

	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		return fmt.Errorf("failed to disable VERIFY_PROFILE: %w", err)
	}
	defer putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent && putResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(putResp.Body)
		return fmt.Errorf("failed to disable VERIFY_PROFILE: status %d, body: %s", putResp.StatusCode, string(body))
	}
	return nil
}

func EnsureAccessTokenLifespan(adminToken, keycloakHost, realm string, seconds int) error {
	realmURL := fmt.Sprintf("%s/admin/realms/%s", keycloakHost, realm)

	getReq, err := http.NewRequest(http.MethodGet, realmURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create realm GET request: %w", err)
	}
	getReq.Header.Set("Authorization", "Bearer "+adminToken)
	getReq.Header.Set("Content-Type", "application/json")

	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		return fmt.Errorf("failed to GET realm: %w", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(getResp.Body)
		return fmt.Errorf("failed to GET realm, status %d, body: %s", getResp.StatusCode, string(body))
	}

	var realmData map[string]any
	if err := json.NewDecoder(getResp.Body).Decode(&realmData); err != nil {
		return fmt.Errorf("failed to decode realm data: %w", err)
	}

	if current, ok := realmData["accessTokenLifespan"].(float64); !ok || int(current) != seconds {
		realmData["accessTokenLifespan"] = seconds

		payload, err := json.Marshal(realmData)
		if err != nil {
			return fmt.Errorf("failed to marshal realm data: %w", err)
		}

		putReq, err := http.NewRequest(http.MethodPut, realmURL, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("failed to create realm PUT request: %w", err)
		}
		putReq.Header.Set("Authorization", "Bearer "+adminToken)
		putReq.Header.Set("Content-Type", "application/json")

		putResp, err := http.DefaultClient.Do(putReq)
		if err != nil {
			return fmt.Errorf("failed to PUT realm: %w", err)
		}
		defer putResp.Body.Close()
		if putResp.StatusCode != http.StatusNoContent {
			body, _ := io.ReadAll(putResp.Body)
			return fmt.Errorf("failed to update realm token lifespan, status %d, body: %s", putResp.StatusCode, string(body))
		}
	}

	return nil
}

func createRoleIfNotExists(adminToken, keycloakHost, realm, roleName string) error {
	checkURL := fmt.Sprintf("%s/admin/realms/%s/roles/%s", keycloakHost, realm, roleName)
	req, err := http.NewRequest(http.MethodGet, checkURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create check request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to check role: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return nil
	}

	if resp.StatusCode == http.StatusNotFound {
		createURL := fmt.Sprintf("%s/admin/realms/%s/roles", keycloakHost, realm)
		roleData := RoleData{
			Name: roleName,
		}

		body, err := json.Marshal(roleData)
		if err != nil {
			return fmt.Errorf("failed to marshal role data: %w", err)
		}

		req, err = http.NewRequest(http.MethodPost, createURL, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("failed to create role request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+adminToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("failed to create role: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusCreated {
			responseBody, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("failed to create role %s: status %d, body: %s", roleName, resp.StatusCode, string(responseBody))
		}

		return nil
	}

	responseBody, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("unexpected status when checking role %s: %d, body: %s", roleName, resp.StatusCode, string(responseBody))
}
func (kc *KeycloakAdminService) HealthCheck(ctx context.Context) model.ServicesStatus {
	// Use OIDC discovery endpoint - always available on port 8080 and indicates functional health
	healthEndpoint := fmt.Sprintf("%s/realms/%s/.well-known/openid-configuration", kc.host, kc.realm)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthEndpoint, nil)
	if err != nil {
		msg := fmt.Sprintf("Failed to create health check request: %v", err)
		return model.ServicesStatus{
			Status:  model.HealthStatusDown,
			Message: &msg,
		}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		msg := fmt.Sprintf("Keycloak unreachable: %v", err)
		return model.ServicesStatus{
			Status:  model.HealthStatusDown,
			Message: &msg,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return model.ServicesStatus{
			Status:  model.HealthStatusHealthy,
			Message: nil,
		}
	}

	return model.ServicesStatus{
		Status:  model.HealthStatusDown,
		Message: nil,
	}
}
