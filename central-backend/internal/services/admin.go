package services

import (
	"context"
	"fmt"

	"github.com/odigos-io/central-backend/internal/keycloak"
	"github.com/odigos-io/central-backend/internal/logger"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
	"github.com/odigos-io/central-backend/internal/utils"
)

type AdminService struct {
	redisClient      *redisclient.RedisClient
	adminCredentials *AdminCredentials
	kcAdmin          *keycloak.KeycloakAdminService
}

func NewAdminService(redisClient *redisclient.RedisClient, adminCredentials *AdminCredentials) *AdminService {
	return &AdminService{
		redisClient:      redisClient,
		adminCredentials: adminCredentials,
		kcAdmin:          keycloak.NewKeycloakAdminService(),
	}
}

func (svc *AdminService) GetAdminToken() (string, error) {
	return svc.kcAdmin.GetAdminToken(svc.adminCredentials.Username, svc.adminCredentials.Password)
}

func (svc *AdminService) GetToken(ctx context.Context) (string, error) {
	isInitialized, err := svc.redisClient.GetSystemInitialized(ctx)
	if err != nil {
		logger.Log.Error("Failed to get system initialization status from cache", "error", err)
		isInitialized = false
	}

	if !isInitialized {
		return svc.GetAdminToken()
	}
	if token := utils.GetTokenFromContext(ctx); token != "" {
		if expired, _ := utils.IsTokenExpired(token); expired {
			return "", fmt.Errorf("authentication expired")
		}
		_, err := svc.ValidateAdminPermissions(token)
		if err == nil {
			return token, nil
		}
	}
	return "", fmt.Errorf("admin permissions required")
}

func (svc *AdminService) ValidateAdminPermissions(userToken string) (string, error) {
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
