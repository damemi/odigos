package gql

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
)

type instructionsCentral struct{ api.CentralInterface }

func (instructionsCentral) ManagedCollectorInstructions(_ context.Context, region string) (*api.ManagedCollectorInstructions, error) {
	if region != "us-east-1" {
		return nil, nil
	}
	return &api.ManagedCollectorInstructions{
		Region: region, HostNetworkID: "vpc-123", Mode: "manual",
		UpdatedAt: time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC),
		Documents: []api.InstructionDocument{{ID: "aws-fargate-collector-terraform", Variant: "terraform", Content: "resource {}"}},
	}, nil
}

func TestManagedCollectorInstructionsGraphQLQuery(t *testing.T) {
	query := []byte(`{"query":"query { managedCollectorInstructions(region: \"us-east-1\") { region networkId mode documents { variant content } } }"}`)
	req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(query))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	NewHandler(instructionsCentral{}).ServeHTTP(response, req)
	var payload struct {
		Data struct {
			Instructions struct {
				Region, NetworkID, Mode string
				Documents               []struct{ Variant, Content string }
			} `json:"managedCollectorInstructions"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	got := payload.Data.Instructions
	if len(payload.Errors) != 0 || got.Region != "us-east-1" || got.NetworkID != "vpc-123" ||
		got.Mode != "manual" || len(got.Documents) != 1 || got.Documents[0].Variant != "terraform" {
		t.Fatalf("GraphQL instructions = %+v, errors = %+v", got, payload.Errors)
	}
}
