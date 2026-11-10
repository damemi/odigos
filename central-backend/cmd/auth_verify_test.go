package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/odigos-io/central-backend/internal/keycloak"
	"github.com/odigos-io/central-backend/internal/logger"
	"github.com/odigos-io/central-backend/internal/services"
	"github.com/odigos-io/central-backend/internal/utils"
	"github.com/odigos-io/central-backend/internal/utils/tokentest"
)

// A forged token naming a real admin's sub must not be enriched with that
// admin's live role: verification fails first, so Keycloak is never asked.
func TestAuthContextMiddlewareVerifiesTokens(t *testing.T) {
	logger.InitLogger()
	gin.SetMode(gin.TestMode)
	realm := tokentest.Install(t)

	const adminID, viewerID = "real-admin", "real-viewer"
	t.Cleanup(func() { utils.InvalidateAuthCaches(adminID); utils.InvalidateAuthCaches(viewerID) })
	liveRoles := map[string]string{adminID: keycloak.RoleAdmin, viewerID: keycloak.RoleViewer}

	var roleLookups atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /realms/master/protocol/openid-connect/token", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "admin-token", "expires_in": 60})
	})
	mux.HandleFunc("GET /admin/realms/master/users/{id}/role-mappings/realm", func(w http.ResponseWriter, r *http.Request) {
		roleLookups.Add(1)
		_ = json.NewEncoder(w).Encode([]map[string]any{{"name": liveRoles[r.PathValue("id")]}})
	})
	mux.HandleFunc("GET /admin/realms/master/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": r.PathValue("id"), "notBefore": 0})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Setenv("KEYCLOAK_HOST", server.URL)

	adminService := services.NewAdminService(&services.AdminCredentials{Username: "admin", Password: "admin"})
	router := gin.New()
	var got error
	router.POST("/graphql", authContextMiddleware(adminService), func(c *gin.Context) {
		got = utils.RequireAdmin(c.Request.Context())
	})
	call := func(authorization string) error {
		got = errors.New("handler did not run")
		req := httptest.NewRequest(http.MethodPost, "/graphql", nil)
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		router.ServeHTTP(httptest.NewRecorder(), req)
		return got
	}

	b64 := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	attackerKey := tokentest.NewKey(t)

	rejected := map[string]string{
		"no token":                         "",
		"not bearer":                       "Basic " + realm.Token(t, adminID, "admin"),
		"alg none with known admin sub":    "Bearer " + b64(map[string]any{"alg": "none", "kid": "key-1"}) + "." + b64(realm.Claims(adminID, "admin")) + ".",
		"forged with known admin sub":      "Bearer " + tokentest.SignWith(t, attackerKey, "key-1", realm.Claims(adminID, "admin")),
		"forged with unknown sub and role": "Bearer " + tokentest.SignWith(t, attackerKey, "key-1", realm.Claims("nobody", "admin")),
	}
	for name, authorization := range rejected {
		t.Run(name, func(t *testing.T) {
			if err := call(authorization); !errors.Is(err, utils.ErrUnauthenticated) {
				t.Fatalf("RequireAdmin() = %v, want ErrUnauthenticated", err)
			}
		})
	}
	if n := roleLookups.Load(); n != 0 {
		t.Fatalf("Keycloak role lookups for unverified tokens = %d, want 0", n)
	}

	t.Run("valid admin token", func(t *testing.T) {
		if err := call("Bearer " + realm.Token(t, adminID, "admin")); err != nil {
			t.Fatalf("RequireAdmin() = %v, want nil", err)
		}
	})
	t.Run("valid viewer token claiming admin uses live role", func(t *testing.T) {
		if err := call("Bearer " + realm.Token(t, viewerID, "admin")); !errors.Is(err, utils.ErrForbidden) {
			t.Fatalf("RequireAdmin() = %v, want ErrForbidden", err)
		}
	})
}
