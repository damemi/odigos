package utils

import (
	"testing"

	"github.com/odigos-io/odigos/central-backend/graph/model"
)

func TestIsRoleDowngrade(t *testing.T) {
	cases := []struct {
		name    string
		oldRole model.Role
		newRole model.Role
		want    bool
	}{
		{"maintainer to viewer", model.RoleMaintainer, model.RoleViewer, true},
		{"admin to no-access", model.RoleAdmin, model.RoleNoAccess, true},
		{"viewer to no-access", model.RoleViewer, model.RoleNoAccess, true},
		{"viewer to maintainer", model.RoleViewer, model.RoleMaintainer, false},
		{"same role", model.RoleMaintainer, model.RoleMaintainer, false},
		{"owner to admin", model.RoleOwner, model.RoleAdmin, true},
		{"admin to owner", model.RoleAdmin, model.RoleOwner, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRoleDowngrade(tc.oldRole, tc.newRole); got != tc.want {
				t.Fatalf("IsRoleDowngrade(%v, %v) = %v, want %v", tc.oldRole, tc.newRole, got, tc.want)
			}
		})
	}
}
