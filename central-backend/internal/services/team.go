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
}

func NewTeamService(redisClient *redisclient.RedisClient, teamRepo TeamRepository) *TeamService {
	return &TeamService{
		redisClient: redisClient,
		teamRepo:    teamRepo,
	}
}

func (s *TeamService) CreateTeam(ctx context.Context, name string, description *string, users []*string, computePlatforms []*string) (*model.CreateTeamPayload, error) {
	if err := s.validateCreateTeamInput(name, description); err != nil {
		errorMsg := err.Error()
		return &model.CreateTeamPayload{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}

	exists, err := s.teamRepo.CheckTeamExists(ctx, name)
	if err != nil {
		errorMsg := "Failed to validate team name: " + err.Error()
		return &model.CreateTeamPayload{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}

	if exists {
		errorMsg := fmt.Sprintf("Team with name '%s' already exists", name)
		return &model.CreateTeamPayload{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}

	createdTeam, err := s.teamRepo.CreateTeam(ctx, name, description, users, computePlatforms)
	if err != nil {
		errorMsg := "Failed to create team: " + err.Error()
		return &model.CreateTeamPayload{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}

	return &model.CreateTeamPayload{
		Success: true,
		Team:    createdTeam,
	}, nil
}

func (s *TeamService) validateCreateTeamInput(name string, description *string) error {
	if name == "" {
		return fmt.Errorf("team name is required")
	}

	if len(name) < 2 {
		return fmt.Errorf("team name must be at least 2 characters long")
	}

	if len(name) > 50 {
		return fmt.Errorf("team name must be at most 50 characters long")
	}

	if description != nil && len(*description) > 500 {
		return fmt.Errorf("team description must be at most 500 characters long")
	}

	return nil
}

func (s *TeamService) GetTeams(ctx context.Context) ([]*model.Team, error) {
	return s.teamRepo.GetTeams(ctx)
}

func (s *TeamService) AssignTeamsToUser(ctx context.Context, input model.AssignTeamsToUserInput) (*model.AssignTeamsToUserPayload, error) {
	if err := s.validateAssignTeamsInput(input); err != nil {
		errorMsg := err.Error()
		return &model.AssignTeamsToUserPayload{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}

	for _, teamID := range input.TeamIDs {
		exists, err := s.teamRepo.CheckTeamExists(ctx, teamID)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, fmt.Errorf("team %s does not exist", teamID)
		}
	}

	err := s.teamRepo.AssignTeamsToUser(ctx, input.UserID, input.TeamIDs)
	if err != nil {
		errorMsg := "Failed to assign teams: " + err.Error()
		return &model.AssignTeamsToUserPayload{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}

	return &model.AssignTeamsToUserPayload{
		Success: true,
	}, nil
}

func (s *TeamService) validateAssignTeamsInput(input model.AssignTeamsToUserInput) error {
	if input.UserID == "" {
		return fmt.Errorf("user ID is required")
	}

	if len(input.TeamIDs) == 0 {
		return fmt.Errorf("at least one team ID is required")
	}

	for i, teamID := range input.TeamIDs {
		if teamID == "" {
			return fmt.Errorf("team ID at index %d is empty", i)
		}
	}

	return nil
}

func (s *TeamService) DeleteTeam(ctx context.Context, teamID string) (*DeleteTeamResult, error) {
	return s.teamRepo.DeleteTeam(ctx, teamID)
}
