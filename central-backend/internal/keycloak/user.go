package keycloak

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/odigos-io/central-backend/graph/model"
	"github.com/odigos-io/central-backend/internal/consts"
)

const (
	StatusOK        = http.StatusOK        // 200
	StatusCreated   = http.StatusCreated   // 201
	StatusNoContent = http.StatusNoContent // 204
)

type UserInfo map[string]any
type UserList []map[string]any
type RoleList []map[string]any

type UserService struct {
	host  string
	realm string
}

type CredentialPayload struct {
	Type      string `json:"type"`
	Value     string `json:"value"`
	Temporary bool   `json:"temporary"`
}

type UserPayload struct {
	Username      string              `json:"username"`
	Email         string              `json:"email"`
	Enabled       bool                `json:"enabled"`
	EmailVerified bool                `json:"emailVerified"`
	Credentials   []CredentialPayload `json:"credentials"`
}

type RolePayload struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type PasswordUpdatePayload struct {
	Type      string `json:"type"`
	Value     string `json:"value"`
	Temporary bool   `json:"temporary"`
}

func NewUserService() *UserService {
	return &UserService{
		host:  consts.GetKeycloakHost(),
		realm: consts.KeycloakRealm,
	}
}

func (u *UserService) GetUserRole(adminToken, userID string) (string, error) {
	rolesURL := fmt.Sprintf("%s/admin/realms/%s/users/%s/role-mappings/realm", u.host, u.realm, userID)
	req, err := http.NewRequest("GET", rolesURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create roles request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to get roles: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != StatusOK {
		return RoleViewer, nil
	}

	var roles RoleList
	if err := json.NewDecoder(resp.Body).Decode(&roles); err != nil {
		return "", fmt.Errorf("failed to decode roles: %w", err)
	}

	for _, role := range roles {
		roleName := role["name"].(string)
		switch roleName {
		case RoleAdmin:
			return RoleAdmin, nil
		case RoleMaintainer:
			return RoleMaintainer, nil
		case RoleViewer:
			return RoleViewer, nil
		case RoleOwner:
			return RoleOwner, nil
		}
	}

	return RoleViewer, nil
}

func (u *UserService) CheckUserNeedsPasswordChange(adminToken, email string) (bool, error) {
	users, err := u.FindUsersByEmail(adminToken, email)
	if err != nil || len(users) == 0 {
		return false, fmt.Errorf("failed to find user by email: %w", err)
	}
	userData := users[0]
	requiredActions, ok := userData["requiredActions"].([]any)
	if !ok {
		return false, nil
	}

	for _, action := range requiredActions {
		if actionStr, ok := action.(string); ok && actionStr == consts.UpdatePasswordAction {
			return true, nil
		}
	}

	return false, nil

}

func (u *UserService) GetUserInfo(token string) (UserInfo, error) {
	url := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/userinfo", u.host, u.realm)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get user info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get user info: status %d, body: %s", resp.StatusCode, string(responseBody))
	}

	var userInfo UserInfo
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		return nil, fmt.Errorf("failed to decode user info: %w", err)
	}

	return userInfo, nil

}

func (u *UserService) AddUserToRealm(token, username, email, password string) error {
	url := fmt.Sprintf("%s/admin/realms/%s/users", u.host, u.realm)
	user := UserPayload{
		Username:      username,
		Email:         email,
		Enabled:       true,
		EmailVerified: true,
		Credentials: []CredentialPayload{
			{
				Type:      "password",
				Value:     password,
				Temporary: false,
			},
		},
	}
	body, err := json.Marshal(user)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != StatusCreated {
		responseBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to add user to realm: %s", responseBody)
	}

	return nil
}

