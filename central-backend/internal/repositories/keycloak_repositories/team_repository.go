package keycloak_repositories

import (
	"context"
	"fmt"

	"github.com/odigos-io/central-backend/graph/model"
	"github.com/odigos-io/central-backend/internal/consts"
	"github.com/odigos-io/central-backend/internal/keycloak"
	"github.com/odigos-io/central-backend/internal/logger"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
	"github.com/odigos-io/central-backend/internal/services"
)

type TeamRepository struct {
	redisClient        *redisclient.RedisClient
	credentialsService *services.CredentialsService
	adminService       *services.AdminService
}

func NewTeamRepository(redisClient *redisclient.RedisClient, credentialsService *services.CredentialsService, adminService *services.AdminService) *TeamRepository {
	return &TeamRepository{
		redisClient:        redisClient,
		credentialsService: credentialsService,
		adminService:       adminService,
	}
}

func (k *TeamRepository) CheckTeamExists(ctx context.Context, teamNameOrTeamID string) (bool, error) {
	adminToken, err := k.adminService.GetAdminToken(ctx)
	if err != nil {
		return false, err
	}

	teams, err := keycloak.ListTeams(adminToken, consts.GetKeycloakHost(), consts.KeycloakRealm)
	if err != nil {
		return false, err
	}

	for _, team := range teams {
		if team.Name == teamNameOrTeamID || team.ID == teamNameOrTeamID {
			return true, nil
		}
	}

	return false, nil
}

func (k *TeamRepository) CreateTeam(ctx context.Context, input *model.WriteTeamInput) (*model.Team, error) {
	name := input.Name
	description := input.Description

	if input.Name == nil {
		return nil, fmt.Errorf("team name is required")
	}

	adminToken, err := k.adminService.GetAdminToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get admin token: %w", err)
	}

	createdTeam, err := keycloak.CreateTeam(adminToken, consts.GetKeycloakHost(), consts.KeycloakRealm, *name, description)
	if err != nil {
		return nil, fmt.Errorf("failed to create team in Keycloak: %w", err)
	}

	logger.Log.Info("Team created successfully", "teamID", createdTeam.ID, "teamName", createdTeam.Name)

	return createdTeam, nil
}

func (k *TeamRepository) UpdateTeam(ctx context.Context, teamID string, input *model.WriteTeamInput) (*model.Team, error) {
	name := input.Name
	description := input.Description

	if input.Name == nil {
		return nil, fmt.Errorf("team name is required")
	}

	adminToken, err := k.adminService.GetAdminToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get admin token: %w", err)
	}

	updatedTeam, err := keycloak.UpdateTeam(adminToken, consts.GetKeycloakHost(), consts.KeycloakRealm, teamID, *name, description)
	if err != nil {
		return nil, fmt.Errorf("failed to update team in Keycloak: %w", err)
	}

	return updatedTeam, nil
}

func (k *TeamRepository) GetTeams(ctx context.Context) ([]*model.Team, error) {
	adminToken, err := k.adminService.GetAdminTokenFromKeycloak(ctx)
	if err != nil {
		return nil, err
	}

	teams, err := keycloak.ListTeams(adminToken, consts.GetKeycloakHost(), consts.KeycloakRealm)
	if err != nil {
		return nil, err
	}

	// get users for each team
	for _, team := range teams {
		users, err := k.GetUsersForTeam(ctx, team.ID)
		if err != nil {
			return nil, err
		}
		team.Users = users
	}

	// get compute platforms for each team
	for _, team := range teams {
		computePlatforms, err := k.GetComputePlatformsForTeam(ctx, team.ID)
		if err != nil {
			return nil, err
		}
		team.ComputePlatforms = computePlatforms
	}

	return teams, nil
}

