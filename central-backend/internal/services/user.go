package services

import (
	"context"

	"github.com/odigos-io/central-backend/graph/model"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
)

type UserService struct {
	redisClient *redisclient.RedisClient
	userRepo    UserRepository
	teamRepo    TeamRepository
}

func NewUserService(redisClient *redisclient.RedisClient, userRepo UserRepository, teamRepo TeamRepository) *UserService {
	return &UserService{
		redisClient: redisClient,
		userRepo:    userRepo,
		teamRepo:    teamRepo,
	}
}

func (s *UserService) CreateUser(ctx context.Context, input *model.WriteUserInput) (*model.CreateUserResult, error) {
	user, err := s.userRepo.CreateUser(ctx, input)
	if err != nil {
		return nil, err
	}
	userID := user.UserID

	teamIDs := make([]string, 0)
	for _, teamID := range input.Teams {
		if teamID != nil {
			teamIDs = append(teamIDs, *teamID)
		}
	}
	err = s.teamRepo.AssignTeamsToUser(ctx, userID, teamIDs)
	if err != nil {
		return nil, err
	}

	// TODO: assign compute platforms to user
	// computePlatformIDs := make([]string, len(input.ComputePlatforms))
	// for i, computePlatformID := range input.ComputePlatforms {
	// 	computePlatformIDs[i] = *computePlatformID
	// }
	// _, err = s.userRepo.AssignComputePlatformsToUser(ctx, userID, computePlatformIDs)
	// if err != nil {
	// 	return nil, err
	// }

	return user, nil
}

func (s *UserService) UpdateUser(ctx context.Context, userID string, input *model.WriteUserInput) (*model.GeneralResult, error) {
	user, err := s.userRepo.UpdateUser(ctx, userID, input)
	if err != nil {
		return nil, err
	}

	// remove user from old teams
	oldTeams, err := s.teamRepo.GetTeamsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	oldTeamIDs := make([]string, len(oldTeams))
	for i, team := range oldTeams {
		oldTeamIDs[i] = team.ID
	}
	err = s.teamRepo.RemoveTeamsFromUser(ctx, userID, oldTeamIDs)
	if err != nil {
		return nil, err
	}

	// add user to new teams
	newTeamIDs := make([]string, 0)
	for _, teamID := range input.Teams {
		if teamID != nil {
			newTeamIDs = append(newTeamIDs, *teamID)
		}
	}
	err = s.teamRepo.AssignTeamsToUser(ctx, userID, newTeamIDs)
	if err != nil {
		return nil, err
	}

	// TODO: assign compute platforms to user
	// computePlatformIDs := make([]string, len(input.ComputePlatforms))
	// for i, computePlatformID := range input.ComputePlatforms {
	// 	computePlatformIDs[i] = *computePlatformID
	// }
	// _, err = s.userRepo.AssignComputePlatformsToUser(ctx, userID, computePlatformIDs)
	// if err != nil {
	// 	return nil, err
	// }

	return user, nil
}

func (s *UserService) DeleteUser(ctx context.Context, userID string) (*model.GeneralResult, error) {
	return s.userRepo.DeleteUser(ctx, userID)
}

func (s *UserService) GetUsers(ctx context.Context) ([]*model.User, error) {
	return s.userRepo.GetUsers(ctx)
}

func (s *UserService) GetUserByID(ctx context.Context, userID string) (*model.User, error) {
	return s.userRepo.GetUserByID(ctx, userID)
}
