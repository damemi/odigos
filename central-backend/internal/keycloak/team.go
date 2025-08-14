package keycloak

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/odigos-io/central-backend/graph/model"
	"github.com/odigos-io/central-backend/internal/consts"
)

type KeycloakGroup struct {
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	Attributes  map[string][]string    `json:"attributes,omitempty"`
	SubGroups   []*KeycloakGroup       `json:"subGroups,omitempty"`
	RealmRoles  []string               `json:"realmRoles,omitempty"`
	ClientRoles map[string][]string    `json:"clientRoles,omitempty"`
	Access      map[string]interface{} `json:"access,omitempty"`
}

type KeycloakGroupCreate struct {
	Name       string              `json:"name"`
	Attributes map[string][]string `json:"attributes,omitempty"`
}

type KeycloakGroupUpdate struct {
	Name       string              `json:"name"`
	Attributes map[string][]string `json:"attributes,omitempty"`
}

type KeycloakUser struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

func CreateTeam(token, keycloakHost, realm string, name string, description *string) (*model.Team, error) {
	url := fmt.Sprintf("%s/admin/realms/%s/groups", keycloakHost, realm)

	now := time.Now().Format(time.RFC3339)
	attributes := map[string][]string{
		consts.TeamAttributeCreatedAt: {now},
		consts.TeamAttributeUpdatedAt: {now},
	}

	if description != nil && *description != "" {
		attributes[consts.TeamAttributeDescription] = []string{*description}
	}

	group := KeycloakGroupCreate{
		Name:       name,
		Attributes: attributes,
	}

	body, err := json.Marshal(group)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal team data: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
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

	if resp.StatusCode != 201 {
		responseBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to create team: status %d, response: %s", resp.StatusCode, string(responseBody))
	}

	teamID, err := getTeamIDFromResponse(resp)
	if err != nil {
		return nil, fmt.Errorf("failed to get team ID: %w", err)
	}

	createdTeam := &model.Team{
		ID:               teamID,
		Name:             name,
		Description:      description,
		CreatedAt:        now,
		UpdatedAt:        now,
		Users:            []*model.User{},            // Empty array for new teams
		ComputePlatforms: []*model.ComputePlatform{}, // Empty array for new teams
	}

	return createdTeam, nil
}

func UpdateTeam(token, keycloakHost, realm, teamID, name string, description *string) (*model.Team, error) {
	url := fmt.Sprintf("%s/admin/realms/%s/groups/%s", keycloakHost, realm, teamID)

	now := time.Now().Format(time.RFC3339)
	attributes := map[string][]string{
		consts.TeamAttributeUpdatedAt: {now},
	}

	if description != nil && *description != "" {
		attributes[consts.TeamAttributeDescription] = []string{*description}
	}

	group := KeycloakGroupUpdate{
		Name:       name,
		Attributes: attributes,
	}

	body, err := json.Marshal(group)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal team data: %w", err)
	}

	req, err := http.NewRequest("PUT", url, bytes.NewReader(body))
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

	if resp.StatusCode != 204 && resp.StatusCode != 200 {
		responseBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to update team: status %d, response: %s", resp.StatusCode, string(responseBody))
	}

	updatedTeam := &model.Team{
		ID:          teamID,
		Name:        name,
		Description: description,
		UpdatedAt:   now,
	}

	return updatedTeam, nil
}

func ListTeams(token, keycloakHost, realm string) ([]*model.Team, error) {
	url := fmt.Sprintf("%s/admin/realms/%s/groups", keycloakHost, realm)

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

	if resp.StatusCode != 200 {
		responseBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to list teams: status %d, response: %s", resp.StatusCode, string(responseBody))
	}

	var groups []KeycloakGroup
	if err := json.NewDecoder(resp.Body).Decode(&groups); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	var teams []*model.Team
	for _, group := range groups {
		// Get full group details including attributes
		fullGroup, err := getGroupDetails(token, keycloakHost, realm, group.ID)
		if err != nil {
			fmt.Printf("Failed to get details for group %s: %v\n", group.ID, err)
			// Fall back to basic group info if detailed fetch fails
			fullGroup = &group
		}

		team, err := mapKeycloakGroupToTeam(*fullGroup)
		if err != nil {
			continue
		}
		teams = append(teams, team)
	}

	return teams, nil
}

func getGroupDetails(token, keycloakHost, realm, groupID string) (*KeycloakGroup, error) {
	url := fmt.Sprintf("%s/admin/realms/%s/groups/%s", keycloakHost, realm, groupID)

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

	if resp.StatusCode != 200 {
		responseBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get group details: status %d, response: %s", resp.StatusCode, string(responseBody))
	}

	var group KeycloakGroup
	if err := json.NewDecoder(resp.Body).Decode(&group); err != nil {
		return nil, fmt.Errorf("failed to decode group details: %w", err)
	}

	return &group, nil
}