func (k *TeamRepository) GetTeamByID(ctx context.Context, teamID string) (*model.Team, error) {
	adminToken, err := k.adminService.GetAdminToken(ctx)
	if err != nil {
		return nil, err
	}

	team, err := keycloak.GetTeamByID(adminToken, consts.GetKeycloakHost(), consts.KeycloakRealm, teamID)
	if err != nil {
		return nil, err
	}

	users, err := k.GetUsersForTeam(ctx, teamID)
	if err != nil {
		return nil, err
	}
	team.Users = users

	computePlatforms, err := k.GetComputePlatformsForTeam(ctx, teamID)
	if err != nil {
		return nil, err
	}
	team.ComputePlatforms = computePlatforms

	return team, nil
}

func (k *TeamRepository) GetTeamByName(ctx context.Context, teamName string) (*model.Team, error) {
	adminToken, err := k.adminService.GetAdminToken(ctx)
	if err != nil {
		return nil, err
	}

	return keycloak.GetTeamByName(adminToken, consts.GetKeycloakHost(), consts.KeycloakRealm, teamName)
}

func (k *TeamRepository) GetUsersForTeam(ctx context.Context, teamID string) ([]*model.User, error) {
	adminToken, err := k.adminService.GetAdminToken(ctx)
	if err != nil {
		return nil, err
	}

	users, err := keycloak.GetUsersForTeam(adminToken, consts.GetKeycloakHost(), consts.KeycloakRealm, teamID)
	if err != nil {
		return nil, err
	}

	return users, nil
}

func (k *TeamRepository) GetTeamsByUserID(ctx context.Context, userID string) ([]*model.Team, error) {
	adminToken, err := k.adminService.GetAdminTokenFromKeycloak(ctx)
	if err != nil {
		return nil, err
	}

	teams, err := keycloak.GetUserTeams(adminToken, consts.GetKeycloakHost(), consts.KeycloakRealm, userID)
	if err != nil {
		return nil, err
	}

	return teams, nil
}

func (k *TeamRepository) DeleteTeam(ctx context.Context, teamID string) (*model.GeneralResult, error) {
	adminToken, err := k.adminService.GetAdminToken(ctx)
	if err != nil {
		errorMsg := err.Error()
		return &model.GeneralResult{Success: false, Error: &errorMsg}, nil
	}

	err = keycloak.DeleteTeam(adminToken, consts.GetKeycloakHost(), consts.KeycloakRealm, teamID)
	if err != nil {
		errorMsg := err.Error()
		return &model.GeneralResult{Success: false, Error: &errorMsg}, nil
	}

	return &model.GeneralResult{Success: true, Error: nil}, nil
}

func (k *TeamRepository) AssignComputePlatformsToTeam(ctx context.Context, teamID string, computePlatformIDs []string) error {
	adminToken, err := k.adminService.GetAdminToken(ctx)
	if err != nil {
		return err
	}

	err = keycloak.AssignComputePlatformsToTeam(adminToken, consts.GetKeycloakHost(), consts.KeycloakRealm, teamID, computePlatformIDs)
	if err != nil {
		return err
	}

	return nil
}

func (k *TeamRepository) RemoveComputePlatformsFromTeam(ctx context.Context, teamID string, computePlatformIDs []string) error {
	adminToken, err := k.adminService.GetAdminToken(ctx)
	if err != nil {
		return err
	}

	err = keycloak.RemoveComputePlatformsFromTeam(adminToken, consts.GetKeycloakHost(), consts.KeycloakRealm, teamID, computePlatformIDs)
	if err != nil {
		return err
	}

	return nil
}

func (k *TeamRepository) GetComputePlatformsForTeam(ctx context.Context, teamID string) ([]*model.ComputePlatform, error) {
	adminToken, err := k.adminService.GetAdminToken(ctx)
	if err != nil {
		return nil, err
	}

	computePlatformIds, err := keycloak.GetComputePlatformIdsForTeam(adminToken, consts.GetKeycloakHost(), consts.KeycloakRealm, teamID)
	if err != nil {
		return nil, err
	}

	var computePlatformModels []*model.ComputePlatform
	for _, id := range computePlatformIds {
		computePlatformModels = append(computePlatformModels, &model.ComputePlatform{ID: id})
	}

	return computePlatformModels, nil
}
