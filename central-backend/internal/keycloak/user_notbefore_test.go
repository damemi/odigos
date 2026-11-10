package keycloak

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSetUserNotBefore(t *testing.T) {
	var putBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/admin/realms/odigos/users/user-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":        "user-1",
				"username":  "alice",
				"email":     "alice@example.com",
				"enabled":   true,
				"notBefore": 0,
			})
		case r.Method == http.MethodPut && r.URL.Path == "/admin/realms/odigos/users/user-1":
			if err := json.NewDecoder(r.Body).Decode(&putBody); err != nil {
				t.Fatalf("decode put body: %v", err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	kc := &KeycloakUserService{host: server.URL, realm: "odigos"}
	if err := kc.SetUserNotBefore("token", "user-1", 1_700_000_000); err != nil {
		t.Fatalf("SetUserNotBefore returned error: %v", err)
	}
	if got, ok := putBody["notBefore"].(float64); !ok || int64(got) != 1_700_000_000 {
		t.Fatalf("put notBefore = %v, want 1700000000", putBody["notBefore"])
	}
	if putBody["email"] != "alice@example.com" {
		t.Fatalf("expected full user payload to be preserved, got email=%v", putBody["email"])
	}
}

func TestGetUserNotBefore(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/realms/odigos/users/user-1" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":        "user-1",
			"notBefore": float64(1_700_000_123),
		})
	}))
	defer server.Close()

	kc := &KeycloakUserService{host: server.URL, realm: "odigos"}
	got, err := kc.GetUserNotBefore("token", "user-1")
	if err != nil {
		t.Fatalf("GetUserNotBefore returned error: %v", err)
	}
	if got != 1_700_000_123 {
		t.Fatalf("GetUserNotBefore() = %d, want 1700000123", got)
	}
}
