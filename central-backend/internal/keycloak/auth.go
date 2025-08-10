package keycloak

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/odigos-io/central-backend/internal/consts"
	"github.com/odigos-io/central-backend/internal/logger"
)

type AuthService struct {
	host        string
	realm       string
	client      string
	userService *UserService
}

type UserAuthResult struct {
	AccessToken string
	UserData    map[string]interface{}
}

func NewAuthService() *AuthService {
	return &AuthService{
		host:        consts.GetKeycloakHost(),
		realm:       consts.KeycloakRealm,
		client:      consts.ClientID,
		userService: NewUserService(),
	}
}

func UserSignIn(keycloakHost, realm, clientID, username, password string) (string, error) {
	tokenURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", keycloakHost, realm)

	formData := make(url.Values)
	formData.Set("grant_type", "password")
	formData.Set("client_id", clientID)
	formData.Set("username", username)
	formData.Set("password", password)

	req, err := http.NewRequest("POST", tokenURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		responseBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("authentication failed with status %d: %s", resp.StatusCode, string(responseBody))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	accessToken, ok := result["access_token"].(string)
	if !ok {
		return "", fmt.Errorf("access token not found in response")
	}

	return accessToken, nil
}

func LogoutUser(keycloakHost, realm, clientID, token string) error {
	// For logout, we'll simply return success since the frontend will clear the token
	// This is a simpler approach that doesn't require additional Keycloak permissions
	// The actual session invalidation happens on the frontend by clearing the token

	// We could validate the token format here if needed, but for now we'll just return success
	// The frontend will handle the actual logout by clearing the authentication state

	return nil
}

func (a *AuthService) AuthenticateUser(email, password, adminUsername, adminPassword string) (*UserAuthResult, error) {

	accessToken, err := UserSignIn(a.host, a.realm, a.client, email, password)
	if err != nil {
		return a.handleAuthError(err, email)
	}

	adminToken, err := GetKeycloakAccessToken(a.host, adminUsername, adminPassword)
	if err != nil {
		logger.Log.Error("Failed to get admin token", "error", err)
		return nil, fmt.Errorf("failed to get admin token: %w", err)
	}

	if err := a.checkPasswordUpdateRequired(adminToken, email); err != nil {
		return nil, err
	}

	userData, err := a.getUserDetails(adminToken, email)
	if err != nil {
		return nil, err
	}

	return &UserAuthResult{
		AccessToken: accessToken,
		UserData:    userData,
	}, nil
}

func (a *AuthService) checkPasswordUpdateRequired(adminToken, email string) error {
	requiredActions, err := a.userService.GetUserRequiredActions(adminToken, email)
	if err != nil {
		logger.Log.Error("Failed to get user required actions", "email", email, "error", err)
		return nil
	}

	logger.Log.Info("User required actions", "email", email, "actions", requiredActions)
	for _, action := range requiredActions {
		if action == consts.UpdatePasswordAction {
			logger.Log.Info("Password update required for user", "email", email)
			return fmt.Errorf("password update required")
		}
	}

	return nil
}

func (a *AuthService) handleAuthError(err error, email string) (*UserAuthResult, error) {
	if strings.Contains(err.Error(), "Account is not fully set up") {
		return nil, fmt.Errorf("password update required: please use the set-password endpoint to set your initial password")
	}
	logger.Log.Error("Authentication failed", "email", email, "error", err)
	return nil, fmt.Errorf("authentication failed for user %s: %v", email, err)
}

func (a *AuthService) getUserDetails(adminToken, email string) (map[string]any, error) {
	users, err := a.userService.FindUsersByEmail(adminToken, email)
	if err != nil || len(users) == 0 {
		logger.Log.Error("Failed to find user after login", "email", email, "error", err)
		return nil, fmt.Errorf("failed to find user by email after successful login: %w", err)
	}
	return users[0], nil
}
