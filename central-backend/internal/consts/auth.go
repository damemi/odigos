package consts

import (
	"os"
	"slices"
	"strconv"
	"strings"
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

// GetTokenSigningAlgorithms returns the JWT algorithms accepted on Keycloak
// access tokens: KEYCLOAK_TOKEN_SIGNING_ALGORITHMS (comma-separated RSA
// algorithms) or Keycloak's realm default, RS256. Unsupported values are dropped.
func GetTokenSigningAlgorithms() []string {
	supported := []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512"}
	var algs []string
	for _, alg := range strings.Split(os.Getenv("KEYCLOAK_TOKEN_SIGNING_ALGORITHMS"), ",") {
		if alg = strings.TrimSpace(alg); slices.Contains(supported, alg) && !slices.Contains(algs, alg) {
			algs = append(algs, alg)
		}
	}
	if len(algs) == 0 {
		return []string{"RS256"}
	}
	return algs
}

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
