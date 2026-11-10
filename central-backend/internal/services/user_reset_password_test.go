package services

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/odigos-io/central-backend/internal/utils"
)

func newResetPasswordUserService() *UserService {
	return NewUserService(NewAdminService(&AdminCredentials{Username: "admin", Password: "admin"}))
}

func resetCaller(t *testing.T, role string) context.Context {
	t.Helper()
	return utils.SetTokenInContext(context.Background(), jwtFor(t, "caller-"+role, time.Now(), role))
}

// A reset puts the user back in the invited state: a one-time password they
// must replace, with their existing sessions ended.
func TestResetUserPasswordIssuesOneTimePassword(t *testing.T) {
	const userID = "forgetful"
	t.Cleanup(func() {
		utils.GlobalTokenRevocations.Clear(userID)
		utils.InvalidateAuthCaches(userID)
	})
	user := &fakeKeycloakUser{id: userID, username: "forgetful@example.com", email: "forgetful@example.com", password: "old-password", role: "maintainer"}
	kc := newFakePasswordKeycloak(t, user)

	res, err := newResetPasswordUserService().ResetUserPassword(resetCaller(t, "admin"), userID)
	if err != nil {
		t.Fatalf("ResetUserPassword() error = %v", err)
	}
	if !res.Success || res.GeneratedPassword == nil || *res.GeneratedPassword == "" {
		t.Fatalf("ResetUserPassword() = %+v, want success with a generated password", res)
	}
	if user.password != *res.GeneratedPassword {
		t.Fatal("the user's password was not replaced with the generated one")
	}
	if fmt.Sprint(user.requiredActions) != "[UPDATE_PASSWORD]" {
		t.Fatalf("requiredActions = %v, want [UPDATE_PASSWORD]", user.requiredActions)
	}
	if !utils.GlobalTokenRevocations.IsRevoked(userID, time.Now().Add(-time.Minute).Unix()) {
		t.Fatal("expected the user's outstanding tokens to be revoked")
	}
	if got, want := kc.writeLog(), []string{"reset-password " + userID, "update " + userID, "logout " + userID, "update " + userID}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("writes = %v, want %v", got, want)
	}

	// The user finishes the reset the same way an invited user signs in first.
	signIn, err := newInitialPasswordAuthService().SetInitialPassword(context.Background(), "forgetful@example.com", *res.GeneratedPassword, "new-password", "")
	if err != nil {
		t.Fatalf("SetInitialPassword() error = %v", err)
	}
	if signIn.AccessToken == nil || user.password != "new-password" || len(user.requiredActions) != 0 {
		t.Fatalf("after SetInitialPassword: result = %+v, user = %+v", signIn, user)
	}
}

// Owners rank above Admins: a caller may reset users up to their own role.
func TestResetUserPasswordFollowsRoleHierarchy(t *testing.T) {
	cases := []struct {
		caller, target string
		allowed        bool
	}{
		{caller: "admin", target: "viewer", allowed: true},
		{caller: "admin", target: "admin", allowed: true},
		{caller: "admin", target: "owner", allowed: false},
		{caller: "owner", target: "owner", allowed: true},
		{caller: "owner", target: "admin", allowed: true},
	}
	for _, tc := range cases {
		t.Run(tc.caller+" resets "+tc.target, func(t *testing.T) {
			userID := "target-" + tc.target
			t.Cleanup(func() {
				utils.GlobalTokenRevocations.Clear(userID)
				utils.InvalidateAuthCaches(userID)
			})
			user := &fakeKeycloakUser{id: userID, username: userID + "@example.com", email: userID + "@example.com", password: "old-password", role: tc.target}
			kc := newFakePasswordKeycloak(t, user)

			res, err := newResetPasswordUserService().ResetUserPassword(resetCaller(t, tc.caller), userID)
			if err != nil {
				t.Fatalf("ResetUserPassword() error = %v", err)
			}
			if res.Success != tc.allowed {
				t.Fatalf("ResetUserPassword() success = %v, want %v (error %v)", res.Success, tc.allowed, res.Error)
			}
			if !tc.allowed {
				if user.password != "old-password" || len(kc.writeLog()) != 0 {
					t.Fatalf("a refused reset changed the user: password changed = %v, writes = %v", user.password != "old-password", kc.writeLog())
				}
				if utils.GlobalTokenRevocations.NotBefore(userID) != 0 {
					t.Fatal("a refused reset must not revoke the user's tokens")
				}
			}
		})
	}
}

func TestResetUserPasswordRefuses(t *testing.T) {
	cases := []struct {
		name string
		user *fakeKeycloakUser
	}{
		{
			name: "own user",
			user: &fakeKeycloakUser{id: "caller-admin", username: "self@example.com", email: "self@example.com", password: "old-password", role: "admin"},
		},
		{
			name: "SSO user",
			user: &fakeKeycloakUser{id: "sso-user", username: "sso@example.com", email: "sso@example.com", federated: true, role: "viewer"},
		},
		{
			name: "built-in admin",
			user: &fakeKeycloakUser{id: "kc-admin", username: "admin", email: "admin@example.com", password: "admin", role: "admin"},
		},
		{
			name: "username is not the email",
			user: &fakeKeycloakUser{id: "console-user", username: "console-user", email: "console@example.com", password: "old-password", role: "viewer"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() {
				utils.GlobalTokenRevocations.Clear(tc.user.id)
				utils.InvalidateAuthCaches(tc.user.id)
			})
			kc := newFakePasswordKeycloak(t, tc.user)

			res, err := newResetPasswordUserService().ResetUserPassword(resetCaller(t, "admin"), tc.user.id)
			if err != nil {
				t.Fatalf("ResetUserPassword() error = %v", err)
			}
			if res.Success || res.Error == nil || res.GeneratedPassword != nil {
				t.Fatalf("ResetUserPassword() = %+v, want a refusal without a password", res)
			}
			if writes := kc.writeLog(); len(writes) != 0 {
				t.Fatalf("ResetUserPassword() wrote to Keycloak: %v", writes)
			}
			if utils.GlobalTokenRevocations.NotBefore(tc.user.id) != 0 {
				t.Fatal("a refused reset must not revoke the user's tokens")
			}
		})
	}
}
