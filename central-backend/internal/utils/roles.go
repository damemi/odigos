package utils

import "github.com/odigos-io/odigos/central-backend/graph/model"

// RolePrivilegeLevel returns a comparable privilege rank for Odigos roles.
// Higher values are more privileged. Unknown roles rank below no-access.
func RolePrivilegeLevel(role model.Role) int {
	switch role {
	case model.RoleOwner:
		return 5
	case model.RoleAdmin:
		return 4
	case model.RoleMaintainer:
		return 3
	case model.RoleViewer:
		return 2
	case model.RoleNoAccess:
		return 1
	default:
		return 0
	}
}

// IsRoleDowngrade reports whether newRole is strictly less privileged than oldRole.
func IsRoleDowngrade(oldRole, newRole model.Role) bool {
	return RolePrivilegeLevel(newRole) < RolePrivilegeLevel(oldRole)
}
