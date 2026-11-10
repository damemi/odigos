package utils_test

import (
	"context"
	"testing"
	"time"

	"github.com/odigos-io/odigos/central-backend/internal/utils"
	"github.com/odigos-io/odigos/central-backend/internal/utils/tokentest"
)

func TestRequireAuthRequiresOdigosRole(t *testing.T) {
	realm := tokentest.Install(t)

	t.Run("rejects missing token", func(t *testing.T) {
		if err := utils.RequireAuth(context.Background()); err != utils.ErrUnauthenticated {
			t.Fatalf("utils.RequireAuth() = %v, want ErrUnauthenticated", err)
		}
	})

	t.Run("rejects token without odigos role", func(t *testing.T) {
		ctx := utils.SetTokenInContext(context.Background(), realm.Token(t, "u", "offline_access", "uma_authorization"))
		if err := utils.RequireAuth(ctx); err != utils.ErrUnauthenticated {
			t.Fatalf("utils.RequireAuth() = %v, want ErrUnauthenticated", err)
		}
	})

	t.Run("accepts viewer role", func(t *testing.T) {
		ctx := utils.SetTokenInContext(context.Background(), realm.Token(t, "u", "viewer"))
		if err := utils.RequireAuth(ctx); err != nil {
			t.Fatalf("utils.RequireAuth() = %v, want nil", err)
		}
	})

	t.Run("accepts admin role case-insensitive", func(t *testing.T) {
		ctx := utils.SetTokenInContext(context.Background(), realm.Token(t, "u", "ADMIN"))
		if err := utils.RequireAuth(ctx); err != nil {
			t.Fatalf("utils.RequireAuth() = %v, want nil", err)
		}
	})
}

