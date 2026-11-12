package services

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/odigos-io/odigos/central-backend/graph/model"
	"github.com/odigos-io/odigos/central-backend/internal/keycloak"
	"github.com/odigos-io/odigos/central-backend/internal/logger"
	"github.com/odigos-io/odigos/central-backend/internal/utils"
)

// defaultGroupClaimName is the IdP token claim used to carry group membership
// when the admin does not specify one explicitly. Azure/Entra emits group
// membership in the "groups" claim.
const defaultGroupClaimName = "groups"

// RoleMappingService manages IdP group -> Odigos role mappings, persisted as
// Keycloak "Advanced Claim to Role" identity provider mappers.
type RoleMappingService struct {
	adminService *AdminService
	kcAdmin      *keycloak.KeycloakAdminService
	// writeMu serializes the list -> mutate -> reconcile cycle so concurrent
	// create/update/delete calls cannot clobber each other (lost update).
	writeMu sync.Mutex
}

func NewRoleMappingService(adminService *AdminService) *RoleMappingService {
	return &RoleMappingService{
		adminService: adminService,
		kcAdmin:      keycloak.NewKeycloakAdminService(),
	}
}

// providerAliasFromID converts a GraphQL AuthProviderId into the lowercase
// Keycloak IdP alias (matching the convention used by AdminService).
func providerAliasFromID(providerID model.AuthProviderID) string {
	return strings.ToLower(string(providerID))
}

func claimNameOrDefault(claimName *string) string {
	if claimName != nil && *claimName != "" {
		return *claimName
	}
	return defaultGroupClaimName
}

func displayNameOrEmpty(displayName *string) string {
	if displayName != nil {
		return *displayName
	}
	return ""
}

// validateClaimValue rejects empty values and the only sequence that could break
// the Java \Q..\E-quoted regex an aggregated mapper is built from. A literal `\E`
// inside the value would terminate the quote early and corrupt (or inject into)
// the stored regex. Real IdP group identifiers never contain it.
func validateClaimValue(claimValue string) error {
	if claimValue == "" {
		return fmt.Errorf("claimValue is required")
	}
	if strings.Contains(claimValue, `\E`) {
		return fmt.Errorf(`claimValue must not contain the sequence \E`)
	}
	return nil
}

// providerNotConfiguredError returns a user-facing error explaining that the IdP
// must be set up before group role mappings can be managed. Keycloak's mapper API
// lives under an existing identity provider instance, so without one it returns a
// confusing 404.
func providerNotConfiguredError(providerID model.AuthProviderID) error {
	return fmt.Errorf("identity provider %q is not configured. Set up SSO for this provider before managing group role mappings", string(providerID))
}

// providerExists reports whether the IdP instance for the given provider is
// configured in Keycloak.
func (svc *RoleMappingService) providerExists(providerID model.AuthProviderID) (bool, error) {
	idp, err := svc.adminService.GetIdentityProvider(providerAliasFromID(providerID))
	if err != nil {
		return false, fmt.Errorf("failed to check identity provider: %w", err)
	}
	return idp != nil, nil
}

// rowID is the stable, self-describing identity of a single group-role row. Because
// rows are stored aggregated (one Keycloak mapper per claimName+role), there is no
// per-row Keycloak ID; the UI uses this opaque encoded value to edit/delete a row.
type rowID struct {
	ClaimName  string `json:"c"`
	Role       string `json:"r"`
	ClaimValue string `json:"v"`
}

func encodeRowID(r keycloak.GroupRoleMapping) string {
	b, _ := json.Marshal(rowID{ClaimName: r.ClaimName, Role: r.Role, ClaimValue: r.ClaimValue})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeRowID(id string) (rowID, error) {
	b, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return rowID{}, fmt.Errorf("invalid mapping id: %w", err)
	}
	var r rowID
	if err := json.Unmarshal(b, &r); err != nil {
		return rowID{}, fmt.Errorf("invalid mapping id: %w", err)
	}
	return r, nil
}

// listRows returns the current flattened group-role rows, defaulting the claim name.
func (svc *RoleMappingService) listRows(adminToken, alias string) ([]keycloak.GroupRoleMapping, error) {
	rows, err := svc.kcAdmin.ListGroupRoleMappers(adminToken, alias)
	if err != nil {
		return nil, fmt.Errorf("failed to list group role mappings: %w", err)
	}
	for i := range rows {
		if rows[i].ClaimName == "" {
			rows[i].ClaimName = defaultGroupClaimName
		}
	}
	return rows, nil
}

