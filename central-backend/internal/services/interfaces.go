package services

import (
	"context"

	"github.com/odigos-io/central-backend/graph/model"
)

type AuthResult struct {
	AccessToken string
	UserData    map[string]any
}


type CreateUserRequest struct {
	Email    string
	Password *string
	Role     model.Role
}

type CreateUserResult struct {
	Success           bool
	UserID            string
	GeneratedPassword *string
}

type DeleteUserResult struct {
	Success bool
	Error   string
}

type DeleteTeamResult struct {
	Success bool
	Error   string
}

type UserRepository interface {
	CreateUser(ctx context.Context, req CreateUserRequest) (*CreateUserResult, error)
	GetUsers(ctx context.Context) ([]*model.User, error)
	GetUserByEmail(ctx context.Context, email string) (*model.User, error)
	UpdateUserPassword(ctx context.Context, email, password string) error
	SetUserPasswordRequirement(ctx context.Context, email string, required bool) error
	CheckUserExists(ctx context.Context, email string) error
	AssignComputePlatformsToUser(ctx context.Context, input model.AssignComputePlatformsToUserInput) (*model.AssignComputePlatformsToUserPayload, error)
	CheckUserNeedsPasswordChange(ctx context.Context, email string) (bool, error)
	GetUserRole(ctx context.Context, userID string) (string, error)
	DeleteUser(ctx context.Context, userID string) (*DeleteUserResult, error)
}

type AuthRepository interface {
	AuthenticateUser(ctx context.Context, email, password string) (*AuthResult, error)
	GetAdminTokenForInitialPassword(ctx context.Context) (string, error)
}

type TeamRepository interface {
	CreateTeam(ctx context.Context, name string, description *string, users []*string, computePlatforms []*string) (*model.Team, error)
	GetTeams(ctx context.Context) ([]*model.Team, error)
	AssignTeamsToUser(ctx context.Context, userID string, teamIDs []string) error
	CheckTeamExists(ctx context.Context, teamName string) (bool, error)
	DeleteTeam(ctx context.Context, teamID string) (*DeleteTeamResult, error)
}
