package services

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/odigos-io/central-backend/graph/model"
	"github.com/odigos-io/central-backend/internal/keycloak"
	"github.com/odigos-io/central-backend/internal/logger"
)

// signInKeycloak fakes the Keycloak endpoints SignIn hits for an already-initialized
// install. role is the SSO/password user's realm role (empty means no Odigos role).
// When mappings is true an Azure IdP has a group→role mapper, so an unmapped SSO
// user must be denied rather than defaulted to Viewer.
type signInKeycloak struct {
	role     string
	mappings bool

	mu           sync.Mutex
	logouts      int
	assignedRole string
}

func newSignInKeycloak(t *testing.T, role string, mappings bool) *signInKeycloak {
	t.Helper()
	logger.InitLogger()
	fake := &signInKeycloak{role: role, mappings: mappings}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /realms/master/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form := string(body)
		if strings.Contains(form, "grant_type=authorization_code") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "sso-access",
				"id_token":      "sso-id",
				"refresh_token": "sso-refresh",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "user-access",
			"expires_in":   60,
		})
	})
	mux.HandleFunc("GET /realms/master/protocol/openid-connect/userinfo", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sub":   "sso-1",
			"email": "sso@example.com",
		})
	})
	mux.HandleFunc("GET /admin/realms/master/users", func(w http.ResponseWriter, r *http.Request) {
		if email := r.URL.Query().Get("email"); email != "" {
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id": "sso-1", "username": email, "email": email,
			}})
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "bootstrap", "username": "admin"},
			{"id": "owner-1", "username": "owner@example.com"},
			{"id": "sso-1", "username": "sso@example.com", "email": "sso@example.com"},
		})
	})
	mux.HandleFunc("GET /admin/realms/master/users/{id}/role-mappings/realm", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "owner-1" {
			_ = json.NewEncoder(w).Encode([]map[string]any{{"name": keycloak.RoleOwner}})
			return
		}
		if fake.role == "" {
			_ = json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"name": fake.role}})
	})
	mux.HandleFunc("POST /admin/realms/master/users/{id}/logout", func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.logouts++
		fake.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /admin/realms/master/identity-provider/instances", func(w http.ResponseWriter, r *http.Request) {
		if !fake.mappings {
			_ = json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"alias": "azure", "providerId": "oidc"}})
	})
	mux.HandleFunc("GET /admin/realms/master/identity-provider/instances/{alias}/mappers", func(w http.ResponseWriter, r *http.Request) {
		if !fake.mappings {
			_ = json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id":                     "m1",
			"name":                   "odigos-role-groups-admin",
			"identityProviderMapper": "oidc-advanced-role-idp-mapper",
			"config": map[string]string{
				"role":   keycloak.RoleAdmin,
				"claims": `[{"key":"groups","value":"(^\\Qadmins\\E$)"}]`,
			},
		}})
	})
	mux.HandleFunc("GET /admin/realms/master/roles/{name}", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "role-" + r.PathValue("name"), "name": r.PathValue("name")})
	})
	mux.HandleFunc("POST /admin/realms/master/users/{id}/role-mappings/realm", func(w http.ResponseWriter, r *http.Request) {
		var roles []map[string]any
		_ = json.NewDecoder(r.Body).Decode(&roles)
		if len(roles) > 0 {
			if name, ok := roles[0]["name"].(string); ok {
				fake.mu.Lock()
				fake.assignedRole = name
				fake.mu.Unlock()
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected Keycloak call %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Setenv("KEYCLOAK_HOST", server.URL)
	return fake
}

func (f *signInKeycloak) logoutCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logouts
}

func (f *signInKeycloak) assigned() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.assignedRole
}

func newSignInAuth(t *testing.T) *AuthService {
	t.Helper()
	admin := NewAdminService(&AdminCredentials{Username: "admin", Password: "admin"})
	return NewAuthService(admin, NewUserService(admin), NewRoleMappingService(admin))
}

func TestSignInSSODeniesNoAccessRole(t *testing.T) {
	fake := newSignInKeycloak(t, keycloak.RoleNoAccess, false)
	got, err := newSignInAuth(t).SignIn(context.Background(), "", "", "sso-code")
	if err != nil {
		t.Fatalf("SignIn() error: %v", err)
	}
	if got.Status != model.SignInStatusAccessDenied {
		t.Fatalf("status = %v, want ACCESS_DENIED", got.Status)
	}
	if got.AccessToken != nil {
		t.Fatalf("accessToken = %v, want nil", *got.AccessToken)
	}
	if got.IDToken == nil || *got.IDToken != "sso-id" {
		t.Fatalf("idToken = %v, want sso-id for deferred IdP logout", got.IDToken)
	}
	if fake.logoutCount() != 1 {
		t.Fatalf("logouts = %d, want 1", fake.logoutCount())
	}
	if fake.assigned() != "" {
		t.Fatalf("assigned role %q; no-access users must not be rewritten", fake.assigned())
	}
}

func TestSignInSSODeniesUnmappedUserWhenMappingsExist(t *testing.T) {
	fake := newSignInKeycloak(t, "", true)
	got, err := newSignInAuth(t).SignIn(context.Background(), "", "", "sso-code")
	if err != nil {
		t.Fatalf("SignIn() error: %v", err)
	}
	if got.Status != model.SignInStatusAccessDenied {
		t.Fatalf("status = %v, want ACCESS_DENIED", got.Status)
	}
	if got.AccessToken != nil {
		t.Fatalf("accessToken = %v, want nil", *got.AccessToken)
	}
	if got.IDToken == nil || *got.IDToken != "sso-id" {
		t.Fatalf("idToken = %v, want sso-id for deferred IdP logout", got.IDToken)
	}
	if fake.logoutCount() != 1 {
		t.Fatalf("logouts = %d, want 1", fake.logoutCount())
	}
	if fake.assigned() != keycloak.RoleNoAccess {
		t.Fatalf("assigned role = %q, want %q so the next attempt stays denied", fake.assigned(), keycloak.RoleNoAccess)
	}
}

func TestSignInPasswordDeniesNoAccessRole(t *testing.T) {
	fake := newSignInKeycloak(t, keycloak.RoleNoAccess, false)
	got, err := newSignInAuth(t).SignIn(context.Background(), "sso@example.com", "password", "")
	if err != nil {
		t.Fatalf("SignIn() error: %v", err)
	}
	if got.Status != model.SignInStatusAccessDenied {
		t.Fatalf("status = %v, want ACCESS_DENIED", got.Status)
	}
	if got.AccessToken != nil {
		t.Fatalf("accessToken = %v, want nil", *got.AccessToken)
	}
	if got.IDToken != nil {
		t.Fatalf("idToken = %v, want nil on password deny", *got.IDToken)
	}
	if fake.logoutCount() != 1 {
		t.Fatalf("logouts = %d, want 1", fake.logoutCount())
	}
}
