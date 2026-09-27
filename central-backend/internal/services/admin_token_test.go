package services

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fakeTokenEndpoint(t *testing.T, expiresIn int, grants *atomic.Int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /realms/master/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		grants.Add(1)
		// Keep the grant slow enough that concurrent callers overlap.
		time.Sleep(50 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "admin-token", "expires_in": expiresIn})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// A page load fires many GraphQL requests at once; they must share one password grant.
func TestGetAdminTokenCollapsesConcurrentGrants(t *testing.T) {
	var grants atomic.Int32
	t.Setenv("KEYCLOAK_HOST", fakeTokenEndpoint(t, 60, &grants).URL)
	svc := NewAdminService(&AdminCredentials{Username: "admin", Password: "admin"})

	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if token, err := svc.GetAdminToken(); err != nil || token != "admin-token" {
				t.Errorf("GetAdminToken() = %q, %v", token, err)
			}
		}()
	}
	wg.Wait()

	if _, err := svc.GetAdminToken(); err != nil {
		t.Fatalf("GetAdminToken() error: %v", err)
	}
	if got := grants.Load(); got != 1 {
		t.Fatalf("password grants = %d, want 1", got)
	}
}

func TestGetAdminTokenRefreshesAfterExpiry(t *testing.T) {
	var grants atomic.Int32
	t.Setenv("KEYCLOAK_HOST", fakeTokenEndpoint(t, 60, &grants).URL)
	svc := NewAdminService(&AdminCredentials{Username: "admin", Password: "admin"})

	if _, err := svc.GetAdminToken(); err != nil {
		t.Fatalf("GetAdminToken() error: %v", err)
	}
	svc.tokenMu.Lock()
	svc.tokenExpiresAt = time.Now().Add(-time.Second)
	svc.tokenMu.Unlock()
	if _, err := svc.GetAdminToken(); err != nil {
		t.Fatalf("GetAdminToken() error: %v", err)
	}
	if got := grants.Load(); got != 2 {
		t.Fatalf("password grants = %d, want 2", got)
	}
}

func TestGetAdminTokenWithoutLifetimeIsNotCached(t *testing.T) {
	var grants atomic.Int32
	t.Setenv("KEYCLOAK_HOST", fakeTokenEndpoint(t, 0, &grants).URL)
	svc := NewAdminService(&AdminCredentials{Username: "admin", Password: "admin"})

	for range 2 {
		if _, err := svc.GetAdminToken(); err != nil {
			t.Fatalf("GetAdminToken() error: %v", err)
		}
	}
	if got := grants.Load(); got != 2 {
		t.Fatalf("password grants = %d, want 2", got)
	}
}
