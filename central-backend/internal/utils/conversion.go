package utils

import (
	"strings"

	"github.com/odigos-io/central-backend/graph/model"
)

func ConvertKeycloakUserToModel(userData map[string]any, role model.Role, needsPasswordChange bool) *model.User {
	userID, _ := userData["id"].(string)
	email, _ := userData["email"].(string)
	username, _ := userData["username"].(string)

	return &model.User{
		ID:                  userID,
		Email:               email,
		Username:            username,
		Role:                role,
		NeedsPasswordChange: needsPasswordChange,
	}
}

func ConvertStringRoleToModel(roleStr string) model.Role {
	normalized := strings.ToUpper(roleStr)
	switch normalized {
	case "OWNER":
		return model.RoleOwner
	case "ADMIN":
		return model.RoleAdmin
	case "MAINTAINER":
		return model.RoleMaintainer
	case "VIEWER":
		return model.RoleViewer
	default:
		return model.RoleViewer
	}
}
