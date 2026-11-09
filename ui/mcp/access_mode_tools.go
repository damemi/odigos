package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/frontend/services"
)

// Access mode is exposed as tools (not just static config) so a human can
// grant/revoke write access mid-session — a deliberate human-in-the-loop gate
// that also lets someone safely experiment; token-scoped auth will supersede this
// later.
//
// registerAccessModeTools mounts the tools that read and change the MCP access
// mode. They are registered with the cluster client because changing the mode
// persists it in the Odigos configuration. get_access_mode is a plain read.
// enable_write_mode is marked destructive so a well-behaved client asks the user
// to confirm before allowing writes — the human-in-the-loop that authorizes the
// escalation; disable_write_mode lowers the privilege and needs no confirmation.
func registerAccessModeTools(s *server.MCPServer, c client.Client) {
	s.AddTool(
		mcp.NewTool(getAccessModeToolName,
			mcp.WithDescription("Report whether the Odigos MCP server is in read-only or read-write mode. Read-only is the default: a write tool called in read-only mode is not run and returns guidance to enable write mode first."),
			mcp.WithToolAnnotation(mcp.ToolAnnotation{
				ReadOnlyHint:   mcp.ToBoolPtr(true),
				IdempotentHint: mcp.ToBoolPtr(true),
			}),
		),
		getAccessModeHandler(),
	)

	s.AddTool(
		mcp.NewTool(enableWriteModeToolName,
			mcp.WithDescription("Switch the Odigos MCP server to read-write mode, enabling the cluster-mutating tools. Confirm with the user first. The change is persisted in the Odigos configuration and may take a few seconds to take effect."),
			mcp.WithToolAnnotation(mcp.ToolAnnotation{
				ReadOnlyHint:    mcp.ToBoolPtr(false),
				DestructiveHint: mcp.ToBoolPtr(true),
			}),
		),
		setAccessModeHandler(c, common.McpAccessModeReadWrite),
	)

	s.AddTool(
		mcp.NewTool(disableWriteModeToolName,
			mcp.WithDescription("Switch the Odigos MCP server back to read-only mode. Reducing privilege, so it runs without confirmation. The change is persisted in the Odigos configuration."),
			mcp.WithToolAnnotation(mcp.ToolAnnotation{
				ReadOnlyHint:    mcp.ToBoolPtr(false),
				DestructiveHint: mcp.ToBoolPtr(false),
			}),
		),
		setAccessModeHandler(c, common.McpAccessModeReadOnly),
	)
}

func getAccessModeHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText(string(accessMode(ctx))), nil
	}
}

func setAccessModeHandler(c client.Client, mode common.McpAccessMode) server.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := services.SetMcpAccessMode(ctx, c, mode); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to set access mode: %v", err)), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("access mode set to %s", mode)), nil
	}
}
