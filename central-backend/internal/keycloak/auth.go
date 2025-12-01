package keycloak

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/odigos-io/central-backend/internal/consts"
	"github.com/odigos-io/central-backend/internal/logger"
	"github.com/odigos-io/central-backend/internal/types"
)

type KeycloakAuthService struct {
	host   string
	realm  string
	client string
	kcUser *KeycloakUserService
}

type UserAuthResult struct {
	AccessToken string            `json:"access_token"`
	UserData    types.UserSummary `json:"user_data"`
}

func NewKeycloakAuthService() *KeycloakAuthService {
	return &KeycloakAuthService{
		host:   consts.GetKeycloakHost(),
		realm:  consts.KeycloakRealm,
		client: consts.ClientID,
		kcUser: NewKeycloakUserService(),
	}
}

func (kc *KeycloakAuthService) VerifyToken(token string) (bool, error) {
	if token == "" {
		return false, fmt.Errorf("token is empty")
	}

	// Parse JWT token to extract claims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false, fmt.Errorf("invalid JWT token format")
	}

	// Decode the payload (second part)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false, fmt.Errorf("failed to decode JWT payload: %w", err)
	}

	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return false, fmt.Errorf("failed to parse JWT payload: %w", err)
	}

	// Verify token is not expired
	exp, ok := claims["exp"].(float64)
	if !ok {
		return false, fmt.Errorf("expiration claim not found in token")
	}

	if time.Now().Unix() >= int64(exp) {
		return false, fmt.Errorf("token is expired")
	}

	return true, nil
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

	var result UserAuthResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}
	if result.AccessToken == "" {
		return "", fmt.Errorf("access token not found in response")
	}

	return result.AccessToken, nil
}

func (kc *KeycloakAuthService) LogoutUser(token string) error {
	// For logout, we'll simply return success since the frontend will clear the token
	// This is kc simpler approach that doesn't require additional Keycloak permissions
	// The actual session invalidation happens on the frontend by clearing the token

	// We could validate the token format here if needed, but for now we'll just return success
	// The frontend will handle the actual logout by clearing the authentication state

	return nil
}

func (kc *KeycloakAuthService) AuthenticateUser(token string, email string, password string) (*UserAuthResult, error) {
	accessToken, err := UserSignIn(kc.host, kc.realm, kc.client, email, password)
	if err != nil {
		return kc.handleAuthError(err, email)
	}

	if err := kc.checkPasswordUpdateRequired(token, email); err != nil {
		return nil, err
	}

	userData, err := kc.getUserDetails(token, email)
	if err != nil {
		return nil, err
	}

	return &UserAuthResult{
		AccessToken: accessToken,
		UserData:    userData,
	}, nil
}

func (kc *KeycloakAuthService) checkPasswordUpdateRequired(adminToken, email string) error {
	requiredActions, err := kc.kcUser.GetUserRequiredActions(adminToken, email)
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

func (kc *KeycloakAuthService) handleAuthError(err error, email string) (*UserAuthResult, error) {
	if strings.Contains(err.Error(), "Account is not fully set up") {
		return nil, fmt.Errorf("password update required: please use the set-password endpoint to set your initial password")
	}
	logger.Log.Error("Authentication failed", "email", email, "error", err)
	return nil, fmt.Errorf("authentication failed for user %s: %v", email, err)
}

func (kc *KeycloakAuthService) getUserDetails(adminToken, email string) (types.UserSummary, error) {
	users, err := kc.kcUser.FindUsersByEmail(adminToken, email)
	if err != nil || len(users) == 0 {
		logger.Log.Error("Failed to find user after login", "email", email, "error", err)
		return types.UserSummary{}, fmt.Errorf("failed to find user by email after successful login: %w", err)
	}
	u := users[0]
	return types.UserSummary{
		ID:    getString(u, "id"),
		Email: getString(u, "email"),
	}, nil
}

func getString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
