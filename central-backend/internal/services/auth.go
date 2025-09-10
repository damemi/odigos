package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/odigos-io/central-backend/graph/model"
	"github.com/odigos-io/central-backend/internal/logger"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
	"github.com/odigos-io/central-backend/internal/types"
	"github.com/odigos-io/central-backend/internal/utils"
)

type AuthService struct {
	redisClient *redisclient.RedisClient
	userRepo    UserRepository
	authRepo    AuthRepository
	teamRepo    TeamRepository
}

func NewAuthService(redisClient *redisclient.RedisClient, userRepo UserRepository, authRepo AuthRepository, teamRepo TeamRepository) *AuthService {
	return &AuthService{
		redisClient: redisClient,
		userRepo:    userRepo,
		authRepo:    authRepo,
		teamRepo:    teamRepo,
	}
}

func (a *AuthService) SignIn(ctx context.Context, email, password string) (*model.SignInResult, error) {
	if a.isFirstTimeSetup(ctx, email, password) {
		logger.Log.Info("First-time setup detected, creating first admin user")

		role := model.RoleAdmin
		_, err := a.userRepo.CreateUser(ctx, &model.WriteUserInput{
			Email:    &email,
			Password: &password,
			Role:     &role,
		})
		if err != nil {
			logger.Log.Error("Failed to create first admin user", "error", err)
			return nil, fmt.Errorf("failed to create first admin user: %w", err)
		}

		logger.Log.Info("First admin user created successfully", "email", email)

		authResult, err := a.authRepo.AuthenticateUser(ctx, email, password)
		if err != nil {
			return nil, fmt.Errorf("failed to authenticate newly created admin: %w", err)
		}

		user, err := a.buildUserModel(ctx, authResult.AccessToken, authResult.UserData, false)
		if err != nil {
			return nil, err
		}

		logger.Log.Info("First admin login successful", "email", email, "userID", user.ID)

		err = a.redisClient.SetSystemInitialized(ctx, true)
		if err != nil {
			logger.Log.Error("Failed to set system as initialized", "error", err)
		}

		return &model.SignInResult{
			Token:  authResult.AccessToken,
			Status: model.SignInStatusSuccess,
			User:   user,
		}, nil
	}

	authResult, err := a.authRepo.AuthenticateUser(ctx, email, password)
	if err != nil {

		if strings.Contains(err.Error(), "password update required") {
			adminToken, err := a.authRepo.GetAdminToken(ctx)
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

	adminCtx := utils.SetTokenInContext(ctx, authResult.AccessToken)
	needsPasswordChange, err := a.userRepo.CheckUserNeedsPasswordChange(adminCtx, email)
	if err != nil {
		logger.Log.Error("Failed to check if user needs password change", "error", err)
		needsPasswordChange = false
	}

	user, err := a.buildUserModel(adminCtx, authResult.AccessToken, authResult.UserData, needsPasswordChange)
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

func (a *AuthService) SetInitialPassword(ctx context.Context, email, password string) (*model.SignInResult, error) {
	token := utils.GetTokenFromContext(ctx)
	if token == "" {
		return nil, fmt.Errorf("authentication required")
	}

	adminCtx := utils.SetTokenInContext(ctx, token)

	if err := a.validateUserExists(adminCtx, email); err != nil {
		return nil, err
	}

	if err := a.validatePasswordChangeRequired(adminCtx, email); err != nil {
		return nil, err
	}

	if err := a.updateUserPassword(adminCtx, email, password); err != nil {
		return nil, err
	}

	if err := a.clearPasswordRequirement(adminCtx, email); err != nil {
		return nil, err
	}

	token, user, err := a.authenticateUserWithNewPassword(ctx, email, password)
	if err != nil {
		return nil, err
	}

	return &model.SignInResult{
		Token:  token,
		Status: model.SignInStatusSuccess,
		User:   user,
	}, nil
}

func (a *AuthService) validateUserExists(ctx context.Context, email string) error {
	err := a.userRepo.CheckUserExists(ctx, email)
	if err != nil {
		return fmt.Errorf("user not found: %w", err)
	}
	return nil
}

func (a *AuthService) validatePasswordChangeRequired(ctx context.Context, email string) error {
	needsChange, err := a.userRepo.CheckUserNeedsPasswordChange(ctx, email)
	if err != nil {
		return fmt.Errorf("failed to check password change requirement: %w", err)
	}
	if !needsChange {
		return fmt.Errorf("password change not required for this user")
	}
	return nil
}

func (a *AuthService) updateUserPassword(ctx context.Context, email, password string) error {
	err := a.userRepo.UpdateUserPassword(ctx, email, password)
	if err != nil {
		return fmt.Errorf("failed to update password: %w", err)
	}
	return nil
}

func (a *AuthService) clearPasswordRequirement(ctx context.Context, email string) error {
	err := a.userRepo.SetUserPasswordRequirement(ctx, email, false)
	if err != nil {
		return fmt.Errorf("failed to clear password requirement: %w", err)
	}
	return nil
}

func (a *AuthService) authenticateUserWithNewPassword(ctx context.Context, email, password string) (string, *model.User, error) {
	authResult, err := a.authRepo.AuthenticateUser(ctx, email, password)
	if err != nil {
		return "", nil, fmt.Errorf("failed to sign in with new password: %w", err)
	}

	adminCtx := utils.SetTokenInContext(ctx, utils.GetTokenFromContext(ctx))
	user, err := a.userRepo.GetUserByEmail(adminCtx, email)
	if err != nil {
		return "", nil, fmt.Errorf("failed to find user by email: %w", err)
	}

	return authResult.AccessToken, user, nil
}

func (a *AuthService) buildUserModel(ctx context.Context, token string, userData types.UserSummary, needsPasswordChange bool) (*model.User, error) {
	userID := userData.ID
	email := userData.Email
	username := ""

	roleStr, err := a.userRepo.GetUserRole(ctx, userID)
	if err != nil {
		logger.Log.Error("Failed to get user role", "userID", userID, "error", err)
		roleStr = "user"
	}
	role := utils.ConvertStringRoleToModel(roleStr)
	logger.Log.Info("buildUserModel: resolved role for user", "userID", userID, "email", email, "roleStr", roleStr, "roleModel", role)

	teams, err := a.teamRepo.GetTeamsByUserID(ctx, userID)
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

func (a *AuthService) isFirstTimeSetup(ctx context.Context, email, password string) bool {
	isInitialized, err := a.redisClient.GetSystemInitialized(ctx)
	if err != nil {
		logger.Log.Error("Failed to get system initialization status from cache", "error", err)
		isInitialized = false
	}

	return !isInitialized
}

func (a *AuthService) SignOut(ctx context.Context) (*model.GeneralResult, error) {
	token := utils.GetTokenFromContext(ctx)
	if token == "" {
		return nil, fmt.Errorf("authentication required")
	}

	err := a.authRepo.Logout(ctx, token)
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
