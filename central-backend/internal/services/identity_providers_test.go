package services

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/odigos-io/odigos/central-backend/graph/model"
	"github.com/odigos-io/odigos/central-backend/internal/keycloak"
	"github.com/odigos-io/odigos/central-backend/internal/logger"
)

func TestIdentityProvidersFromRepresentations(t *testing.T) {
	t.Setenv("CENTRAL_BACKEND_HOST", "https://central.example.com")
	got := identityProvidersFromRepresentations([]keycloak.IdentityProviderRepresentation{
		{Alias: "azure", ProviderID: "oidc", Enabled: true, Config: map[string]string{
			"clientId":              "client-1",
			"clientSecret":          keycloak.SecretMask,
			"metadataDescriptorUrl": "https://login.example.com/tenant-1/.well-known/openid-configuration",
			"odigosDiscoveryUrl":    "https://login.example.com/{tenantid}/.well-known/openid-configuration",
			"odigosTenantId":        "tenant-1",
		}},
		{Alias: "okta", ProviderID: "saml", Enabled: true, Config: map[string]string{
			"entityId":               "http://www.okta.com/exk1",
			"singleSignOnServiceUrl": "https://acme.okta.com/app/sso/saml",
			"signingCertificate":     "MIIC",
		}},
		{Alias: "github", ProviderID: "oidc"},
		{Alias: "google", ProviderID: "keycloak-oidc"},
		{Alias: "AZURE", ProviderID: "oidc"},
	})

	if len(got) != 2 {
		t.Fatalf("got %d providers, want 2: %+v", len(got), got)
	}
	azure, okta := got[0], got[1]
	if azure.ProviderID != model.AuthProviderIDAzure || azure.Protocol != model.IdentityProviderProtocolOidc {
		t.Fatalf("unexpected azure provider %+v", azure)
	}
	if *azure.ClientID != "client-1" || *azure.TenantID != "tenant-1" || !strings.Contains(*azure.DiscoveryURL, "{tenantid}") || !azure.HasClientSecret {
		t.Fatalf("unexpected azure settings %+v", azure)
	}
	if azure.CallbackURL != "https://central.example.com/realms/master/broker/azure/endpoint" {
		t.Fatalf("unexpected callback URL %q", azure.CallbackURL)
	}
	if okta.Protocol != model.IdentityProviderProtocolSaml || *okta.IdpEntityID != "http://www.okta.com/exk1" || !okta.HasSigningCertificate || okta.ClientID != nil {
		t.Fatalf("unexpected okta provider %+v", okta)
	}

	// Providers set up before the entered values were stored fall back to the resolved URL.
	legacy := identityProvidersFromRepresentations([]keycloak.IdentityProviderRepresentation{
		{Alias: "azure", ProviderID: "oidc", Config: map[string]string{"metadataDescriptorUrl": "https://resolved"}},
	})
	if *legacy[0].DiscoveryURL != "https://resolved" || legacy[0].TenantID != nil || legacy[0].HasClientSecret {
		t.Fatalf("unexpected legacy provider %+v", legacy[0])
	}
}

// idpKeycloak serves the given identity providers and records the body of every
// write to an identity provider instance.
type idpKeycloak struct {
	mu     sync.Mutex
	writes map[string]map[string]any
}

func newIdpKeycloak(t *testing.T, existing []keycloak.IdentityProviderRepresentation) *idpKeycloak {
	t.Helper()
	logger.InitLogger()
	fake := &idpKeycloak{writes: map[string]map[string]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /realms/master/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "admin-token", "expires_in": 60})
	})
	mux.HandleFunc("GET /admin/realms/master/identity-provider/instances", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(existing)
	})
	record := func(w http.ResponseWriter, r *http.Request, status int) {
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		fake.mu.Lock()
		fake.writes[r.Method+" "+r.URL.Path] = body
		fake.mu.Unlock()
		w.WriteHeader(status)
	}
	mux.HandleFunc("POST /admin/realms/master/identity-provider/instances", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, http.StatusCreated)
	})
	mux.HandleFunc("PUT /admin/realms/master/identity-provider/instances/{alias}", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, http.StatusNoContent)
	})
	// Attribute mappers and the OIDC discovery document are not under test.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if strings.HasSuffix(r.URL.Path, "/mappers") {
				_, _ = w.Write([]byte("[]"))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Setenv("KEYCLOAK_HOST", server.URL)
	return fake
}

func (f *idpKeycloak) config(t *testing.T, key string) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	body, ok := f.writes[key]
	if !ok {
		t.Fatalf("no write %q; writes: %v", key, f.writes)
	}
	config, _ := body["config"].(map[string]any)
	return config
}