func getTeamIDFromResponse(resp *http.Response) (string, error) {

	location := resp.Header.Get("Location")
	if location == "" {
		return "", fmt.Errorf("no location header in response")
	}

	lastSlashIndex := -1
	for i := len(location) - 1; i >= 0; i-- {
		if location[i] == '/' {
			lastSlashIndex = i
			break
		}
	}

	if lastSlashIndex == -1 || lastSlashIndex == len(location)-1 {
		return "", fmt.Errorf("invalid location header format: %s", location)
	}

	return location[lastSlashIndex+1:], nil
}

func mapKeycloakGroupToTeam(group KeycloakGroup) (*model.Team, error) {
	if group.ID == "" {
		return nil, fmt.Errorf("invalid team ID")
	}
	if group.Name == "" {
		return nil, fmt.Errorf("invalid team name")
	}

	// we get these in the team repository
	var users []*model.User
	var computePlatforms []*model.ComputePlatform

	createdAt := time.Now().Format(time.RFC3339)
	updatedAt := time.Now().Format(time.RFC3339)
	var description *string

	if group.Attributes != nil {
		if createdAtValues, exists := group.Attributes[consts.TeamAttributeCreatedAt]; exists && len(createdAtValues) > 0 {
			createdAt = createdAtValues[0]
		}
		if updatedAtValues, exists := group.Attributes[consts.TeamAttributeUpdatedAt]; exists && len(updatedAtValues) > 0 {
			updatedAt = updatedAtValues[0]
		}
		if descValues, exists := group.Attributes[consts.TeamAttributeDescription]; exists && len(descValues) > 0 {
			description = &descValues[0]
		}
		// Note: compute platforms will be populated by the team repository layer
		// since it needs to resolve platform IDs to actual ComputePlatform objects
	}

	return &model.Team{
		ID:               group.ID,
		Name:             group.Name,
		Description:      description,
		CreatedAt:        createdAt,
		UpdatedAt:        updatedAt,
		Users:            users,
		ComputePlatforms: computePlatforms,
	}, nil
}

func AssignTeamsToUser(token, keycloakHost, realm, userID string, teamIDs []string) error {
	userURL := fmt.Sprintf("%s/admin/realms/%s/users/%s", keycloakHost, realm, userID)

	req, err := http.NewRequest("GET", userURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request to get user: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to get user: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("user not found or access denied: status %d", resp.StatusCode)
	}

	for _, teamID := range teamIDs {
		err = addUserToTeam(token, keycloakHost, realm, userID, teamID)
		if err != nil {
			return fmt.Errorf("failed to add user to team %s: %w", teamID, err)
		}
	}

	return nil
}

func RemoveTeamsFromUser(token, keycloakHost, realm, userID string, teamIDs []string) error {
	// First verify the user exists
	userURL := fmt.Sprintf("%s/admin/realms/%s/users/%s", keycloakHost, realm, userID)

	req, err := http.NewRequest("GET", userURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request to get user: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to get user: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("user not found or access denied: status %d", resp.StatusCode)
	}

	// Remove user from each team individually
	for _, teamID := range teamIDs {
		err = removeUserFromTeam(token, keycloakHost, realm, userID, teamID)
		if err != nil {
			return fmt.Errorf("failed to remove user from team %s: %w", teamID, err)
		}
	}

	return nil
}

func removeUserFromTeam(token, keycloakHost, realm, userID, teamID string) error {
	url := fmt.Sprintf("%s/admin/realms/%s/users/%s/groups/%s", keycloakHost, realm, userID, teamID)

	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to remove user from team: status %d, response: %s", resp.StatusCode, string(responseBody))
	}

	return nil
}

func addUserToTeam(token, keycloakHost, realm, userID, teamID string) error {
	url := fmt.Sprintf("%s/admin/realms/%s/users/%s/groups/%s", keycloakHost, realm, userID, teamID)

	req, err := http.NewRequest("PUT", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to add user to team: status %d, response: %s", resp.StatusCode, string(responseBody))
	}

	return nil
}

func GetUsersForTeam(token, keycloakHost, realm, teamID string) ([]*model.User, error) {
	url := fmt.Sprintf("%s/admin/realms/%s/groups/%s/members", keycloakHost, realm, teamID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get users for team: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get users for team: status %d, response: %s", resp.StatusCode, string(responseBody))
	}

	var users []KeycloakUser
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	modelUsers := make([]*model.User, len(users))
	for i, user := range users {
		modelUsers[i] = &model.User{
			ID:       user.ID,
			Email:    user.Email,
			Username: user.Username,
			Role:     model.Role(user.Role),
		}
	}

	return modelUsers, nil
}

func GetUserTeams(token, keycloakHost, realm, userID string) ([]*model.Team, error) {
	url := fmt.Sprintf("%s/admin/realms/%s/users/%s/groups", keycloakHost, realm, userID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get user teams: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get user teams: status %d, response: %s", resp.StatusCode, string(responseBody))
	}

	var groups []KeycloakGroup
	if err := json.NewDecoder(resp.Body).Decode(&groups); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	var teams []*model.Team
	for _, group := range groups {
		// Get full group details including attributes
		fullGroup, err := getGroupDetails(token, keycloakHost, realm, group.ID)
		if err != nil {
			fmt.Printf("Failed to get details for group %s: %v\n", group.ID, err)
			// Fall back to basic group info if detailed fetch fails
			fullGroup = &group
		}

		team, err := mapKeycloakGroupToTeam(*fullGroup)
		if err != nil {
			continue
		}
		teams = append(teams, team)
	}

	return teams, nil
}

