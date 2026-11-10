package keycloak

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeleteUserByID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Fatalf("expected DELETE, got %s", r.Method)
		}
		if r.URL.Path != "/admin/realms/odigos/users/user-1" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	kc := &KeycloakUserService{host: server.URL, realm: "odigos"}
	if err := kc.DeleteUserByID("token", "user-1"); err != nil {
		t.Fatalf("DeleteUserByID returned error: %v", err)
	}
}
