package consts

import (
	"os"
)

const (
	UpdatePasswordAction = "UPDATE_PASSWORD"
)

// GetKeycloakHost returns the Keycloak host from environment variable or defaults to localhost
func GetKeycloakHost() string {
	if host := os.Getenv("KEYCLOAK_HOST"); host != "" {
		return host
	}
	return "http://localhost:8080"
}

const (
	KeycloakRealm = "master"
	ClientID      = "odigos-central"
)

const (
	DefaultKeycloakSecretNamespace = "odigos-central"
	DefaultKeycloakSecretName      = "keycloak-admin-credentials"
	AdminUsernameKey               = "admin-username"
	AdminPasswordKey               = "admin-password"
)

const (
	DefaultAdminUsername = "admin"
	DefaultAdminPassword = "admin"
)

const (
	SystemInitializedKey = "system:initialized"
)

const (
	SystemInitializedTTL = 3600
)
