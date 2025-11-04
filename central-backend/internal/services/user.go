package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/odigos-io/central-backend/graph/model"
	"github.com/odigos-io/central-backend/internal/keycloak"
	"github.com/odigos-io/central-backend/internal/logger"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
	"github.com/odigos-io/central-backend/internal/utils"
)

type UserService struct {
	redisClient  *redisclient.RedisClient
	adminService *AdminService
	kcUser       *keycloak.KeycloakUserService
	kcTeam       *keycloak.KeycloakTeamService
}

func NewUserService(redisClient *redisclient.RedisClient, adminService *AdminService) *UserService {
	return &UserService{
		redisClient:  redisClient,
		adminService: adminService,
		kcUser:       keycloak.NewKeycloakUserService(),
		kcTeam:       keycloak.NewKeycloakTeamService(),
	}
}

func (svc *UserService) CheckUserExists(email string) (bool, error) {
	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return false, err
	}

	users, err := svc.kcUser.FindUsersByEmail(adminToken, email)
	if err != nil {
		return false, err
	}
	if len(users) > 0 {
		return true, nil
	}

	return false, nil
}

func (svc *UserService) CreateUser(ctx context.Context, input *model.WriteUserInput) (*model.CreateUserResult, error) {
	email := input.Email
	role := input.Role

	if email == nil {
		return nil, fmt.Errorf("email is required")
	}
	if role == nil {
		return nil, fmt.Errorf("role is required")
	}

	userExists, err := svc.CheckUserExists(*email)
	if err != nil {
		return nil, fmt.Errorf("failed to validate user email: %w", err)
	}
	if userExists {
		return nil, fmt.Errorf("user with email '%s' already exists", *email)
	}

	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return nil, fmt.Errorf("failed to get admin token: %w", err)
	}

	tempPassword, generatedPassword := svc.preparePassword(input.Password)

	err = svc.kcUser.AddUserToRealm(adminToken, *email, *email, tempPassword)
	if err != nil {
		return nil, fmt.Errorf("failed to create user in Keycloak: %w", err)
	}
	err = svc.kcUser.SetUserPasswordRequirement(adminToken, *email, input.Password == nil)
	if err != nil {
		return nil, fmt.Errorf("failed to set password requirement: %w", err)
	}
	err = svc.kcUser.AddRealmRoleToUser(adminToken, *email, strings.ToLower(string(*role)))
	if err != nil {
		return nil, fmt.Errorf("failed to assign role: %w", err)
	}

	user, err := svc.GetUserByEmail(ctx, *email)
	if err != nil {
		return nil, err
	}
	userID := user.ID

	if len(input.AddTeamIds) > 0 {
		err = svc.kcTeam.AssignTeamsToUser(adminToken, userID, utils.ConvertStringPtrsToStrings(input.AddTeamIds))
		if err != nil {
			return nil, err
		}
	}
	if len(input.RemoveTeamIds) > 0 {
		err = svc.kcTeam.RemoveTeamsFromUser(adminToken, userID, utils.ConvertStringPtrsToStrings(input.RemoveTeamIds))
		if err != nil {
			return nil, err
		}
	}

	if len(input.AddComputePlatformIds) > 0 {
		err := svc.kcUser.AssignComputePlatformsToUser(adminToken, userID, utils.ConvertStringPtrsToStrings(input.AddComputePlatformIds))
		if err != nil {
			return nil, err
		}
	}
	if len(input.RemoveComputePlatformIds) > 0 {
		err := svc.kcUser.RemoveComputePlatformsFromUser(adminToken, userID, utils.ConvertStringPtrsToStrings(input.RemoveComputePlatformIds))
		if err != nil {
			return nil, err
		}
	}

	return &model.CreateUserResult{
		Success:           true,
		UserID:            userID,
		GeneratedPassword: generatedPassword,
	}, nil
}

func (svc *UserService) UpdateUser(ctx context.Context, userID string, input *model.WriteUserInput) (*model.GeneralResult, error) {
	email := input.Email
	newRole := input.Role

	if email == nil {
		return nil, fmt.Errorf("email is required")
	}
	if newRole == nil {
		return nil, fmt.Errorf("role is required")
	}

	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return nil, fmt.Errorf("failed to get admin token: %w", err)
	}

	user, err := svc.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	oldRole := user.Role

	err = svc.kcUser.UpdateUser(adminToken, userID, input)
	if err != nil {
		return nil, err
	}
	err = svc.kcUser.RemoveRealmRoleFromUser(adminToken, *email, strings.ToLower(string(oldRole)))
	if err != nil {
		return nil, fmt.Errorf("failed to remove role: %w", err)
	}
	err = svc.kcUser.AddRealmRoleToUser(adminToken, *email, strings.ToLower(string(*newRole)))
	if err != nil {
		return nil, fmt.Errorf("failed to assign role: %w", err)
	}

	if len(input.AddTeamIds) > 0 {
		err = svc.kcTeam.AssignTeamsToUser(adminToken, userID, utils.ConvertStringPtrsToStrings(input.AddTeamIds))
		if err != nil {
			return nil, err
		}
	}
	if len(input.RemoveTeamIds) > 0 {
		err = svc.kcTeam.RemoveTeamsFromUser(adminToken, userID, utils.ConvertStringPtrsToStrings(input.RemoveTeamIds))
		if err != nil {
			return nil, err
		}
	}

	if len(input.AddComputePlatformIds) > 0 {
		err = svc.kcUser.AssignComputePlatformsToUser(adminToken, userID, utils.ConvertStringPtrsToStrings(input.AddComputePlatformIds))
		if err != nil {
			return nil, err
		}
	}
	if len(input.RemoveComputePlatformIds) > 0 {
		err = svc.kcUser.RemoveComputePlatformsFromUser(adminToken, userID, utils.ConvertStringPtrsToStrings(input.RemoveComputePlatformIds))
		if err != nil {
			return nil, err
		}
	}

	return &model.GeneralResult{Success: true, Error: nil}, nil
}