func TestHasOdigosRole(t *testing.T) {
	realm := tokentest.Install(t)

	cases := []struct {
		name  string
		roles []string
		want  bool
	}{
		{"owner", []string{"owner"}, true},
		{"maintainer", []string{"maintainer"}, true},
		{"viewer", []string{"viewer"}, true},
		{"unrelated", []string{"offline_access"}, false},
		{"empty", nil, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := utils.SetTokenInContext(context.Background(), realm.Token(t, "u", c.roles...))
			if got := utils.HasOdigosRole(ctx); got != c.want {
				t.Fatalf("utils.HasOdigosRole() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestRequireAdminAndEditorRoles(t *testing.T) {
	realm := tokentest.Install(t)

	cases := []struct {
		role    string
		admin   bool
		editor  bool
		require error
	}{
		{"owner", true, true, nil},
		{"ADMIN", true, true, nil},
		{"maintainer", false, true, utils.ErrForbidden},
		{"viewer", false, false, utils.ErrForbidden},
		{"offline_access", false, false, utils.ErrUnauthenticated},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			ctx := utils.SetTokenInContext(context.Background(), realm.Token(t, "role-user", tc.role))
			if got := utils.IsUserAdmin(ctx); got != tc.admin {
				t.Fatalf("utils.IsUserAdmin() = %v, want %v", got, tc.admin)
			}
			if got := utils.IsUserEditor(ctx); got != tc.editor {
				t.Fatalf("utils.IsUserEditor() = %v, want %v", got, tc.editor)
			}
			if err := utils.RequireAdmin(ctx); err != tc.require {
				t.Fatalf("utils.RequireAdmin() = %v, want %v", err, tc.require)
			}
		})
	}
}

func TestRequireAuthRejectsExpiredToken(t *testing.T) {
	realm := tokentest.Install(t)
	claims := realm.Claims("expired-user", "admin")
	claims["iat"] = time.Now().Add(-2 * time.Hour).Unix()
	claims["exp"] = time.Now().Add(-time.Hour).Unix()

	ctx := utils.SetTokenInContext(context.Background(), realm.Sign(t, claims))
	if err := utils.RequireAuth(ctx); err != utils.ErrUnauthenticated {
		t.Fatalf("utils.RequireAuth() = %v, want ErrUnauthenticated", err)
	}
	// An expired admin session must not pass the admin check, even with a live role.
	if err := utils.RequireAdmin(utils.SetLiveRoleInContext(ctx, "admin")); err != utils.ErrUnauthenticated {
		t.Fatalf("utils.RequireAdmin() = %v, want ErrUnauthenticated", err)
	}
}

func TestRequireAdminRejectsAnonymous(t *testing.T) {
	if err := utils.RequireAdmin(context.Background()); err != utils.ErrUnauthenticated {
		t.Fatalf("utils.RequireAdmin() = %v, want ErrUnauthenticated", err)
	}
}

// A live role is only applied on top of a verified token; on its own it grants nothing.
func TestLiveRoleWithoutVerifiedTokenGrantsNothing(t *testing.T) {
	ctx := utils.SetLiveRoleInContext(context.Background(), "admin")
	if utils.IsUserAdmin(ctx) || utils.IsUserEditor(ctx) || utils.HasOdigosRole(ctx) {
		t.Fatal("live role without a verified token must not grant privileges")
	}
	if err := utils.RequireAdmin(ctx); err != utils.ErrUnauthenticated {
		t.Fatalf("utils.RequireAdmin() = %v, want ErrUnauthenticated", err)
	}
}

func TestRevokedTokenIgnoresLiveRole(t *testing.T) {
	realm := tokentest.Install(t)
	const userID = "revoked-live-role"
	t.Cleanup(func() { utils.GlobalTokenRevocations.Clear(userID) })
	utils.GlobalTokenRevocations.Revoke(userID, time.Now().Unix()+60)

	ctx := utils.SetLiveRoleInContext(utils.SetTokenInContext(context.Background(), realm.Token(t, userID, "admin")), "admin")
	if utils.IsUserAdmin(ctx) || utils.IsUserEditor(ctx) || utils.HasOdigosRole(ctx) {
		t.Fatal("revoked token must not grant privileges via a live role")
	}
	if err := utils.RequireAdmin(ctx); err != utils.ErrUnauthenticated {
		t.Fatalf("utils.RequireAdmin() = %v, want ErrUnauthenticated", err)
	}
}

func TestRequireAuthRejectsRevokedToken(t *testing.T) {
	realm := tokentest.Install(t)
	const userID = "revoked-user"
	t.Cleanup(func() { utils.GlobalTokenRevocations.Clear(userID) })
	utils.GlobalTokenRevocations.Revoke(userID, time.Now().Unix()+60)

	ctx := utils.SetTokenInContext(context.Background(), realm.Token(t, userID, "maintainer"))
	if err := utils.RequireAuth(ctx); err != utils.ErrUnauthenticated {
		t.Fatalf("utils.RequireAuth() = %v, want utils.ErrUnauthenticated for revoked token", err)
	}
}

func TestLiveRoleOverridesJWTClaims(t *testing.T) {
	realm := tokentest.Install(t)

	ctx := utils.SetTokenInContext(context.Background(), realm.Token(t, "user-live", "maintainer"))
	if !utils.IsUserEditor(ctx) {
		t.Fatal("expected JWT maintainer to be an editor")
	}

	ctx = utils.SetLiveRoleInContext(ctx, "viewer")
	if utils.IsUserEditor(ctx) {
		t.Fatal("expected live viewer role to revoke editor privileges")
	}
	if !utils.HasOdigosRole(ctx) {
		t.Fatal("expected live viewer role to still count as an Odigos role")
	}

	ctx = utils.SetLiveRoleInContext(ctx, "no-access")
	if utils.HasOdigosRole(ctx) {
		t.Fatal("expected live no-access role to fail HasOdigosRole")
	}
}

// Keycloak always sets iat, but a signed token without it must be rejected
// before revocation checks dereference it.
func TestRequireAuthRejectsTokenWithoutIssuedAt(t *testing.T) {
	realm := tokentest.Install(t)
	claims := realm.Claims("no-iat", "admin")
	delete(claims, "iat")

	ctx, err := utils.AuthenticateToken(context.Background(), realm.Sign(t, claims))
	if err == nil {
		t.Fatal("AuthenticateToken() accepted a token without iat")
	}
	if err := utils.RequireAdmin(ctx); err != utils.ErrUnauthenticated {
		t.Fatalf("RequireAdmin() = %v, want ErrUnauthenticated", err)
	}
}
