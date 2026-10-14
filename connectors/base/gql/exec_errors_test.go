package gql

import (
	"testing"

	"github.com/vektah/gqlparser/v2/gqlerror"
)

func TestGraphQLTransportKeepsTypedErrorExtensions(t *testing.T) {
	payload := gqlErrorPayload(gqlerror.List{{Message: "invalid network", Extensions: map[string]any{
		"code": "INVALID_FIELD", "field": "networkId",
	}}})
	errors, ok := payload["errors"].([]map[string]any)
	if !ok || len(errors) != 1 {
		t.Fatalf("unexpected error payload: %#v", payload)
	}
	extensions, ok := errors[0]["extensions"].(map[string]any)
	if !ok || extensions["field"] != "networkId" {
		t.Fatalf("typed extensions lost: %#v", errors[0])
	}
}
