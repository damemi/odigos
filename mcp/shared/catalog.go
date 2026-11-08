package shared

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/odigos-io/odigos-enterprise/mcp/provider"
)

// A "tool" is a single named operation an MCP client (Claude, Cursor, an in-cluster
// agent) can call. registerListTool adds one read-only "list" tool to the server:
// when the client calls it, the handler runs listItems and returns the result as
// machine-readable structured content (a JSON object the client can parse and act
// on directly), with an indented-JSON text rendering as a fallback for clients that
// only display text. Errors are returned to the client as tool errors.
//
// It is generic over the element type T solely so the three list tools below
// (sources, actions, rules) reuse this one body instead of duplicating it; T is
// only ever the canonical odigos spec the backend returns, so the tool output can't
// drift from odigos.
func registerListTool[T any](mcpServer *server.MCPServer, name, desc string, listItems func(context.Context) ([]T, error)) {
	tool := mcp.NewTool(name,
		mcp.WithDescription(desc),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
	)
	mcpServer.AddTool(tool, func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		items, err := listItems(ctx)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("%s: %v", name, err)), nil
		}
		// Wrap in an object so structured content is a JSON object (per the MCP
		// spec) rather than a bare array; the text fallback mirrors it.
		result := map[string]any{"items": items}
		buf, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("marshal: %v", err)), nil
		}
		return mcp.NewToolResultStructured(result, string(buf)), nil
	})
}

func registerSources(mcpServer *server.MCPServer, backend provider.Backend) {
	registerListTool(mcpServer, "list_sources",
		"List all Odigos Sources (instrumented workloads on k8s / services on a VM): namespace, name, and the canonical Source spec (workload, instrumentation state, OTel service name).",
		backend.ListSources)
}

func registerActions(mcpServer *server.MCPServer, backend provider.Backend) {
	registerListTool(mcpServer, "list_actions",
		"List all configured Odigos Actions (telemetry processing: attribute edits, cluster info, PII masking, …): namespace, name, and the canonical Action spec.",
		backend.ListActions)
}

func registerRules(mcpServer *server.MCPServer, backend provider.Backend) {
	registerListTool(mcpServer, "list_instrumentation_rules",
		"List all configured Odigos instrumentation rules: namespace, name, and the canonical InstrumentationRule spec.",
		backend.ListInstrumentationRules)
}
