package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/odigos-io/central-backend/internal/logger"
)

// firstRunKeycloak serves one real user with the given realm role next to the
// bootstrap admin, and fails the test on any write. An empty role makes the
// role lookup fail with a server error.
func firstRunKeycloak(t *testing.T, role string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /realms/master/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "admin-token", "expires_in": 60})
	})
	mux.HandleFunc("GET /admin/realms/master/users", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "bootstrap", "username": "admin"},
			{"id": "u1", "username": "first@example.com"},
		})
	})
	mux.HandleFunc("GET /admin/realms/master/users/u1/role-mappings/realm", func(w http.ResponseWriter, r *http.Request) {
		if role == "" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"name": role}})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected Keycloak call %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Setenv("KEYCLOAK_HOST", server.URL)
}

// The first SSO user is promoted to admin only while no admin-level user exists.
// An install whose only admin-level user is an OWNER, or whose roles cannot be
// read, must not promote anyone.
func TestNeedToSetAdminUserCountsOwners(t *testing.T) {
	logger.InitLogger()
	// "" is a failed role lookup: an unknown role may be an admin's, so no promotion.
	for role, want := range map[string]bool{"owner": false, "admin": false, "viewer": true, "": false} {
		t.Run("role="+role, func(t *testing.T) {
			firstRunKeycloak(t, role)
			svc := NewAdminService(&AdminCredentials{Username: "admin", Password: "admin"})
			if got := svc.NeedToSetAdminUser(); got != want {
				t.Fatalf("NeedToSetAdminUser() = %v, want %v", got, want)
			}
		})
	}
}

// When Keycloak cannot list users, password sign-in must not fall into
// first-run setup, which creates an admin from the submitted credentials.
func TestSignInFailsClosedWhenInitializationUnknown(t *testing.T) {
	logger.InitLogger()
	var writes []string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /realms/master/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "admin-token", "expires_in": 60})
	})
	mux.HandleFunc("GET /admin/realms/master/users", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes = append(writes, r.Method+" "+r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Setenv("KEYCLOAK_HOST", server.URL)

	admin := NewAdminService(&AdminCredentials{Username: "admin", Password: "admin"})
	auth := NewAuthService(admin, NewUserService(admin), NewRoleMappingService(admin))

	if _, err := admin.IsSystemInitialized(); err == nil {
		t.Fatal("IsSystemInitialized() returned no error when Keycloak failed")
	}
	if _, err := auth.SignIn(context.Background(), "attacker@example.com", "password", ""); err == nil {
		t.Fatal("SignIn() succeeded although initialization state was unknown")
	}
	if len(writes) != 0 {
		t.Fatalf("SignIn() wrote to Keycloak: %v", writes)
	}
}
