package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/odigos-io/odigos/central-backend/internal/logger"
)

type fakeKeycloakUser struct {
	id              string
	username        string
	email           string
	password        string
	requiredActions []string
	// federated users are linked to an identity provider.
	federated bool
	// role is the user's realm role name, e.g. "admin".
	role string
}

// fakePasswordKeycloak mimics the Keycloak endpoints setInitialPassword uses.
// Password grants check credentials before required actions, as Keycloak does.
type fakePasswordKeycloak struct {
	mu      sync.Mutex
	users   []*fakeKeycloakUser
	writes  []string
	logouts int
	down    bool
}

func newFakePasswordKeycloak(t *testing.T, users ...*fakeKeycloakUser) *fakePasswordKeycloak {
	t.Helper()
	logger.InitLogger()
	kc := &fakePasswordKeycloak{users: users}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /realms/master/protocol/openid-connect/token", kc.token)
	mux.HandleFunc("POST /realms/master/protocol/openid-connect/logout", func(w http.ResponseWriter, r *http.Request) {
		kc.mu.Lock()
		kc.logouts++
		kc.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /admin/realms/master/users", kc.search)
	mux.HandleFunc("PUT /admin/realms/master/users/{id}/reset-password", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Value     string `json:"value"`
			Temporary bool   `json:"temporary"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		kc.mu.Lock()
		defer kc.mu.Unlock()
		kc.writes = append(kc.writes, "reset-password "+r.PathValue("id"))
		if u := kc.byID(r.PathValue("id")); u != nil {
			u.password = body.Value
			// Keycloak drops a pending password change when a permanent password is set.
			if !body.Temporary {
				u.requiredActions = slices.DeleteFunc(u.requiredActions, func(a string) bool { return a == "UPDATE_PASSWORD" })
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PUT /admin/realms/master/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		// Keycloak leaves required actions unchanged when the field is absent.
		var body struct {
			RequiredActions *[]string `json:"requiredActions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		kc.mu.Lock()
		defer kc.mu.Unlock()
		kc.writes = append(kc.writes, "update "+r.PathValue("id"))
		if u := kc.byID(r.PathValue("id")); u != nil && body.RequiredActions != nil {
			u.requiredActions = *body.RequiredActions
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /admin/realms/master/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		kc.mu.Lock()
		defer kc.mu.Unlock()
		u := kc.byID(r.PathValue("id"))
		if u == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": u.id, "username": u.username, "email": u.email, "requiredActions": u.requiredActions})
	})
	mux.HandleFunc("GET /admin/realms/master/users/{id}/federated-identity", func(w http.ResponseWriter, r *http.Request) {
		kc.mu.Lock()
		defer kc.mu.Unlock()
		links := []map[string]any{}
		if u := kc.byID(r.PathValue("id")); u != nil && u.federated {
			links = append(links, map[string]any{"identityProvider": "oidc", "userId": u.id})
		}
		_ = json.NewEncoder(w).Encode(links)
	})
	mux.HandleFunc("GET /admin/realms/master/users/{id}/role-mappings/realm", func(w http.ResponseWriter, r *http.Request) {
		kc.mu.Lock()
		defer kc.mu.Unlock()
		roles := []map[string]any{}
		if u := kc.byID(r.PathValue("id")); u != nil && u.role != "" {
			roles = append(roles, map[string]any{"name": u.role})
		}
		_ = json.NewEncoder(w).Encode(roles)
	})
	mux.HandleFunc("POST /admin/realms/master/users/{id}/logout", func(w http.ResponseWriter, r *http.Request) {
		kc.mu.Lock()
		defer kc.mu.Unlock()
		kc.writes = append(kc.writes, "logout "+r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected Keycloak call %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Setenv("KEYCLOAK_HOST", server.URL)
	return kc
}

func (kc *fakePasswordKeycloak) byID(id string) *fakeKeycloakUser {
	for _, u := range kc.users {
		if u.id == id {
			return u
		}
	}
	return nil
}

func (kc *fakePasswordKeycloak) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	if r.Form.Get("client_id") == "admin-cli" {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "admin-token", "expires_in": 60})
		return
	}
	kc.mu.Lock()
	defer kc.mu.Unlock()
	if kc.down {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	writeGrantError := func(status int, description string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": description})
	}
	username := strings.ToLower(r.Form.Get("username"))
	for _, u := range kc.users {
		if u.username != username {
			continue
		}
		if u.password != r.Form.Get("password") {
			writeGrantError(http.StatusUnauthorized, "Invalid user credentials")
			return
		}
		if len(u.requiredActions) > 0 {
			writeGrantError(http.StatusBadRequest, "Account is not fully set up")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "user-token-" + u.id, "refresh_token": "refresh-" + u.id})
		return
	}
	writeGrantError(http.StatusUnauthorized, "Invalid user credentials")
}

// search matches emails by substring unless exact=true, like Keycloak.
func (kc *fakePasswordKeycloak) search(w http.ResponseWriter, r *http.Request) {
	kc.mu.Lock()
	defer kc.mu.Unlock()
	if kc.down {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	email := strings.ToLower(r.URL.Query().Get("email"))
	exact := r.URL.Query().Get("exact") == "true"
	result := []map[string]any{}
	for _, u := range kc.users {
		if (exact && u.email == email) || (!exact && strings.Contains(u.email, email)) {
			actions := make([]any, 0, len(u.requiredActions))
			for _, a := range u.requiredActions {
				actions = append(actions, a)
			}
			result = append(result, map[string]any{"id": u.id, "username": u.username, "email": u.email, "requiredActions": actions})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i]["username"].(string) < result[j]["username"].(string) })
	_ = json.NewEncoder(w).Encode(result)
}

func (kc *fakePasswordKeycloak) writeLog() []string {
	kc.mu.Lock()
	defer kc.mu.Unlock()
	return append([]string(nil), kc.writes...)
}

func newInitialPasswordAuthService() *AuthService {
	admin := NewAdminService(&AdminCredentials{Username: "admin", Password: "admin"})
	return NewAuthService(admin, NewUserService(admin), NewRoleMappingService(admin))
}

func invitedUser(id, email, oneTimePassword string) *fakeKeycloakUser {
	return &fakeKeycloakUser{id: id, username: email, email: email, password: oneTimePassword, requiredActions: []string{"UPDATE_PASSWORD"}}
}

func TestSetInitialPasswordRejectsWithoutProof(t *testing.T) {
	cases := []struct {
		name            string
		email           string
		oneTimePassword string
	}{
		{name: "unknown email", email: "nobody@example.com", oneTimePassword: "otp-123"},
		{name: "wrong one-time password", email: "invited@example.com", oneTimePassword: "guess"},
		{name: "empty one-time password", email: "invited@example.com", oneTimePassword: ""},
		{name: "no pending password change", email: "active@example.com", oneTimePassword: "current-password"},
		{name: "account not created by an admin", email: "sso@example.com", oneTimePassword: "otp-sso"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kc := newFakePasswordKeycloak(t,
				invitedUser("invited", "invited@example.com", "otp-123"),
				&fakeKeycloakUser{id: "active", username: "active@example.com", email: "active@example.com", password: "current-password"},
				&fakeKeycloakUser{id: "sso", username: "sso-user", email: "sso@example.com", password: "otp-sso", requiredActions: []string{"UPDATE_PASSWORD"}},
			)
			svc := newInitialPasswordAuthService()

			result, err := svc.SetInitialPassword(context.Background(), tc.email, tc.oneTimePassword, "new-password", "10.0.0.1")
			if !errors.Is(err, ErrInvalidOneTimePassword) {
				t.Fatalf("SetInitialPassword() error = %v, want %v", err, ErrInvalidOneTimePassword)
			}
			if result != nil {
				t.Fatalf("SetInitialPassword() returned a result: %+v", result)
			}
			if writes := kc.writeLog(); len(writes) != 0 {
				t.Fatalf("SetInitialPassword() wrote to Keycloak: %v", writes)
			}
		})
	}
}

func TestSetInitialPasswordEndsSessionOpenedByCheck(t *testing.T) {
	kc := newFakePasswordKeycloak(t, &fakeKeycloakUser{id: "active", username: "active@example.com", email: "active@example.com", password: "current-password"})
	svc := newInitialPasswordAuthService()

	if _, err := svc.SetInitialPassword(context.Background(), "active@example.com", "current-password", "new-password", ""); !errors.Is(err, ErrInvalidOneTimePassword) {
		t.Fatalf("SetInitialPassword() error = %v, want %v", err, ErrInvalidOneTimePassword)
	}
	if kc.logouts != 1 {
		t.Fatalf("logouts = %d, want 1", kc.logouts)
	}
}

func TestSetInitialPasswordWithValidOneTimePassword(t *testing.T) {
	// admin@example.com contains dmin@example.com, so a substring search would
	// return the admin first; only the invited user may change.
	admin := invitedUser("admin", "admin@example.com", "admin-otp")
	invited := invitedUser("invited", "dmin@example.com", "otp-123")
	kc := newFakePasswordKeycloak(t, admin, invited)
	svc := newInitialPasswordAuthService()

	result, err := svc.SetInitialPassword(context.Background(), " DMIN@example.com ", "otp-123", "new-password", "10.0.0.1")
	if err != nil {
		t.Fatalf("SetInitialPassword() error = %v", err)
	}
	if result.AccessToken == nil || *result.AccessToken != "user-token-invited" {
		t.Fatalf("AccessToken = %v, want the invited user's token", result.AccessToken)
	}
	if result.User == nil || result.User.ID != "invited" || result.User.NeedsPasswordChange {
		t.Fatalf("User = %+v, want the invited user without a pending change", result.User)
	}
	if invited.password != "new-password" || len(invited.requiredActions) != 0 {
		t.Fatalf("invited user = %+v, want new password and no required actions", invited)
	}
	if admin.password != "admin-otp" || len(admin.requiredActions) != 1 {
		t.Fatalf("admin user changed: %+v", admin)
	}
	if got, want := kc.writeLog(), []string{"reset-password invited", "update invited"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("writes = %v, want %v", got, want)
	}

	// The one-time password no longer works.
	if _, err := svc.SetInitialPassword(context.Background(), "dmin@example.com", "otp-123", "other", "10.0.0.1"); !errors.Is(err, ErrInvalidOneTimePassword) {
		t.Fatalf("second SetInitialPassword() error = %v, want %v", err, ErrInvalidOneTimePassword)
	}
}

func TestSetInitialPasswordKeycloakUnavailable(t *testing.T) {
	kc := newFakePasswordKeycloak(t, invitedUser("invited", "invited@example.com", "otp-123"))
	kc.down = true
	svc := newInitialPasswordAuthService()

	_, err := svc.SetInitialPassword(context.Background(), "invited@example.com", "otp-123", "new-password", "")
	if err == nil || errors.Is(err, ErrInvalidOneTimePassword) {
		t.Fatalf("SetInitialPassword() error = %v, want a distinct failure", err)
	}
	if writes := kc.writeLog(); len(writes) != 0 {
		t.Fatalf("SetInitialPassword() wrote to Keycloak: %v", writes)
	}
}

func TestSetInitialPasswordRateLimitsPerEmail(t *testing.T) {
	invited := invitedUser("invited", "invited@example.com", "otp-123")
	kc := newFakePasswordKeycloak(t, invited)
	svc := newInitialPasswordAuthService()

	// An empty code is rejected before Keycloak and not counted.
	if _, err := svc.SetInitialPassword(context.Background(), "invited@example.com", "", "new-password", "10.0.9.9"); !errors.Is(err, ErrInvalidOneTimePassword) {
		t.Fatalf("empty code error = %v", err)
	}
	for i := 0; i < initialPasswordEmailLimit; i++ {
		// A new address each time, so only the per-email limit applies.
		ip := fmt.Sprintf("10.0.0.%d", i)
		if _, err := svc.SetInitialPassword(context.Background(), "invited@example.com", "guess", "new-password", ip); !errors.Is(err, ErrInvalidOneTimePassword) {
			t.Fatalf("attempt %d error = %v, want %v", i, err, ErrInvalidOneTimePassword)
		}
	}
	// Even the right code is refused once the email is limited, and nothing is checked.
	if _, err := svc.SetInitialPassword(context.Background(), "Invited@example.com", "otp-123", "new-password", "10.0.1.1"); !errors.Is(err, ErrTooManyPasswordAttempts) {
		t.Fatalf("limited attempt error = %v, want %v", err, ErrTooManyPasswordAttempts)
	}
	if invited.password != "otp-123" || len(kc.writeLog()) != 0 {
		t.Fatal("a rate-limited attempt changed the user")
	}
	// Unknown emails are limited the same way.
	for i := 0; i <= initialPasswordEmailLimit; i++ {
		_, err := svc.SetInitialPassword(context.Background(), "nobody@example.com", "guess", "new-password", fmt.Sprintf("10.0.2.%d", i))
		if i == initialPasswordEmailLimit && !errors.Is(err, ErrTooManyPasswordAttempts) {
			t.Fatalf("unknown email attempt %d error = %v, want %v", i, err, ErrTooManyPasswordAttempts)
		}
	}
}

func TestSetInitialPasswordRateLimitsPerClient(t *testing.T) {
	newFakePasswordKeycloak(t, invitedUser("invited", "invited@example.com", "otp-123"))
	svc := newInitialPasswordAuthService()

	for i := 0; i < initialPasswordClientLimit; i++ {
		email := fmt.Sprintf("user%d@example.com", i)
		if _, err := svc.SetInitialPassword(context.Background(), email, "guess", "new-password", "203.0.113.7"); !errors.Is(err, ErrInvalidOneTimePassword) {
			t.Fatalf("attempt %d error = %v, want %v", i, err, ErrInvalidOneTimePassword)
		}
	}
	if _, err := svc.SetInitialPassword(context.Background(), "invited@example.com", "otp-123", "new-password", "203.0.113.7"); !errors.Is(err, ErrTooManyPasswordAttempts) {
		t.Fatalf("limited client error = %v, want %v", err, ErrTooManyPasswordAttempts)
	}
	if _, err := svc.SetInitialPassword(context.Background(), "invited@example.com", "otp-123", "new-password", "198.51.100.1"); err != nil {
		t.Fatalf("another client error = %v, want success", err)
	}
}

func TestSetInitialPasswordRejectsOversizedInput(t *testing.T) {
	kc := newFakePasswordKeycloak(t, invitedUser("invited", "invited@example.com", "otp-123"))
	svc := newInitialPasswordAuthService()

	long := strings.Repeat("a", maxEmailLength) + "@example.com"
	if _, err := svc.SetInitialPassword(context.Background(), long, "otp-123", "new-password", ""); !errors.Is(err, ErrInvalidOneTimePassword) {
		t.Fatalf("long email error = %v, want %v", err, ErrInvalidOneTimePassword)
	}
	if _, err := svc.SetInitialPassword(context.Background(), "invited@example.com", "otp-123", strings.Repeat("p", maxPasswordLength+1), ""); err == nil {
		t.Fatal("oversized new password accepted")
	}
	if len(svc.initialPasswordAttempts.failures) != 0 || len(kc.writeLog()) != 0 {
		t.Fatal("oversized input was tracked or reached Keycloak writes")
	}
}

// Without a forwarded address every Central UI request comes from the same
// pod, so failures must not pile up in one shared per-address bucket.
func TestSetInitialPasswordWithoutClientIPHasNoSharedBucket(t *testing.T) {
	newFakePasswordKeycloak(t, invitedUser("invited", "invited@example.com", "otp-123"))
	svc := newInitialPasswordAuthService()

	for i := 0; i <= initialPasswordClientLimit; i++ {
		email := fmt.Sprintf("user%d@example.com", i)
		if _, err := svc.SetInitialPassword(context.Background(), email, "guess", "new-password", ""); !errors.Is(err, ErrInvalidOneTimePassword) {
			t.Fatalf("attempt %d error = %v, want %v", i, err, ErrInvalidOneTimePassword)
		}
	}
	if _, err := svc.SetInitialPassword(context.Background(), "invited@example.com", "otp-123", "new-password", ""); err != nil {
		t.Fatalf("SetInitialPassword() error = %v, want success", err)
	}
	for key := range svc.initialPasswordAttempts.failures {
		if strings.HasPrefix(key, "ip:") {
			t.Fatalf("tracked a per-address key without a forwarded address: %q", key)
		}
	}
}

func TestAttemptLimiterWindow(t *testing.T) {
	now := time.Unix(0, 0)
	l := newAttemptLimiter(time.Minute)
	l.now = func() time.Time { return now }
	a := attemptKey{key: "a", limit: 2}
	b := attemptKey{key: "b", limit: 1}

	if l.blocked(a, b) {
		t.Fatal("blocked before any failure")
	}
	l.fail(a)
	l.fail(a)
	if !l.blocked(a) || !l.blocked(a, b) {
		t.Fatal("not blocked after reaching the limit")
	}
	if l.blocked(b) {
		t.Fatal("b blocked without failures")
	}

	now = now.Add(time.Minute)
	if l.blocked(a) {
		t.Fatal("blocked after the window ended")
	}

	l.fail(b)
	l.reset("b")
	if l.blocked(b) {
		t.Fatal("blocked after reset")
	}
}

func TestAttemptLimiterBoundsMemory(t *testing.T) {
	l := newAttemptLimiter(time.Minute)
	for i := 0; i < maxTrackedKeys; i++ {
		l.failures[fmt.Sprint(i)] = attemptWindow{start: l.now(), count: 1}
	}
	k := attemptKey{key: "new", limit: 5}
	if !l.blocked(k) {
		t.Fatal("new key allowed while the limiter is full")
	}
	l.fail(k)
	if len(l.failures) != maxTrackedKeys {
		t.Fatalf("limiter grew past its cap: %d", len(l.failures))
	}
}
