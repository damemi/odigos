package gql

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcredential"
)

type credentialCentral struct {
	api.CentralInterface
	called bool
}

func (c *credentialCentral) IssueManagedCollectorCredential(_ context.Context, input api.ManualCollectorCredentialInput) (api.ManualCollectorCredentialIssue, error) {
	c.called = true
	if input.Region != "us-east-1" || input.NetworkID != "vpc-123" || !input.Rotate {
		panic("credential input was not forwarded")
	}
	return api.ManualCollectorCredentialIssue{
		CollectorID: "collector-1", CredentialID: "credential-2", SecretRef: input.SecretRef,
		AuthorizationHeader: collectorcredential.NewSecret("Bearer one-time-value"),
	}, nil
}

func TestIssueManagedCollectorCredentialRequiresAdminAndReturnsOneTimeHeader(t *testing.T) {
	central := &credentialCentral{}
	exec := NewExecutor(central)
	query := `mutation IssueCredential { issueManagedCollectorCredential(input: {region: "us-east-1", networkId: "vpc-123", secretRef: "arn:aws:secretsmanager:us-east-1:123456789012:secret:collector", rotate: true}) { collectorId credentialId authorizationHeader secretRef } }`
	denied := RunOperation(context.Background(), exec, query, nil, "central", false)
	if central.called || !strings.Contains(strings.ToLower(toJSON(t, denied)), "admin privileges") {
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
	if central.called || !strings.Contains(httpResponse.Body.String(), "authenticated admin operation") {
		t.Fatalf("direct HTTP credential mutation was not rejected: %s", httpResponse.Body.String())
	}
	allowed := RunOperation(context.Background(), exec, query, nil, "central", true)
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
