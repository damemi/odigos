package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/odigos-io/central-backend/graph/model"
	"github.com/odigos-io/central-backend/internal/consts"
	"github.com/odigos-io/central-backend/internal/keycloak"
	"github.com/odigos-io/central-backend/internal/logger"
	"github.com/odigos-io/central-backend/internal/types"
	"github.com/odigos-io/central-backend/internal/utils"
	"github.com/odigos-io/odigos/api/k8sconsts"
)

type AuthService struct {
	adminService       *AdminService
	userService        *UserService
	roleMappingService *RoleMappingService
	kcUser             *keycloak.KeycloakUserService
	kcAuth             *keycloak.KeycloakAuthService
	kcTeam             *keycloak.KeycloakTeamService

	initialPasswordAttempts *attemptLimiter
}

func NewAuthService(adminService *AdminService, userService *UserService, roleMappingService *RoleMappingService) *AuthService {
	return &AuthService{
		adminService:       adminService,
		userService:        userService,
		roleMappingService: roleMappingService,
		kcAuth:             keycloak.NewKeycloakAuthService(),
		kcUser:             keycloak.NewKeycloakUserService(),
		kcTeam:             keycloak.NewKeycloakTeamService(),

		initialPasswordAttempts: newAttemptLimiter(initialPasswordWindow),
	}
}

func (svc *AuthService) GetRedirectUrlForSignInByProviderID(providerID model.AuthProviderID) (*string, error) {
	providerIDStr := strings.ToLower(string(providerID))

	idp, err := svc.adminService.GetIdentityProvider(providerIDStr)
	if err != nil {
		return nil, err
	}
	if idp == nil {
		return nil, err
	}

	url := svc.kcAuth.GetRedirectUrlForSignIn(providerIDStr)
	return &url, nil
}

// ConfiguredIdentityProviders returns AuthProviderIDs for Keycloak IdPs whose
// aliases match known providers (google/azure/okta). Unknown aliases are ignored.
// Only provider IDs are returned — never IdP secrets or full Keycloak config.
func (svc *AuthService) ConfiguredIdentityProviders() ([]model.AuthProviderID, error) {
	idps, err := svc.adminService.ListIdentityProviders()
	if err != nil {
		return nil, err
	}
	return authProviderIDsFromIdentityProviders(idps), nil
}

func authProviderIDsFromIdentityProviders(idps []keycloak.IdentityProviderRepresentation) []model.AuthProviderID {
	result := make([]model.AuthProviderID, 0, len(idps))
	seen := make(map[model.AuthProviderID]struct{}, len(idps))
	for _, idp := range idps {
		providerID := model.AuthProviderID(strings.ToUpper(idp.Alias))
		if !providerID.IsValid() {
			continue
		}
		if _, ok := seen[providerID]; ok {
			continue
		}
		seen[providerID] = struct{}{}
		result = append(result, providerID)
	}
	return result
}

func (svc *AuthService) GetRedirectUrlForSignOut(ctx context.Context, idToken string) string {
	return svc.kcAuth.GetRedirectUrlForSignOut(idToken)
}

// isFirstTimeSetup fails closed: if Keycloak cannot be queried, the caller must not
// treat the sign-in as first-run setup (which would create an admin user).
func (svc *AuthService) isFirstTimeSetup(ctx context.Context) (bool, error) {
	isInitialized, err := svc.adminService.IsSystemInitialized()
	if err != nil {
		return false, fmt.Errorf("failed to determine initialization state: %w", err)
	}
	return !isInitialized, nil
}

