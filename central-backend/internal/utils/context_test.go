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
