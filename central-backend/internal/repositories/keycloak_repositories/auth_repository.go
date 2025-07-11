package keycloak_repositories

import (
	"context"

	"github.com/odigos-io/central-backend/internal/consts"
	"github.com/odigos-io/central-backend/internal/keycloak"
	"github.com/odigos-io/central-backend/internal/services"
)

type AuthRepository struct {
	keycloakAuthService *keycloak.AuthService
	credentialsService  *services.CredentialsService
}

func NewAuthRepository(credentialsService *services.CredentialsService) *AuthRepository {
	return &AuthRepository{
		keycloakAuthService: keycloak.NewAuthService(),
		credentialsService:  credentialsService,
	}
}

func (k *AuthRepository) AuthenticateUser(ctx context.Context, email, password string) (*services.AuthResult, error) {

	credentials, err := k.credentialsService.GetAdminCredentials(ctx)
	if err != nil {
		return nil, err
	}

	authResult, err := k.keycloakAuthService.AuthenticateUser(email, password, credentials.Username, credentials.Password)
	if err != nil {
		return nil, err
	}

	return &services.AuthResult{
		AccessToken: authResult.AccessToken,
		UserData:    authResult.UserData,
	}, nil
}

func (k *AuthRepository) GetAdminTokenForInitialPassword(ctx context.Context) (string, error) {
	credentials, err := k.credentialsService.GetAdminCredentials(ctx)
	if err != nil {
		return "", err
	}

	adminToken, err := keycloak.GetKeycloakAccessToken(consts.KeycloakHost, credentials.Username, credentials.Password)
	if err != nil {
		return "", err
	}
	return adminToken, nil
}
