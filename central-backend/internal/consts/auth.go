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

// GetCentralBackendHost returns the browser-accessible central-backend URL.
// Browser-facing Keycloak URLs (sign-in, sign-out) are routed through the backend's
// reverse proxy, so they use the backend host instead of the Keycloak host directly.
func GetCentralBackendHost() string {
	if host := os.Getenv("CENTRAL_BACKEND_HOST"); host != "" {
		return host
	}
	return "http://localhost:8081"
}

func GetCentralUIHost() string {
	if host := os.Getenv("CENTRAL_UI_HOST"); host != "" {
		if backendHost := os.Getenv("CENTRAL_BACKEND_HOST"); host == backendHost {
			if uiHost := uiHostForSharedExternalURL(host); uiHost != "" {
				return uiHost
			}
		}
		return host
	}
	return "http://localhost:3000"
}

// uiHostForSharedExternalURL returns the Central UI URL when CENTRAL_UI_HOST was
// set to the same value as CENTRAL_BACKEND_HOST (the Keycloak proxy URL).
// Production uses one browser origin for both services; local port-forward uses
// backend on :8081 and UI on :3000.
func uiHostForSharedExternalURL(shared string) string {
	switch shared {
	case "http://localhost:8081", "http://127.0.0.1:8081":
		return "http://localhost:3000"
	default:
		return ""
	}
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
	DefaultKeycloakOidcSecretName  = "keycloak-oidc-credentials"
	OidcProviderKey                = "provider"
	OidcClientIdKey                = "client-id"
	OidcClientSecretKey            = "client-secret"
	OidcDiscoveryUrlKey            = "discovery-url"
	OidcTenantIdKey                = "tenant-id"
)

const (
	DefaultAdminUsername = "admin"
	DefaultAdminPassword = "admin"
)
