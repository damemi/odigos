package keycloak

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOidcIdentityProviderConfigToConfigMap(t *testing.T) {
	cfg := oidcIdentityProviderConfig{
		ClientAuthMethod:      "client_secret_post",
		ClientID:              "my-client",
		ClientSecret:          "my-secret",
		UseDiscovery:          "true",
		MetadataDescriptorURL: "https://login.example.com/.well-known/openid-configuration",
		SyncMode:              "FORCE",
		ValidateSignature:     "true",
		UseJwksURL:            "true",
		DefaultScope:          "openid email profile",
		AuthorizationURL:      "https://login.example.com/oauth2/v2.0/authorize",
		TokenURL:              "https://login.example.com/oauth2/v2.0/token",
	}

	got, err := cfg.ToConfigMap()
	if err != nil {
		t.Fatalf("ToConfigMap() unexpected error: %v", err)
	}

	wantKeys := map[string]string{
		"clientAuthMethod":      "client_secret_post",
		"clientId":              "my-client",
		"clientSecret":          "my-secret",
		"useDiscovery":          "true",
		"metadataDescriptorUrl": "https://login.example.com/.well-known/openid-configuration",
		"syncMode":              "FORCE",
		"validateSignature":     "true",
		"useJwksUrl":            "true",
		"defaultScope":          "openid email profile",
		"authorizationUrl":      "https://login.example.com/oauth2/v2.0/authorize",
		"tokenUrl":              "https://login.example.com/oauth2/v2.0/token",
	}

	for k, v := range wantKeys {
		if got[k] != v {
			t.Fatalf("ToConfigMap()[%q] = %q, want %q", k, got[k], v)
		}
	}

	if _, ok := got["logoutUrl"]; ok {
		t.Fatalf("ToConfigMap() unexpectedly included empty optional key logoutUrl")
	}
}

func TestGetIDPConfigDefaults(t *testing.T) {
	payload, err := getIDPConfig(&OidcIdentityProviderConfig{
		ProviderAlias: "azure",
		ClientID:      "client",
		ClientSecret:  "secret",
		DiscoveryURL:  "",
		TenantID:      "tenant",
	})
	if err != nil {
		t.Fatalf("getIDPConfig() unexpected error: %v", err)
	}

	if payload.ProviderID != "oidc" {
		t.Fatalf("ProviderID = %q, want oidc", payload.ProviderID)
	}
	if payload.Alias != "azure" {
		t.Fatalf("Alias = %q, want azure", payload.Alias)
	}
	if payload.Config["defaultScope"] != "openid email profile" {
		t.Fatalf("defaultScope = %q, want default scopes", payload.Config["defaultScope"])
	}
	if payload.Config["clientAuthMethod"] != "client_secret_post" {
		t.Fatalf("clientAuthMethod = %q", payload.Config["clientAuthMethod"])
	}
}

func TestGetOIDCDiscoveryEndpointsTypedDecode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"authorization_endpoint": "https://login.example.com/authorize",
			"token_endpoint":         "https://login.example.com/token",
			"userinfo_endpoint":      "https://login.example.com/userinfo",
			"jwks_uri":               "https://login.example.com/jwks",
			"issuer":                 "https://login.example.com/{tenantid}/v2.0",
			"end_session_endpoint":   "https://login.example.com/logout",
		})
	}))
	defer server.Close()

	endpoints := getOIDCDiscoveryEndpoints(&OidcIdentityProviderConfig{
		DiscoveryURL: server.URL,
		TenantID:     "tenant-123",
	}, server.URL)

	if endpoints.AuthorizationURL != "https://login.example.com/authorize" {
		t.Fatalf("AuthorizationURL = %q", endpoints.AuthorizationURL)
	}
	if endpoints.TokenURL != "https://login.example.com/token" {
		t.Fatalf("TokenURL = %q", endpoints.TokenURL)
	}
	if endpoints.UserInfoURL != "https://login.example.com/userinfo" {
		t.Fatalf("UserInfoURL = %q", endpoints.UserInfoURL)
	}
	if endpoints.JWKSURL != "https://login.example.com/jwks" {
		t.Fatalf("JWKSURL = %q", endpoints.JWKSURL)
	}
	if endpoints.Issuer != "https://login.example.com/tenant-123/v2.0" {
		t.Fatalf("Issuer = %q, want tenant substituted", endpoints.Issuer)
	}
	if endpoints.LogoutURL != "https://login.example.com/logout" {
		t.Fatalf("LogoutURL = %q", endpoints.LogoutURL)
	}
}

func TestApplyDiscoveryEndpoints(t *testing.T) {
	cfg := oidcIdentityProviderConfig{ClientID: "keep-me"}
	cfg.ApplyDiscoveryEndpoints(oidcDiscoveryEndpoints{
		AuthorizationURL: "https://a",
		TokenURL:         "https://t",
	})

	if cfg.ClientID != "keep-me" {
		t.Fatalf("ClientID mutated: %q", cfg.ClientID)
	}
	if cfg.AuthorizationURL != "https://a" || cfg.TokenURL != "https://t" {
		t.Fatalf("discovery endpoints not applied: %+v", cfg)
	}
}
