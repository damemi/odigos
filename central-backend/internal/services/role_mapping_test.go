package services

import (
	"reflect"
	"testing"

	"github.com/odigos-io/central-backend/internal/keycloak"
)

func TestRowIDRoundTrip(t *testing.T) {
	row := keycloak.GroupRoleMapping{ClaimName: "groups", Role: "admin", ClaimValue: "grp-1"}
	id := encodeRowID(row)

	decoded, err := decodeRowID(id)
	if err != nil {
		t.Fatalf("decodeRowID returned error: %v", err)
	}
	if decoded.ClaimName != row.ClaimName || decoded.Role != row.Role || decoded.ClaimValue != row.ClaimValue {
		t.Errorf("round trip mismatch: got %+v, want %+v", decoded, row)
	}
}

func TestDecodeRowIDInvalid(t *testing.T) {
	if _, err := decodeRowID("not-base64!!!"); err == nil {
		t.Errorf("expected error for invalid id")
	}
}

func TestValidateClaimValue(t *testing.T) {
	if err := validateClaimValue(""); err == nil {
		t.Errorf("expected error for empty claim value")
	}
	if err := validateClaimValue(`bad\Evalue`); err == nil {
		t.Errorf(`expected error for claim value containing \E`)
	}
	if err := validateClaimValue("8ada902a-f155-4d88-a227-57baaef489ff"); err != nil {
		t.Errorf("expected GUID to be valid, got %v", err)
	}
}

func TestRemoveByGroup(t *testing.T) {
	rows := []keycloak.GroupRoleMapping{
		{ClaimName: "groups", ClaimValue: "a", Role: "admin"},
		{ClaimName: "groups", ClaimValue: "b", Role: "viewer"},
		{ClaimName: "teams", ClaimValue: "a", Role: "maintainer"},
	}

	got := removeByGroup(rows, "groups", "a")
	want := []keycloak.GroupRoleMapping{
		{ClaimName: "groups", ClaimValue: "b", Role: "viewer"},
		{ClaimName: "teams", ClaimValue: "a", Role: "maintainer"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("removeByGroup = %+v, want %+v", got, want)
	}
}