func (svc *AuthService) VerifyToken(ctx context.Context, token string) (*model.GeneralResult, error) {
	_, err := svc.kcAuth.VerifyToken(token)
	if err != nil {
		errorMsg := err.Error()
		return &model.GeneralResult{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}

	roles, err := utils.ExtractRolesFromToken(token)
	if err != nil || !hasOdigosRoleInList(roles) {
		errorMsg := "token does not include an Odigos role"
		return &model.GeneralResult{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}

	return &model.GeneralResult{
		Success: true,
	}, nil
}

func hasOdigosRoleInList(roles []string) bool {
	for _, role := range roles {
		if strings.EqualFold(role, keycloak.RoleOwner) ||
			strings.EqualFold(role, keycloak.RoleAdmin) ||
			strings.EqualFold(role, keycloak.RoleMaintainer) ||
			strings.EqualFold(role, keycloak.RoleViewer) {
			return true
		}
	}
	return false
}

// passwordUpdateRequiredResult is returned when a user authenticates with a
// temporary/one-time password and must set a permanent password before a real
// session can be issued. AccessToken is intentionally nil — never an admin token.
func passwordUpdateRequiredResult(email string) *model.SignInResult {
	return &model.SignInResult{
		AccessToken: nil,
		Status:      model.SignInStatusPasswordUpdateRequired,
		User: &model.User{
			Email:               email,
			NeedsPasswordChange: true,
			Teams:               []*model.Team{},
			ComputePlatforms:    []*model.ComputePlatform{},
		},
	}
}

func extractJWTPayload(token string) (map[string]interface{}, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT token format")
	}

	// Decode the payload (second part of the JWT)
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("failed to decode JWT payload: %w", err)
	}

	// Parse the payload as JSON
	var payload map[string]interface{}
	err = json.Unmarshal(payloadBytes, &payload)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal JWT payload: %w", err)
	}

	return payload, nil
}

func (r *AuthService) APITokens(ctx context.Context) ([]*model.APIToken, error) {

	token := os.Getenv(k8sconsts.OdigosOnpremTokenEnvName)
	tokenPayload, err := extractJWTPayload(token)
	if err != nil {
		return nil, err
	}

	aud, _ := tokenPayload["aud"].(string)
	// JWT iat/exp are numeric dates (seconds since epoch) and are commonly decoded as float64.
	iatSec, _ := tokenPayload["iat"].(float64)
	expSec, _ := tokenPayload["exp"].(float64)
	issuedAtMs := int(int64(iatSec) * 1000)
	expiresAtMs := int(int64(expSec) * 1000)

	return []*model.APIToken{
		{
			Token:     token,
			Name:      aud,
			IssuedAt:  issuedAtMs,
			ExpiresAt: expiresAtMs,
		},
	}, nil
}

