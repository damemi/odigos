package keycloak

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestBuildAndParseAdvancedClaims(t *testing.T) {
	claimsJSON, err := buildAdvancedClaimsJSON("groups", "11111111-2222-3333-4444-555555555555")
	if err != nil {
		t.Fatalf("buildAdvancedClaimsJSON returned error: %v", err)
	}

	name, value := parseAdvancedClaims(claimsJSON)
	if name != "groups" {
		t.Errorf("expected claim name %q, got %q", "groups", name)
	}
	if value != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("expected claim value %q, got %q", "11111111-2222-3333-4444-555555555555", value)
	}
}

func TestParseAdvancedClaimsEmpty(t *testing.T) {
	name, value := parseAdvancedClaims("")
	if name != "" || value != "" {
		t.Errorf("expected empty name/value for empty input, got %q/%q", name, value)
	}

	name, value = parseAdvancedClaims("not-json")
	if name != "" || value != "" {
		t.Errorf("expected empty name/value for invalid JSON, got %q/%q", name, value)
	}
}

func TestAggregatedMapperName(t *testing.T) {
	if got := aggregatedMapperName("groups", "admin"); got != "odigos-role-groups-admin" {
		t.Errorf("unexpected mapper name: %q", got)
	}
}

func TestBuildAndParseValuesRegex(t *testing.T) {
	values := []string{"11111111-2222-3333-4444-555555555555", "grp-with-special.chars+1"}
	regexValue := buildValuesRegex(values)

	// The stored regex must use anchored \Q..\E literals so special characters in
	// group values are treated literally.
	if !strings.Contains(regexValue, `\Q11111111-2222-3333-4444-555555555555\E`) {
		t.Errorf("regex missing quoted value: %q", regexValue)
	}

	got := parseValuesRegex(regexValue)
	if !reflect.DeepEqual(got, values) {
		t.Errorf("round trip mismatch: got %v, want %v", got, values)
	}
}

func TestBuildValuesRegexEmptyUsesSentinel(t *testing.T) {
	regexValue := buildValuesRegex(nil)
	if !strings.Contains(regexValue, noMatchSentinel) {
		t.Errorf("expected sentinel in empty regex, got %q", regexValue)
	}
	if got := parseValuesRegex(regexValue); len(got) != 0 {
		t.Errorf("expected no values parsed from sentinel regex, got %v", got)
	}
}

func TestUniqueSortedValues(t *testing.T) {
	got := uniqueSortedValues([]string{"b", "a", "", "b", "c"})
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("uniqueSortedValues = %v, want %v", got, want)
	}
}

func TestBuildAndParseLabelsJSON(t *testing.T) {
	if got, err := buildLabelsJSON(nil); err != nil || got != "" {
		t.Errorf("expected empty string for nil labels, got %q err %v", got, err)
	}

	labels := map[string]string{"guid-1": "odigos-admins"}
	s, err := buildLabelsJSON(labels)
	if err != nil {
		t.Fatalf("buildLabelsJSON error: %v", err)
	}
	if got := parseLabelsJSON(s); !reflect.DeepEqual(got, labels) {
		t.Errorf("labels round trip mismatch: got %v, want %v", got, labels)
	}

	if got := parseLabelsJSON(""); len(got) != 0 {
		t.Errorf("expected empty map for empty input, got %v", got)
	}
	if got := parseLabelsJSON("not-json"); len(got) != 0 {
		t.Errorf("expected empty map for invalid json, got %v", got)
	}
}

func newTestAdminService(serverURL string) *KeycloakAdminService {
	return &KeycloakAdminService{host: serverURL, realm: "central"}
}

