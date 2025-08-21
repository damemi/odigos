package keycloak

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/odigos-io/central-backend/internal/consts"
)

const (
	RoleAdmin      = "admin"
	RoleOwner      = "owner"
	RoleMaintainer = "maintainer"
	RoleViewer     = "viewer"
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

func CreateOdigosUIClient(token, keycloakHost, realm string) error {
	clientsURL := fmt.Sprintf("%s/admin/realms/%s/clients", keycloakHost, realm)
	req, err := http.NewRequest(http.MethodGet, clientsURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to get clients: %w", err)
	}
	defer resp.Body.Close()

	var clients []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&clients); err != nil {
		return fmt.Errorf("failed to decode clients: %w", err)
	}

	for _, client := range clients {
		if clientID, ok := client["clientId"].(string); ok && clientID == consts.ClientID {
			return nil
		}
	}

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

	req, err = http.NewRequest(http.MethodPost, clientsURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create POST request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err = http.DefaultClient.Do(req)
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
	requiredRoles := []string{RoleAdmin, RoleOwner, RoleMaintainer, RoleViewer}

	for _, roleName := range requiredRoles {
		err := createRoleIfNotExists(adminToken, keycloakHost, realm, roleName)
		if err != nil {
			return fmt.Errorf("failed to ensure role %s: %w", roleName, err)
		}
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