func (svc *AuthService) SignIn(ctx context.Context, email string, password string, code string) (*model.SignInResult, error) {
	// Resolve initialization first. A cached admin token that Keycloak has rejected
	// (session idle, or Keycloak restarted) is dropped and replaced there, so the
	// token captured below is one the admin API will accept.
	isFirstTimeSetup, err := svc.isFirstTimeSetup(ctx)
	if err != nil {
		return nil, err
	}

	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return nil, fmt.Errorf("failed to get admin token: %w", err)
	}

	if code != "" {
		authResult, err := svc.kcAuth.AuthenticateUser(adminToken, "", "", code)
		if err != nil {
			return nil, err
		}

		roleAssigned := false
		// NeedToSetAdminUser may replace a rejected admin token.
		needAdmin := svc.adminService.NeedToSetAdminUser()
		adminToken, err = svc.adminService.GetAdminToken()
		if err != nil {
			return nil, fmt.Errorf("failed to get admin token: %w", err)
		}
		if needAdmin {
			if err := svc.kcUser.AddRealmRoleToUser(adminToken, authResult.UserData.Email, keycloak.RoleAdmin); err != nil {
				return nil, fmt.Errorf("failed to assign admin role to first SSO user: %w", err)
			}
			roleAssigned = true
		}

		roleStr, err := svc.kcUser.GetUserRole(adminToken, authResult.UserData.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to get user role after SSO login: %w", err)
		}
		if roleStr == keycloak.RoleNoAccess {
			logger.Log.Info("SSO login denied: user has no-access role", "userID", authResult.UserData.ID, "email", authResult.UserData.Email)
			if err := svc.kcUser.LogoutUserSessions(adminToken, authResult.UserData.ID); err != nil {
				logger.Log.Warn("Failed to logout denied user sessions", "userID", authResult.UserData.ID, "error", err)
			}
			idToken := authResult.IDToken
			return &model.SignInResult{
				Status:  model.SignInStatusAccessDenied,
				IDToken: &idToken,
			}, nil
		}
		if roleStr == "" {
			hasMappings, err := svc.roleMappingService.HasAnyGroupRoleMappings()
			if err != nil {
				return nil, fmt.Errorf("failed to check group role mappings: %w", err)
			}
			if hasMappings {
				logger.Log.Info("SSO login denied: user has no mapped role", "userID", authResult.UserData.ID, "email", authResult.UserData.Email)
				if err := svc.kcUser.LogoutUserSessions(adminToken, authResult.UserData.ID); err != nil {
					logger.Log.Warn("Failed to logout denied user sessions", "userID", authResult.UserData.ID, "error", err)
				}
				if err := svc.kcUser.AddRealmRoleToUser(adminToken, authResult.UserData.Email, keycloak.RoleNoAccess); err != nil {
					logger.Log.Warn("Failed to assign no-access role to denied SSO user", "userID", authResult.UserData.ID, "error", err)
				}
				// Return idToken for deferred IdP logout on the next SSO attempt (not used as a session).
				idToken := authResult.IDToken
				return &model.SignInResult{
					Status:  model.SignInStatusAccessDenied,
					IDToken: &idToken,
				}, nil
			}
			if err := svc.kcUser.AddRealmRoleToUser(adminToken, authResult.UserData.Email, keycloak.RoleViewer); err != nil {
				return nil, fmt.Errorf("failed to assign default viewer role: %w", err)
			}
			roleAssigned = true
		}

		idToken := authResult.IDToken
		accessToken := authResult.AccessToken
		if roleAssigned && authResult.RefreshToken != "" {
			refreshed, err := svc.kcAuth.RefreshTokens(authResult.RefreshToken)
			if err != nil {
				return nil, fmt.Errorf("failed to refresh tokens after role assignment: %w", err)
			}
			accessToken = refreshed.AccessToken
			if refreshed.IDToken != "" {
				idToken = refreshed.IDToken
			}
		}

		user, err := svc.buildUserModel(adminToken, authResult.UserData, false)
		if err != nil {
			return nil, err
		}

		logger.Log.Info("OAuth login successful", "userID", user.ID, "email", user.Email)

		return &model.SignInResult{
			IDToken:     &idToken,
			AccessToken: &accessToken,
			Status:      model.SignInStatusSuccess,
			User:        user,
		}, nil
	}

	// Password-based flow (when email/password are provided)
	if isFirstTimeSetup {
		logger.Log.Info("First-time setup detected, creating first admin user")

		role := model.RoleAdmin
		_, err := svc.userService.CreateUser(ctx, &model.WriteUserInput{
			Email:    &email,
			Password: &password,
			Role:     &role,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create first admin user: %w", err)
		}

		// CreateUser may replace a rejected admin token.
		adminToken, err = svc.adminService.GetAdminToken()
		if err != nil {
			return nil, fmt.Errorf("failed to get admin token: %w", err)
		}

		authResult, err := svc.kcAuth.AuthenticateUser(adminToken, email, password, "")
		if err != nil {
			return nil, fmt.Errorf("failed to authenticate newly created admin: %w", err)
		}

		user, err := svc.buildUserModel(adminToken, authResult.UserData, false)
		if err != nil {
			return nil, err
		}

		logger.Log.Info("First admin login successful", "email", email, "userID", user.ID)

		return &model.SignInResult{
			IDToken:     &authResult.IDToken,
			AccessToken: &authResult.AccessToken,
			Status:      model.SignInStatusSuccess,
			User:        user,
		}, nil
	}

	authResult, err := svc.kcAuth.AuthenticateUser(adminToken, email, password, "")
	if err != nil {
		if strings.Contains(err.Error(), "password update required") {
			// Do not return a Keycloak admin/master token here. setInitialPassword uses
			// admin credentials server-side, and the UI must reach /update-password
			// without a verifiable user session token.
			return passwordUpdateRequiredResult(email), nil
		}
		return nil, err
	}

	roleStr, err := svc.kcUser.GetUserRole(adminToken, authResult.UserData.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user role after password login: %w", err)
	}
	if roleStr == keycloak.RoleNoAccess {
		logger.Log.Info("Password login denied: user has no-access role", "userID", authResult.UserData.ID, "email", email)
		if err := svc.kcUser.LogoutUserSessions(adminToken, authResult.UserData.ID); err != nil {
			logger.Log.Warn("Failed to logout denied user sessions", "userID", authResult.UserData.ID, "error", err)
		}
		return &model.SignInResult{
			Status: model.SignInStatusAccessDenied,
		}, nil
	}

	needsPasswordChange, err := svc.kcUser.CheckUserNeedsPasswordChange(adminToken, email)
	if err != nil {
		logger.Log.Error("Failed to check if user needs password change", "error", err)
		needsPasswordChange = false
	}

	user, err := svc.buildUserModel(adminToken, authResult.UserData, needsPasswordChange)
	if err != nil {
		return nil, err
	}

	logger.Log.Info("User login successful", "email", email, "userID", user.ID, "needsPasswordChange", user.NeedsPasswordChange)

	return &model.SignInResult{
		IDToken:     &authResult.IDToken,
		AccessToken: &authResult.AccessToken,
		Status:      model.SignInStatusSuccess,
		User:        user,
	}, nil
}

