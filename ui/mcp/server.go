// Package mcp is the in-process MCP server mounted at /mcp by the enterprise UI,
// reusing the frontend's informer cache (no extra hop).
//
//   - Enabled via OdigosConfiguration.McpEnabled (helm ui.mcp.enabled, default on).
//   - OdigosConfiguration.McpAccessMode (default read-only) gates write tools;
//     tools/list always returns the full catalog.
//   - Every tools/call is audited (tool, outcome, duration).
//
// Tool definitions live in the platform-neutral mcp/ module; this package binds
// the k8s backend. The VM agent binds the same module to its own backend.
package mcp

import (
	"context"
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/odigos-io/odigos-enterprise/mcp/audit"
	odigosmcp "github.com/odigos-io/odigos-enterprise/mcp/shared"
	"github.com/odigos-io/odigos-enterprise/ui/mcp/k8s"
	commonlogger "github.com/odigos-io/odigos/common/logger"
	"github.com/odigos-io/odigos/frontend/version"
)

// Handler returns the /mcp HTTP handler (streamable HTTP, stateless). The caller
// (ui/cmd/main.go) only mounts it when MCP is enabled.
//
// deps is the bundle of shared frontend clients produced by server.Bootstrap —
// the k8s cache client, Prometheus API, Odigos metrics consumer, profile store and
// logger. The k8s-only tools reuse these exact clients so MCP observes precisely
// what the GraphQL UI does, instead of constructing its own data access.
func Handler(deps k8s.Deps) http.Handler {
	return server.NewStreamableHTTPServer(newServer(deps),
		server.WithStateLess(true),
		server.WithEndpointPath("/mcp"),
	)
}

func newServer(deps k8s.Deps) *server.MCPServer {
	s := server.NewMCPServer("odigos-mcp", version.OdigosVersion,
		// Audit every tool call (name, outcome, duration); registered first so it
		// is the outermost middleware and records access-mode escalations too. The
		// audit middleware is shared with the VM agent so the trail matches.
		server.WithToolHandlerMiddleware(audit.Middleware(commonlogger.LoggerCompat())),
		// Refuse write tools unless the server is in read-write mode.
		server.WithToolHandlerMiddleware(enforceAccessMode),
	)
	// Mount the full k8s-only catalog first, then the portable shared catalog
	// bound to the k8s backend. The shared catalog re-registers the four
	// list_* tools with the canonical CRD-spec shape, intentionally overriding
	// the catalog's own (mcp-go AddTool is last-wins), so those four match the
	// VM agent's contract exactly.
	k8s.RegisterAll(s, deps)
	odigosmcp.Register(s, newK8sBackend())
	// Tools to read and toggle the access mode; registered with the cluster
	// client because changing the mode persists it in the Odigos configuration.
	registerAccessModeTools(s, deps.K8sCacheClient)
	return s
}

// enforceAccessMode refuses write tools unless the server is in read-write mode.
// It keys off each tool's ReadOnlyHint annotation, so every write tool is covered
// with nothing to wire per tool. Rather than a flat error it returns an
// escalation result, so the agent can ask a human to enable write mode.
func enforceAccessMode(next server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if isWriteTool(ctx, req.Params.Name) && !writesAllowed(ctx) {
			return mcp.NewToolResultError(escalationMessage(req.Params.Name)), nil
		}
		return next(ctx, req)
	}
}
