package consts

const (
	UpdatePasswordAction = "UPDATE_PASSWORD"
)

const (
	KeycloakHost  = "http://localhost:8080"
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
