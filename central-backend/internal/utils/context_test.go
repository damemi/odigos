package utils

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func makeTestJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestRequireAuthRequiresOdigosRole(t *testing.T) {
	exp := float64(time.Now().Add(time.Hour).Unix())

	t.Run("rejects missing token", func(t *testing.T) {
		if err := RequireAuth(context.Background()); err != ErrUnauthenticated {
			t.Fatalf("RequireAuth() = %v, want ErrUnauthenticated", err)
		}
	})

	t.Run("rejects token without odigos role", func(t *testing.T) {
		token := makeTestJWT(t, map[string]any{
			"exp": exp,
			"realm_access": map[string]any{
				"roles": []any{"offline_access", "uma_authorization"},
			},
		})
		ctx := SetTokenInContext(context.Background(), token)
		if err := RequireAuth(ctx); err != ErrUnauthenticated {
			t.Fatalf("RequireAuth() = %v, want ErrUnauthenticated", err)
		}
	})

	t.Run("accepts viewer role", func(t *testing.T) {
		token := makeTestJWT(t, map[string]any{
			"exp": exp,
			"realm_access": map[string]any{
				"roles": []any{"viewer"},
			},
		})
		ctx := SetTokenInContext(context.Background(), token)
		if err := RequireAuth(ctx); err != nil {
			t.Fatalf("RequireAuth() = %v, want nil", err)
		}
	})

	t.Run("accepts admin role case-insensitive", func(t *testing.T) {
		token := makeTestJWT(t, map[string]any{
			"exp": exp,
			"realm_access": map[string]any{
				"roles": []any{"ADMIN"},
			},
		})
		ctx := SetTokenInContext(context.Background(), token)
		if err := RequireAuth(ctx); err != nil {
			t.Fatalf("RequireAuth() = %v, want nil", err)
		}
	})
}

func TestHasOdigosRole(t *testing.T) {
	exp := float64(time.Now().Add(time.Hour).Unix())

	cases := []struct {
		name  string
		roles []any
		want  bool
	}{
		{"owner", []any{"owner"}, true},
		{"maintainer", []any{"maintainer"}, true},
		{"viewer", []any{"viewer"}, true},
		{"unrelated", []any{"offline_access"}, false},
		{"empty", []any{}, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			token := makeTestJWT(t, map[string]any{
				"exp": exp,
				"realm_access": map[string]any{
					"roles": c.roles,
				},
			})
			ctx := SetTokenInContext(context.Background(), token)
			if got := HasOdigosRole(ctx); got != c.want {
				t.Fatalf("HasOdigosRole() = %v, want %v", got, c.want)
			}
		})
	}
}

func roleToken(t *testing.T, role string) string {
	t.Helper()
	return makeTestJWT(t, map[string]any{
		"sub": "role-user",
		"iat": float64(time.Now().Unix()),
		"exp": float64(time.Now().Add(time.Hour).Unix()),
		"realm_access": map[string]any{
			"roles": []any{role},
		},
	})
}

func TestRequireAdminAndEditorRoles(t *testing.T) {
	cases := []struct {
		role     string
		admin    bool
		editor   bool
		require  error
	}{
		{"owner", true, true, nil},
		{"ADMIN", true, true, nil},
		{"maintainer", false, true, ErrForbidden},
		{"viewer", false, false, ErrForbidden},
		{"offline_access", false, false, ErrForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			ctx := SetTokenInContext(context.Background(), roleToken(t, tc.role))
			if got := IsUserAdmin(ctx); got != tc.admin {
				t.Fatalf("IsUserAdmin() = %v, want %v", got, tc.admin)
			}
			if got := IsUserEditor(ctx); got != tc.editor {
				t.Fatalf("IsUserEditor() = %v, want %v", got, tc.editor)
			}
			if err := RequireAdmin(ctx); err != tc.require {
				t.Fatalf("RequireAdmin() = %v, want %v", err, tc.require)
			}
		})
	}
}

func TestRequireAuthRejectsExpiredToken(t *testing.T) {
	token := makeTestJWT(t, map[string]any{
		"sub": "expired-user",
		"iat": float64(time.Now().Add(-2 * time.Hour).Unix()),
		"exp": float64(time.Now().Add(-time.Hour).Unix()),
		"realm_access": map[string]any{
			"roles": []any{"admin"},
		},
	})
	ctx := SetTokenInContext(context.Background(), token)
	if expired, err := IsTokenExpired(token); err != nil || !expired {
		t.Fatalf("IsTokenExpired() = %v, %v, want true", expired, err)
	}
	if err := RequireAuth(ctx); err != ErrUnauthenticated {
		t.Fatalf("RequireAuth() = %v, want ErrUnauthenticated", err)
	}
}

func TestRevokedTokenIgnoresLiveRole(t *testing.T) {
	const userID = "revoked-live-role"
	t.Cleanup(func() {
		GlobalTokenRevocations.Clear(userID)
	})

	now := time.Now().Unix()
	GlobalTokenRevocations.Revoke(userID, now+60)
	token := makeTestJWT(t, map[string]any{
		"sub": userID,
		"iat": float64(now),
		"exp": float64(now + 3600),
		"realm_access": map[string]any{
			"roles": []any{"admin"},
		},
	})
	ctx := SetLiveRoleInContext(SetTokenInContext(context.Background(), token), "admin")
	if IsUserAdmin(ctx) || IsUserEditor(ctx) || HasOdigosRole(ctx) {
		t.Fatal("revoked token must not grant privileges via a live role")
	}
	if err := RequireAdmin(ctx); err != ErrForbidden {
		t.Fatalf("RequireAdmin() = %v, want ErrForbidden", err)
	}
}

func TestIsTokenRevokedMissingClaims(t *testing.T) {
	if IsTokenRevoked("not-a-jwt") {
		t.Fatal("malformed token must not report revoked")
	}
	token := makeTestJWT(t, map[string]any{"sub": "user-no-iat"})
	if IsTokenRevoked(token) {
		t.Fatal("token without iat must not report revoked")
	}
}
