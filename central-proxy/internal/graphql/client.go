package graphql

import (
	"context"
	"os"

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
func (g *GraphQLClient) ForwardGraphQLRequest(ctx context.Context, query string, variables map[string]any) (map[string]any, error) {
	req := graphql.NewRequest(query)

	for key, value := range variables {
		req.Var(key, value)
	}

	req.Header.Set("Content-Type", "application/json")

	var respData map[string]any
	err := g.client.Run(ctx, req, &respData)
	if err != nil {
		return nil, err
	}

	return respData, nil
}
