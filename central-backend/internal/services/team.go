package services

import (
	"context"
	"fmt"

	"github.com/odigos-io/central-backend/graph/model"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
)

type TeamService struct {
	redisClient *redisclient.RedisClient
	teamRepo    TeamRepository
	userRepo    UserRepository
}

func NewTeamService(redisClient *redisclient.RedisClient, teamRepo TeamRepository, userRepo UserRepository) *TeamService {
	return &TeamService{
		redisClient: redisClient,
		teamRepo:    teamRepo,
		userRepo:    userRepo,
	}
}

func (s *TeamService) CreateTeam(ctx context.Context, input *model.WriteTeamInput) (*model.GeneralResult, error) {
	name := input.Name

	if name == nil {
		errorMsg := "Team name is required"
		return &model.GeneralResult{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}

	teamExists, err := s.teamRepo.CheckTeamExists(ctx, *name)
	if err != nil {
		errorMsg := "Failed to validate team name: " + err.Error()
		return &model.GeneralResult{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}
	if teamExists {
		errorMsg := fmt.Sprintf("Team with name '%s' already exists", *name)
		return &model.GeneralResult{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}

	createdTeam, err := s.teamRepo.CreateTeam(ctx, input)
	if err != nil {
		errorMsg := "Failed to create team: " + err.Error()
		return &model.GeneralResult{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}

	for _, uid := range input.AddUserIds {
		if uid != nil {
			err = s.userRepo.AssignTeamsToUser(ctx, *uid, []string{createdTeam.ID})
			if err != nil {
				return nil, err
			}
		}
	}
	for _, uid := range input.RemoveUserIds {
		if uid != nil {
			err = s.userRepo.RemoveTeamsFromUser(ctx, *uid, []string{createdTeam.ID})
			if err != nil {
				return nil, err
			}
		}
	}

	for _, cpid := range input.AddComputePlatformIds {
		if cpid != nil {
			err = s.teamRepo.AssignComputePlatformsToTeam(ctx, createdTeam.ID, []string{*cpid})
			if err != nil {
				return nil, err
			}
		}
	}
	for _, cpid := range input.RemoveComputePlatformIds {
		if cpid != nil {
			err = s.teamRepo.RemoveComputePlatformsFromTeam(ctx, createdTeam.ID, []string{*cpid})
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

func (s *TeamService) UpdateTeam(ctx context.Context, teamID string, input *model.WriteTeamInput) (*model.GeneralResult, error) {
	name := input.Name

	if name == nil {
		errorMsg := "Team name is required"
		return &model.GeneralResult{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}

	// Check if the team being updated exists
	existingTeam, err := s.teamRepo.GetTeamByID(ctx, teamID)
	if err != nil {
		errorMsg := "Failed to validate team existence: " + err.Error()
		return &model.GeneralResult{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}
	if existingTeam == nil {
		errorMsg := fmt.Sprintf("Team with ID '%s' does not exist", teamID)
		return &model.GeneralResult{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}

	// Check if another team already has the new name
	if *name != existingTeam.Name {
		existingTeamWithNewName, err := s.teamRepo.GetTeamByName(ctx, *name)
		if err != nil {
			errorMsg := "Failed to validate team name: " + err.Error()
			return &model.GeneralResult{
				Success: false,
				Error:   &errorMsg,
			}, nil
		}
		if existingTeamWithNewName != nil {
			errorMsg := fmt.Sprintf("Team with name '%s' already exists", *name)
			return &model.GeneralResult{
				Success: false,
				Error:   &errorMsg,
			}, nil
		}
	}

	_, err = s.teamRepo.UpdateTeam(ctx, teamID, input)
	if err != nil {
		errorMsg := "Failed to update team: " + err.Error()
		return &model.GeneralResult{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}

	for _, uid := range input.AddUserIds {
		if uid != nil {
			err = s.userRepo.AssignTeamsToUser(ctx, *uid, []string{teamID})
			if err != nil {
				return nil, err
			}
		}
	}
	for _, uid := range input.RemoveUserIds {
		if uid != nil {
			err = s.userRepo.RemoveTeamsFromUser(ctx, *uid, []string{teamID})
			if err != nil {
				return nil, err
			}
		}
	}

	for _, cpid := range input.AddComputePlatformIds {
		if cpid != nil {
			err = s.teamRepo.AssignComputePlatformsToTeam(ctx, teamID, []string{*cpid})
			if err != nil {
				return nil, err
			}
		}
	}
	for _, cpid := range input.RemoveComputePlatformIds {
		if cpid != nil {
			err = s.teamRepo.RemoveComputePlatformsFromTeam(ctx, teamID, []string{*cpid})
			if err != nil {
				return nil, err
			}
		}
	}

	return &model.GeneralResult{
		Success: true,
	}, nil
}

func (s *TeamService) DeleteTeam(ctx context.Context, teamID string) (*model.GeneralResult, error) {
	return s.teamRepo.DeleteTeam(ctx, teamID)
}

func (s *TeamService) GetTeams(ctx context.Context) ([]*model.Team, error) {
	return s.teamRepo.GetTeams(ctx)
}

func (s *TeamService) GetUsersForTeam(ctx context.Context, teamID string) ([]*model.User, error) {
	return s.teamRepo.GetUsersForTeam(ctx, teamID)
}

func (s *TeamService) AssignComputePlatformsToTeam(ctx context.Context, teamID string, computePlatformIDs []string) error {
	return s.teamRepo.AssignComputePlatformsToTeam(ctx, teamID, computePlatformIDs)
}

func (s *TeamService) RemoveComputePlatformsFromTeam(ctx context.Context, teamID string, computePlatformIDs []string) error {
	return s.teamRepo.RemoveComputePlatformsFromTeam(ctx, teamID, computePlatformIDs)
}

func (s *TeamService) GetComputePlatformsForTeam(ctx context.Context, teamID string) ([]*model.ComputePlatform, error) {
	return s.teamRepo.GetComputePlatformsForTeam(ctx, teamID)
}
