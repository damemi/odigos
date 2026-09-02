package utils

import (
	"context"
	"testing"
	"time"
)

func TestTokenRevocationStore(t *testing.T) {
	store := NewTokenRevocationStore()
	const userID = "user-1"

	if store.IsRevoked(userID, time.Now().Unix()) {
		t.Fatal("expected no revocation before Revoke")
	}

	notBefore := time.Now().Unix()
	store.Revoke(userID, notBefore)

	if !store.IsRevoked(userID, notBefore-1) {
		t.Fatal("expected token issued before not-before to be revoked")
	}
	if store.IsRevoked(userID, notBefore) {
		t.Fatal("expected token issued at not-before to remain valid")
	}
	if store.IsRevoked(userID, notBefore+10) {
		t.Fatal("expected token issued after not-before to remain valid")
	}
}

func TestRequireAuthRejectsRevokedToken(t *testing.T) {
	const userID = "revoked-user"
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
			"roles": []any{"maintainer"},
		},
	})
	ctx := SetTokenInContext(context.Background(), token)
	if err := RequireAuth(ctx); err != ErrUnauthenticated {
		t.Fatalf("RequireAuth() = %v, want ErrUnauthenticated for revoked token", err)
	}
}

func TestLiveRoleOverridesJWTClaims(t *testing.T) {
	exp := float64(time.Now().Add(time.Hour).Unix())
	token := makeTestJWT(t, map[string]any{
		"sub": "user-live",
		"iat": float64(time.Now().Unix()),
		"exp": exp,
		"realm_access": map[string]any{
			"roles": []any{"maintainer"},
		},
	})

	ctx := SetTokenInContext(context.Background(), token)
	if !IsUserEditor(ctx) {
		t.Fatal("expected JWT maintainer to be an editor")
	}

	ctx = SetLiveRoleInContext(ctx, "viewer")
	if IsUserEditor(ctx) {
		t.Fatal("expected live viewer role to revoke editor privileges")
	}
	if !HasOdigosRole(ctx) {
		t.Fatal("expected live viewer role to still count as an Odigos role")
	}

	ctx = SetLiveRoleInContext(ctx, "no-access")
	if HasOdigosRole(ctx) {
		t.Fatal("expected live no-access role to fail HasOdigosRole")
	}
}
