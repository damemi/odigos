package keycloak

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

	var users []*model.User
	userService := NewUserService()
	for _, group := range groups {
		userList, err := userService.ListUsersByTeamID(token, group.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to list users by team ID: %w", err)
		}

		for _, user := range userList {
			// Safely extract values with nil checks
			id, _ := user["id"].(string)
			email, _ := user["email"].(string)
			username, _ := user["username"].(string)
			role, _ := user["role"].(string)

			// Skip users with missing required fields
			if id == "" || email == "" || username == "" {
				continue
			}

			users = append(users, &model.User{
				ID:       id,
				Email:    email,
				Username: username,
				Role:     model.Role(role),
				Teams: []*model.Team{
					{
						ID:   group.ID,
						Name: group.Name,
					},
				},
			})
		}
	}

	// For now, return empty arrays for computePlatforms
	// In a real implementation, you would fetch these from Keycloak or other services
	var computePlatforms []*model.ComputePlatform

	var teams []*model.Team
	for _, group := range groups {
		team, err := mapKeycloakGroupToTeam(group, users, computePlatforms)
		if err != nil {
			continue
		}
		teams = append(teams, team)
	}

	return teams, nil
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

func mapKeycloakGroupToTeam(group KeycloakGroup, users []*model.User, computePlatforms []*model.ComputePlatform) (*model.Team, error) {
	if group.ID == "" {
		return nil, fmt.Errorf("invalid team ID")
	}

	if group.Name == "" {
		return nil, fmt.Errorf("invalid team name")
	}

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
		team, err := mapKeycloakGroupToTeam(group, []*model.User{}, []*model.ComputePlatform{})
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
