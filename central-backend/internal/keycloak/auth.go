package keycloak

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/odigos-io/odigos/central-backend/internal/consts"
	"github.com/odigos-io/odigos/central-backend/internal/logger"
	"github.com/odigos-io/odigos/central-backend/internal/types"
	"github.com/odigos-io/odigos/central-backend/internal/utils"
)

type KeycloakAuthService struct {
	host                string // internal cluster URL for server-to-server calls
	backendHost         string // central-backend URL for browser-facing redirect URLs (proxied to Keycloak)
	realm               string
	client              string
	uiSignInRedirectUrl string
	kcUser              *KeycloakUserService
}

type UserAuthResult struct {
	AccessToken  string            `json:"access_token"`
	IDToken      string            `json:"id_token,omitempty"`
	RefreshToken string            `json:"refresh_token,omitempty"`
	UserData     types.UserSummary `json:"user_data"`
}

type TokenResponse struct {
	AccessToken      string `json:"access_token"`
	IDToken          string `json:"id_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshExpiresIn int    `json:"refresh_expires_in"`
	TokenType        string `json:"token_type"`
	SessionState     string `json:"session_state"`
	Scope            string `json:"scope"`
}

func NewKeycloakAuthService() *KeycloakAuthService {
	return &KeycloakAuthService{
		host:                consts.GetKeycloakHost(),
		backendHost:         consts.GetCentralBackendHost(),
		realm:               consts.KeycloakRealm,
		client:              consts.ClientID,
		uiSignInRedirectUrl: fmt.Sprintf("%s/signin", consts.GetCentralUIHost()),
		kcUser:              NewKeycloakUserService(),
	}
}

func (kc *KeycloakAuthService) GetRedirectUrlForSignIn(provider string) string {
	authURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/auth", kc.backendHost, kc.realm)
	queryParams := make(url.Values)
	queryParams.Set("client_id", kc.client)
	queryParams.Set("kc_idp_hint", provider)
	queryParams.Set("scope", "openid profile email")
	queryParams.Set("response_type", "code")
	queryParams.Set("redirect_uri", kc.uiSignInRedirectUrl)
	// Force the IdP account picker so a previously denied SSO session cannot silently
	// re-authenticate the same user on the next "Continue with …" click.
	queryParams.Set("prompt", "select_account")

	return fmt.Sprintf("%s?%s", authURL, queryParams.Encode())
}

// GetRedirectUrlForSignOut returns the URL that the browser should navigate to for proper logout.
// This ensures Keycloak cookies are cleared and logout is propagated to the IDP (e.g., Azure).
// The browser navigation is required because:
// 1. It clears Keycloak session cookies (AUTH_SESSION_ID, KC_AUTH_SESSION_HASH, KEYCLOAK_IDENTITY, KEYCLOAK_SESSION)
// 2. It can propagate logout to the external IDP (when idToken is provided)
// 3. It prevents auto-login when trying to sign in with a different account
func (kc *KeycloakAuthService) GetRedirectUrlForSignOut(idToken string) string {
	logoutURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/logout", kc.backendHost, kc.realm)

	queryParams := make(url.Values)
	queryParams.Set("client_id", kc.client)
	queryParams.Set("post_logout_redirect_uri", kc.uiSignInRedirectUrl)

	// id_token_hint is required for proper logout with external IDPs like Azure
	// It ensures the logout is propagated to the IDP and all sessions are cleared
	if idToken != "" {
		queryParams.Set("id_token_hint", idToken)
	}

	return fmt.Sprintf("%s?%s", logoutURL, queryParams.Encode())
}

// VerifyToken reports whether token is a valid, unexpired access token signed
// by the realm for this client.
func (kc *KeycloakAuthService) VerifyToken(token string) (bool, error) {
	if token == "" {
		return false, fmt.Errorf("token is empty")
	}
	if _, err := utils.VerifyAccessToken(token); err != nil {
		return false, err
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

// accountNotFullySetUp is Keycloak's password-grant error description for a user
// with a pending required action. Keycloak checks the password before required
// actions, so this error also means the password was correct.
const accountNotFullySetUp = "Account is not fully set up"

// VerifyPassword reports whether password is the current Keycloak password of
// username, using a password grant. A session the grant opens is ended at once.
// It returns false for wrong, disabled or unknown credentials and an error only
// when Keycloak could not answer.
func (kc *KeycloakAuthService) VerifyPassword(username, password string) (bool, error) {
	tokenURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", kc.host, kc.realm)

	formData := make(url.Values)
	formData.Set("grant_type", "password")
	formData.Set("client_id", kc.client)
	formData.Set("username", username)
	formData.Set("password", password)

	req, err := http.NewRequest("POST", tokenURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return false, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var tokens TokenResponse
		if err := json.NewDecoder(resp.Body).Decode(&tokens); err == nil && tokens.RefreshToken != "" {
			if err := kc.endSession(tokens.RefreshToken); err != nil {
				logger.Log.Warn("Failed to end session opened by password check", "error", err)
			}
		}
		return true, nil
	case http.StatusBadRequest, http.StatusUnauthorized:
		var oauthErr struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&oauthErr); err != nil {
			return false, fmt.Errorf("failed to decode password check response with status %d: %w", resp.StatusCode, err)
		}
		if oauthErr.Error != "invalid_grant" {
			return false, fmt.Errorf("password check failed with status %d: %s", resp.StatusCode, oauthErr.Error)
		}
		return oauthErr.Description == accountNotFullySetUp, nil
	default:
		return false, fmt.Errorf("password check failed with status %d", resp.StatusCode)
	}
}

// SignInWithPassword returns an access token for username from a password grant.
func (kc *KeycloakAuthService) SignInWithPassword(username, password string) (string, error) {
	return UserSignIn(kc.host, kc.realm, kc.client, username, password)
}

// endSession ends the Keycloak session that refreshToken belongs to.
func (kc *KeycloakAuthService) endSession(refreshToken string) error {
	logoutURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/logout", kc.host, kc.realm)

	formData := make(url.Values)
	formData.Set("client_id", kc.client)
	formData.Set("refresh_token", refreshToken)

	req, err := http.NewRequest("POST", logoutURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != StatusNoContent && resp.StatusCode != StatusOK {
		return fmt.Errorf("logout failed with status %d", resp.StatusCode)
	}
	return nil
}

func (kc *KeycloakAuthService) AuthenticateUser(adminToken string, email string, password string, code string) (*UserAuthResult, error) {
	var idToken string
	var accessToken string
	var userData types.UserSummary
	var err error

	// OAuth code flow (when code is provided)
	if code != "" {
		// Exchange the authorization code for tokens from Keycloak
		tokenResponse, err := kc.exchangeCodeForTokens(code)
		if err != nil {
			return nil, fmt.Errorf("failed to exchange code for tokens: %w", err)
		}

		idToken = tokenResponse.IDToken
		accessToken = tokenResponse.AccessToken
		refreshToken := tokenResponse.RefreshToken

		// Get user information from Keycloak using the access token
		// Keycloak already has the user synced from Azure at this point
		userInfo, err := kc.kcUser.GetUserInfo(accessToken)
		if err != nil {
			return nil, fmt.Errorf("failed to get user information from Keycloak: %w", err)
		}

		userID := getString(userInfo, "sub")
		userEmail := getString(userInfo, "email")

		// Azure AD may not include the "email" claim; fall back to "preferred_username"
		// which typically contains the user's UPN (e.g. user@domain.com).
		if userEmail == "" {
			userEmail = getString(userInfo, "preferred_username")
		}

		if userID == "" {
			return nil, fmt.Errorf("user ID (sub) not found in userinfo response")
		}

		userData = types.UserSummary{
			ID:    userID,
			Email: userEmail,
		}

		return &UserAuthResult{
			IDToken:      idToken,
			AccessToken:  accessToken,
			RefreshToken: refreshToken,
			UserData:     userData,
		}, nil
	} else {
		// Password-based flow (when email/password are provided)
		accessToken, err = UserSignIn(kc.host, kc.realm, kc.client, email, password)
		if err != nil {
			return kc.handleAuthError(err, email)
		}

		if err := kc.checkPasswordUpdateRequired(adminToken, email); err != nil {
			return nil, err
		}

		userData, err = kc.getUserDetails(adminToken, email)
		if err != nil {
			return nil, err
		}
	}

	return &UserAuthResult{
		IDToken:     idToken,
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

// exchangeCodeForTokens exchanges the authorization code for tokens from Keycloak.
// Note: Since the client is configured as a public client, no client_secret is required.
func (kc *KeycloakAuthService) exchangeCodeForTokens(code string) (*TokenResponse, error) {
	tokenURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", kc.host, kc.realm)

	formData := make(url.Values)
	formData.Set("grant_type", "authorization_code")
	formData.Set("client_id", kc.client)
	formData.Set("code", code)
	formData.Set("redirect_uri", kc.uiSignInRedirectUrl)

	req, err := http.NewRequest("POST", tokenURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		responseBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("token exchange failed with status %d: %s", resp.StatusCode, string(responseBody))
	}

	var tokenResponse TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResponse); err != nil {
		return nil, fmt.Errorf("failed to decode token response: %w", err)
	}

	if tokenResponse.AccessToken == "" {
		return nil, fmt.Errorf("access token not found in response")
	}

	return &tokenResponse, nil
}

// RefreshTokens exchanges a refresh token for a new access/id token pair so that
// realm role changes made after the original login are reflected in the JWT.
func (kc *KeycloakAuthService) RefreshTokens(refreshToken string) (*TokenResponse, error) {
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token is empty")
	}

	tokenURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", kc.host, kc.realm)

	formData := make(url.Values)
	formData.Set("grant_type", "refresh_token")
	formData.Set("client_id", kc.client)
	formData.Set("refresh_token", refreshToken)

	req, err := http.NewRequest("POST", tokenURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh tokens: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		responseBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("token refresh failed with status %d: %s", resp.StatusCode, string(responseBody))
	}

	var tokenResponse TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResponse); err != nil {
		return nil, fmt.Errorf("failed to decode refresh response: %w", err)
	}
	if tokenResponse.AccessToken == "" {
		return nil, fmt.Errorf("access token not found in refresh response")
	}

	return &tokenResponse, nil
}

func getString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
