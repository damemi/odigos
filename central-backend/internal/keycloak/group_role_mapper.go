package keycloak

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// groupRoleMapperType is the Keycloak IdP mapper type that maps an IdP token
// claim value to a realm role ("Advanced Claim to Role").
const groupRoleMapperType = "oidc-advanced-role-idp-mapper"

// aggregatedMapperPrefix names the per-(claimName, role) mappers managed by this
// feature. A single aggregated mapper holds a regex of every group value that maps
// to one role, so Keycloak's FORCE sync both grants and strips the role on login.
const aggregatedMapperPrefix = "odigos-role-"

// noMatchSentinel is a claim value that real IdP groups never equal. It is used as
// the regex value for an aggregated mapper whose role currently has no groups, so
// the FORCE mapper keeps stripping that stale role on login instead of granting it.
const noMatchSentinel = "__odigos_no_match__"

// legacyMapperPrefix names the older one-mapper-per-group format that earlier
// iterations of this feature created. These are folded into aggregated mappers and
// then deleted on reconcile. Only mappers Odigos itself created carry this prefix;
// any other "Advanced Claim to Role" mapper (e.g. one an admin configured by hand)
// is intentionally left untouched.
const legacyMapperPrefix = "group-role-"

// quotedLiteralRe extracts the literal values from an aggregated mapper's regex,
// which are stored as Java \Q..\E quoted literals (see buildValuesRegex).
var quotedLiteralRe = regexp.MustCompile(`\\Q(.*?)\\E`)

// GroupRoleMapping represents a single IdP group -> realm role mapping, backed
// by a Keycloak "Advanced Claim to Role" identity provider mapper.
type GroupRoleMapping struct {
	ID         string
	ClaimName  string
	ClaimValue string
	Role       string
	// DisplayName is an optional, human-readable label for the group (cosmetic only).
	// It is persisted alongside the values in the aggregated mapper config and never
	// affects matching, which always keys on ClaimValue.
	DisplayName string
}

// labelsConfigKey is the custom IdP-mapper config key under which we persist a
// claimValue -> display name JSON map. Keycloak preserves unknown config keys.
const labelsConfigKey = "odigosLabels"

func buildLabelsJSON(labels map[string]string) (string, error) {
	if len(labels) == 0 {
		return "", nil
	}
	b, err := json.Marshal(labels)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func parseLabelsJSON(labelsJSON string) map[string]string {
	if labelsJSON == "" {
		return map[string]string{}
	}
	var labels map[string]string
	if err := json.Unmarshal([]byte(labelsJSON), &labels); err != nil {
		return map[string]string{}
	}
	return labels
}

// advancedClaim is a single entry in the Keycloak advanced role mapper "claims" config.
type advancedClaim struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func buildAdvancedClaimsJSON(claimName, claimValue string) (string, error) {
	b, err := json.Marshal([]advancedClaim{{Key: claimName, Value: claimValue}})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func parseAdvancedClaims(claimsJSON string) (claimName string, claimValue string) {
	if claimsJSON == "" {
		return "", ""
	}
	var claims []advancedClaim
	if err := json.Unmarshal([]byte(claimsJSON), &claims); err != nil || len(claims) == 0 {
		return "", ""
	}
	return claims[0].Key, claims[0].Value
}

// aggregatedMapperName builds the stable, unique mapper name for a (claimName, role)
// pair so that all groups mapping to the same role share one mapper.
func aggregatedMapperName(claimName, role string) string {
	return fmt.Sprintf("%s%s-%s", aggregatedMapperPrefix, claimName, role)
}

// buildValuesRegex turns a set of group values into the regex used by an aggregated
// "Advanced Claim to Role" mapper (are.claim.values.regex=true). Each value is
// Java \Q..\E-quoted and anchored, then OR-joined, e.g. (^\Qa\E$|^\Qb\E$). When no
// values are given it returns an anchored sentinel that never matches a real group,
// keeping the FORCE mapper around to strip the role on login.
func buildValuesRegex(values []string) string {
	vals := values
	if len(vals) == 0 {
		vals = []string{noMatchSentinel}
	}
	parts := make([]string, 0, len(vals))
	for _, v := range vals {
		parts = append(parts, `^\Q`+v+`\E$`)
	}
	return "(" + strings.Join(parts, "|") + ")"
}

// parseValuesRegex is the inverse of buildValuesRegex: it extracts the group values
// from the stored regex, dropping the never-match sentinel.
func parseValuesRegex(regexValue string) []string {
	matches := quotedLiteralRe.FindAllStringSubmatch(regexValue, -1)
	values := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) < 2 || m[1] == noMatchSentinel {
			continue
		}
		values = append(values, m[1])
	}
	return values
}

