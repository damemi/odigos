package consts

import (
	"os"
	"strconv"
)

const (
	UpdatePasswordAction = "UPDATE_PASSWORD"
)

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

// Default: 43200 seconds (12 hours)
func GetAccessTokenLifespanSeconds() int {
	if s := os.Getenv("KEYCLOAK_ACCESS_TOKEN_LIFESPAN"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			return v
		}
	}
	return 43200
}

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
