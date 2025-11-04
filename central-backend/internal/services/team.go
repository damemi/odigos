package services

import (
	"context"
	"fmt"

	"github.com/odigos-io/central-backend/graph/model"
	"github.com/odigos-io/central-backend/internal/keycloak"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
)

type TeamService struct {
	redisClient  *redisclient.RedisClient
	adminService *AdminService
	kcTeam       *keycloak.KeycloakTeamService
}

func NewTeamService(redisClient *redisclient.RedisClient, adminService *AdminService) *TeamService {
	return &TeamService{
		redisClient:  redisClient,
		adminService: adminService,
		kcTeam:       keycloak.NewKeycloakTeamService(),
	}
}

func (svc *TeamService) CheckTeamExists(teamNameOrTeamID string) (bool, error) {
	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return false, err
	}

	teams, err := svc.kcTeam.ListTeams(adminToken)
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

func (svc *TeamService) CreateTeam(input *model.WriteTeamInput) (*model.GeneralResult, error) {
	name := input.Name

	if name == nil {
		return nil, fmt.Errorf("team name is required")
	}

	teamExists, err := svc.CheckTeamExists(*name)
	if err != nil {
		return nil, fmt.Errorf("failed to validate team name: %w", err)
	}
	if teamExists {
		return nil, fmt.Errorf("team with name '%s' already exists", *name)
	}

	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return nil, fmt.Errorf("failed to get admin token: %w", err)
	}

	createdTeam, err := svc.kcTeam.CreateTeam(adminToken, *name, input.Description)
	if err != nil {
		return nil, fmt.Errorf("failed to create team: %w", err)
	}

	for _, uid := range input.AddUserIds {
		if uid != nil {
			err = svc.kcTeam.AssignTeamsToUser(adminToken, *uid, []string{createdTeam.ID})
			if err != nil {
				return nil, err
			}
		}
	}
	for _, uid := range input.RemoveUserIds {
		if uid != nil {
			err = svc.kcTeam.RemoveTeamsFromUser(adminToken, *uid, []string{createdTeam.ID})
			if err != nil {
				return nil, err
			}
		}
	}

	for _, cpid := range input.AddComputePlatformIds {
		if cpid != nil {
			err = svc.kcTeam.AssignComputePlatformsToTeam(adminToken, createdTeam.ID, []string{*cpid})
			if err != nil {
				return nil, err
			}
		}
	}
	for _, cpid := range input.RemoveComputePlatformIds {
		if cpid != nil {
			err = svc.kcTeam.RemoveComputePlatformsFromTeam(adminToken, createdTeam.ID, []string{*cpid})
			if err != nil {
				return nil, err
			}
		}
	}

	return &model.GeneralResult{
		Success: true,
		Error:   nil,
	}, nil
}

func (svc *TeamService) UpdateTeam(teamID string, input *model.WriteTeamInput) (*model.GeneralResult, error) {
	name := input.Name

	if name == nil {
		return nil, fmt.Errorf("team name is required")
	}

	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return nil, fmt.Errorf("failed to get admin token: %w", err)
	}

	existingTeam, err := svc.kcTeam.GetTeamByID(adminToken, teamID)
	if err != nil {
		return nil, fmt.Errorf("failed to validate team existence: %w", err)
	}
	if existingTeam == nil {
		return nil, fmt.Errorf("team with id '%s' does not exist", teamID)
	}

	// Check if another team already has the new name
	if *name != existingTeam.Name {
		existingTeamWithNewName, err := svc.kcTeam.GetTeamByName(adminToken, *name)
		if err != nil {
			return nil, fmt.Errorf("failed to validate team name: %w", err)
		}
		if existingTeamWithNewName != nil {
			return nil, fmt.Errorf("team with name '%s' already exists", *name)
		}
	}

	_, err = svc.kcTeam.UpdateTeam(adminToken, teamID, *name, input.Description)
	if err != nil {
		return nil, fmt.Errorf("failed to update team: %w", err)
	}

	for _, uid := range input.AddUserIds {
		if uid != nil {
			err = svc.kcTeam.AssignTeamsToUser(adminToken, *uid, []string{teamID})
			if err != nil {
				return nil, err
			}
		}
	}
	for _, uid := range input.RemoveUserIds {
		if uid != nil {
			err = svc.kcTeam.RemoveTeamsFromUser(adminToken, *uid, []string{teamID})
			if err != nil {
				return nil, err
			}
		}
	}

	for _, cpid := range input.AddComputePlatformIds {
		if cpid != nil {
			err = svc.kcTeam.AssignComputePlatformsToTeam(adminToken, teamID, []string{*cpid})
			if err != nil {
				return nil, err
			}
		}
	}
	for _, cpid := range input.RemoveComputePlatformIds {
		if cpid != nil {
			err = svc.kcTeam.RemoveComputePlatformsFromTeam(adminToken, teamID, []string{*cpid})
			if err != nil {
				return nil, err
			}
		}
	}

	return &model.GeneralResult{
		Success: true,
	}, nil
}

func (svc *TeamService) DeleteTeam(ctx context.Context, teamID string) (*model.GeneralResult, error) {
	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return nil, fmt.Errorf("failed to get admin token: %w", err)
	}

	err = svc.kcTeam.DeleteTeam(adminToken, teamID)
	if err != nil {
		return nil, fmt.Errorf("failed to delete team: %w", err)
	}

	return &model.GeneralResult{
		Success: true,
	}, nil
}

func (svc *TeamService) GetTeams(ctx context.Context) ([]*model.Team, error) {
	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return nil, fmt.Errorf("failed to get admin token: %w", err)
	}

	return svc.kcTeam.ListTeams(adminToken)
}

func (svc *TeamService) GetUsersForTeam(ctx context.Context, teamID string) ([]*model.User, error) {
	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return nil, fmt.Errorf("failed to get admin token: %w", err)
	}

	return svc.kcTeam.GetUsersForTeam(adminToken, teamID)
}

func (svc *TeamService) AssignComputePlatformsToTeam(ctx context.Context, teamID string, computePlatformIDs []string) error {
	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return err
	}

	return svc.kcTeam.AssignComputePlatformsToTeam(adminToken, teamID, computePlatformIDs)
}

func (svc *TeamService) RemoveComputePlatformsFromTeam(ctx context.Context, teamID string, computePlatformIDs []string) error {
	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return err
	}

	return svc.kcTeam.RemoveComputePlatformsFromTeam(adminToken, teamID, computePlatformIDs)
}

func (svc *TeamService) GetComputePlatformsForTeam(ctx context.Context, teamID string) ([]*model.ComputePlatform, error) {
	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return nil, fmt.Errorf("failed to get admin token: %w", err)
	}

	computePlatformIds, err := svc.kcTeam.GetComputePlatformIdsForTeam(adminToken, teamID)
	if err != nil {
		return nil, err
	}

	var computePlatformModels []*model.ComputePlatform
	for _, id := range computePlatformIds {
		computePlatformModels = append(computePlatformModels, &model.ComputePlatform{ID: id})
	}

	return computePlatformModels, nil
}
