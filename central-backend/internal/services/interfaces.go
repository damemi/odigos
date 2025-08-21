package services

import (
	"context"

	"github.com/odigos-io/central-backend/graph/model"
)

type AuthResult struct {
	AccessToken string
	UserData    map[string]any
}

type AuthRepository interface {
	AuthenticateUser(ctx context.Context, email, password string) (*AuthResult, error)
	GetAdminTokenForInitialPassword(ctx context.Context) (string, error)
	Logout(ctx context.Context, token string) error
}

type UserRepository interface {
	CheckUserExists(ctx context.Context, email string) error
	CreateUser(ctx context.Context, input *model.WriteUserInput) (*model.CreateUserResult, error)
	UpdateUser(ctx context.Context, userID string, input *model.WriteUserInput) (*model.GeneralResult, error)
	DeleteUser(ctx context.Context, userID string) (*model.GeneralResult, error)
	GetUsers(ctx context.Context) ([]*model.User, error)
	GetUserByID(ctx context.Context, userID string) (*model.User, error)
	GetUserByEmail(ctx context.Context, email string) (*model.User, error)

	CheckUserNeedsPasswordChange(ctx context.Context, email string) (bool, error)
	UpdateUserPassword(ctx context.Context, email string, password string) error
	SetUserPasswordRequirement(ctx context.Context, email string, required bool) error
	GetUserRole(ctx context.Context, userID string) (string, error)

	AssignTeamsToUser(ctx context.Context, userID string, teamIDs []string) error
	RemoveTeamsFromUser(ctx context.Context, userID string, teamIDs []string) error
	GetTeamsForUser(ctx context.Context, userID string) ([]*model.Team, error)

	AssignComputePlatformsToUser(ctx context.Context, userID string, computePlatformIDs []string) error
	RemoveComputePlatformsFromUser(ctx context.Context, userID string, computePlatformIDs []string) error
	GetComputePlatformsForUser(ctx context.Context, userID string) ([]*model.ComputePlatform, error)
}

type TeamRepository interface {
	CheckTeamExists(ctx context.Context, teamNameOrTeamID string) (bool, error)
	CreateTeam(ctx context.Context, input *model.WriteTeamInput) (*model.Team, error)
	UpdateTeam(ctx context.Context, teamID string, input *model.WriteTeamInput) (*model.Team, error)
	DeleteTeam(ctx context.Context, teamID string) (*model.GeneralResult, error)
	GetTeams(ctx context.Context) ([]*model.Team, error)
	GetTeamByID(ctx context.Context, teamID string) (*model.Team, error)
	GetTeamByName(ctx context.Context, teamName string) (*model.Team, error)

	GetUsersForTeam(ctx context.Context, teamID string) ([]*model.User, error)

	GetTeamsByUserID(ctx context.Context, userID string) ([]*model.Team, error)

	AssignComputePlatformsToTeam(ctx context.Context, teamID string, computePlatformIDs []string) error
	RemoveComputePlatformsFromTeam(ctx context.Context, teamID string, computePlatformIDs []string) error
	GetComputePlatformsForTeam(ctx context.Context, teamID string) ([]*model.ComputePlatform, error)
}
