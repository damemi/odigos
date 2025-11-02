package keycloak

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

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

type KeycloakUserService struct {
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

func NewKeycloakUserService() *KeycloakUserService {
	return &KeycloakUserService{
		host:  consts.GetKeycloakHost(),
		realm: consts.KeycloakRealm,
	}
}

func (kc *KeycloakUserService) GetUserRole(token string, userID string) (string, error) {
	rolesURL := fmt.Sprintf("%s/admin/realms/%s/users/%s/role-mappings/realm", kc.host, kc.realm, userID)
	req, err := http.NewRequest("GET", rolesURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create roles request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
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

	hasRole := map[string]bool{}
	// var roleNames []string
	for _, role := range roles {
		if name, ok := role["name"].(string); ok {
			hasRole[strings.ToLower(name)] = true
			// roleNames = append(roleNames, name)
		}
	}

	if hasRole[strings.ToLower(RoleOwner)] {
		return RoleOwner, nil
	}
	if hasRole[strings.ToLower(RoleAdmin)] {
		return RoleAdmin, nil
	}
	if hasRole[strings.ToLower(RoleMaintainer)] {
		return RoleMaintainer, nil
	}
	if hasRole[strings.ToLower(RoleViewer)] {
		return RoleViewer, nil
	}

	return RoleViewer, nil
}

func (kc *KeycloakUserService) CheckUserNeedsPasswordChange(token string, email string) (bool, error) {
	users, err := kc.FindUsersByEmail(token, email)
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

func (kc *KeycloakUserService) GetUserInfo(token string) (UserInfo, error) {
	url := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/userinfo", kc.host, kc.realm)
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

func (kc *KeycloakUserService) AddUserToRealm(token string, username string, email string, password string) error {
	url := fmt.Sprintf("%s/admin/realms/%s/users", kc.host, kc.realm)
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

func (kc *KeycloakUserService) AddRealmRoleToUser(token string, email, roleName string) error {
	searchUserURL := fmt.Sprintf("%s/admin/realms/%s/users?email=%s", kc.host, kc.realm, email)
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

	searchRoleURL := fmt.Sprintf("%s/admin/realms/%s/roles/%s", kc.host, kc.realm, roleName)
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

	assignRoleURL := fmt.Sprintf("%s/admin/realms/%s/users/%s/role-mappings/realm", kc.host, kc.realm, userID)
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

func (kc *KeycloakUserService) RemoveRealmRoleFromUser(token string, email, roleName string) error {
	searchUserURL := fmt.Sprintf("%s/admin/realms/%s/users?email=%s", kc.host, kc.realm, email)
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

	searchRoleURL := fmt.Sprintf("%s/admin/realms/%s/roles/%s", kc.host, kc.realm, roleName)
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

	removeRoleURL := fmt.Sprintf("%s/admin/realms/%s/users/%s/role-mappings/realm", kc.host, kc.realm, userID)
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
	req, err = http.NewRequest("DELETE", removeRoleURL, bytes.NewReader(body))
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
		return fmt.Errorf("failed to remove role from user: %s", string(body))
	}
	return nil
}

func (kc *KeycloakUserService) UpdateUser(token string, userID string, input *model.WriteUserInput) error {
	// Only marshal fields that Keycloak expects, not the full input (which may include "role" etc.)
	payload := make(map[string]interface{})
	if input.Email != nil {
		payload["email"] = *input.Email
		payload["username"] = *input.Email
	}
	// Add more fields as needed, but do NOT include "role" or other non-Keycloak fields

	url := fmt.Sprintf("%s/admin/realms/%s/users/%s", kc.host, kc.realm, userID)
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("PUT", url, bytes.NewReader(body))
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
	if resp.StatusCode != StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to update user: %s", string(body))
	}
	return nil
}

func (kc *KeycloakUserService) FindUsersByEmail(token string, email string) (UserList, error) {
	searchURL := fmt.Sprintf("%s/admin/realms/%s/users?email=%s", kc.host, kc.realm, email)
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

func (kc *KeycloakUserService) UpdateUserPassword(token string, email, newPassword string) error {
	users, err := kc.FindUsersByEmail(token, email)
	if err != nil || len(users) == 0 {
		return fmt.Errorf("failed to find user by email: %w", err)
	}
	userData := users[0]
	userID := userData["id"].(string)

	url := fmt.Sprintf("%s/admin/realms/%s/users/%s/reset-password", kc.host, kc.realm, userID)

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

func (kc *KeycloakUserService) CheckUserDetails(token string, email string) (map[string]interface{}, error) {
	searchURL := fmt.Sprintf("%s/admin/realms/%s/users?email=%s", kc.host, kc.realm, email)
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

func (kc *KeycloakUserService) SetUserPasswordRequirement(token string, email string, required bool) error {
	users, err := kc.FindUsersByEmail(token, email)
	if err != nil || len(users) == 0 {
		return fmt.Errorf("failed to find user by email: %w", err)
	}
	userData := users[0]
	userID := userData["id"].(string)

	url := fmt.Sprintf("%s/admin/realms/%s/users/%s", kc.host, kc.realm, userID)

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

func (kc *KeycloakUserService) GetUserRequiredActions(token string, email string) ([]string, error) {
	searchURL := fmt.Sprintf("%s/admin/realms/%s/users?email=%s", kc.host, kc.realm, email)
	req, err := http.NewRequest("GET", searchURL, nil)
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

func (kc *KeycloakUserService) ListUsers(token string) (UserList, error) {
	url := fmt.Sprintf("%s/admin/realms/%s/users", kc.host, kc.realm)

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

func (kc *KeycloakUserService) AssignComputePlatformsToUser(token string, userID string, computePlatformIDs []string) error {
	url := fmt.Sprintf("%s/admin/realms/%s/users/%s", kc.host, kc.realm, userID)

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

func (kc *KeycloakUserService) RemoveComputePlatformsFromUser(token string, userID string, computePlatformIDs []string) error {
	url := fmt.Sprintf("%s/admin/realms/%s/users/%s", kc.host, kc.realm, userID)

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

	attributes, ok := userData["attributes"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("failed to get attributes: %w", err)
	}

	computePlatformsAttr, ok := attributes[consts.UserAttributeComputePlatforms]
	if !ok {
		return fmt.Errorf("failed to get platform IDs: %w", err)
	}

	var platformIDs []interface{}
	// Handle both string array and interface array formats
	if stringArray, ok := computePlatformsAttr.([]string); ok {
		for _, id := range stringArray {
			platformIDs = append(platformIDs, id)
		}
	} else if interfaceArray, ok := computePlatformsAttr.([]interface{}); ok {
		platformIDs = interfaceArray
	} else {
		return fmt.Errorf("failed to get platform IDs: unexpected format")
	}

	// Filter out the compute platform IDs that are in the remove list
	var remainingPlatformIDs []string
	for _, id := range platformIDs {
		idStr, ok := id.(string)
		if !ok {
			continue // skip non-string IDs
		}
		found := false
		for _, removeID := range computePlatformIDs {
			if idStr == removeID {
				found = true
				break
			}
		}
		if !found {
			remainingPlatformIDs = append(remainingPlatformIDs, idStr)
		}
	}

	// Update the compute platforms attribute
	attributes[consts.UserAttributeComputePlatforms] = remainingPlatformIDs

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

func (kc *KeycloakUserService) GetComputePlatformIdsForUser(token string, userID string) ([]string, error) {
	url := fmt.Sprintf("%s/admin/realms/%s/users/%s", kc.host, kc.realm, userID)

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

	// Try to handle as string array first (new format)
	if stringArray, ok := computePlatformsAttr.([]string); ok {
		var result []string
		for _, id := range stringArray {
			if id != "" {
				result = append(result, id)
			}
		}
		return result, nil
	}

	// Fall back to interface array (old format for backwards compatibility)
	if interfaceArray, ok := computePlatformsAttr.([]interface{}); ok {
		var result []string
		for _, id := range interfaceArray {
			if idStr, ok := id.(string); ok && idStr != "" {
				result = append(result, idStr)
			}
		}
		return result, nil
	}

	return []string{}, nil
}

func (kc *KeycloakUserService) GetUserByID(token string, userID string) (UserInfo, error) {
	url := fmt.Sprintf("%s/admin/realms/%s/users/%s", kc.host, kc.realm, userID)
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
		return nil, fmt.Errorf("failed to get user by ID: status %d, response: %s", resp.StatusCode, string(responseBody))
	}
	var userData UserInfo
	if err := json.NewDecoder(resp.Body).Decode(&userData); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return userData, nil
}

func (kc *KeycloakUserService) DeleteUserByID(token string, userID string) error {
	url := fmt.Sprintf("%s/admin/realms/%s/users/%s", kc.host, kc.realm, userID)
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

func (kc *KeycloakUserService) ListUsersByTeamID(token string, teamID string) (UserList, error) {
	url := fmt.Sprintf("%s/admin/realms/%s/groups/%s/members", kc.host, kc.realm, teamID)
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
