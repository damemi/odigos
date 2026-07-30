package utils

import (
	"testing"

	"github.com/odigos-io/central-backend/graph/model"
)

func TestConvertStringRoleToModel(t *testing.T) {
	cases := []struct {
		in   string
		want model.Role
	}{
		{"owner", model.RoleOwner},
		{"ADMIN", model.RoleAdmin},
		{"maintainer", model.RoleMaintainer},
		{"viewer", model.RoleViewer},
		{"no-access", model.RoleNoAccess},
		{"", model.RoleNoAccess},
		{"unknown", model.RoleViewer},
	}
	for _, c := range cases {
		if got := ConvertStringRoleToModel(c.in); got != c.want {
			t.Errorf("ConvertStringRoleToModel(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestConvertModelRoleToString(t *testing.T) {
	cases := []struct {
		role model.Role
		want string
	}{
		{model.RoleOwner, "owner"},
		{model.RoleAdmin, "admin"},
		{model.RoleMaintainer, "maintainer"},
		{model.RoleViewer, "viewer"},
		{model.RoleNoAccess, "no-access"},
	}

	for _, c := range cases {
		if got := ConvertModelRoleToString(c.role); got != c.want {
			t.Errorf("ConvertModelRoleToString(%v) = %q, want %q", c.role, got, c.want)
		}
	}
}

func TestRoleConversionRoundTrip(t *testing.T) {
	roles := []model.Role{model.RoleOwner, model.RoleAdmin, model.RoleMaintainer, model.RoleViewer}
	for _, role := range roles {
		roundTripped := ConvertStringRoleToModel(ConvertModelRoleToString(role))
		if roundTripped != role {
			t.Errorf("round trip for %v produced %v", role, roundTripped)
		}
	}
}