// removeByGroup drops any row matching the given (claimName, claimValue). A single
// group maps to a single role, so claimName+claimValue uniquely identifies a row.
func removeByGroup(rows []keycloak.GroupRoleMapping, claimName, claimValue string) []keycloak.GroupRoleMapping {
	out := rows[:0:0]
	for _, r := range rows {
		if r.ClaimName == claimName && r.ClaimValue == claimValue {
			continue
		}
		out = append(out, r)
	}
	return out
}

func (svc *RoleMappingService) GetGroupRoleMappings(providerID model.AuthProviderID) ([]*model.GroupRoleMapping, error) {
	// If the IdP isn't configured yet there are no mappings to list. Return an
	// empty list rather than surfacing Keycloak's 404 so the UI loads cleanly.
	exists, err := svc.providerExists(providerID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return []*model.GroupRoleMapping{}, nil
	}

	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return nil, fmt.Errorf("failed to get admin token: %w", err)
	}

	alias := providerAliasFromID(providerID)
	mappings, err := svc.listRows(adminToken, alias)
	if err != nil {
		return nil, err
	}

	result := make([]*model.GroupRoleMapping, 0, len(mappings))
	for _, m := range mappings {
		mapping := &model.GroupRoleMapping{
			ID:         encodeRowID(m),
			ProviderID: providerID,
			ClaimName:  m.ClaimName,
			ClaimValue: m.ClaimValue,
			Role:       utils.ConvertStringRoleToModel(m.Role),
		}
		if m.DisplayName != "" {
			displayName := m.DisplayName
			mapping.DisplayName = &displayName
		}
		result = append(result, mapping)
	}

	return result, nil
}

// HasAnyGroupRoleMappings reports whether any configured identity provider has at
// least one group→role mapping. Used at SSO SignIn to decide whether an unmapped
// user should be denied (mappings exist) or defaulted to Viewer (none exist).
func (svc *RoleMappingService) HasAnyGroupRoleMappings() (bool, error) {
	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return false, fmt.Errorf("failed to get admin token: %w", err)
	}

	for _, providerID := range model.AllAuthProviderID {
		exists, err := svc.providerExists(providerID)
		if err != nil {
			return false, err
		}
		if !exists {
			continue
		}
		rows, err := svc.listRows(adminToken, providerAliasFromID(providerID))
		if err != nil {
			return false, err
		}
		if len(rows) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// ReconcileExistingMappers folds any legacy per-group mappers into the aggregated
// format for every configured provider. It is best-effort and idempotent, intended
// to run once at startup; ongoing writes reconcile on their own.
func (svc *RoleMappingService) ReconcileExistingMappers() {
	// Runs in its own goroutine at startup; never let a panic take down the process.
	defer func() {
		if r := recover(); r != nil {
			logger.Log.Error("role mapping migration: recovered from panic", "panic", r)
		}
	}()

	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		logger.Log.Error("role mapping migration: failed to get admin token", "error", err)
		return
	}

	for _, providerID := range model.AllAuthProviderID {
		exists, err := svc.providerExists(providerID)
		if err != nil || !exists {
			continue
		}
		alias := providerAliasFromID(providerID)

		// Serialize against live create/update/delete writes.
		svc.writeMu.Lock()
		rows, err := svc.listRows(adminToken, alias)
		if err != nil {
			svc.writeMu.Unlock()
			logger.Log.Error("role mapping migration: failed to list mappers", "provider", providerID, "error", err)
			continue
		}
		err = svc.kcAdmin.ReconcileGroupRoleMappers(adminToken, alias, rows)
		svc.writeMu.Unlock()
		if err != nil {
			logger.Log.Error("role mapping migration: failed to reconcile mappers", "provider", providerID, "error", err)
			continue
		}
		logger.Log.Info("role mapping migration: reconciled mappers", "provider", providerID, "rows", len(rows))
	}
}

