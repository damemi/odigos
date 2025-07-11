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
