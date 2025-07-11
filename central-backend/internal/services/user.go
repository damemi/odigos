package services

import (
	"context"
	"fmt"

	"github.com/odigos-io/central-backend/graph/model"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
)

type UserService struct {
	redisClient *redisclient.RedisClient
	userRepo    UserRepository
	authRepo    AuthRepository
}

func NewUserService(redisClient *redisclient.RedisClient, userRepo UserRepository, authRepo AuthRepository) *UserService {
	return &UserService{
		redisClient: redisClient,
		userRepo:    userRepo,
		authRepo:    authRepo,
	}
}

type CreateUserRequest struct {
	Email    string
	Password *string
	Role     model.Role
}

type CreateUserResult struct {
	Success           bool
	GeneratedPassword *string
}

type DeleteUserResult struct {
	Success bool
	Error   string
}

func (s *UserService) CreateUser(ctx context.Context, req CreateUserRequest) (*CreateUserResult, error) {
	return s.userRepo.CreateUser(ctx, req)
}

func (s *UserService) GetUsers(ctx context.Context) ([]*model.User, error) {
	return s.userRepo.GetUsers(ctx)
}

func (s *UserService) AssignComputePlatformsToUser(ctx context.Context, input model.AssignComputePlatformsToUserInput) (*model.AssignComputePlatformsToUserPayload, error) {
	if err := s.validateAssignComputePlatformsInput(input); err != nil {
		errorMsg := err.Error()
		return &model.AssignComputePlatformsToUserPayload{
			Success: false,
			Error:   &errorMsg,
		}, nil
	}

	return s.userRepo.AssignComputePlatformsToUser(ctx, input)
}

func (s *UserService) validateAssignComputePlatformsInput(input model.AssignComputePlatformsToUserInput) error {
	if input.UserID == "" {
		return fmt.Errorf("user ID is required")
	}

	if len(input.ComputePlatformIDs) == 0 {
		return fmt.Errorf("at least one compute platform ID is required")
	}

	for i, platformID := range input.ComputePlatformIDs {
		if platformID == "" {
			return fmt.Errorf("compute platform ID at index %d is empty", i)
		}
	}

	return nil
}

func (s *UserService) DeleteUser(ctx context.Context, userID string) (*DeleteUserResult, error) {
	return s.userRepo.DeleteUser(ctx, userID)
}
