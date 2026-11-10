package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/odigos-io/odigos/central-backend/internal/keycloak"
	"github.com/odigos-io/odigos/central-backend/internal/services"
	"github.com/odigos-io/odigos/central-backend/internal/utils"
)

// Opening a cluster fires ~12 GraphQL requests at once. With a cold cache they
// must share one admin grant and one role lookup instead of hitting Keycloak 12 times.
func TestEnrichAuthContextCollapsesConcurrentLookups(t *testing.T) {
	const userID = "burst-user"
	t.Cleanup(func() { utils.InvalidateAuthCaches(userID) })

	var grants, roleLookups, userLookups atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /realms/master/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		grants.Add(1)
		time.Sleep(50 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "admin-token", "expires_in": 60})
	})
	mux.HandleFunc("GET /admin/realms/master/users/"+userID+"/role-mappings/realm", func(w http.ResponseWriter, r *http.Request) {
		roleLookups.Add(1)
		_ = json.NewEncoder(w).Encode([]map[string]any{{"name": keycloak.RoleAdmin}})
	})
	mux.HandleFunc("GET /admin/realms/master/users/"+userID, func(w http.ResponseWriter, r *http.Request) {
		userLookups.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": userID, "notBefore": 0})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Setenv("KEYCLOAK_HOST", server.URL)

	adminService := services.NewAdminService(&services.AdminCredentials{Username: "admin", Password: "admin"})
	kcUser := keycloak.NewKeycloakUserService()

	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := enrichAuthContext(adminService, kcUser, userID, context.Background())
			if role, ok := utils.GetLiveRoleFromContext(ctx); !ok || role != keycloak.RoleAdmin {
				t.Errorf("live role = %q, %v; want %q", role, ok, keycloak.RoleAdmin)
			}
		}()
	}
	wg.Wait()

	if got := grants.Load(); got != 1 {
		t.Errorf("admin grants = %d, want 1", got)
	}
	if got := roleLookups.Load(); got != 1 {
		t.Errorf("role lookups = %d, want 1", got)
	}
	if got := userLookups.Load(); got != 1 {
		t.Errorf("not-before lookups = %d, want 1", got)
	}
}
