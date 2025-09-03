package graphql

import (
	"context"

	"github.com/machinebox/graphql"
	"github.com/odigos-io/central-proxy/internal/config"
)

type GraphQLClient struct {
	client *graphql.Client
}

func NewGraphQLClient() *GraphQLClient {
	client := graphql.NewClient(config.FrontendGraphQLURL())

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

	var respData map[string]any
	err := g.client.Run(ctx, req, &respData)
	if err != nil {
		return nil, err
	}

	return respData, nil
}