func TestListGroupRoleMappers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		wantPath := "/admin/realms/central/identity-provider/instances/azure/mappers"
		if r.URL.Path != wantPath {
			t.Errorf("expected path %q, got %q", wantPath, r.URL.Path)
		}

		w.Header().Set("Content-Type", "application/json")
		// One aggregated mapper (two values), one legacy single-value mapper, a
		// non-role mapper, and a foreign admin-defined advanced-role mapper. Only the
		// first two are Odigos-managed; the last two must be ignored.
		_, _ = io.WriteString(w, `[
			{"id":"agg1","name":"odigos-role-groups-admin","identityProviderMapper":"oidc-advanced-role-idp-mapper","config":{"are.claim.values.regex":"true","role":"admin","claims":"[{\"key\":\"groups\",\"value\":\"(^\\\\Qa1\\\\E$|^\\\\Qa2\\\\E$)\"}]","odigosLabels":"{\"a1\":\"Admins One\"}"}},
			{"id":"legacy1","name":"group-role-grp-viewer","identityProviderMapper":"oidc-advanced-role-idp-mapper","config":{"are.claim.values.regex":"false","role":"viewer","claims":"[{\"key\":\"groups\",\"value\":\"grp-viewer\"}]"}},
			{"id":"m2","identityProviderMapper":"oidc-user-attribute-idp-mapper","config":{"claim":"email"}},
			{"id":"foreign1","name":"my-custom-role-mapper","identityProviderMapper":"oidc-advanced-role-idp-mapper","config":{"are.claim.values.regex":"false","role":"admin","claims":"[{\"key\":\"groups\",\"value\":\"hand-made\"}]"}}
		]`)
	}))
	defer srv.Close()

	kc := newTestAdminService(srv.URL)
	mappings, err := kc.ListGroupRoleMappers("token", "azure")
	if err != nil {
		t.Fatalf("ListGroupRoleMappers returned error: %v", err)
	}

	if len(mappings) != 3 {
		t.Fatalf("expected 3 rows (2 from aggregated + 1 legacy), got %d: %+v", len(mappings), mappings)
	}

	var sawA1, sawA2, sawViewer bool
	for _, m := range mappings {
		switch {
		case m.Role == "admin" && m.ClaimValue == "a1":
			sawA1 = true
			if m.DisplayName != "Admins One" {
				t.Errorf("expected display name for a1, got %q", m.DisplayName)
			}
		case m.Role == "admin" && m.ClaimValue == "a2":
			sawA2 = true
			if m.DisplayName != "" {
				t.Errorf("expected no display name for a2, got %q", m.DisplayName)
			}
		case m.Role == "viewer" && m.ClaimValue == "grp-viewer":
			sawViewer = true
		case m.ClaimValue == "hand-made":
			t.Errorf("foreign admin-defined advanced-role mapper must not be surfaced as a row")
		}
		if m.ClaimName != "groups" {
			t.Errorf("expected claim name groups, got %q", m.ClaimName)
		}
	}
	if !sawA1 || !sawA2 || !sawViewer {
		t.Errorf("missing expected rows: a1=%v a2=%v viewer=%v", sawA1, sawA2, sawViewer)
	}
}

type recordedRequest struct {
	method string
	path   string
	body   map[string]any
}

// newReconcileServer returns a test server that serves the given mapper list on GET
// and records all mutating requests.
func newReconcileServer(t *testing.T, listJSON string, recorded *[]recordedRequest, mu *sync.Mutex) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, listJSON)
			return
		}

		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		*recorded = append(*recorded, recordedRequest{method: r.Method, path: r.URL.Path, body: body})
		mu.Unlock()

		switch r.Method {
		case http.MethodPost:
			w.Header().Set("Location", r.URL.Path+"/created-id")
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
}

func claimsValueOf(t *testing.T, body map[string]any) string {
	t.Helper()
	config, _ := body["config"].(map[string]any)
	claims, _ := config["claims"].(string)
	_, value := parseAdvancedClaims(claims)
	return value
}

func labelsOf(t *testing.T, body map[string]any) map[string]string {
	t.Helper()
	config, _ := body["config"].(map[string]any)
	labels, _ := config[labelsConfigKey].(string)
	return parseLabelsJSON(labels)
}

func TestReconcileGroupRoleMappers_PersistsLabels(t *testing.T) {
	var recorded []recordedRequest
	var mu sync.Mutex
	srv := newReconcileServer(t, `[]`, &recorded, &mu)
	defer srv.Close()

	kc := newTestAdminService(srv.URL)
	rows := []GroupRoleMapping{
		{ClaimName: "groups", ClaimValue: "guid-1", Role: "viewer", DisplayName: "odigos-admins"},
	}
	if err := kc.ReconcileGroupRoleMappers("token", "azure", rows); err != nil {
		t.Fatalf("ReconcileGroupRoleMappers returned error: %v", err)
	}

	var found bool
	for _, req := range recorded {
		if req.method == http.MethodPost {
			found = true
			labels := labelsOf(t, req.body)
			if labels["guid-1"] != "odigos-admins" {
				t.Errorf("expected persisted label for guid-1, got %v", labels)
			}
		}
	}
	if !found {
		t.Errorf("expected a POST creating the viewer mapper (recorded=%+v)", recorded)
	}
}

