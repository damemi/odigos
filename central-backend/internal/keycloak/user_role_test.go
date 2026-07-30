package keycloak

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetUserRoleReturnsEmptyWhenNoOdigosRole(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"name": "offline_access"},
			{"name": "uma_authorization"},
			{"name": "default-roles-odigos"},
		})
	}))
	defer server.Close()

	kc := &KeycloakUserService{host: server.URL, realm: "odigos"}
	role, err := kc.GetUserRole("token", "user-1")
	if err != nil {
		t.Fatalf("GetUserRole returned error: %v", err)
	}
	if role != "" {
		t.Fatalf("GetUserRole() = %q, want empty string", role)
	}
}

func TestGetUserRolePrecedence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"name": "viewer"},
			{"name": "admin"},
			{"name": "maintainer"},
		})
	}))
	defer server.Close()

	kc := &KeycloakUserService{host: server.URL, realm: "odigos"}
	role, err := kc.GetUserRole("token", "user-1")
	if err != nil {
		t.Fatalf("GetUserRole returned error: %v", err)
	}
	if role != RoleAdmin {
		t.Fatalf("GetUserRole() = %q, want %q", role, RoleAdmin)
	}
}

func TestGetUserRoleReturnsNoAccessRole(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"name": "no-access"},
		})
	}))
	defer server.Close()

	kc := &KeycloakUserService{host: server.URL, realm: "odigos"}
	role, err := kc.GetUserRole("token", "user-1")
	if err != nil {
		t.Fatalf("GetUserRole returned error: %v", err)
	}
	if role != RoleNoAccess {
		t.Fatalf("GetUserRole() = %q, want %q", role, RoleNoAccess)
	}
}

func TestGetUserRoleNonOKStatusReturnsEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	kc := &KeycloakUserService{host: server.URL, realm: "odigos"}
	role, err := kc.GetUserRole("token", "user-1")
	if err != nil {
		t.Fatalf("GetUserRole returned error: %v", err)
	}
	if role != "" {
		t.Fatalf("GetUserRole() = %q, want empty string", role)
	}
}