func (f *idpKeycloak) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writes)
}

const testDiscoveryURL = "http://127.0.0.1:1/.well-known/openid-configuration"

func oidcUpdate(secret string) *model.OidcIdentityProviderInput {
	return &model.OidcIdentityProviderInput{
		ProviderID:   model.AuthProviderIDAzure,
		ClientID:     "client-1",
		ClientSecret: secret,
		DiscoveryURL: testDiscoveryURL,
		TenantID:     "tenant-1",
	}
}

func storedOidc(enabled bool, secret string) []keycloak.IdentityProviderRepresentation {
	return []keycloak.IdentityProviderRepresentation{{Alias: "azure", ProviderID: "oidc", Enabled: enabled, Config: map[string]string{
		"clientId":              "client-1",
		"clientSecret":          secret,
		"metadataDescriptorUrl": testDiscoveryURL,
		"odigosTenantId":        "tenant-1",
	}}}
}

const azureInstance = "PUT /admin/realms/master/identity-provider/instances/azure"

func TestEnsureOidcIdentityProviderSecret(t *testing.T) {
	newSvc := func() *AdminService { return NewAdminService(&AdminCredentials{Username: "admin", Password: "admin"}) }

	t.Run("blank secret keeps the stored one when the app is unchanged", func(t *testing.T) {
		fake := newIdpKeycloak(t, storedOidc(true, keycloak.SecretMask))
		if err := newSvc().EnsureOidcIdentityProvider(oidcUpdate("")); err != nil {
			t.Fatalf("EnsureOidcIdentityProvider() error = %v", err)
		}
		config := fake.config(t, azureInstance)
		if config["clientSecret"] != keycloak.SecretMask || config["clientId"] != "client-1" || config["odigosTenantId"] != "tenant-1" {
			t.Fatalf("unexpected update config %v", config)
		}
	})

	// Keycloak sends the client secret to the token endpoint found through the discovery
	// URL, so a blank secret must not survive a change that points the provider elsewhere.
	for name, change := range map[string]func(*model.OidcIdentityProviderInput){
		"discovery URL": func(in *model.OidcIdentityProviderInput) {
			in.DiscoveryURL = "https://attacker.example.com/.well-known/openid-configuration"
		},
		"client ID": func(in *model.OidcIdentityProviderInput) { in.ClientID = "client-2" },
		"tenant ID": func(in *model.OidcIdentityProviderInput) { in.TenantID = "tenant-2" },
	} {
		t.Run("blank secret rejected when the "+name+" changes", func(t *testing.T) {
			fake := newIdpKeycloak(t, storedOidc(true, keycloak.SecretMask))
			for _, secret := range []string{"", keycloak.SecretMask} {
				input := oidcUpdate(secret)
				change(input)
				if err := newSvc().EnsureOidcIdentityProvider(input); err == nil || !strings.Contains(err.Error(), "client secret is required") {
					t.Fatalf("expected a client secret error for secret %q, got %v", secret, err)
				}
			}
			if n := fake.writeCount(); n != 0 {
				t.Fatalf("rejected call still wrote %d times", n)
			}
		})
	}

	t.Run("blank secret rejected when none is stored", func(t *testing.T) {
		fake := newIdpKeycloak(t, storedOidc(true, ""))
		if err := newSvc().EnsureOidcIdentityProvider(oidcUpdate("")); err == nil {
			t.Fatal("expected an error")
		}
		if n := fake.writeCount(); n != 0 {
			t.Fatalf("rejected call still wrote %d times", n)
		}
	})

	t.Run("new secret replaces the stored one, even with a new client ID", func(t *testing.T) {
		fake := newIdpKeycloak(t, storedOidc(true, keycloak.SecretMask))
		input := oidcUpdate("rotated")
		input.ClientID = "client-2"
		if err := newSvc().EnsureOidcIdentityProvider(input); err != nil {
			t.Fatalf("EnsureOidcIdentityProvider() error = %v", err)
		}
		if got := fake.config(t, azureInstance)["clientSecret"]; got != "rotated" {
			t.Fatalf("clientSecret = %v, want rotated", got)
		}
	})

	t.Run("a disabled provider stays disabled", func(t *testing.T) {
		fake := newIdpKeycloak(t, storedOidc(false, keycloak.SecretMask))
		if err := newSvc().EnsureOidcIdentityProvider(oidcUpdate("")); err != nil {
			t.Fatalf("EnsureOidcIdentityProvider() error = %v", err)
		}
		fake.mu.Lock()
		enabled := fake.writes[azureInstance]["enabled"]
		fake.mu.Unlock()
		if enabled != false {
			t.Fatalf("enabled = %v, want false", enabled)
		}
	})

	t.Run("blank or masked secret rejected on create", func(t *testing.T) {
		for _, secret := range []string{"", keycloak.SecretMask} {
			fake := newIdpKeycloak(t, nil)
			if err := newSvc().EnsureOidcIdentityProvider(oidcUpdate(secret)); err == nil {
				t.Fatalf("expected an error for secret %q", secret)
			}
			if n := fake.writeCount(); n != 0 {
				t.Fatalf("rejected create still wrote %d times", n)
			}
		}
	})
}

