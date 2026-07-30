package services

import (
	"testing"

	"github.com/odigos-io/central-backend/graph/model"
	"github.com/odigos-io/central-backend/internal/keycloak"
)

func TestAuthProviderIDsFromIdentityProviders(t *testing.T) {
	cases := []struct {
		name string
		idps []keycloak.IdentityProviderRepresentation
		want []model.AuthProviderID
	}{
		{
			name: "empty",
			idps: nil,
			want: []model.AuthProviderID{},
		},
		{
			name: "azure only",
			idps: []keycloak.IdentityProviderRepresentation{{Alias: "azure"}},
			want: []model.AuthProviderID{model.AuthProviderIDAzure},
		},
		{
			name: "ignores unknown aliases",
			idps: []keycloak.IdentityProviderRepresentation{
				{Alias: "azure"},
				{Alias: "custom-saml"},
				{Alias: "google"},
			},
			want: []model.AuthProviderID{model.AuthProviderIDAzure, model.AuthProviderIDGoogle},
		},
		{
			name: "dedupes aliases",
			idps: []keycloak.IdentityProviderRepresentation{
				{Alias: "okta"},
				{Alias: "okta"},
			},
			want: []model.AuthProviderID{model.AuthProviderIDOkta},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := authProviderIDsFromIdentityProviders(tc.idps)
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d (%v vs %v)", len(got), len(tc.want), got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("got[%d] = %v, want %v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

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
		{name: "no access", roles: []string{keycloak.RoleNoAccess}, want: false},
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
