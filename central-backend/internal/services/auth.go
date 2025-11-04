package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/odigos-io/central-backend/graph/model"
	"github.com/odigos-io/central-backend/internal/keycloak"
	"github.com/odigos-io/central-backend/internal/logger"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
	"github.com/odigos-io/central-backend/internal/types"
	"github.com/odigos-io/central-backend/internal/utils"
)

type AuthService struct {
	redisClient  *redisclient.RedisClient
	adminService *AdminService
	userService  *UserService
	kcUser       *keycloak.KeycloakUserService
	kcAuth       *keycloak.KeycloakAuthService
	kcTeam       *keycloak.KeycloakTeamService
}

func NewAuthService(redisClient *redisclient.RedisClient, adminService *AdminService, userService *UserService) *AuthService {
	return &AuthService{
		redisClient:  redisClient,
		adminService: adminService,
		userService:  userService,
		kcAuth:       keycloak.NewKeycloakAuthService(),
		kcUser:       keycloak.NewKeycloakUserService(),
		kcTeam:       keycloak.NewKeycloakTeamService(),
	}
}

func (svc *AuthService) isFirstTimeSetup(ctx context.Context) bool {
	isInitialized, err := svc.redisClient.GetSystemInitialized(ctx)
	if err != nil {
		logger.Log.Error("Failed to get system initialization status from cache", "error", err)
		isInitialized = false
	}

	return !isInitialized
}

func (svc *AuthService) SignIn(ctx context.Context, email string, password string) (*model.SignInResult, error) {
	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return nil, fmt.Errorf("failed to get admin token: %w", err)
	}

	if svc.isFirstTimeSetup(ctx) {
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

		authResult, err := svc.kcAuth.AuthenticateUser(adminToken, email, password)
		if err != nil {
			return nil, fmt.Errorf("failed to authenticate newly created admin: %w", err)
		}

		user, err := svc.buildUserModel(authResult.AccessToken, authResult.UserData, false)
		if err != nil {
			return nil, err
		}

		logger.Log.Info("First admin login successful", "email", email, "userID", user.ID)

		err = svc.redisClient.SetSystemInitialized(ctx, true)
		if err != nil {
			logger.Log.Error("Failed to set system as initialized", "error", err)
		}

		return &model.SignInResult{
			Token:  authResult.AccessToken,
			Status: model.SignInStatusSuccess,
			User:   user,
		}, nil
	}

	authResult, err := svc.kcAuth.AuthenticateUser(adminToken, email, password)
	if err != nil {
		if strings.Contains(err.Error(), "password update required") {
			adminToken, err := svc.adminService.GetAdminToken()
			if err != nil {
				return nil, fmt.Errorf("failed to get admin token for initial password setup: %w", err)
			}
			return &model.SignInResult{
				Token:  adminToken,
				Status: model.SignInStatusPasswordUpdateRequired,
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

	user, err := svc.buildUserModel(authResult.AccessToken, authResult.UserData, needsPasswordChange)
	if err != nil {
		return nil, err
	}

	logger.Log.Info("User login successful", "email", email, "userID", user.ID, "needsPasswordChange", user.NeedsPasswordChange)

	return &model.SignInResult{
		Token:  authResult.AccessToken,
		Status: model.SignInStatusSuccess,
		User:   user,
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

	token, user, err := svc.authenticateUserWithNewPassword(ctx, email, password)
	if err != nil {
		return nil, err
	}

	return &model.SignInResult{
		Token:  token,
		Status: model.SignInStatusSuccess,
		User:   user,
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

func (svc *AuthService) authenticateUserWithNewPassword(ctx context.Context, email string, password string) (string, *model.User, error) {
	token, err := svc.adminService.GetAdminToken()
	if err != nil {
		return "", nil, fmt.Errorf("failed to get admin token: %w", err)
	}

	authResult, err := svc.kcAuth.AuthenticateUser(token, email, password)
	if err != nil {
		return "", nil, fmt.Errorf("failed to sign in with new password: %w", err)
	}

	user, err := svc.userService.GetUserByEmail(ctx, email)
	if err != nil {
		return "", nil, fmt.Errorf("failed to find user by email: %w", err)
	}

	return authResult.AccessToken, user, nil
}

func (svc *AuthService) buildUserModel(token string, userData types.UserSummary, needsPasswordChange bool) (*model.User, error) {
	userID := userData.ID
	email := userData.Email
	username := ""

	roleStr, err := svc.kcUser.GetUserRole(token, userID)
	if err != nil {
		logger.Log.Error("Failed to get user role", "userID", userID, "error", err)
		roleStr = "user"
	}
	role := utils.ConvertStringRoleToModel(roleStr)
	logger.Log.Info("buildUserModel: resolved role for user", "userID", userID, "email", email, "roleStr", roleStr, "roleModel", role)

	teams, err := svc.kcTeam.GetTeamsByUserID(token, userID)
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

func (svc *AuthService) SignOut(ctx context.Context) (*model.GeneralResult, error) {
	token := utils.GetTokenFromContext(ctx)
	if token == "" {
		return nil, fmt.Errorf("authentication required")
	}

	err := svc.kcAuth.LogoutUser(token)
	if err != nil {
		logger.Log.Error("Failed to logout user", "error", err)
		errorMsg := fmt.Sprintf("failed to logout: %v", err)
		return &model.GeneralResult{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}

	logger.Log.Info("User logged out successfully")
	return &model.GeneralResult{
		Success: true,
	}, nil
}
