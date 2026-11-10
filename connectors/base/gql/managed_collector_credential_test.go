package gql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorcredential"
)

type credentialCentral struct {
	api.CentralInterface
	called    bool
	revoked   bool
	issueErr  error
	revokeErr error
}

func (c *credentialCentral) RevokeManagedCollectorCredential(_ context.Context, region, credentialID string) error {
	if region != "us-east-1" || credentialID != "credential-2" {
		panic("revoke input was not forwarded")
	}
	c.revoked = true
	return c.revokeErr
}

func (c *credentialCentral) IssueManagedCollectorCredential(_ context.Context, input api.ManualCollectorCredentialInput) (api.ManualCollectorCredentialIssue, error) {
	c.called = true
	if input.Region != "us-east-1" || input.NetworkID != "vpc-123" || !input.Rotate {
		panic("credential input was not forwarded")
	}
	if c.issueErr != nil {
		return api.ManualCollectorCredentialIssue{}, c.issueErr
	}
	return api.ManualCollectorCredentialIssue{
		CollectorID: "collector-1", CredentialID: "credential-2", SecretRef: input.SecretRef,
		AuthorizationHeader: collectorcredential.NewSecret("Bearer one-time-value"),
	}, nil
}

func TestManualCredentialErrorsKeepFieldExtensions(t *testing.T) {
	central := &credentialCentral{issueErr: errors.New("secretRef must be a Secrets Manager secret ARN")}
	exec := NewExecutor(central)
	issue := `mutation { issueManagedCollectorCredential(input: {region: "us-east-1", networkId: "vpc-123", secretRef: "bad", rotate: true}) { credentialId } }`
	result := toJSON(t, RunOperation(context.Background(), exec, issue, nil, "central", true, true))
	if !strings.Contains(result, `"code":"INVALID_FIELD"`) || !strings.Contains(result, `"field":"secretRef"`) {
		t.Fatalf("issue validation lost typed extensions: %s", result)
	}
	central.revokeErr = errors.New("credentialId is required")
	revoke := `mutation { revokeManagedCollectorCredential(input: {region: "us-east-1", credentialId: "credential-2"}) { revoked } }`
	result = toJSON(t, RunOperation(context.Background(), exec, revoke, nil, "central", true, true))
	if !strings.Contains(result, `"code":"INVALID_FIELD"`) || !strings.Contains(result, `"field":"credentialId"`) {
		t.Fatalf("revoke validation lost typed extensions: %s", result)
	}
}

func TestRevokeManagedCollectorCredentialRequiresAdmin(t *testing.T) {
	central := &credentialCentral{}
	exec := NewExecutor(central)
	query := `mutation RevokeCredential { revokeManagedCollectorCredential(input: {region: "us-east-1", credentialId: "credential-2"}) { region credentialId revoked } }`
	denied := RunOperation(context.Background(), exec, query, nil, "central", false, false)
	if central.revoked || !strings.Contains(strings.ToLower(toJSON(t, denied)), "edit privileges") {
		t.Fatalf("non-admin revoke was not rejected: %s", toJSON(t, denied))
	}
	maintainer := RunOperation(context.Background(), exec, query, nil, "central", true, false)
	if central.revoked || !strings.Contains(toJSON(t, maintainer), "authenticated admin operation") {
		t.Fatalf("maintainer revoke was not rejected: %s", toJSON(t, maintainer))
	}
	allowed := RunOperation(context.Background(), exec, query, nil, "central", true, true)
	if !central.revoked || !strings.Contains(toJSON(t, allowed), `"revoked":true`) {
		t.Fatalf("admin revoke failed: %s", toJSON(t, allowed))
	}
}

func TestHTTPGraphQLRejectsEveryMutation(t *testing.T) {
	body := []byte(`{"query":"mutation Remove { teardownManagedCollector(input: {region: \"us-east-1\"}) { deleted } }"}`)
	req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	NewHandler(&credentialCentral{}).ServeHTTP(response, req)
	if !strings.Contains(response.Body.String(), "HTTP GraphQL mutations are disabled") {
		t.Fatalf("HTTP mutation was accepted: %s", response.Body.String())
	}
}

func TestIssueManagedCollectorCredentialRequiresAdminAndReturnsOneTimeHeader(t *testing.T) {
	central := &credentialCentral{}
	exec := NewExecutor(central)
	query := `mutation IssueCredential { issueManagedCollectorCredential(input: {region: "us-east-1", networkId: "vpc-123", secretRef: "arn:aws:secretsmanager:us-east-1:123456789012:secret:collector", rotate: true}) { collectorId credentialId authorizationHeader secretRef } }`
	denied := RunOperation(context.Background(), exec, query, nil, "central", false, false)
	if central.called || !strings.Contains(strings.ToLower(toJSON(t, denied)), "edit privileges") {
		t.Fatalf("non-admin credential mutation was not rejected: %s", toJSON(t, denied))
	}
	requestBody, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		t.Fatal(err)
	}
	httpRequest := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(requestBody))
	httpRequest.Header.Set("Content-Type", "application/json")
	httpResponse := httptest.NewRecorder()
	NewHandler(central).ServeHTTP(httpResponse, httpRequest)
	if central.called || !strings.Contains(httpResponse.Body.String(), "HTTP GraphQL mutations are disabled") {
		t.Fatalf("direct HTTP credential mutation was not rejected: %s", httpResponse.Body.String())
	}
	maintainer := RunOperation(context.Background(), exec, query, nil, "central", true, false)
	if central.called || !strings.Contains(toJSON(t, maintainer), "authenticated admin operation") {
		t.Fatalf("maintainer issuance was not rejected: %s", toJSON(t, maintainer))
	}
	allowed := RunOperation(context.Background(), exec, query, nil, "central", true, true)
	var result struct {
		Issue struct {
			CollectorID, CredentialID, AuthorizationHeader, SecretRef string
		} `json:"issueManagedCollectorCredential"`
	}
	if err := json.Unmarshal([]byte(toJSON(t, allowed)), &result); err != nil {
		t.Fatal(err)
	}
	if !central.called || result.Issue.CollectorID != "collector-1" ||
		result.Issue.CredentialID != "credential-2" ||
		result.Issue.AuthorizationHeader != "Bearer one-time-value" ||
		result.Issue.SecretRef == "" {
		t.Fatalf("credential GraphQL response = %+v", result.Issue)
	}
}

func toJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
