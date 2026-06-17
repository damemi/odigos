package shared

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/odigos-io/odigos-enterprise/mcp/provider"
)

// registerDestinations adds the cross-platform list_destinations tool. The
// handler is platform-neutral: it calls backend.ListDestinations and serializes
// the result; the host's backend supplies the data (k8s from the OSS frontend
// services, VM from its config API).
func registerDestinations(s *server.MCPServer, b provider.Backend) {
	t := mcp.NewTool("list_destinations",
		mcp.WithDescription("List all configured Odigos Destinations (telemetry backends), each with its name, destination type, and the signals (traces/metrics/logs) it exports."),
	)
	readOnly := true
	notDestructive := false
	t.Annotations.ReadOnlyHint = &readOnly
	t.Annotations.DestructiveHint = &notDestructive

	s.AddTool(t, func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		dests, err := b.ListDestinations(ctx)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("list destinations: %v", err)), nil
		}
		buf, err := json.MarshalIndent(dests, "", "  ")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("marshal: %v", err)), nil
		}
		return mcp.NewToolResultText(string(buf)), nil
	})
}
