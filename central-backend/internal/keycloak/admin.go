package keycloak

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

func GetKeycloakAccessToken(keycloakHost, username, password string) (string, error) {
	url := fmt.Sprintf("%s/realms/master/protocol/openid-connect/token", keycloakHost)
	payload := []byte("grant_type=password&client_id=admin-cli&username=" + username + "&password=" + password)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("failed to get admin token: %s", string(body))
	}

	var token tokenResponse
	err = json.Unmarshal(body, &token)
	if err != nil {
		return "", err
	}

	return token.AccessToken, nil
}
