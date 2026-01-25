package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/odigos-io/central-backend/graph/model"
	"github.com/odigos-io/central-backend/internal/keycloak"
	"github.com/odigos-io/central-backend/internal/logger"
	"github.com/odigos-io/central-backend/internal/types"
	"github.com/odigos-io/central-backend/internal/utils"
	"github.com/odigos-io/odigos/api/k8sconsts"
)

type AuthService struct {
	adminService *AdminService
	userService  *UserService
	kcUser       *keycloak.KeycloakUserService
	kcAuth       *keycloak.KeycloakAuthService
	kcTeam       *keycloak.KeycloakTeamService
}

func NewAuthService(adminService *AdminService, userService *UserService) *AuthService {
	return &AuthService{
		adminService: adminService,
		userService:  userService,
		kcAuth:       keycloak.NewKeycloakAuthService(),
		kcUser:       keycloak.NewKeycloakUserService(),
		kcTeam:       keycloak.NewKeycloakTeamService(),
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

func (svc *AuthService) GetRedirectUrlForSignOut(ctx context.Context, idToken string) string {
	return svc.kcAuth.GetRedirectUrlForSignOut(idToken)
}

func (svc *AuthService) isFirstTimeSetup(ctx context.Context) bool {
	isInitialized := svc.adminService.DetermineSystemInitialized()
	return !isInitialized
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
	return &model.GeneralResult{
		Success: true,
	}, nil
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
	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return nil, fmt.Errorf("failed to get admin token: %w", err)
	}

	isFirstTimeSetup := svc.isFirstTimeSetup(ctx)

	if code != "" {
		authResult, err := svc.kcAuth.AuthenticateUser(adminToken, "", "", code)
		if err != nil {
			return nil, err
		}

		if svc.adminService.NeedToSetAdminUser() {
			if err := svc.kcUser.AddRealmRoleToUser(adminToken, authResult.UserData.Email, keycloak.RoleAdmin); err != nil {
				return nil, fmt.Errorf("failed to assign admin role to first SSO user: %w", err)
			}
		}

		user, err := svc.buildUserModel(adminToken, authResult.UserData, false)
		if err != nil {
			return nil, err
		}

		logger.Log.Info("OAuth login successful", "userID", user.ID, "email", user.Email)

		return &model.SignInResult{
			IDToken:     &authResult.IDToken,
			AccessToken: &authResult.AccessToken,
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
			adminToken, err := svc.adminService.GetAdminToken()
			if err != nil {
				return nil, fmt.Errorf("failed to get admin token for initial password setup: %w", err)
			}
			return &model.SignInResult{
				// IDToken:     ,
				AccessToken: &adminToken,
				Status:      model.SignInStatusPasswordUpdateRequired,
				User: &model.User{
					Email:               email,
					NeedsPasswordChange: true,
				},
			}, nil
		}
		return nil, err
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

func (svc *AuthService) SetInitialPassword(ctx context.Context, email, password string) (*model.SignInResult, error) {
	if err := svc.validateUserExists(email); err != nil {
		return nil, err
	}

	if err := svc.validatePasswordChangeRequired(email); err != nil {
		return nil, err
	}

	if err := svc.updateUserPassword(email, password); err != nil {
		return nil, err
	}

	if err := svc.clearPasswordRequirement(email); err != nil {
		return nil, err
	}

	idToken, accessToken, user, err := svc.authenticateUserWithNewPassword(ctx, email, password)
	if err != nil {
		return nil, err
	}

	return &model.SignInResult{
		IDToken:     &idToken,
		AccessToken: &accessToken,
		Status:      model.SignInStatusSuccess,
		User:        user,
	}, nil
}

func (svc *AuthService) validateUserExists(email string) error {
	exists, err := svc.userService.CheckUserExists(email)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("user not found: %w", err)
	}
	return nil
}

func (svc *AuthService) validatePasswordChangeRequired(email string) error {
	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return fmt.Errorf("failed to get admin token: %w", err)
	}
	needsChange, err := svc.kcUser.CheckUserNeedsPasswordChange(adminToken, email)
	if err != nil {
		return fmt.Errorf("failed to check password change requirement: %w", err)
	}
	if !needsChange {
		return fmt.Errorf("password change not required for this user")
	}
	return nil
}

func (svc *AuthService) updateUserPassword(email, password string) error {
	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return fmt.Errorf("failed to get admin token: %w", err)
	}

	err = svc.kcUser.UpdateUserPassword(adminToken, email, password)
	if err != nil {
		return fmt.Errorf("failed to update password: %w", err)
	}
	return nil
}

func (svc *AuthService) clearPasswordRequirement(email string) error {
	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return fmt.Errorf("failed to get admin token: %w", err)
	}

	err = svc.kcUser.SetUserPasswordRequirement(adminToken, email, false)
	if err != nil {
		return fmt.Errorf("failed to clear password requirement: %w", err)
	}
	return nil
}

func (svc *AuthService) authenticateUserWithNewPassword(ctx context.Context, email string, password string) (string, string, *model.User, error) {
	token, err := svc.adminService.GetAdminToken()
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to get admin token: %w", err)
	}

	authResult, err := svc.kcAuth.AuthenticateUser(token, email, password, "")
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to sign in with new password: %w", err)
	}

	user, err := svc.userService.GetUserByEmail(ctx, email)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to find user by email: %w", err)
	}

	return authResult.IDToken, authResult.AccessToken, user, nil
}

func (svc *AuthService) buildUserModel(adminToken string, userData types.UserSummary, needsPasswordChange bool) (*model.User, error) {
	userID := userData.ID
	email := userData.Email
	username := ""

	roleStr, err := svc.kcUser.GetUserRole(adminToken, userID)
	if err != nil {
		logger.Log.Error("Failed to get user role", "userID", userID, "error", err)
		roleStr = "user"
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
