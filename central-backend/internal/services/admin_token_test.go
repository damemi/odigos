package services

import (
	"encoding/json"
	"fmt"
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

// Access-token lifespan is raised to 12h, so Keycloak reports expires_in near the SSO
// session max (10h) while the admin API rejects the token at SSO idle (refresh_expires_in,
// default 30m). Caching the JWT lifetime serves that dead token and user creation 401s.
func TestGetAdminTokenCacheFollowsSessionLifetime(t *testing.T) {
	var grants atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /realms/master/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		grants.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":       "admin-token",
			"expires_in":         36000,
			"refresh_expires_in": 1800,
		})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Setenv("KEYCLOAK_HOST", server.URL)
	svc := NewAdminService(&AdminCredentials{Username: "admin", Password: "admin"})

	before := time.Now()
	if _, err := svc.GetAdminToken(); err != nil {
		t.Fatalf("GetAdminToken() error: %v", err)
	}
	svc.tokenMu.Lock()
	expiresAt := svc.tokenExpiresAt
	svc.tokenMu.Unlock()

	sessionCache := 1800 * time.Second * 3 / 4
	jwtCache := 36000 * time.Second * 3 / 4
	if expiresAt.Before(before.Add(sessionCache-time.Second)) || expiresAt.After(time.Now().Add(sessionCache+time.Second)) {
		t.Fatalf("tokenExpiresAt = %s, want about %s after start", expiresAt, sessionCache)
	}
	if !expiresAt.Before(before.Add(jwtCache)) {
		t.Fatalf("tokenExpiresAt = %s, cached for the JWT lifetime", expiresAt)
	}

	if _, err := svc.GetAdminToken(); err != nil {
		t.Fatalf("GetAdminToken() error: %v", err)
	}
	if got := grants.Load(); got != 1 {
		t.Fatalf("password grants = %d, want 1", got)
	}
}

func TestWithAdminTokenRetriesOnceOnUnauthorized(t *testing.T) {
	var grants atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /realms/master/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		n := grants.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": fmt.Sprintf("token-%d", n),
			"expires_in":   60,
		})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Setenv("KEYCLOAK_HOST", server.URL)
	svc := NewAdminService(&AdminCredentials{Username: "admin", Password: "admin"})

	var attempts atomic.Int32
	err := svc.WithAdminToken(func(token string) error {
		n := attempts.Add(1)
		if n == 1 {
			if token != "token-1" {
				t.Errorf("first token = %q, want token-1", token)
			}
			return fmt.Errorf(`failed to add user to realm: {"error":"HTTP 401 Unauthorized"}`)
		}
		if token != "token-2" {
			t.Errorf("retry token = %q, want token-2", token)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithAdminToken() error: %v", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
	if got := grants.Load(); got != 2 {
		t.Fatalf("password grants = %d, want 2", got)
	}
}

func TestWithAdminTokenDoesNotRetryOtherErrors(t *testing.T) {
	var grants atomic.Int32
	t.Setenv("KEYCLOAK_HOST", fakeTokenEndpoint(t, 60, &grants).URL)
	svc := NewAdminService(&AdminCredentials{Username: "admin", Password: "admin"})

	var attempts atomic.Int32
	err := svc.WithAdminToken(func(token string) error {
		attempts.Add(1)
		return fmt.Errorf("failed to add user to realm: conflict")
	})
	if err == nil || err.Error() != "failed to add user to realm: conflict" {
		t.Fatalf("WithAdminToken() error = %v", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
	if got := grants.Load(); got != 1 {
		t.Fatalf("password grants = %d, want 1", got)
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
