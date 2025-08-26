package graphql

import (
	"context"
	"os"
	"strings"

	"github.com/machinebox/graphql"
)

type GraphQLClient struct {
	client *graphql.Client
}

func NewGraphQLClient() *GraphQLClient {
	frontendBackendURL := os.Getenv("FRONTEND_BACKEND_URL")
	if frontendBackendURL == "" {
		if os.Getenv("KUBERNETES_SERVICE_HOST") == "" {
			frontendBackendURL = "http://localhost:8085/graphql"
		} else {
			frontendBackendURL = "http://ui.odigos-system:3000/graphql"
		}
	}

	client := graphql.NewClient(frontendBackendURL)

	return &GraphQLClient{client: client}
}

// ForwardGraphQLRequest executes a generic GraphQL query with optional variables
func (g *GraphQLClient) ForwardGraphQLRequest(ctx context.Context, requestId string, query string, variables map[string]any) (map[string]any, error) {
	req := graphql.NewRequest(query)

	finalVars := variables
	if v, ok := variables["variables"]; ok {
		if inner, ok2 := v.(map[string]any); ok2 {
			finalVars = inner
		}
	}

	for key, value := range finalVars {
		req.Var(key, value)
	}

	req.Header.Set("Content-Type", "application/json")

	variableKeys := make([]string, 0, len(finalVars))
	for k := range finalVars {
		variableKeys = append(variableKeys, k)
	}

	snippet := strings.TrimSpace(query)
	if len(snippet) > 160 {
		snippet = snippet[:160] + "…"
	}

	var respData map[string]any
	err := g.client.Run(ctx, req, &respData)
	if err != nil {
		return nil, err
	}

	return respData, nil
}
