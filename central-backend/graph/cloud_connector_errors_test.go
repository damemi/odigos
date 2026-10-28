package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/odigos-io/central-backend/internal/logger"
	"github.com/odigos-io/central-backend/internal/utils"
)

// Exercise the generated GraphQL transport, real provider tester and result
// mapper together: failures must return a check and reach logs without inputs.
func TestCreateConnectorProviderErrorsReachGraphQLAndLogs(t *testing.T) {
	aws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(403)
		fmt.Fprint(w, `<ErrorResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><Error><Type>Sender</Type><Code>AccessDenied</Code><Message>Cannot assume role using caller-key caller-secret</Message></Error><RequestId>aws-request-123</RequestId></ErrorResponse>`)
	}))
	defer aws.Close()
	t.Setenv("AWS_ENDPOINT_URL", aws.URL)
	t.Setenv("AWS_MAX_ATTEMPTS", "1")
	for _, tc := range []struct {
		provider   string
		creds      map[string]string
		diagnostic string
	}{
		{"aws", map[string]string{"accessKeyId": "caller-key", "secretAccessKey": "caller-secret", "roleArn": "arn:aws:iam::123456789012:role/Connector", "externalId": "customer-id"}, "AccessDenied"},
		{"gcp", map[string]string{"credentialsJson": "{invalid-json"}, "failed to initialize GCP credentials:"},
		{"azure", map[string]string{"tenantId": "invalid tenant", "clientId": "client-id", "clientSecret": "caller-secret"}, "failed to initialize Azure credentials:"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			var logs bytes.Buffer
			original := logger.Log
			logger.Log = slog.New(slog.NewJSONHandler(&logs, nil))
			t.Cleanup(func() { logger.Log = original })
			body, _ := json.Marshal(map[string]any{"query": `mutation Create($input: CreateCloudConnectorInput!) { createCloudConnector(input:$input) { created test { success checks { passed message remediation } } } }`, "variables": map[string]any{"input": map[string]any{"provider": tc.provider, "displayName": "test", "credentials": tc.creds, "expectedAccountId": "123456789012", "capabilities": []any{}}}})
			req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Request-ID", "central-request-123")
			req = req.WithContext(utils.SetLiveRoleInContext(context.Background(), "ADMIN"))
			rec := httptest.NewRecorder()
			handler.NewDefaultServer(NewExecutableSchema(Config{Resolvers: &Resolver{}})).ServeHTTP(rec, req)
			var response struct {
				Errors []any `json:"errors"`
				Data   struct {
					Create struct {
						Created bool
						Test    struct {
							Success bool
							Checks  []struct {
								Passed  bool
								Message string
							}
						}
					} `json:"createCloudConnector"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if rec.Code != 200 || len(response.Errors) > 0 || response.Data.Create.Created || response.Data.Create.Test.Success || len(response.Data.Create.Test.Checks) == 0 {
				t.Fatalf("failure lost: %s", rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.diagnostic) || !strings.Contains(logs.String(), tc.diagnostic) || !strings.Contains(logs.String(), "central-request-123") {
				t.Fatalf("diagnostic/log missing: response=%s logs=%s", rec.Body.String(), logs.String())
			}
			for _, secret := range []string{"caller-key", "caller-secret", "{invalid-json"} {
				if strings.Contains(rec.Body.String()+logs.String(), secret) {
					t.Fatalf("credential leaked: %s", tc.provider)
				}
			}
		})
	}
}

func TestInternalConnectorErrorIsLoggedAndRedacted(t *testing.T) {
	var logs bytes.Buffer
	original := logger.Log
	logger.Log = slog.New(slog.NewJSONHandler(&logs, nil))
	t.Cleanup(func() { logger.Log = original })
	err := cloudConnectorOperationError(context.Background(), "createCloudConnector", "aws", map[string]string{"secretAccessKey": "caller-secret"}, fmt.Errorf("failed to save connector in Central: admission denied caller-secret"))
	if !strings.Contains(err.Error(), "admission denied") || !strings.Contains(logs.String(), "Cloud connector operation failed") || strings.Contains(err.Error()+logs.String(), "caller-secret") {
		t.Fatal("internal error lost or secret exposed")
	}
}
