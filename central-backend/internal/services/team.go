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

	exists, err := s.teamRepo.CheckTeamExists(ctx, *name)
	if err != nil {
		errorMsg := "Failed to validate team name: " + err.Error()
		return &model.GeneralResult{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}
	if exists {
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

	for _, uid := range input.Users {
		if uid != nil {
			err = s.teamRepo.AssignTeamsToUser(ctx, *uid, []string{createdTeam.ID})
			if err != nil {
				return nil, fmt.Errorf("failed to assign team to user: %w", err)
			}
		}
	}

	// TODO: assign computePlatforms to team? currently assigns to users of the team...
	// for _, cpid := range input.ComputePlatforms {
	// 	for _, uid := range input.Users {
	// 		_, err = s.userRepo.AssignComputePlatformsToUser(ctx, *uid, []string{*cpid})
	// 		if err != nil {
	// 			return nil, fmt.Errorf("failed to assign compute platform to user: %w", err)
	// 		}
	// 	}
	// }

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
	teamExists, err := s.teamRepo.CheckTeamExists(ctx, teamID)
	if err != nil {
		errorMsg := "Failed to validate team existence: " + err.Error()
		return &model.GeneralResult{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}
	if !teamExists {
		errorMsg := fmt.Sprintf("Team with ID '%s' does not exist", teamID)
		return &model.GeneralResult{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}

	// Check if another team already has the new name
	nameExists, err := s.teamRepo.CheckTeamExists(ctx, *name)
	if err != nil {
		errorMsg := "Failed to validate team name: " + err.Error()
		return &model.GeneralResult{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}
	if nameExists {
		errorMsg := fmt.Sprintf("Team with name '%s' already exists", *name)
		return &model.GeneralResult{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}

	_, err = s.teamRepo.UpdateTeam(ctx, teamID, input)
	if err != nil {
		errorMsg := "Failed to update team: " + err.Error()
		return &model.GeneralResult{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}

	users, err := s.teamRepo.GetUsersForTeam(ctx, teamID)
	if err != nil {
		errorMsg := "Failed to get users for team: " + err.Error()
		return &model.GeneralResult{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}

	// remove old users from team
	for _, user := range users {
		err = s.teamRepo.RemoveTeamsFromUser(ctx, user.ID, []string{teamID})
		if err != nil {
			return nil, fmt.Errorf("failed to remove team from user: %w", err)
		}
	}

	// add new users to team
	for _, userID := range input.Users {
		if userID != nil {
			err = s.teamRepo.AssignTeamsToUser(ctx, *userID, []string{teamID})
			if err != nil {
				return nil, fmt.Errorf("failed to add user to team: %w", err)
			}
		}
	}

	// TODO: computePlatforms?

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
	exists, err := s.teamRepo.CheckTeamExists(ctx, teamID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("team %s does not exist", teamID)
	}

	return s.teamRepo.GetUsersForTeam(ctx, teamID)
}

func (s *TeamService) GetTeamsForUser(ctx context.Context, userID string) ([]*model.Team, error) {
	return s.teamRepo.GetTeamsForUser(ctx, userID)
}

func (s *TeamService) AssignTeamsToUser(ctx context.Context, userID string, teamIDs []string) (*model.GeneralResult, error) {
	for _, teamID := range teamIDs {
		exists, err := s.teamRepo.CheckTeamExists(ctx, teamID)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, fmt.Errorf("team %s does not exist", teamID)
		}
	}

	err := s.teamRepo.AssignTeamsToUser(ctx, userID, teamIDs)
	if err != nil {
		errorMsg := "Failed to assign teams: " + err.Error()
		return &model.GeneralResult{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}

	return &model.GeneralResult{
		Success: true,
	}, nil
}

func (s *TeamService) RemoveTeamsFromUser(ctx context.Context, userID string, teamIDs []string) (*model.GeneralResult, error) {
	err := s.teamRepo.RemoveTeamsFromUser(ctx, userID, teamIDs)
	if err != nil {
		errorMsg := "Failed to remove teams: " + err.Error()
		return &model.GeneralResult{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}

	return &model.GeneralResult{
		Success: true,
	}, nil
}
