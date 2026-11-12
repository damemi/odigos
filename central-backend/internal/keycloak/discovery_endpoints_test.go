package keycloak

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// Provider discovery URLs mirror central-ui AUTH_PROVIDERS defaults.
const (
	googleDiscoveryURL = "https://accounts.google.com/.well-known/openid-configuration"
	azureDiscoveryURL  = "https://login.microsoftonline.com/organizations/v2.0/.well-known/openid-configuration"
	oktaDiscoveryTmpl  = "https://{tenantid}.okta.com/.well-known/openid-configuration"
)

func TestResolveDiscoveryURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		discoveryURL string
		tenantID     string
		want         string
	}{
		{
			name:         "okta template",
			discoveryURL: oktaDiscoveryTmpl,
			tenantID:     "dev-12345",
			want:         "https://dev-12345.okta.com/.well-known/openid-configuration",
		},
		{
			name:         "google unchanged",
			discoveryURL: googleDiscoveryURL,
			tenantID:     "unused",
			want:         googleDiscoveryURL,
		},
		{
			name:         "azure unchanged",
			discoveryURL: azureDiscoveryURL,
			tenantID:     "tenant-guid",
			want:         azureDiscoveryURL,
		},
		{
			name:         "multiple placeholders",
			discoveryURL: "https://{tenantid}.example.com/{tenantid}/.well-known/openid-configuration",
			tenantID:     "acme",
			want:         "https://acme.example.com/acme/.well-known/openid-configuration",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := resolveDiscoveryURL(tt.discoveryURL, tt.tenantID)
			if got != tt.want {
				t.Fatalf("resolveDiscoveryURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetOIDCDiscoveryEndpoints_LiveProviders(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live discovery tests in short mode")
	}

	azureTenantID := "00000000-0000-0000-0000-000000000001"

	providers := []struct {
		name         string
		discoveryURL string
		tenantID     string
		wantIssuer   string
		// Okta org discovery requires a real tenant subdomain.
		skipWithoutEnv string
	}{
		{
			name:         "google",
			discoveryURL: googleDiscoveryURL,
			tenantID:     "n/a",
			wantIssuer:   "https://accounts.google.com",
		},
		{
			name:         "azure",
			discoveryURL: azureDiscoveryURL,
			tenantID:     azureTenantID,
			wantIssuer:   "https://login.microsoftonline.com/" + azureTenantID + "/v2.0",
		},
		{
			name:           "okta",
			discoveryURL:   oktaDiscoveryTmpl,
			tenantID:       os.Getenv("E2E_OKTA_TENANT_ID"),
			skipWithoutEnv: "E2E_OKTA_TENANT_ID",
		},
	}

	for _, p := range providers {
		t.Run(p.name, func(t *testing.T) {
			if p.skipWithoutEnv != "" && os.Getenv(p.skipWithoutEnv) == "" {
				t.Skipf("set %s to run live Okta discovery against https://{tenant}.okta.com", p.skipWithoutEnv)
			}

			resolved := resolveDiscoveryURL(p.discoveryURL, p.tenantID)
			cfg := &OidcIdentityProviderConfig{
				ProviderAlias: p.name,
				ClientID:      "e2e-client",
				ClientSecret:  "e2e-secret",
				DiscoveryURL:  p.discoveryURL,
				TenantID:      p.tenantID,
			}

			endpoints := getOIDCDiscoveryEndpoints(cfg, resolved)
			assertDiscoveryEndpointsPopulated(t, endpoints)

			if p.wantIssuer != "" && endpoints.Issuer != p.wantIssuer {
				t.Fatalf("issuer = %q, want %q", endpoints.Issuer, p.wantIssuer)
			}
			if strings.Contains(endpoints.Issuer, "{tenantid}") {
				t.Fatalf("issuer still contains unresolved placeholder: %q", endpoints.Issuer)
			}
		})
	}
}

func TestGetIDPConfig_PopulatesEndpointsFromLiveDiscovery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live discovery tests in short mode")
	}

	cases := []struct {
		name string
		cfg  *OidcIdentityProviderConfig
	}{
		{
			name: "google",
			cfg: &OidcIdentityProviderConfig{
				ProviderAlias: "google",
				ClientID:      "google-client",
				ClientSecret:  "google-secret",
				DiscoveryURL:  googleDiscoveryURL,
				TenantID:      "google-tenant",
			},
		},
		{
			name: "azure",
			cfg: &OidcIdentityProviderConfig{
				ProviderAlias: "azure",
				ClientID:      "azure-client",
				ClientSecret:  "azure-secret",
				DiscoveryURL:  azureDiscoveryURL,
				TenantID:      "11111111-2222-3333-4444-555555555555",
			},
		},
	}

	if oktaTenant := os.Getenv("E2E_OKTA_TENANT_ID"); oktaTenant != "" {
		cases = append(cases, struct {
			name string
			cfg  *OidcIdentityProviderConfig
		}{
			name: "okta",
			cfg: &OidcIdentityProviderConfig{
				ProviderAlias: "okta",
				ClientID:      "okta-client",
				ClientSecret:  "okta-secret",
				DiscoveryURL:  oktaDiscoveryTmpl,
				TenantID:      oktaTenant,
			},
		})
	} else {
		t.Log("E2E_OKTA_TENANT_ID unset — skipping live Okta getIDPConfig case")
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := getIDPConfig(tc.cfg)
			if err != nil {
				t.Fatalf("getIDPConfig() unexpected error: %v", err)
			}
			if payload.ProviderID != "oidc" {
				t.Fatalf("ProviderID = %q, want oidc", payload.ProviderID)
			}
			if payload.Alias != tc.cfg.ProviderAlias {
				t.Fatalf("Alias = %q, want %q", payload.Alias, tc.cfg.ProviderAlias)
			}

			required := []string{"authorizationUrl", "tokenUrl", "issuer", "jwksUrl", "metadataDescriptorUrl"}
			for _, key := range required {
				if strings.TrimSpace(payload.Config[key]) == "" {
					t.Fatalf("expected non-empty config %q after discovery, got %#v", key, payload.Config)
				}
			}
			if strings.Contains(payload.Config["issuer"], "{tenantid}") {
				t.Fatalf("issuer placeholder was not resolved: %q", payload.Config["issuer"])
			}
			if strings.Contains(payload.Config["metadataDescriptorUrl"], "{tenantid}") {
				t.Fatalf("metadataDescriptorUrl placeholder was not resolved: %q", payload.Config["metadataDescriptorUrl"])
			}
		})
	}
}

