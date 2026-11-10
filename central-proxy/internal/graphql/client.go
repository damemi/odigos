package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/odigos-io/central-proxy/internal/config"
	"github.com/odigos-io/central-proxy/internal/logger"
	"github.com/odigos-io/odigos/api/k8sconsts"
)

const tokenPathEnvVar = "ODIGOS_UI_TOKEN_PATH"

type GraphQLClient struct {
	httpClient   *http.Client
	tokenMissing atomic.Bool
}

func tokenPath() string {
	if p := os.Getenv(tokenPathEnvVar); p != "" {
		return p
	}
	return k8sconsts.CentralProxyUITokenPath
}

// proxyToken reads the projected ServiceAccount token (audience odigos-ui). It is
// re-read on every call because the kubelet rotates it. It returns "" when the
// token is unavailable or malformed (e.g. a chart that does not mount it yet),
// warning once per outage. The token value is never logged.
func (g *GraphQLClient) proxyToken() string {
	path := tokenPath()
	raw, err := os.ReadFile(path)
	token := strings.TrimSpace(string(raw))
	if err == nil && token != "" && !strings.ContainsAny(token, "\r\n") {
		g.tokenMissing.Store(false)
		return token
	}
	if g.tokenMissing.CompareAndSwap(false, true) && logger.Log != nil {
		logger.Log.Warn("ServiceAccount token for the in-cluster UI is unavailable; admin requests are sent without it", "path", path, "error", err)
	}
	return ""
}

func NewGraphQLClient() *GraphQLClient {
	return &GraphQLClient{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			// Never follow redirects: Go would replay the custom admin and token
			// headers to the redirect target.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
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
		httpReq.Header.Set(k8sconsts.CentralProxyAdminOverrideHeader, "true")
		if token := g.proxyToken(); token != "" {
			httpReq.Header.Set(k8sconsts.CentralProxyUITokenHeader, token)
		}
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
