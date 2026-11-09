package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/server"

	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/frontend/services"
)

// accessMode reads the MCP write posture from the effective Odigos config; fails safe to read-only.
func accessMode(ctx context.Context) common.McpAccessMode {
	cfg, err := services.GetOdigosConfiguration(ctx)
	if err != nil || cfg.McpAccessMode != common.McpAccessModeReadWrite {
		return common.McpAccessModeReadOnly
	}
	return common.McpAccessModeReadWrite
}

// writesAllowed reports whether write tools may run.
func writesAllowed(ctx context.Context) bool {
	return accessMode(ctx) == common.McpAccessModeReadWrite
}

// isAccessControlTool reports whether a tool governs the access mode itself (never gated).
func isAccessControlTool(name string) bool {
	return name == getAccessModeToolName ||
		name == enableWriteModeToolName ||
		name == disableWriteModeToolName
}

// isWriteTool reports whether the named tool mutates the cluster, via its ReadOnlyHint (nil guards are defensive).
func isWriteTool(ctx context.Context, name string) bool {
	if isAccessControlTool(name) {
		return false
	}
	srv := server.ServerFromContext(ctx)
	if srv == nil {
		return false
	}
	tool := srv.GetTool(name)
	if tool == nil {
		return false
	}
	hint := tool.Tool.Annotations.ReadOnlyHint
	return hint != nil && !*hint
}

// escalationMessage tells the agent a write was refused in read-only mode and how to enable write mode.
func escalationMessage(tool string) string {
	return fmt.Sprintf("%q is a write operation and the Odigos MCP server is in read-only mode, "+
		"so it was NOT performed. Enabling write mode allows mutating the cluster. Confirm with the "+
		"user, then call %q to switch to read-write and retry; call %q when done.",
		tool, enableWriteModeToolName, disableWriteModeToolName)
}