func (svc *RoleMappingService) CreateGroupRoleMapping(input *model.WriteGroupRoleMappingInput) (*model.GeneralResult, error) {
	if input == nil {
		return nil, fmt.Errorf("input is required")
	}
	if err := validateClaimValue(input.ClaimValue); err != nil {
		return nil, err
	}
	if input.Role == model.RoleNoAccess {
		return nil, fmt.Errorf("no-access cannot be used for group role mappings")
	}

	if result, err := svc.requireProvider(input.ProviderID); result != nil || err != nil {
		return result, err
	}

	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return nil, fmt.Errorf("failed to get admin token: %w", err)
	}

	alias := providerAliasFromID(input.ProviderID)
	claimName := claimNameOrDefault(input.ClaimName)
	role := utils.ConvertModelRoleToString(input.Role)

	svc.writeMu.Lock()
	defer svc.writeMu.Unlock()

	rows, err := svc.listRows(adminToken, alias)
	if err != nil {
		return nil, err
	}

	// Enforce one role per group: replace any existing mapping for this group.
	rows = removeByGroup(rows, claimName, input.ClaimValue)
	rows = append(rows, keycloak.GroupRoleMapping{ClaimName: claimName, ClaimValue: input.ClaimValue, Role: role, DisplayName: displayNameOrEmpty(input.DisplayName)})

	if err := svc.kcAdmin.ReconcileGroupRoleMappers(adminToken, alias, rows); err != nil {
		return nil, fmt.Errorf("failed to create group role mapping: %w", err)
	}

	return &model.GeneralResult{Success: true}, nil
}

// requireProvider returns a non-nil failed GeneralResult when the IdP is not
// configured, so callers can short-circuit with a clean, user-facing message.
func (svc *RoleMappingService) requireProvider(providerID model.AuthProviderID) (*model.GeneralResult, error) {
	exists, err := svc.providerExists(providerID)
	if err != nil {
		return nil, err
	}
	if !exists {
		msg := providerNotConfiguredError(providerID).Error()
		return &model.GeneralResult{Success: false, Error: &msg}, nil
	}
	return nil, nil
}

func (svc *RoleMappingService) UpdateGroupRoleMapping(id string, input *model.WriteGroupRoleMappingInput) (*model.GeneralResult, error) {
	if id == "" {
		return nil, fmt.Errorf("mapping id is required")
	}
	if input == nil {
		return nil, fmt.Errorf("input is required")
	}
	if err := validateClaimValue(input.ClaimValue); err != nil {
		return nil, err
	}
	if input.Role == model.RoleNoAccess {
		return nil, fmt.Errorf("no-access cannot be used for group role mappings")
	}

	if result, err := svc.requireProvider(input.ProviderID); result != nil || err != nil {
		return result, err
	}

	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return nil, fmt.Errorf("failed to get admin token: %w", err)
	}

	alias := providerAliasFromID(input.ProviderID)
	claimName := claimNameOrDefault(input.ClaimName)
	role := utils.ConvertModelRoleToString(input.Role)

	old, err := decodeRowID(id)
	if err != nil {
		return nil, err
	}

	svc.writeMu.Lock()
	defer svc.writeMu.Unlock()

	rows, err := svc.listRows(adminToken, alias)
	if err != nil {
		return nil, err
	}

	// Drop the original row and any row for the (possibly new) target group, then
	// add the updated row. This keeps one role per group even if the group changed.
	rows = removeByGroup(rows, old.ClaimName, old.ClaimValue)
	rows = removeByGroup(rows, claimName, input.ClaimValue)
	rows = append(rows, keycloak.GroupRoleMapping{ClaimName: claimName, ClaimValue: input.ClaimValue, Role: role, DisplayName: displayNameOrEmpty(input.DisplayName)})

	if err := svc.kcAdmin.ReconcileGroupRoleMappers(adminToken, alias, rows); err != nil {
		return nil, fmt.Errorf("failed to update group role mapping: %w", err)
	}

	return &model.GeneralResult{Success: true}, nil
}

func (svc *RoleMappingService) DeleteGroupRoleMapping(providerID model.AuthProviderID, id string) (*model.GeneralResult, error) {
	if id == "" {
		return nil, fmt.Errorf("mapping id is required")
	}

	if result, err := svc.requireProvider(providerID); result != nil || err != nil {
		return result, err
	}

	adminToken, err := svc.adminService.GetAdminToken()
	if err != nil {
		return nil, fmt.Errorf("failed to get admin token: %w", err)
	}

	alias := providerAliasFromID(providerID)

	target, err := decodeRowID(id)
	if err != nil {
		return nil, err
	}

	svc.writeMu.Lock()
	defer svc.writeMu.Unlock()

	rows, err := svc.listRows(adminToken, alias)
	if err != nil {
		return nil, err
	}

	rows = removeByGroup(rows, target.ClaimName, target.ClaimValue)

	if err := svc.kcAdmin.ReconcileGroupRoleMappers(adminToken, alias, rows); err != nil {
		return nil, fmt.Errorf("failed to delete group role mapping: %w", err)
	}

	return &model.GeneralResult{Success: true}, nil
}