func DeleteTeam(token, keycloakHost, realm, teamID string) error {
	url := fmt.Sprintf("%s/admin/realms/%s/groups/%s", keycloakHost, realm, teamID)

	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to delete team: status %d, response: %s", resp.StatusCode, string(responseBody))
	}

	return nil
}

func GetTeamByID(token, keycloakHost, realm, teamID string) (*model.Team, error) {
	url := fmt.Sprintf("%s/admin/realms/%s/groups/%s", keycloakHost, realm, teamID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get team by id: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// Team doesn't exist - return nil, nil as expected by service layer
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get team by id: status %d, response: %s", resp.StatusCode, string(responseBody))
	}

	var group KeycloakGroup
	if err := json.NewDecoder(resp.Body).Decode(&group); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return mapKeycloakGroupToTeam(group)
}

func GetTeamByName(token, keycloakHost, realm, teamName string) (*model.Team, error) {
	// URL encode the team name to handle special characters
	encodedTeamName := url.QueryEscape(teamName)
	requestURL := fmt.Sprintf("%s/admin/realms/%s/groups?search=%s", keycloakHost, realm, encodedTeamName)

	req, err := http.NewRequest("GET", requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get team by name: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get team by name: status %d, response: %s", resp.StatusCode, string(responseBody))
	}

	var groups []KeycloakGroup
	if err := json.NewDecoder(resp.Body).Decode(&groups); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// No teams found - return nil, nil as expected by service layer
	if len(groups) == 0 {
		return nil, nil
	}

	// Search for exact name match since Keycloak search is partial
	for _, group := range groups {
		if group.Name == teamName {
			return mapKeycloakGroupToTeam(group)
		}
	}

	// No exact match found - return nil, nil as expected by service layer
	return nil, nil
}

func AssignComputePlatformsToTeam(token, keycloakHost, realm, teamID string, computePlatformIDs []string) error {
	url := fmt.Sprintf("%s/admin/realms/%s/groups/%s", keycloakHost, realm, teamID)

	// First, get the current team data to retrieve existing attributes
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create GET request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to get team data: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to get team data: status %d, response: %s", resp.StatusCode, string(responseBody))
	}

	var teamData map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&teamData); err != nil {
		return fmt.Errorf("failed to decode team data: %w", err)
	}

	// Get existing attributes or create new ones
	attributes, ok := teamData["attributes"].(map[string]interface{})
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
	attributes[consts.TeamAttributeComputePlatforms] = platformIDs

	// Prepare the update payload
	updateData := map[string]interface{}{
		"attributes": attributes,
	}

	body, err := json.Marshal(updateData)
	if err != nil {
		return fmt.Errorf("failed to marshal update data: %w", err)
	}

	// Update the team with new attributes
	req, err = http.NewRequest("PUT", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create PUT request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to update team: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		responseBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to update team compute platforms: status %d, response: %s", resp.StatusCode, string(responseBody))
	}

	return nil
}

func RemoveComputePlatformsFromTeam(token, keycloakHost, realm, teamID string, computePlatformIDs []string) error {
	url := fmt.Sprintf("%s/admin/realms/%s/groups/%s", keycloakHost, realm, teamID)

	// First, get the current team data to retrieve existing attributes
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create GET request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to get team data: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to get team data: status %d, response: %s", resp.StatusCode, string(responseBody))
	}

	var teamData map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&teamData); err != nil {
		return fmt.Errorf("failed to decode team data: %w", err)
	}

	attributes, ok := teamData["attributes"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("failed to get attributes: %w", err)
	}

	platformIDs, ok := attributes[consts.TeamAttributeComputePlatforms].([]interface{})
	if !ok {
		return fmt.Errorf("failed to get platform IDs: %w", err)
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
	attributes[consts.TeamAttributeComputePlatforms] = remainingPlatformIDs

	// Prepare the update payload
	updateData := map[string]interface{}{
		"attributes": attributes,
	}

	body, err := json.Marshal(updateData)
	if err != nil {
		return fmt.Errorf("failed to marshal update data: %w", err)
	}

	// Update the team with new attributes
	req, err = http.NewRequest("PUT", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create PUT request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to update team: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		responseBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to update team compute platforms: status %d, response: %s", resp.StatusCode, string(responseBody))
	}

	return nil
}

func GetComputePlatformIdsForTeam(token, keycloakHost, realm, teamID string) ([]string, error) {
	url := fmt.Sprintf("%s/admin/realms/%s/groups/%s", keycloakHost, realm, teamID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get compute platforms for team: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get compute platforms for team: status %d, response: %s", resp.StatusCode, string(responseBody))
	}

	var teamData map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&teamData); err != nil {
		return nil, fmt.Errorf("failed to decode team data: %w", err)
	}

	attributes, ok := teamData["attributes"].(map[string]interface{})
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