func TestGetOIDCDiscoveryEndpoints_OktaShapedDocument(t *testing.T) {
	t.Parallel()

	oktaDoc := map[string]string{
		"issuer":                 "https://dev-example.okta.com/oauth2/default",
		"authorization_endpoint": "https://dev-example.okta.com/oauth2/default/v1/authorize",
		"token_endpoint":         "https://dev-example.okta.com/oauth2/default/v1/token",
		"userinfo_endpoint":      "https://dev-example.okta.com/oauth2/default/v1/userinfo",
		"jwks_uri":               "https://dev-example.okta.com/oauth2/default/v1/keys",
		"end_session_endpoint":   "https://dev-example.okta.com/oauth2/default/v1/logout",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(oktaDoc)
	}))
	t.Cleanup(server.Close)

	cfg := &OidcIdentityProviderConfig{
		ProviderAlias: "okta",
		ClientID:      "okta-client",
		ClientSecret:  "okta-secret",
		DiscoveryURL:  server.URL + "/.well-known/openid-configuration",
		TenantID:      "dev-example",
	}

	endpoints := getOIDCDiscoveryEndpoints(cfg, cfg.DiscoveryURL)
	assertDiscoveryEndpointsPopulated(t, endpoints)

	if endpoints.Issuer != oktaDoc["issuer"] {
		t.Fatalf("issuer = %q, want %q", endpoints.Issuer, oktaDoc["issuer"])
	}
	if endpoints.LogoutURL != oktaDoc["end_session_endpoint"] {
		t.Fatalf("logoutUrl = %q, want %q", endpoints.LogoutURL, oktaDoc["end_session_endpoint"])
	}

	payload, err := getIDPConfig(cfg)
	if err != nil {
		t.Fatalf("getIDPConfig() unexpected error: %v", err)
	}
	if payload.Config["authorizationUrl"] != oktaDoc["authorization_endpoint"] {
		t.Fatalf("authorizationUrl not applied from discovery")
	}
}

func TestGetOIDCDiscoveryEndpoints_AzureIssuerPlaceholder(t *testing.T) {
	t.Parallel()

	tenantID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 "https://login.microsoftonline.com/{tenantid}/v2.0",
			"authorization_endpoint": "https://login.microsoftonline.com/organizations/oauth2/v2.0/authorize",
			"token_endpoint":         "https://login.microsoftonline.com/organizations/oauth2/v2.0/token",
			"userinfo_endpoint":      "https://graph.microsoft.com/oidc/userinfo",
			"jwks_uri":               "https://login.microsoftonline.com/organizations/discovery/v2.0/keys",
		})
	}))
	t.Cleanup(server.Close)

	cfg := &OidcIdentityProviderConfig{
		ProviderAlias: "azure",
		ClientID:      "azure-client",
		ClientSecret:  "azure-secret",
		DiscoveryURL:  server.URL,
		TenantID:      tenantID,
	}

	endpoints := getOIDCDiscoveryEndpoints(cfg, server.URL)
	wantIssuer := "https://login.microsoftonline.com/" + tenantID + "/v2.0"
	if endpoints.Issuer != wantIssuer {
		t.Fatalf("issuer = %q, want %q", endpoints.Issuer, wantIssuer)
	}
}

func assertDiscoveryEndpointsPopulated(t *testing.T, endpoints oidcDiscoveryEndpoints) {
	t.Helper()
	if endpoints.AuthorizationURL == "" {
		t.Fatal("expected authorization_endpoint from discovery")
	}
	if endpoints.TokenURL == "" {
		t.Fatal("expected token_endpoint from discovery")
	}
	if endpoints.Issuer == "" {
		t.Fatal("expected issuer from discovery")
	}
	if endpoints.JWKSURL == "" {
		t.Fatal("expected jwks_uri from discovery")
	}
}
