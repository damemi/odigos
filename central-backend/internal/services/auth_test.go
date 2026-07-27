package services

import (
	"testing"

	"github.com/odigos-io/central-backend/graph/model"
	"github.com/odigos-io/central-backend/internal/keycloak"
)

func TestHasOdigosRoleInList(t *testing.T) {
	cases := []struct {
		name  string
		roles []string
		want  bool
	}{
		{name: "admin", roles: []string{keycloak.RoleAdmin}, want: true},
		{name: "owner case-insensitive", roles: []string{"OWNER"}, want: true},
		{name: "maintainer", roles: []string{keycloak.RoleMaintainer}, want: true},
		{name: "viewer", roles: []string{keycloak.RoleViewer}, want: true},
		{name: "admin-cli only", roles: []string{"admin-cli", "create-realm"}, want: false},
		{name: "empty", roles: nil, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasOdigosRoleInList(tc.roles); got != tc.want {
				t.Fatalf("hasOdigosRoleInList(%v) = %v, want %v", tc.roles, got, tc.want)
			}
		})
	}
}

func TestPasswordUpdateRequiredResultOmitsAccessToken(t *testing.T) {
	email := "new-user@example.com"
	result := passwordUpdateRequiredResult(email)

	if result.Status != model.SignInStatusPasswordUpdateRequired {
		t.Fatalf("status = %v, want %v", result.Status, model.SignInStatusPasswordUpdateRequired)
	}
	if result.AccessToken != nil {
		t.Fatalf("accessToken = %v, want nil (must not return Keycloak admin token)", *result.AccessToken)
	}
	if result.IDToken != nil {
		t.Fatalf("idToken = %v, want nil", *result.IDToken)
	}
	if result.User == nil {
		t.Fatal("expected user payload")
	}
	if result.User.Email != email {
		t.Fatalf("user.email = %q, want %q", result.User.Email, email)
	}
	if !result.User.NeedsPasswordChange {
		t.Fatal("expected NeedsPasswordChange=true")
	}
	if result.User.Teams == nil || result.User.ComputePlatforms == nil {
		t.Fatal("expected non-nil teams/computePlatforms slices for GraphQL non-null lists")
	}
}
