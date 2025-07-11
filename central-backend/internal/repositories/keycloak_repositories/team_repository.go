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

func (k *TeamRepository) CreateTeam(ctx context.Context, input model.CreateTeamInput) (*model.Team, error) {
	adminToken, err := k.adminService.GetAdminToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get admin token: %w", err)
	}

	createdTeam, err := keycloak.CreateTeam(adminToken, consts.KeycloakHost, consts.KeycloakRealm, input)
	if err != nil {
		return nil, fmt.Errorf("failed to create team in Keycloak: %w", err)
	}

	logger.Log.Info("Team created successfully", "teamID", createdTeam.ID, "teamName", createdTeam.Name)

	return createdTeam, nil
}

func (k *TeamRepository) GetTeams(ctx context.Context) ([]*model.Team, error) {
	adminToken, err := k.adminService.GetAdminToken(ctx)
	if err != nil {
		return nil, err
	}

	teams, err := keycloak.ListTeams(adminToken, consts.KeycloakHost, consts.KeycloakRealm)
	if err != nil {
		return nil, err
	}

	return teams, nil
}

func (k *TeamRepository) AssignTeamsToUser(ctx context.Context, userID string, teamIDs []string) error {
	adminToken, err := k.adminService.GetAdminToken(ctx)
	if err != nil {
		return fmt.Errorf("failed to get admin token: %w", err)
	}

	err = keycloak.AssignTeamsToUser(adminToken, consts.KeycloakHost, consts.KeycloakRealm, userID, teamIDs)
	if err != nil {
		return fmt.Errorf("failed to assign teams to user: %w", err)
	}

	return nil
}

func (k *TeamRepository) CheckTeamExists(ctx context.Context, teamName string) (bool, error) {
	adminToken, err := k.adminService.GetAdminToken(ctx)
	if err != nil {
		return false, err
	}

	teams, err := keycloak.ListTeams(adminToken, consts.KeycloakHost, consts.KeycloakRealm)
	if err != nil {
		return false, err
	}

	for _, team := range teams {
		if team.Name == teamName {
			return true, nil
		}
	}

	return false, nil
}
