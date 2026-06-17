package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/odigos-io/central-proxy/internal/config"
)

type GraphQLClient struct {
	httpClient *http.Client
}

func NewGraphQLClient() *GraphQLClient {
	return &GraphQLClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// ForwardGraphQLRequest executes a generic GraphQL query with optional variables.
//
// Unlike a typed GraphQL client (e.g. machinebox/graphql) which collapses the
// response into a single flattened error string, this forwards the frontend's
// FULL GraphQL response — including the complete `errors` array with `message`,
// `path`, `locations` and `extensions`. Those details are what let the central
// UI pinpoint *which* field a proxy rejected: a bare "unknown field" raised by
// gqlparser variable coercion carries the offending input field in `path`,
// which the previous flattened message ("graphql: unknown field") discarded.
//
// The returned map mirrors the GraphQL `data` object with an optional `errors`
// key appended when the server reported GraphQL-level errors, preserving the
// existing wire contract consumed by the central UI's REMOTE_FETCH parsing.
func (g *GraphQLClient) ForwardGraphQLRequest(ctx context.Context, requestId string, query string, variables map[string]any, isAdmin bool) (map[string]any, error) {
	// The central UI nests the operation variables under a "variables" key.
	finalVars := variables
	if v, ok := variables["variables"]; ok {
		if inner, ok2 := v.(map[string]any); ok2 {
			finalVars = inner
		}
	}

	body, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": finalVars,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal GraphQL request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, config.FrontendGraphQLURL(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to build GraphQL request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if isAdmin {
		httpReq.Header.Set("X-Odigos-Admin-Allow", "true")
	}

	resp, err := g.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("GraphQL request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read GraphQL response: %w", err)
	}

	var parsed struct {
		Data   map[string]any   `json:"data"`
		Errors []map[string]any `json:"errors"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("failed to decode GraphQL response (status %d): %w", resp.StatusCode, err)
	}

	out := parsed.Data
	if out == nil {
		out = map[string]any{}
	}
	// Preserve the FULL errors array (message + path + locations + extensions)
	// so the central UI can surface the precise field a proxy rejected.
	if len(parsed.Errors) > 0 {
		out["errors"] = parsed.Errors
	}

	return out, nil
}