func (u *UserService) AddRealmRoleToUser(token, email, roleName string) error {

	searchUserURL := fmt.Sprintf("%s/admin/realms/%s/users?email=%s", u.host, u.realm, email)
	req, err := http.NewRequest("GET", searchUserURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to search user: %s", string(body))
	}
	var users UserList
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return err
	}
	if len(users) == 0 {
		return fmt.Errorf("user with email %s not found", email)
	}
	userID, ok := users[0]["id"].(string)
	if !ok {
		return fmt.Errorf("failed to get user ID from response")
	}

	searchRoleURL := fmt.Sprintf("%s/admin/realms/%s/roles/%s", u.host, u.realm, roleName)
	req, err = http.NewRequest("GET", searchRoleURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to get role: %s", string(body))
	}
	var role RolePayload
	if err := json.NewDecoder(resp.Body).Decode(&role); err != nil {
		return err
	}
	roleID := role.ID
	if roleID == "" {
		return fmt.Errorf("failed to get role ID from response")
	}

	assignRoleURL := fmt.Sprintf("%s/admin/realms/%s/users/%s/role-mappings/realm", u.host, u.realm, userID)
	roleRep := []RolePayload{
		{
			ID:   roleID,
			Name: roleName,
		},
	}
	body, err := json.Marshal(roleRep)
	if err != nil {
		return err
	}
	req, err = http.NewRequest("POST", assignRoleURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to assign role to user: %s", string(body))
	}
	return nil
}

func (u *UserService) FindUsersByEmail(token, email string) (UserList, error) {
	searchURL := fmt.Sprintf("%s/admin/realms/%s/users?email=%s", u.host, u.realm, email)
	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to search user: %s", string(body))
	}

	var users UserList
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return nil, err
	}
	return users, nil
}

func (u *UserService) UpdateUserPassword(token, email, newPassword string) error {
	users, err := u.FindUsersByEmail(token, email)
	if err != nil || len(users) == 0 {
		return fmt.Errorf("failed to find user by email: %w", err)
	}
	userData := users[0]
	userID := userData["id"].(string)

	url := fmt.Sprintf("%s/admin/realms/%s/users/%s/reset-password", u.host, u.realm, userID)

	updateData := PasswordUpdatePayload{
		Type:      "password",
		Value:     newPassword,
		Temporary: false,
	}

	body, err := json.Marshal(updateData)
	if err != nil {
		return fmt.Errorf("failed to marshal update data: %w", err)
	}

	req, err := http.NewRequest("PUT", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to update password: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != StatusNoContent {
		responseBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to update password: %s", string(responseBody))
	}

	return nil
}

func (u *UserService) CheckUserDetails(token, email string) (map[string]interface{}, error) {
	searchURL := fmt.Sprintf("%s/admin/realms/%s/users?email=%s", u.host, u.realm, email)
	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to search user: %s", string(body))
	}

	var users UserList
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return nil, err
	}

	if len(users) == 0 {
		return nil, fmt.Errorf("user with email %s not found", email)
	}

	user := users[0]
	return user, nil
}

func (u *UserService) SetUserPasswordRequirement(token, email string, required bool) error {
	users, err := u.FindUsersByEmail(token, email)
	if err != nil || len(users) == 0 {
		return fmt.Errorf("failed to find user by email: %w", err)
	}
	userData := users[0]
	userID := userData["id"].(string)

	url := fmt.Sprintf("%s/admin/realms/%s/users/%s", u.host, u.realm, userID)

	var requiredActions []string
	if required {
		requiredActions = []string{consts.UpdatePasswordAction}
	} else {
		requiredActions = []string{}
	}

	updateData := map[string]any{
		"requiredActions": requiredActions,
	}

	body, err := json.Marshal(updateData)
	if err != nil {
		return fmt.Errorf("failed to marshal update data: %w", err)
	}

	req, err := http.NewRequest("PUT", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to update user: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != StatusNoContent {
		responseBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to update user: %s", string(responseBody))
	}

	return nil
}

func (u *UserService) GetUserRequiredActions(adminToken, email string) ([]string, error) {
	searchURL := fmt.Sprintf("%s/admin/realms/%s/users?email=%s", u.host, u.realm, email)
	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get user required actions: status %d, response: %s", resp.StatusCode, string(responseBody))
	}

	var users UserList
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(users) == 0 {
		return nil, fmt.Errorf("user not found")
	}

	userData := users[0]
	requiredActions, ok := userData["requiredActions"].([]any)
	if !ok {
		return []string{}, nil
	}

	var actions []string
	for _, action := range requiredActions {
		if actionStr, ok := action.(string); ok {
			actions = append(actions, actionStr)
		}
	}

	return actions, nil
}