func TestReconcileGroupRoleMappers_UpdateCreateAndDeleteLegacy(t *testing.T) {
	var recorded []recordedRequest
	var mu sync.Mutex
	listJSON := `[
		{"id":"agg-admin","name":"odigos-role-groups-admin","identityProviderMapper":"oidc-advanced-role-idp-mapper","config":{"role":"admin","are.claim.values.regex":"true","claims":"[{\"key\":\"groups\",\"value\":\"(^\\\\Qa-old\\\\E$)\"}]"}},
		{"id":"legacy1","name":"group-role-g-legacy","identityProviderMapper":"oidc-advanced-role-idp-mapper","config":{"role":"viewer","are.claim.values.regex":"false","claims":"[{\"key\":\"groups\",\"value\":\"g-legacy\"}]"}}
	]`
	srv := newReconcileServer(t, listJSON, &recorded, &mu)
	defer srv.Close()

	kc := newTestAdminService(srv.URL)
	rows := []GroupRoleMapping{
		{ClaimName: "groups", ClaimValue: "a-new", Role: "admin"},
		{ClaimName: "groups", ClaimValue: "g-legacy", Role: "viewer"},
	}
	if err := kc.ReconcileGroupRoleMappers("token", "azure", rows); err != nil {
		t.Fatalf("ReconcileGroupRoleMappers returned error: %v", err)
	}

	var putAdmin, postViewer, deleteLegacy bool
	for _, req := range recorded {
		switch {
		case req.method == http.MethodPut && strings.HasSuffix(req.path, "/mappers/agg-admin"):
			putAdmin = true
			if v := claimsValueOf(t, req.body); !strings.Contains(v, `\Qa-new\E`) {
				t.Errorf("admin mapper not updated to a-new: %q", v)
			}
		case req.method == http.MethodPost:
			postViewer = true
			if v := claimsValueOf(t, req.body); !strings.Contains(v, `\Qg-legacy\E`) {
				t.Errorf("viewer mapper not created with g-legacy: %q", v)
			}
		case req.method == http.MethodDelete && strings.HasSuffix(req.path, "/mappers/legacy1"):
			deleteLegacy = true
		}
	}
	if !putAdmin || !postViewer || !deleteLegacy {
		t.Errorf("missing expected calls: putAdmin=%v postViewer=%v deleteLegacy=%v (recorded=%+v)", putAdmin, postViewer, deleteLegacy, recorded)
	}
}

func TestReconcileGroupRoleMappers_IgnoresForeignMapper(t *testing.T) {
	var recorded []recordedRequest
	var mu sync.Mutex
	// A foreign advanced-role mapper not created by Odigos (no group-role-/odigos-role- prefix).
	listJSON := `[
		{"id":"foreign1","name":"my-custom-role-mapper","identityProviderMapper":"oidc-advanced-role-idp-mapper","config":{"role":"admin","are.claim.values.regex":"false","claims":"[{\"key\":\"groups\",\"value\":\"hand-made\"}]"}}
	]`
	srv := newReconcileServer(t, listJSON, &recorded, &mu)
	defer srv.Close()

	kc := newTestAdminService(srv.URL)
	rows := []GroupRoleMapping{
		{ClaimName: "groups", ClaimValue: "a-new", Role: "viewer"},
	}
	if err := kc.ReconcileGroupRoleMappers("token", "azure", rows); err != nil {
		t.Fatalf("ReconcileGroupRoleMappers returned error: %v", err)
	}

	for _, req := range recorded {
		if req.method == http.MethodDelete && strings.HasSuffix(req.path, "/mappers/foreign1") {
			t.Errorf("foreign admin-defined advanced-role mapper must not be deleted (recorded=%+v)", recorded)
		}
	}
}

func TestReconcileGroupRoleMappers_EmptiedRoleGetsSentinel(t *testing.T) {
	var recorded []recordedRequest
	var mu sync.Mutex
	listJSON := `[
		{"id":"agg-admin","name":"odigos-role-groups-admin","identityProviderMapper":"oidc-advanced-role-idp-mapper","config":{"role":"admin","are.claim.values.regex":"true","claims":"[{\"key\":\"groups\",\"value\":\"(^\\\\Qa-old\\\\E$)\"}]"}}
	]`
	srv := newReconcileServer(t, listJSON, &recorded, &mu)
	defer srv.Close()

	kc := newTestAdminService(srv.URL)
	// No rows: the admin role has no groups anymore and must keep a sentinel mapper.
	if err := kc.ReconcileGroupRoleMappers("token", "azure", nil); err != nil {
		t.Fatalf("ReconcileGroupRoleMappers returned error: %v", err)
	}

	var sentinelPut bool
	for _, req := range recorded {
		if req.method == http.MethodPut && strings.HasSuffix(req.path, "/mappers/agg-admin") {
			if v := claimsValueOf(t, req.body); strings.Contains(v, noMatchSentinel) {
				sentinelPut = true
			}
		}
	}
	if !sentinelPut {
		t.Errorf("expected emptied admin mapper to be rewritten with sentinel (recorded=%+v)", recorded)
	}
}

func TestDeleteGroupRoleMapper(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/mappers/m1") {
			t.Errorf("expected path to end with /mappers/m1, got %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	kc := newTestAdminService(srv.URL)
	if err := kc.DeleteGroupRoleMapper("token", "azure", "m1"); err != nil {
		t.Fatalf("DeleteGroupRoleMapper returned error: %v", err)
	}
}