func (svc *UserService) DeleteUser(ctx context.Context, userID string) (*model.GeneralResult, error) {
	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return nil, err
	}

	err = svc.kcUser.DeleteUserByID(adminToken, userID)
	if err != nil {
		return nil, err
	}

	return &model.GeneralResult{Success: true, Error: nil}, nil
}

func (svc *UserService) GetUsers(ctx context.Context) ([]*model.User, error) {
	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return nil, err
	}

	users, err := svc.kcUser.ListUsers(adminToken)
	if err != nil {
		return nil, err
	}

	var result []*model.User
	for _, userData := range users {
		user, err := svc.mapKeycloakUserToModel(userData, adminToken)
		if err != nil {
			return nil, err
		}
		result = append(result, user)
	}

	return result, nil
}

func (svc *UserService) GetUserByID(ctx context.Context, userID string) (*model.User, error) {
	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return nil, err
	}

	userData, err := svc.kcUser.GetUserByID(adminToken, userID)
	if err != nil {
		return nil, err
	}

	return svc.mapKeycloakUserToModel(userData, adminToken)
}

func (svc *UserService) GetUserByEmail(ctx context.Context, email string) (*model.User, error) {
	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return nil, err
	}

	users, err := svc.kcUser.FindUsersByEmail(adminToken, email)
	if err != nil || len(users) == 0 {
		return nil, fmt.Errorf("user not found: %w", err)
	}

	return svc.mapKeycloakUserToModel(users[0], adminToken)
}

func (svc *UserService) AssignTeamsToUser(ctx context.Context, userID string, teamIDs []string) (*model.GeneralResult, error) {
	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return nil, err
	}

	err = svc.kcTeam.AssignTeamsToUser(adminToken, userID, teamIDs)
	if err != nil {
		errorMsg := fmt.Sprintf("Failed to assign teams: %s", err)
		return &model.GeneralResult{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}

	return &model.GeneralResult{
		Success: true,
	}, nil
}

func (svc *UserService) RemoveTeamsFromUser(ctx context.Context, userID string, teamIDs []string) (*model.GeneralResult, error) {
	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return nil, err
	}

	err = svc.kcTeam.RemoveTeamsFromUser(adminToken, userID, teamIDs)
	if err != nil {
		errorMsg := fmt.Sprintf("Failed to remove teams: %s", err)
		return &model.GeneralResult{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}

	return &model.GeneralResult{
		Success: true,
	}, nil
}

func (svc *UserService) GetTeamsForUser(ctx context.Context, userID string) ([]*model.Team, error) {
	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return nil, err
	}

	teams, err := svc.kcTeam.GetTeamsByUserID(adminToken, userID)
	if err != nil {
		return nil, err
	}

	return teams, nil
}

func (svc *UserService) AssignComputePlatformsToUser(ctx context.Context, userID string, computePlatformIDs []string) error {
	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return err
	}

	return svc.kcUser.AssignComputePlatformsToUser(adminToken, userID, computePlatformIDs)
}

func (svc *UserService) RemoveComputePlatformsFromUser(ctx context.Context, userID string, computePlatformIDs []string) error {
	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return err
	}

	return svc.kcUser.RemoveComputePlatformsFromUser(adminToken, userID, computePlatformIDs)
}

func (svc *UserService) GetComputePlatformsForUser(ctx context.Context, userID string) ([]*model.ComputePlatform, error) {
	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return nil, err
	}

	computePlatforms, err := svc.kcUser.GetComputePlatformIdsForUser(adminToken, userID)
	if err != nil {
		return nil, err
	}

	var computePlatformModels []*model.ComputePlatform
	for _, cpID := range computePlatforms {
		computePlatformModels = append(computePlatformModels, &model.ComputePlatform{ID: cpID})
	}

	return computePlatformModels, nil
}

// Helper methods

func (svc *UserService) preparePassword(password *string) (string, *string) {
	tempPassword := password
	var generatedPassword *string

	if tempPassword == nil {
		tmp := utils.GenerateRandomPassword(12)
		tempPassword = &tmp
		generatedPassword = &tmp
	}

	return *tempPassword, generatedPassword
}

func (svc *UserService) mapKeycloakUserToModel(userData map[string]any, adminToken string) (*model.User, error) {
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

	role, err := svc.kcUser.GetUserRole(adminToken, id)
	if err != nil {
		role = keycloak.RoleViewer
	}

	modelRole := utils.ConvertStringRoleToModel(strings.ToUpper(role))

	needsPasswordChange, err := svc.kcUser.CheckUserNeedsPasswordChange(adminToken, email)
	if err != nil {
		needsPasswordChange = false
	}

	teams, err := svc.kcTeam.GetTeamsByUserID(adminToken, id)
	if err != nil {
		logger.Log.Error("Failed to get user teams", "userID", id, "error", err)
		teams = []*model.Team{}
	}

	computePlatformIds, err := svc.kcUser.GetComputePlatformIdsForUser(adminToken, id)
	if err != nil {
		logger.Log.Error("Failed to get user compute platforms", "userID", id, "error", err)
		computePlatformIds = []string{}
	}

	var computePlatformModels []*model.ComputePlatform
	for _, cpID := range computePlatformIds {
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
