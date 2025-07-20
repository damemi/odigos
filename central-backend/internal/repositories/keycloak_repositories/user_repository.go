package keycloak_repositories

import (
	"context"
	"fmt"
	"strings"

	"github.com/odigos-io/central-backend/graph/model"
	"github.com/odigos-io/central-backend/internal/keycloak"
	"github.com/odigos-io/central-backend/internal/logger"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
	"github.com/odigos-io/central-backend/internal/services"
	"github.com/odigos-io/central-backend/internal/utils"
)

type UserRepository struct {
	redisClient         *redisclient.RedisClient
	keycloakUserService *keycloak.UserService
	credentialsService  *services.CredentialsService
	adminService        *services.AdminService
}

func NewUserRepository(redisClient *redisclient.RedisClient, credentialsService *services.CredentialsService, adminService *services.AdminService) *UserRepository {
	return &UserRepository{
		redisClient:         redisClient,
		keycloakUserService: keycloak.NewUserService(),
		credentialsService:  credentialsService,
		adminService:        adminService,
	}
}

func (k *UserRepository) CheckUserExists(ctx context.Context, email string) error {
	// First try to get admin token from context (for SetInitialPassword flow)
	if adminToken := utils.GetTokenFromContext(ctx); adminToken != "" {
		// Try to use the token directly as an admin token
		_, err := k.keycloakUserService.CheckUserDetails(adminToken, email)
		if err == nil {
			return nil
		}
		// If it fails, fall back to getting a fresh admin token
	}

	adminToken, err := k.adminService.GetAdminToken(ctx)
	if err != nil {
		return err
	}

	_, err = k.keycloakUserService.CheckUserDetails(adminToken, email)
	return err
}

func (k *UserRepository) CreateUser(ctx context.Context, input *model.WriteUserInput) (*model.CreateUserResult, error) {
	email := input.Email
	role := input.Role

	if email == nil {
		return nil, fmt.Errorf("email is required")
	}
	if role == nil {
		return nil, fmt.Errorf("role is required")
	}

	adminToken, err := k.adminService.GetAdminToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get admin token: %w", err)
	}

	tempPassword, generatedPassword := k.preparePassword(input.Password)

	err = k.keycloakUserService.AddUserToRealm(adminToken, *email, *email, tempPassword)
	if err != nil {
		return nil, fmt.Errorf("failed to create user in Keycloak: %w", err)
	}
	err = k.keycloakUserService.SetUserPasswordRequirement(adminToken, *email, input.Password == nil)
	if err != nil {
		return nil, fmt.Errorf("failed to set password requirement: %w", err)
	}
	err = k.keycloakUserService.AddRealmRoleToUser(adminToken, *email, strings.ToLower(string(*role)))
	if err != nil {
		return nil, fmt.Errorf("failed to assign role: %w", err)
	}

	user, err := k.GetUserByEmail(ctx, *email)
	if err != nil {
		return nil, err
	}
	userID := user.ID

	return &model.CreateUserResult{
		Success:           true,
		UserID:            userID,
		GeneratedPassword: generatedPassword,
	}, nil
}

func (k *UserRepository) UpdateUser(ctx context.Context, userID string, input *model.WriteUserInput) (*model.GeneralResult, error) {

	email := input.Email
	newRole := input.Role

	if email == nil {
		return nil, fmt.Errorf("email is required")
	}
	if newRole == nil {
		return nil, fmt.Errorf("role is required")
	}

	adminToken, err := k.adminService.GetAdminToken(ctx)
	if err != nil {
		return nil, err
	}

	user, err := k.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	oldRole := user.Role

	err = k.keycloakUserService.UpdateUser(adminToken, userID, input)
	if err != nil {
		return nil, err
	}
	err = k.keycloakUserService.RemoveRealmRoleFromUser(adminToken, *email, strings.ToLower(string(oldRole)))
	if err != nil {
		return nil, fmt.Errorf("failed to remove role: %w", err)
	}
	err = k.keycloakUserService.AddRealmRoleToUser(adminToken, *email, strings.ToLower(string(*newRole)))
	if err != nil {
		return nil, fmt.Errorf("failed to assign role: %w", err)
	}

	return &model.GeneralResult{Success: true, Error: nil}, nil
}

