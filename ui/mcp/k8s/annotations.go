package k8s

import "github.com/mark3labs/mcp-go/mcp"

// dryRunDesc is the standard description string for the dry_run parameter
// that every write tool exposes. Centralized so the message stays consistent.
const dryRunDesc = "Default true. When true, returns what would be created without touching the cluster. Pass false to actually apply."

func boolPtr(b bool) *bool { return &b }

// readAnno marks a tool as read-only and idempotent — safe for an agent to
// call without prompting the user.
func readAnno() mcp.ToolOption {
	return mcp.WithToolAnnotation(mcp.ToolAnnotation{
		ReadOnlyHint:    boolPtr(true),
		DestructiveHint: boolPtr(false),
		IdempotentHint:  boolPtr(true),
		OpenWorldHint:   boolPtr(true),
	})
}

// writeAnno marks a tool as mutating + destructive — well-behaved MCP clients
// (Claude Desktop, Cursor) will prompt the user before executing. Every write
// tool additionally exposes a dry_run parameter defaulting to true.
func writeAnno() mcp.ToolOption {
	return mcp.WithToolAnnotation(mcp.ToolAnnotation{
		ReadOnlyHint:    boolPtr(false),
		DestructiveHint: boolPtr(true),
		IdempotentHint:  boolPtr(false),
		OpenWorldHint:   boolPtr(true),
	})
}
