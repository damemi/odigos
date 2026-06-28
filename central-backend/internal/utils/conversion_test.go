package utils

import (
	"testing"

	"github.com/odigos-io/central-backend/graph/model"
)

func TestConvertModelRoleToString(t *testing.T) {
	cases := []struct {
		role model.Role
		want string
	}{
		{model.RoleOwner, "owner"},
		{model.RoleAdmin, "admin"},
		{model.RoleMaintainer, "maintainer"},
		{model.RoleViewer, "viewer"},
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