func (u *UserService) ListUsers(token string) (UserList, error) {
	url := fmt.Sprintf("%s/admin/realms/%s/users", u.host, u.realm)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to list users: status %d, response: %s", resp.StatusCode, string(responseBody))
	}

	var users UserList
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return users, nil
}

func (u *UserService) AssignComputePlatformsToUser(token, userID string, computePlatformIDs []string) error {
	url := fmt.Sprintf("%s/admin/realms/%s/users/%s", u.host, u.realm, userID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create GET request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to get user data: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to get user data: status %d, response: %s", resp.StatusCode, string(responseBody))
	}

	var userData map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&userData); err != nil {
		return fmt.Errorf("failed to decode user data: %w", err)
	}

	// Get existing attributes or create new ones
	attributes, ok := userData["attributes"].(map[string]interface{})
	if !ok {
		attributes = make(map[string]interface{})
	}

	// Convert compute platform IDs to string array format expected by Keycloak
	var platformIDs []string
	for _, id := range computePlatformIDs {
		if id != "" {
			platformIDs = append(platformIDs, id)
		}
	}

	// Update the compute platforms attribute
	attributes[consts.UserAttributeComputePlatforms] = platformIDs

	// Prepare the update payload
	updateData := map[string]interface{}{
		"attributes": attributes,
	}

	body, err := json.Marshal(updateData)
	if err != nil {
		return fmt.Errorf("failed to marshal update data: %w", err)
	}

	// Update the user with new attributes
	req, err = http.NewRequest("PUT", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create PUT request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to update user: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != StatusNoContent {
		responseBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to update user compute platforms: status %d, response: %s", resp.StatusCode, string(responseBody))
	}

	return nil
}

func (u *UserService) GetUserComputePlatforms(token, userID string) ([]string, error) {
	url := fmt.Sprintf("%s/admin/realms/%s/users/%s", u.host, u.realm, userID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get user data: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get user data: status %d, response: %s", resp.StatusCode, string(responseBody))
	}

	var userData map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&userData); err != nil {
		return nil, fmt.Errorf("failed to decode user data: %w", err)
	}

	attributes, ok := userData["attributes"].(map[string]interface{})
	if !ok {
		return []string{}, nil
	}

	computePlatformsAttr, ok := attributes[consts.UserAttributeComputePlatforms]
	if !ok {
		return []string{}, nil
	}

	platformIDs, ok := computePlatformsAttr.([]interface{})
	if !ok {
		return []string{}, nil
	}

	var result []string
	for _, id := range platformIDs {
		if idStr, ok := id.(string); ok && idStr != "" {
			result = append(result, idStr)
		}
	}

	return result, nil
}

// GetUserTeams gets the teams assigned to a user
func (u *UserService) GetUserTeams(token, userID string) ([]*model.Team, error) {
	return GetUserTeams(token, u.host, u.realm, userID)
}

func (u *UserService) DeleteUserByID(token, userID string) error {
	url := fmt.Sprintf("%s/admin/realms/%s/users/%s", u.host, u.realm, userID)
	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != StatusNoContent {
		responseBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to delete user: status %d, response: %s", resp.StatusCode, string(responseBody))
	}
	return nil
}

func (u *UserService) ListUsersByTeamID(token, teamID string) (UserList, error) {
	url := fmt.Sprintf("%s/admin/realms/%s/groups/%s/members", u.host, u.realm, teamID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get users by team ID: status %d, response: %s", resp.StatusCode, string(responseBody))
	}
	var userList UserList
	if err := json.NewDecoder(resp.Body).Decode(&userList); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return userList, nil
}
