package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/odigos-io/central-backend/internal/logger"
	"github.com/odigos-io/central-backend/internal/utils"
	"github.com/odigos-io/central-backend/internal/utils/tokentest"
)

// fakeKeycloak serves the handful of admin endpoints DeleteUser walks through.
// deleted records whether the account was actually removed.
func fakeKeycloak(t *testing.T, userID, role string, deleted *bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("POST /realms/master/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "admin-token"})
	})
	mux.HandleFunc("GET /admin/realms/master/users/"+userID+"/role-mappings/realm", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"name": role}})
	})
	mux.HandleFunc("GET /admin/realms/master/users/"+userID, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":       userID,
			"username": "offboarded",
			"email":    "offboarded@example.com",
		})
	})
	mux.HandleFunc("PUT /admin/realms/master/users/"+userID, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /admin/realms/master/users/"+userID+"/logout", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /admin/realms/master/users/"+userID, func(w http.ResponseWriter, r *http.Request) {
		*deleted = true
		w.WriteHeader(http.StatusNoContent)
	})
	// Teams / compute platforms / password-change lookups are best-effort in
	// mapKeycloakUserToModel; a 404 exercises their tolerant paths.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func jwtFor(t *testing.T, userID string, issuedAt time.Time, roles ...string) string {
	t.Helper()
	realm := tokentest.Install(t)
	claims := realm.Claims(userID, roles...)
	claims["iat"] = issuedAt.Unix()
	claims["exp"] = issuedAt.Add(12 * time.Hour).Unix()
	return realm.Sign(t, claims)
}

// Deleting a user must also revoke their outstanding access tokens. Authorization
// here is offline: without a revoke, a deleted user's JWT keeps satisfying role
// checks from its own claims for the remaining token lifetime (12h by default).
func TestDeleteUserRevokesOutstandingTokens(t *testing.T) {
	logger.InitLogger()

	const userID = "offboarded-user"
	t.Cleanup(func() {
		utils.GlobalTokenRevocations.Clear(userID)
		utils.InvalidateAuthCaches(userID)
	})

	deleted := false
	server := fakeKeycloak(t, userID, "maintainer", &deleted)
	t.Setenv("KEYCLOAK_HOST", server.URL)

	svc := NewUserService(NewAdminService(&AdminCredentials{Username: "admin", Password: "admin"}))

	// Issued before the delete, as a real session's token would be.
	token := jwtFor(t, userID, time.Now().Add(-time.Hour), "maintainer")
	ctx := utils.SetTokenInContext(context.Background(), token)

	if err := utils.RequireAuth(ctx); err != nil {
		t.Fatalf("pre-delete RequireAuth() = %v, want nil", err)
	}
	if !utils.IsUserEditor(ctx) {
		t.Fatal("pre-delete: expected maintainer JWT to grant editor rights")
	}

	res, err := svc.DeleteUser(context.Background(), userID)
	if err != nil {
		t.Fatalf("DeleteUser() error = %v", err)
	}
	if !res.Success {
		t.Fatalf("DeleteUser() success = false, error = %v", res.Error)
	}
	if !deleted {
		t.Fatal("expected the Keycloak account to be deleted")
	}

	if !utils.GlobalTokenRevocations.IsRevoked(userID, time.Now().Add(-time.Hour).Unix()) {
		t.Fatal("expected the deleted user's outstanding tokens to be revoked")
	}
	if err := utils.RequireAuth(ctx); err != utils.ErrUnauthenticated {
		t.Fatalf("post-delete RequireAuth() = %v, want ErrUnauthenticated", err)
	}
	if utils.IsUserEditor(ctx) {
		t.Fatal("post-delete: deleted user must not keep editor rights from stale JWT claims")
	}
	if utils.IsUserAdmin(ctx) {
		t.Fatal("post-delete: deleted user must not keep admin rights from stale JWT claims")
	}
}

// Admins and owners are refused before any Keycloak mutation, so nothing is
// revoked for them.
func TestDeleteUserRefusesAdminWithoutRevoking(t *testing.T) {
	logger.InitLogger()

	const userID = "admin-user"
	t.Cleanup(func() {
		utils.GlobalTokenRevocations.Clear(userID)
		utils.InvalidateAuthCaches(userID)
	})

	deleted := false
	server := fakeKeycloak(t, userID, "admin", &deleted)
	t.Setenv("KEYCLOAK_HOST", server.URL)

	svc := NewUserService(NewAdminService(&AdminCredentials{Username: "admin", Password: "admin"}))

	res, err := svc.DeleteUser(context.Background(), userID)
	if err != nil {
		t.Fatalf("DeleteUser() error = %v", err)
	}
	if res.Success {
		t.Fatal("DeleteUser() should refuse to delete an admin")
	}
	if deleted {
		t.Fatal("admin account must not be deleted")
	}
	if utils.GlobalTokenRevocations.NotBefore(userID) != 0 {
		t.Fatal("a refused delete must not revoke the user's tokens")
	}
}