func (k *UserRepository) DeleteUser(ctx context.Context, userID string) (*model.GeneralResult, error) {
	adminToken, err := k.adminService.GetAdminToken(ctx)
	if err != nil {
		errorMsg := err.Error()
		return &model.GeneralResult{Success: false, Error: &errorMsg}, nil
	}

	err = k.keycloakUserService.DeleteUserByID(adminToken, userID)
	if err != nil {
		errorMsg := err.Error()
		return &model.GeneralResult{Success: false, Error: &errorMsg}, nil
	}

	return &model.GeneralResult{Success: true, Error: nil}, nil
}

func (k *UserRepository) GetUsers(ctx context.Context) ([]*model.User, error) {
	adminToken, err := k.adminService.GetAdminToken(ctx)
	if err != nil {
		return nil, err
	}

	users, err := k.keycloakUserService.ListUsers(adminToken)
	if err != nil {
		return nil, err
	}

	var result []*model.User
	for _, userData := range users {

		user, err := k.mapKeycloakUserToModel(userData, adminToken)
		if err != nil {
			logger.Log.Error("Failed to map keycloak user to model", "error", err)
			continue
		}
		result = append(result, user)
	}

	return result, nil
}

func (k *UserRepository) GetUserByID(ctx context.Context, userID string) (*model.User, error) {
	adminToken, err := k.adminService.GetAdminToken(ctx)
	if err != nil {
		return nil, err
	}

	userData, err := k.keycloakUserService.GetUserByID(adminToken, userID)
	if err != nil {
		return nil, err
	}

	return k.mapKeycloakUserToModel(userData, adminToken)
}

func (k *UserRepository) GetUserByEmail(ctx context.Context, email string) (*model.User, error) {
	// First try to get admin token from context (for SetInitialPassword flow)
	if adminToken := utils.GetTokenFromContext(ctx); adminToken != "" {
		// Try to use the token directly as an admin token
		users, err := k.keycloakUserService.FindUsersByEmail(adminToken, email)
		if err == nil && len(users) > 0 {
			return k.mapKeycloakUserToModel(users[0], adminToken)
		}
		// If it fails, fall back to getting a fresh admin token
	}

	adminToken, err := k.adminService.GetAdminToken(ctx)
	if err != nil {
		return nil, err
	}

	users, err := k.keycloakUserService.FindUsersByEmail(adminToken, email)
	if err != nil || len(users) == 0 {
		return nil, fmt.Errorf("user not found: %w", err)
	}

	return k.mapKeycloakUserToModel(users[0], adminToken)
}

func (k *UserRepository) CheckUserNeedsPasswordChange(ctx context.Context, email string) (bool, error) {
	// First try to get admin token from context (for SetInitialPassword flow)
	if adminToken := utils.GetTokenFromContext(ctx); adminToken != "" {
		// Try to use the token directly as an admin token
		needsChange, err := k.keycloakUserService.CheckUserNeedsPasswordChange(adminToken, email)
		if err == nil {
			return needsChange, nil
		}
		// If it fails, fall back to getting a fresh admin token
	}

	adminToken, err := k.adminService.GetAdminToken(ctx)
	if err != nil {
		return false, err
	}

	return k.keycloakUserService.CheckUserNeedsPasswordChange(adminToken, email)
}

func (k *UserRepository) UpdateUserPassword(ctx context.Context, email string, password string) error {
	// First try to get admin token from context (for SetInitialPassword flow)
	if adminToken := utils.GetTokenFromContext(ctx); adminToken != "" {
		// Try to use the token directly as an admin token
		err := k.keycloakUserService.UpdateUserPassword(adminToken, email, password)
		if err == nil {
			return nil
		}
		// If it fails, fall back to getting a fresh admin token
	}

	// Check if user is updating their own password
	userToken := utils.GetTokenFromContext(ctx)
	if userToken != "" {
		tokenEmail, err := utils.ExtractEmailFromToken(userToken)
		if err == nil && tokenEmail == email {
			return k.keycloakUserService.UpdateUserPassword(userToken, email, password)
		}
	}

	adminToken, err := k.adminService.GetAdminToken(ctx)
	if err != nil {
		return err
	}
	return k.keycloakUserService.UpdateUserPassword(adminToken, email, password)
}