// buildAggregatedClaimsJSON builds the "claims" config for an aggregated mapper:
// a single claim key matched against the regex of all group values for the role.
func buildAggregatedClaimsJSON(claimName string, values []string) (string, error) {
	b, err := json.Marshal([]advancedClaim{{Key: claimName, Value: buildValuesRegex(values)}})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// uniqueSortedValues removes empty/duplicate values and sorts the result so the
// generated regex is deterministic.
func uniqueSortedValues(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func (kc *KeycloakAdminService) idpMappersURL(providerAlias string) string {
	return fmt.Sprintf("%s/admin/realms/%s/identity-provider/instances/%s/mappers", kc.host, kc.realm, providerAlias)
}

func (kc *KeycloakAdminService) idpMapperURL(providerAlias, mapperID string) string {
	return fmt.Sprintf("%s/admin/realms/%s/identity-provider/instances/%s/mappers/%s", kc.host, kc.realm, providerAlias, mapperID)
}

// rawMapper is the subset of an IdP mapper representation we need to read.
type rawMapper struct {
	ID                     string            `json:"id"`
	Name                   string            `json:"name"`
	IdentityProviderMapper string            `json:"identityProviderMapper"`
	Config                 map[string]string `json:"config"`
}

// idpMapperPayload is the IdP mapper representation we send to Keycloak on
// create/update. ID is omitted on create (POST) and set on update (PUT).
type idpMapperPayload struct {
	ID                     string            `json:"id,omitempty"`
	Name                   string            `json:"name"`
	IdentityProviderAlias  string            `json:"identityProviderAlias"`
	IdentityProviderMapper string            `json:"identityProviderMapper"`
	Config                 map[string]string `json:"config"`
}

// listRawMappers returns all IdP mappers configured on the given provider.
func (kc *KeycloakAdminService) listRawMappers(adminToken, providerAlias string) ([]rawMapper, error) {
	req, err := http.NewRequest(http.MethodGet, kc.idpMappersURL(providerAlias), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create list mappers request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to list mappers: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to list mappers: status %d, response: %s", resp.StatusCode, string(body))
	}

	var raw []rawMapper
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("failed to decode mappers: %w", err)
	}
	return raw, nil
}

// ListGroupRoleMappers returns the flattened group -> role rows configured on the
// given IdP. Aggregated mappers (one per claimName+role) are exploded into one row
// per group value; legacy single-value mappers Odigos previously created (group-role-*)
// are surfaced as one row each so they can be migrated on the next reconcile. Any
// other advanced-role mapper (e.g. admin-defined) is ignored.
func (kc *KeycloakAdminService) ListGroupRoleMappers(adminToken string, providerAlias string) ([]GroupRoleMapping, error) {
	raw, err := kc.listRawMappers(adminToken, providerAlias)
	if err != nil {
		return nil, err
	}

	mappings := make([]GroupRoleMapping, 0, len(raw))
	for _, m := range raw {
		if m.IdentityProviderMapper != groupRoleMapperType {
			continue
		}
		role := m.Config["role"]
		if strings.HasPrefix(m.Name, aggregatedMapperPrefix) {
			claimName, regexValue := parseAdvancedClaims(m.Config["claims"])
			labels := parseLabelsJSON(m.Config[labelsConfigKey])
			for _, value := range parseValuesRegex(regexValue) {
				mappings = append(mappings, GroupRoleMapping{
					ID:          m.ID,
					ClaimName:   claimName,
					ClaimValue:  value,
					Role:        role,
					DisplayName: labels[value],
				})
			}
			continue
		}

		// Legacy single-value mapper created by an earlier Odigos format. Only surface
		// mappers we created (group-role-*); never touch admin-defined advanced-role
		// mappers, which must not appear as managed rows nor be deleted on reconcile.
		if !strings.HasPrefix(m.Name, legacyMapperPrefix) {
			continue
		}
		claimName, claimValue := parseAdvancedClaims(m.Config["claims"])
		if claimValue == "" {
			continue
		}
		mappings = append(mappings, GroupRoleMapping{
			ID:         m.ID,
			ClaimName:  claimName,
			ClaimValue: claimValue,
			Role:       role,
		})
	}

	return mappings, nil
}

// ReconcileGroupRoleMappers makes the IdP's group-role mappers match the desired set
// of rows. Rows are grouped by (claimName, role) into a single aggregated "Advanced
// Claim to Role" mapper whose claim value is a regex of all group values for that
// role. All mappers use syncMode FORCE, so on every login Keycloak grants the role
// when any group matches and strips it otherwise.
//
// Aggregated mappers whose role no longer has any groups are kept but rewritten with
// a never-match sentinel so they keep stripping the stale role on the next login.
// Any legacy (group-role-*) mappers are deleted once folded in.
func (kc *KeycloakAdminService) ReconcileGroupRoleMappers(adminToken, providerAlias string, rows []GroupRoleMapping) error {
	raw, err := kc.listRawMappers(adminToken, providerAlias)
	if err != nil {
		return err
	}

	type aggKey struct{ claimName, role string }

	existingAgg := make(map[aggKey]string) // (claimName, role) -> mapper id
	var legacyIDs []string
	for _, m := range raw {
		if m.IdentityProviderMapper != groupRoleMapperType {
			continue
		}
		if strings.HasPrefix(m.Name, aggregatedMapperPrefix) {
			claimName, _ := parseAdvancedClaims(m.Config["claims"])
			existingAgg[aggKey{claimName, m.Config["role"]}] = m.ID
			continue
		}
		// Only fold/delete mappers Odigos created in the older per-group format.
		// Any other advanced-role mapper (e.g. admin-defined) is left as-is.
		if strings.HasPrefix(m.Name, legacyMapperPrefix) {
			legacyIDs = append(legacyIDs, m.ID)
		}
	}

	desired := make(map[aggKey][]string)
	desiredLabels := make(map[aggKey]map[string]string)
	for _, r := range rows {
		claimName := r.ClaimName
		if claimName == "" {
			claimName = "groups"
		}
		if r.ClaimValue == "" || r.Role == "" {
			continue
		}
		k := aggKey{claimName, r.Role}
		desired[k] = append(desired[k], r.ClaimValue)
		if r.DisplayName != "" {
			if desiredLabels[k] == nil {
				desiredLabels[k] = make(map[string]string)
			}
			desiredLabels[k][r.ClaimValue] = r.DisplayName
		}
	}

	handled := make(map[aggKey]bool, len(desired))
	for k, values := range desired {
		if err := kc.upsertAggregatedMapper(adminToken, providerAlias, existingAgg[k], k.claimName, k.role, uniqueSortedValues(values), desiredLabels[k]); err != nil {
			return err
		}
		handled[k] = true
	}

	// Roles that previously had groups but no longer do: keep a never-match mapper so
	// FORCE strips the now-stale role from users on their next login.
	for k, id := range existingAgg {
		if handled[k] {
			continue
		}
		if err := kc.upsertAggregatedMapper(adminToken, providerAlias, id, k.claimName, k.role, nil, nil); err != nil {
			return err
		}
	}

	for _, id := range legacyIDs {
		if err := kc.DeleteGroupRoleMapper(adminToken, providerAlias, id); err != nil {
			return err
		}
	}

	return nil
}

// upsertAggregatedMapper creates (POST) or updates (PUT) the aggregated mapper for a
// (claimName, role) pair with the given group values, using syncMode FORCE. The
// optional labels map (claimValue -> display name) is persisted as cosmetic metadata.
func (kc *KeycloakAdminService) upsertAggregatedMapper(adminToken, providerAlias, mapperID, claimName, role string, values []string, labels map[string]string) error {
	claimsJSON, err := buildAggregatedClaimsJSON(claimName, values)
	if err != nil {
		return fmt.Errorf("failed to build claims JSON: %w", err)
	}

	config := map[string]string{
		"syncMode":               "FORCE",
		"claims":                 claimsJSON,
		"are.claim.values.regex": "true",
		"role":                   role,
	}
	if labelsJSON, err := buildLabelsJSON(labels); err != nil {
		return fmt.Errorf("failed to build labels JSON: %w", err)
	} else if labelsJSON != "" {
		config[labelsConfigKey] = labelsJSON
	}

	payload := idpMapperPayload{
		ID:                     mapperID, // omitempty -> absent on create (POST)
		Name:                   aggregatedMapperName(claimName, role),
		IdentityProviderAlias:  providerAlias,
		IdentityProviderMapper: groupRoleMapperType,
		Config:                 config,
	}

	method := http.MethodPost
	endpoint := kc.idpMappersURL(providerAlias)
	expectedStatus := http.StatusCreated
	if mapperID != "" {
		method = http.MethodPut
		endpoint = kc.idpMapperURL(providerAlias, mapperID)
		expectedStatus = http.StatusNoContent
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal mapper payload: %w", err)
	}

	req, err := http.NewRequest(method, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create upsert mapper request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to upsert mapper: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != expectedStatus && resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to upsert group role mapper: status %d, response: %s", resp.StatusCode, string(responseBody))
	}

	return nil
}

// DeleteGroupRoleMapper deletes a group -> role mapper by its ID.
func (kc *KeycloakAdminService) DeleteGroupRoleMapper(adminToken, providerAlias, mapperID string) error {
	req, err := http.NewRequest(http.MethodDelete, kc.idpMapperURL(providerAlias, mapperID), nil)
	if err != nil {
		return fmt.Errorf("failed to create delete mapper request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to delete mapper: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to delete group role mapper: status %d, response: %s", resp.StatusCode, string(responseBody))
	}

	return nil
}
