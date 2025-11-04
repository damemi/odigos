package keycloak

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/odigos-io/central-backend/internal/consts"
)

type KeycloakAdminService struct {
	host  string
	realm string
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