func (k *UserRepository) SetUserPasswordRequirement(ctx context.Context, email string, required bool) error {
	// First try to get admin token from context (for SetInitialPassword flow)
	if adminToken := utils.GetTokenFromContext(ctx); adminToken != "" {
		// Try to use the token directly as an admin token
		err := k.keycloakUserService.SetUserPasswordRequirement(adminToken, email, required)
		if err == nil {
			return nil
		}
		// If it fails, fall back to getting a fresh admin token
	}

	adminToken, err := k.adminService.GetAdminToken(ctx)
	if err != nil {
		return err
	}

	return k.keycloakUserService.SetUserPasswordRequirement(adminToken, email, required)
}

func (k *UserRepository) GetUserRole(ctx context.Context, userID string) (string, error) {
	adminToken, err := k.adminService.GetAdminToken(ctx)
	if err != nil {
		return "", err
	}

	return k.keycloakUserService.GetUserRole(adminToken, userID)
}

func (k *UserRepository) AssignComputePlatformsToUser(ctx context.Context, userID string, computePlatformIDs []string) (*model.GeneralResult, error) {
	adminToken, err := k.adminService.GetAdminToken(ctx)
	if err != nil {
		logger.Log.Error("Failed to get admin token for compute platform assignment", "error", err)
		errorMsg := "Failed to authenticate: " + err.Error()
		return &model.GeneralResult{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}

	err = k.keycloakUserService.AssignComputePlatformsToUser(adminToken, userID, computePlatformIDs)
	if err != nil {
		logger.Log.Error("Failed to assign compute platforms to user", "userID", userID, "computePlatformIDs", computePlatformIDs, "error", err)
		errorMsg := "Failed to assign compute platforms: " + err.Error()
		return &model.GeneralResult{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}

	return &model.GeneralResult{
		Success: true,
	}, nil
}

// Helper methods

func (k *UserRepository) preparePassword(password *string) (string, *string) {
	tempPassword := password
	var generatedPassword *string

	if tempPassword == nil {
		tmp := utils.GenerateRandomPassword(12)
		tempPassword = &tmp
		generatedPassword = &tmp
	}

	return *tempPassword, generatedPassword
}

func (k *UserRepository) mapKeycloakUserToModel(userData map[string]any, adminToken string) (*model.User, error) {

	id, ok := userData["id"].(string)
	if !ok || id == "" {
		return nil, fmt.Errorf("invalid user ID")
	}

	username, ok := userData["username"].(string)
	if !ok || username == "" {
		return nil, fmt.Errorf("invalid username")
	}

	email, ok := userData["email"].(string)
	if !ok || email == "" {
		logger.Log.Warn("User email is missing or empty, falling back to username",
			"userID", id,
			"username", username,
			"emailFound", ok,
			"emailValue", email)
		email = username
	}

	role, err := k.keycloakUserService.GetUserRole(adminToken, id)
	if err != nil {
		role = keycloak.RoleViewer
	}

	modelRole := utils.ConvertStringRoleToModel(strings.ToUpper(role))

	needsPasswordChange, err := k.keycloakUserService.CheckUserNeedsPasswordChange(adminToken, email)
	if err != nil {
		needsPasswordChange = false
	}

	teams, err := k.keycloakUserService.GetUserTeams(adminToken, id)
	if err != nil {
		logger.Log.Error("Failed to get user teams", "userID", id, "error", err)
		teams = []*model.Team{}
	}

	computePlatforms, err := k.keycloakUserService.GetUserComputePlatforms(adminToken, id)
	if err != nil {
		logger.Log.Error("Failed to get user compute platforms", "userID", id, "error", err)
		computePlatforms = []string{}
	}

	var computePlatformModels []*model.ComputePlatform
	for _, cpID := range computePlatforms {
		computePlatformModels = append(computePlatformModels, &model.ComputePlatform{ID: cpID})
	}

	return &model.User{
		ID:                  id,
		Email:               email,
		Username:            username,
		Role:                modelRole,
		NeedsPasswordChange: needsPasswordChange,
		Teams:               teams,
		ComputePlatforms:    computePlatformModels,
	}, nil
}