// ErrInvalidOneTimePassword is the only error SetInitialPassword returns for an
// unknown email, a wrong one-time password or a user with no pending password
// change, so a caller cannot tell which emails belong to invited users.
var ErrInvalidOneTimePassword = errors.New("Invalid email or temporary password")

// ErrTooManyPasswordAttempts is returned once an email or client address has
// used up its setInitialPassword attempts for the current window.
var ErrTooManyPasswordAttempts = errors.New("Too many attempts. Try again in a few minutes")

var errSetInitialPasswordFailed = errors.New("Could not set your password. Try again later")

const (
	initialPasswordWindow = 15 * time.Minute
	// Failed attempts allowed per window. Only failures count, so a stranger
	// cannot lock an invitee out by sending requests for their email.
	initialPasswordEmailLimit  = 5
	initialPasswordClientLimit = 50
	maxEmailLength             = 254
	maxPasswordLength          = 1024
)

// SetInitialPassword replaces the one-time password of a user created by an
// admin (pending UPDATE_PASSWORD) with newPassword and signs them in. The caller
// proves they hold the one-time password; Keycloak checks it.
func (svc *AuthService) SetInitialPassword(ctx context.Context, email, oneTimePassword, newPassword, clientIP string) (*model.SignInResult, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || len(email) > maxEmailLength || oneTimePassword == "" || len(oneTimePassword) > maxPasswordLength {
		return nil, ErrInvalidOneTimePassword
	}
	if newPassword == "" || len(newPassword) > maxPasswordLength {
		return nil, fmt.Errorf("new password must be between 1 and %d characters", maxPasswordLength)
	}

	emailKey := "email:" + email
	keys := []attemptKey{{key: emailKey, limit: initialPasswordEmailLimit}}
	// clientIP is empty unless a reverse proxy forwarded it; keying on the
	// connection address would put every Central UI user in one bucket.
	if clientIP != "" {
		keys = append(keys, attemptKey{key: "ip:" + clientIP, limit: initialPasswordClientLimit})
	}
	if svc.initialPasswordAttempts.blocked(keys...) {
		logger.Log.Warn("setInitialPassword rate limited", "clientIP", clientIP)
		return nil, ErrTooManyPasswordAttempts
	}
	reject := func() (*model.SignInResult, error) {
		svc.initialPasswordAttempts.fail(keys...)
		return nil, ErrInvalidOneTimePassword
	}

	// Check the one-time password before looking the user up, so known and
	// unknown emails take the same Keycloak round trip.
	valid, err := svc.kcAuth.VerifyPassword(email, oneTimePassword)
	if err != nil {
		logger.Log.Error("setInitialPassword: one-time password check failed", "error", err)
		return nil, errSetInitialPasswordFailed
	}
	if !valid {
		return reject()
	}

	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		logger.Log.Error("setInitialPassword: failed to get admin token", "error", err)
		return nil, errSetInitialPasswordFailed
	}
	kcUser, err := svc.kcUser.FindUserByExactEmail(adminToken, email)
	if err != nil {
		logger.Log.Error("setInitialPassword: failed to look up user", "error", err)
		return nil, errSetInitialPasswordFailed
	}
	// Only users an admin created (username is their email) with a pending
	// password change qualify: the password grant above signed in by username,
	// so this ties the verified credential to the user changed below.
	if kcUser == nil || !strings.EqualFold(getString(kcUser, "username"), email) || !keycloak.HasRequiredAction(kcUser, consts.UpdatePasswordAction) {
		return reject()
	}
	userID := getString(kcUser, "id")
	if userID == "" {
		return nil, errSetInitialPasswordFailed
	}

	if err := svc.kcUser.UpdateUserPasswordByID(adminToken, userID, newPassword); err != nil {
		// Keycloak password policy errors are safe to show: the caller proved the one-time password.
		return nil, fmt.Errorf("failed to update password: %w", err)
	}
	if err := svc.kcUser.SetUserPasswordRequirementByID(adminToken, userID, false); err != nil {
		return nil, fmt.Errorf("failed to clear password requirement: %w", err)
	}
	svc.initialPasswordAttempts.reset(emailKey)

	accessToken, err := svc.kcAuth.SignInWithPassword(email, newPassword)
	if err != nil {
		return nil, fmt.Errorf("failed to sign in with new password: %w", err)
	}
	user, err := svc.buildUserModel(adminToken, types.UserSummary{ID: userID, Email: getString(kcUser, "email")}, false)
	if err != nil {
		return nil, err
	}

	logger.Log.Info("Initial password set", "userID", userID)

	// Password sign-in has no ID token, as in SignIn.
	idToken := ""

	return &model.SignInResult{
		IDToken:     &idToken,
		AccessToken: &accessToken,
		Status:      model.SignInStatusSuccess,
		User:        user,
	}, nil
}

func getString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func (svc *AuthService) buildUserModel(adminToken string, userData types.UserSummary, needsPasswordChange bool) (*model.User, error) {
	userID := userData.ID
	email := userData.Email
	username := ""

	roleStr, err := svc.kcUser.GetUserRole(adminToken, userID)
	if err != nil {
		logger.Log.Error("Failed to get user role", "userID", userID, "error", err)
		roleStr = ""
	}
	role := utils.ConvertStringRoleToModel(roleStr)
	logger.Log.Info("buildUserModel: resolved role for user", "userID", userID, "email", email, "roleStr", roleStr, "roleModel", role)

	teams, err := svc.kcTeam.GetTeamsByUserID(adminToken, userID)
	if err != nil {
		logger.Log.Error("Failed to get teams by user ID", "userID", userID, "error", err)
		teams = []*model.Team{}
	}

	return &model.User{
		ID:                  userID,
		Email:               email,
		Username:            username,
		Role:                role,
		NeedsPasswordChange: needsPasswordChange,
		Teams:               teams,
	}, nil
}
