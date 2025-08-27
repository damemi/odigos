package services

import (
	"context"
	"fmt"

	"github.com/odigos-io/central-backend/internal/logger"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
	"github.com/odigos-io/central-backend/internal/utils"
)

type AdminService struct {
	redisClient *redisclient.RedisClient
	authRepo    AuthRepository
}

func NewAdminService(redisClient *redisclient.RedisClient, authRepo AuthRepository) *AdminService {
	return &AdminService{
		redisClient: redisClient,
		authRepo:    authRepo,
	}
}

func (a *AdminService) GetAdminTokenFromKeycloak(ctx context.Context) (string, error) {
	adminToken, err := a.authRepo.GetAdminToken(ctx)
	if err != nil {
		return "", err
	}
	return adminToken, nil
}

func (a *AdminService) GetToken(ctx context.Context) (string, error) {
	isInitialized, err := a.redisClient.GetSystemInitialized(ctx)
	if err != nil {
		logger.Log.Error("Failed to get system initialization status from cache", "error", err)
		isInitialized = false
	}

	if !isInitialized {
		return a.authRepo.GetAdminToken(ctx)
	}
	if token := utils.GetTokenFromContext(ctx); token != "" {
		if expired, _ := utils.IsTokenExpired(token); expired {
			return "", fmt.Errorf("authentication expired")
		}
		_, err := a.ValidateAdminPermissions(token)
		if err == nil {
			return token, nil
		}
	}
	return "", fmt.Errorf("admin permissions required")
}

func (a *AdminService) ValidateAdminPermissions(userToken string) (string, error) {
	roles, err := utils.ExtractRolesFromToken(userToken)
	if err != nil {
		return "", fmt.Errorf("failed to extract roles from token: %w", err)
	}

	hasAdminRole := false
	for _, role := range roles {
		if role == "admin" {
			hasAdminRole = true
			break
		}
	}

	if !hasAdminRole {
		return "", fmt.Errorf("user does not have admin permissions")
	}

	email, err := utils.ExtractEmailFromToken(userToken)
	if err != nil {
		return "", fmt.Errorf("failed to extract email from token: %w", err)
	}

	return email, nil
}
