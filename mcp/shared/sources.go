package shared

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/odigos-io/odigos-enterprise/mcp/provider"
)

// registerSources adds the cross-platform list_enabled_sources tool. The handler
// is platform-neutral: it calls backend.ListSources and serializes the result;
// the host's backend supplies the data (k8s from the OSS frontend services, VM
// from its config API) and is responsible for excluding disabled sources.
func registerSources(s *server.MCPServer, b provider.Backend) {
	t := mcp.NewTool("list_enabled_sources",
		mcp.WithDescription("List all enabled Odigos Sources (workloads/namespaces selected for auto-instrumentation), each with its name, namespace, and the workload it targets (name, namespace, kind)."),
	)
	readOnly := true
	notDestructive := false
	t.Annotations.ReadOnlyHint = &readOnly
	t.Annotations.DestructiveHint = &notDestructive

	s.AddTool(t, func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		srcs, err := b.ListSources(ctx)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("list sources: %v", err)), nil
		}
		buf, err := json.MarshalIndent(srcs, "", "  ")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("marshal: %v", err)), nil
		}
		return mcp.NewToolResultText(string(buf)), nil
	})
}