func TestEnsureSamlIdentityProviderCertificate(t *testing.T) {
	input := func(cert string) *model.SamlIdentityProviderInput {
		return &model.SamlIdentityProviderInput{
			ProviderID:             model.AuthProviderIDOkta,
			IdpEntityID:            "http://www.okta.com/exk2",
			SingleSignOnServiceURL: "https://acme.okta.com/sso",
			SigningCertificate:     cert,
		}
	}

	t.Run("blank certificate keeps the stored one and the disabled state on update", func(t *testing.T) {
		fake := newIdpKeycloak(t, []keycloak.IdentityProviderRepresentation{{Alias: "okta", ProviderID: "saml", Enabled: false, Config: map[string]string{"signingCertificate": "MIIC-stored"}}})
		svc := NewAdminService(&AdminCredentials{Username: "admin", Password: "admin"})
		if err := svc.EnsureSamlIdentityProvider(input("")); err != nil {
			t.Fatalf("EnsureSamlIdentityProvider() error = %v", err)
		}
		config := fake.config(t, "PUT /admin/realms/master/identity-provider/instances/okta")
		if config["signingCertificate"] != "MIIC-stored" || config["entityId"] != "http://www.okta.com/exk2" {
			t.Fatalf("unexpected update config %v", config)
		}
		fake.mu.Lock()
		enabled := fake.writes["PUT /admin/realms/master/identity-provider/instances/okta"]["enabled"]
		fake.mu.Unlock()
		if enabled != false {
			t.Fatalf("a disabled provider was re-enabled: enabled = %v", enabled)
		}
	})

	t.Run("blank certificate rejected on create", func(t *testing.T) {
		fake := newIdpKeycloak(t, nil)
		svc := NewAdminService(&AdminCredentials{Username: "admin", Password: "admin"})
		if err := svc.EnsureSamlIdentityProvider(input("")); err == nil {
			t.Fatal("expected an error")
		}
		if n := fake.writeCount(); n != 0 {
			t.Fatalf("rejected create still wrote %d times", n)
		}
	})
}

// Keycloak keeps a provider's type on update, so switching protocol would leave a
// broken provider; it must be rejected without writing.
func TestEnsureIdentityProviderRejectsProtocolChange(t *testing.T) {
	t.Run("OIDC over SAML", func(t *testing.T) {
		fake := newIdpKeycloak(t, []keycloak.IdentityProviderRepresentation{{Alias: "azure", ProviderID: "saml"}})
		svc := NewAdminService(&AdminCredentials{Username: "admin", Password: "admin"})
		err := svc.EnsureOidcIdentityProvider(oidcUpdate("secret"))
		if err == nil || !strings.Contains(err.Error(), "changing its protocol is not supported") {
			t.Fatalf("expected a protocol change error, got %v", err)
		}
		if n := fake.writeCount(); n != 0 {
			t.Fatalf("rejected call still wrote %d times", n)
		}
	})

	t.Run("SAML over OIDC", func(t *testing.T) {
		fake := newIdpKeycloak(t, []keycloak.IdentityProviderRepresentation{{Alias: "okta", ProviderID: "oidc"}})
		svc := NewAdminService(&AdminCredentials{Username: "admin", Password: "admin"})
		err := svc.EnsureSamlIdentityProvider(&model.SamlIdentityProviderInput{ProviderID: model.AuthProviderIDOkta, IdpEntityID: "e", SingleSignOnServiceURL: "https://sso", SigningCertificate: "MIIC"})
		if err == nil || !strings.Contains(err.Error(), "changing its protocol is not supported") {
			t.Fatalf("expected a protocol change error, got %v", err)
		}
		if n := fake.writeCount(); n != 0 {
			t.Fatalf("rejected call still wrote %d times", n)
		}
	})
}
