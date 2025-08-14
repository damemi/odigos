package services

import (
	"context"

	"github.com/odigos-io/central-backend/graph/model"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
	"github.com/odigos-io/central-backend/internal/utils"
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

	if len(input.AddTeamIds) > 0 {
		err = s.userRepo.AssignTeamsToUser(ctx, userID, utils.ConvertStringPtrsToStrings(input.AddTeamIds))
		if err != nil {
			return nil, err
		}
	}
	if len(input.RemoveTeamIds) > 0 {
		err = s.userRepo.RemoveTeamsFromUser(ctx, userID, utils.ConvertStringPtrsToStrings(input.RemoveTeamIds))
		if err != nil {
			return nil, err
		}
	}

	if len(input.AddComputePlatformIds) > 0 {
		err = s.userRepo.AssignComputePlatformsToUser(ctx, userID, utils.ConvertStringPtrsToStrings(input.AddComputePlatformIds))
		if err != nil {
			return nil, err
		}
	}
	if len(input.RemoveComputePlatformIds) > 0 {
		err = s.userRepo.RemoveComputePlatformsFromUser(ctx, userID, utils.ConvertStringPtrsToStrings(input.RemoveComputePlatformIds))
		if err != nil {
			return nil, err
		}
	}

	return user, nil
}

func (s *UserService) UpdateUser(ctx context.Context, userID string, input *model.WriteUserInput) (*model.GeneralResult, error) {
	user, err := s.userRepo.UpdateUser(ctx, userID, input)
	if err != nil {
		return nil, err
	}

	if len(input.AddTeamIds) > 0 {
		err = s.userRepo.AssignTeamsToUser(ctx, userID, utils.ConvertStringPtrsToStrings(input.AddTeamIds))
		if err != nil {
			return nil, err
		}
	}
	if len(input.RemoveTeamIds) > 0 {
		err = s.userRepo.RemoveTeamsFromUser(ctx, userID, utils.ConvertStringPtrsToStrings(input.RemoveTeamIds))
		if err != nil {
			return nil, err
		}
	}

	if len(input.AddComputePlatformIds) > 0 {
		err = s.userRepo.AssignComputePlatformsToUser(ctx, userID, utils.ConvertStringPtrsToStrings(input.AddComputePlatformIds))
		if err != nil {
			return nil, err
		}
	}
	if len(input.RemoveComputePlatformIds) > 0 {
		err = s.userRepo.RemoveComputePlatformsFromUser(ctx, userID, utils.ConvertStringPtrsToStrings(input.RemoveComputePlatformIds))
		if err != nil {
			return nil, err
		}
	}

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

func (s *UserService) AssignTeamsToUser(ctx context.Context, userID string, teamIDs []string) (*model.GeneralResult, error) {
	err := s.userRepo.AssignTeamsToUser(ctx, userID, teamIDs)
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

func (s *UserService) RemoveTeamsFromUser(ctx context.Context, userID string, teamIDs []string) (*model.GeneralResult, error) {
	err := s.userRepo.RemoveTeamsFromUser(ctx, userID, teamIDs)
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

func (s *UserService) GetTeamsForUser(ctx context.Context, userID string) ([]*model.Team, error) {
	return s.userRepo.GetTeamsForUser(ctx, userID)
}

func (s *UserService) AssignComputePlatformsToUser(ctx context.Context, userID string, computePlatformIDs []string) error {
	return s.userRepo.AssignComputePlatformsToUser(ctx, userID, computePlatformIDs)
}

func (s *UserService) RemoveComputePlatformsFromUser(ctx context.Context, userID string, computePlatformIDs []string) error {
	return s.userRepo.RemoveComputePlatformsFromUser(ctx, userID, computePlatformIDs)
}

func (s *UserService) GetComputePlatformsForUser(ctx context.Context, userID string) ([]*model.ComputePlatform, error) {
	return s.userRepo.GetComputePlatformsForUser(ctx, userID)
}
